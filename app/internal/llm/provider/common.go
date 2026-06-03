// Package provider 实现三种 LLM 协议（OpenAI Chat Completions / OpenAI Responses / Anthropic Messages）
// 到 [llm.LLM] 统一接口的适配层。
//
// 所有 provider 都：
//   - 接收标准化的 [llm.GenerateRequest]；
//   - 内部转换为对应官方 SDK 的请求格式；
//   - 统一返回 [llm.Message]（非流式）或 [llm.StreamChunk]（流式）；
//   - 统一将 SDK 错误（apierror.Error）包装为 [llm.Error]。
package provider

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// defaultTemperature 缺省采样温度。三家协议均支持 0~2 / 0~1，
// 1.0 为各协议的中性默认值；调用方暂不暴露覆盖。
const defaultTemperature float64 = 1.0

// fallbackMaxTokens 当 [config.Model.MaxResponse] 未配置时使用的兜底值。
// Anthropic 协议必填 MaxTokens，0 会触发 prompt-cache 模式（不输出），
// 故统一使用 1024 作为安全兜底。
const fallbackMaxTokens int64 = 1024

// baseURLString 把 [url.URL] 还原成字符串；若 [config.LLMConfig.BaseURL]
// 未配置（Host 为空）则回退到协议官方默认地址。
func baseURLString(cfg config.LLMConfig) string {
	if cfg.BaseURL.Host != "" {
		return cfg.BaseURL.String()
	}
	if d := cfg.Sdk.DefaultBaseURL(); d != "" {
		return d
	}
	return ""
}

// openaiClientOptions 根据 [config.LLMConfig] 构造 openai-go 的 Option 列表。
func openaiClientOptions(cfg config.LLMConfig) []openaioption.RequestOption {
	opts := []openaioption.RequestOption{
		openaioption.WithAPIKey(cfg.APIKey),
		openaioption.WithMaxRetries(2),
	}
	if u := baseURLString(cfg); u != "" {
		opts = append(opts, openaioption.WithBaseURL(u))
	}
	return opts
}

// anthropicClientOptions 根据 [config.LLMConfig] 构造 anthropic-sdk-go 的 Option 列表。
func anthropicClientOptions(cfg config.LLMConfig) []option.RequestOption {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(2),
		// 避免与 OPENAI_* 等无关环境变量互相污染
		option.WithoutEnvironmentDefaults(),
	}
	if u := baseURLString(cfg); u != "" {
		opts = append(opts, option.WithBaseURL(u))
	}
	return opts
}

// resolveMaxTokens 把 [config.Model.MaxResponse] 转换为各 SDK 接受的 int64，
// 未配置时使用 [fallbackMaxTokens]。
func resolveMaxTokens(cfg config.LLMConfig) int64 {
	if cfg.Model.MaxResponse > 0 {
		return int64(cfg.Model.MaxResponse)
	}
	return fallbackMaxTokens
}

// parseToolSchema 把 [llm.ToolInfo.Schema]（JSON Schema 字符串）解析为 map[string]any。
// 解析失败返回错误，调用方应将其包装为 [llm.Error]。
func parseToolSchema(schema string) (map[string]any, error) {
	if schema == "" {
		// 空 schema 等价于 {"type": "object"}；OpenAI/Anthropic 都接受空 parameters。
		return map[string]any{"type": "object"}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(schema), &m); err != nil {
		return nil, fmt.Errorf("tool schema 不是合法 JSON: %w", err)
	}
	return m, nil
}

// wrapError 把 SDK 返回的 error 统一包装为 [llm.Error]。
//
// 优先通过 errors.As 识别 [openai.Error]/[anthropic.Error]（两个 SDK 都把
// 内部 apierror.Error 以 type alias 形式公开）以提取 HTTP 状态码；其他错误
// 视作网络层错误（StatusCode=0）。provider 取自 [config.Sdk] 的字符串值。
func wrapError(cfg config.LLMConfig, err error) error {
	if err == nil {
		return nil
	}
	if e := (*llm.Error)(nil); errors.As(err, &e) {
		return err // 已经是统一错误，避免重复包装
	}

	provider := string(cfg.Sdk)

	// 尝试 openai-go 错误
	var openaiErr *openai.Error
	if errors.As(err, &openaiErr) {
		status := openaiErr.StatusCode
		msg := openaiErr.Error()
		if openaiErr.Message != "" {
			msg = fmt.Sprintf("%s: %s", openaiErr.Type, openaiErr.Message)
		}
		return &llm.Error{
			Provider:   provider,
			StatusCode: status,
			Message:    msg,
			Cause:      err,
		}
	}

	// 尝试 anthropic-sdk-go 错误
	var anthErr *anthropic.Error
	if errors.As(err, &anthErr) {
		status := anthErr.StatusCode
		msg := anthErr.Error()
		if t := anthErr.Type(); t != "" {
			msg = fmt.Sprintf("%s: %s", t, anthErr.Error())
		}
		return &llm.Error{
			Provider:   provider,
			StatusCode: status,
			Message:    msg,
			Cause:      err,
		}
	}

	// 兜底：网络/上下文等非 HTTP 错误
	return &llm.Error{
		Provider:   provider,
		StatusCode: 0,
		Message:    err.Error(),
		Cause:      err,
	}
}

// toAnthropicUserContent 构造 anthropic 的 user 消息 Content 列表。
// Anthropic 不允许 user 消息为空；纯文本走单 TextBlockParam，多模态按块构造。
func toAnthropicUserContent(parts []*llm.ContentPart) ([]anthropic.ContentBlockParamUnion, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("user 消息不能为空")
	}
	var blocks []anthropic.ContentBlockParamUnion
	for _, p := range parts {
		switch p.PartType {
		case llm.ContentPartTypeText:
			blocks = append(blocks, anthropic.ContentBlockParamUnion{
				OfText: &anthropic.TextBlockParam{Text: p.Text()},
			})
		case llm.ContentPartTypeImage:
			info := p.Image()
			if info.URL == "" {
				return nil, fmt.Errorf("图片片段缺少 URL")
			}
			blocks = append(blocks, anthropic.ContentBlockParamUnion{
				OfImage: &anthropic.ImageBlockParam{
					Source: anthropic.ImageBlockParamSourceUnion{
						OfURL: &anthropic.URLImageSourceParam{URL: info.URL},
					},
				},
			})
		default:
			return nil, fmt.Errorf("不支持的内容片段类型: %s", p.PartType)
		}
	}
	return blocks, nil
}

// toOpenAIUserMessage 构造一个 openai 的 user 消息：支持纯文本与图片。
// text content 直接拼接成单个 string part；image part 走 [openai.ImageContentPart]。
func toOpenAIUserMessage(parts []*llm.ContentPart) (openai.ChatCompletionMessageParamUnion, error) {
	// 简单路径：所有 part 都是 text → 单 string content
	allText := true
	for _, p := range parts {
		if p.PartType != llm.ContentPartTypeText {
			allText = false
			break
		}
	}
	if allText {
		return openai.UserMessage(joinTextParts(parts)), nil
	}

	// 多模态路径
	var contentParts []openai.ChatCompletionContentPartUnionParam
	for _, p := range parts {
		switch p.PartType {
		case llm.ContentPartTypeText:
			contentParts = append(contentParts, openai.TextContentPart(p.Text()))
		case llm.ContentPartTypeImage:
			info := p.Image()
			if info.URL == "" {
				return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("图片片段缺少 URL")
			}
			contentParts = append(contentParts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: info.URL,
			}))
		default:
			return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("不支持的内容片段类型: %s", p.PartType)
		}
	}
	return openai.UserMessage(contentParts), nil
}

// joinTextParts 把全部 text part 拼接为单个字符串；非 text part 会被忽略。
func joinTextParts(parts []*llm.ContentPart) string {
	var s string
	for _, p := range parts {
		if p.PartType == llm.ContentPartTypeText {
			s += p.Text()
		}
	}
	return s
}

// usageFromPromptCompletion 把 openai-style 的 prompt/completion/total token
// 转换为统一的 [llm.Usage]；任一字段为零按零处理，全零返回 nil。
func usageFromPromptCompletion(prompt, completion, total int64) *llm.Usage {
	if prompt == 0 && completion == 0 && total == 0 {
		return nil
	}
	return &llm.Usage{
		PromptTokens:     uint32(prompt),
		CompletionTokens: uint32(completion),
		TotalTokens:      uint32(total),
	}
}
