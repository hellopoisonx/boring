package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// DeepSeek 协议适配。
//
// DeepSeek 提供与 OpenAI Chat Completions 完全兼容的 HTTP API：
//   - BaseURL: https://api.deepseek.com（注意无 /v1 前缀）
//   - 路径:    POST /chat/completions
//   - 鉴权:    Authorization: Bearer <api_key>
//   - 工具调用、流式 SSE、Usage 字段格式与 OpenAI 一致
//
// 关键差异（相对 OpenAI Chat）：
//   - 终止原因多了 `insufficient_system_resource`（系统资源不足），已由
//     [mapChatFinishReason] 统一处理。
//   - 流式响应中需要在请求里显式带 `stream_options.include_usage=true`，
//     才能在最后一个 chunk（`choices: []`）拿到本次请求的 usage 统计。
//   - 思考模式默认开启：delta 中除 `content` 外还可能有 `reasoning_content`，
//     当前不暴露给上层（统一 [llm.Message]/[llm.StreamChunk] 不含 reasoning
//     字段），直接丢弃；详见 README "已知限制" 段。
//   - usage 还含 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` /
//     `completion_tokens_details.reasoning_tokens`，当前也只透出
//     `prompt_tokens` / `completion_tokens` / `total_tokens` 三个字段。
//
// 实现策略：复用 openai-go SDK（WithBaseURL 指向 https://api.deepseek.com），
// 所有 Chat Completions 协议级转换函数（convertHistoryMessage /
// convertInputMessage / convertToolsChat / parseChatResponse 等）都从
// [openai_chat.go] 直接调用。

// DeepSeekChat 实现 [llm.LLM]，对接 DeepSeek（OpenAI 兼容 Chat Completions）。
//
// 内部完全使用 openai-go SDK，仅 BaseURL 与流式 usage 开关与 OpenAI 不同。
type DeepSeekChat struct {
	cfg    config.LLMConfig
	client openai.Client
}

// NewDeepSeekChat 用给定的 [config.LLMConfig] 构造 [DeepSeekChat]。
func NewDeepSeekChat(cfg config.LLMConfig) *DeepSeekChat {
	return &DeepSeekChat{
		cfg:    cfg,
		client: openai.NewClient(openaiClientOptions(cfg)...),
	}
}

// Compile-time 断言：DeepSeekChat 必须实现 [llm.LLM]。
var _ llm.LLM = (*DeepSeekChat)(nil)

// Generate 同步调用 DeepSeek Chat Completions；返回 [llm.Message]。
func (p *DeepSeekChat) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, wrapError(p.cfg, err)
	}
	return parseChatResponse(string(p.cfg.Sdk), resp)
}

// GenerateWithStream 流式调用 DeepSeek；通过 [asyncrw.AsyncReader] 暴露 [llm.StreamChunk]。
func (p *DeepSeekChat) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	w := asyncrw.NewAsyncWriter[llm.StreamChunk](64)
	go p.consumeStream(ctx, stream, w)
	return w.ToReader(), nil
}

// buildParams 把统一的 [llm.GenerateRequest] 翻译为 [openai.ChatCompletionNewParams]。
//
// 与 OpenAI Chat 的差异：在请求里显式设置 `stream_options.include_usage = true`。
// 非流式调用时 SDK 会忽略该字段；流式调用时 DeepSeek 会在最后一个 chunk
// 携带 usage 字段（prompt/completion/total tokens），从而实现 token 统计。
func (p *DeepSeekChat) buildParams(req llm.GenerateRequest) (openai.ChatCompletionNewParams, error) {
	// 1. system 提示作为第一条消息
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.History)+1)
	if req.System != "" {
		messages = append(messages, openai.SystemMessage(req.System))
	}

	// 2. 历史消息
	for i, m := range req.History {
		converted, err := convertHistoryMessage(i, m)
		if err != nil {
			return openai.ChatCompletionNewParams{}, fmt.Errorf("history[%d]: %w", i, err)
		}
		messages = append(messages, converted...)
	}

	// 3. 最新输入
	if req.Input != nil {
		converted, err := convertInputMessage(req.Input)
		if err != nil {
			return openai.ChatCompletionNewParams{}, fmt.Errorf("input: %w", err)
		}
		messages = append(messages, converted)
	}

	// 4. 工具定义
	tools, err := convertToolsChat(req.Tools)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	params := openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(p.cfg.Model.ID),
		Messages:    messages,
		Temperature: param.NewOpt(defaultTemperature),
		MaxTokens:   param.NewOpt(resolveMaxTokens(p.cfg)),
		Tools:       tools,
		// DeepSeek 关键差异：显式请求流式 usage。
		// 非流式调用时 SDK 会忽略此字段。
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}
	return params, nil
}

// consumeStream 在独立 goroutine 中消费 ssestream，把每个 chunk 翻译为 [llm.StreamChunk]
// 并写入 writer。DeepSeek 的流式响应里：
//   - 每个 chunk 的 choices[].delta.content 含文本增量
//   - choices[].delta.tool_calls 含工具调用增量
//   - 最后一个 chunk（choices=[]）的顶层 `usage` 字段含本次请求的 token 统计
//   - 思考模式（默认开启）可能含 delta.reasoning_content —— 当前直接丢弃
func (p *DeepSeekChat) consumeStream(ctx context.Context, stream *ssestream.Stream[openai.ChatCompletionChunk], w asyncrw.AsyncWriter[llm.StreamChunk]) {
	defer w.Close()

	// 累积器：按 tool call index 拼接 ID/Name/Arguments；text 整段输出
	type toolBuf struct {
		ID        string
		Name      string
		Arguments strings.Builder
	}
	var (
		textBuf   strings.Builder
		tools     = make(map[int64]*toolBuf)
		lastUsage *llm.Usage
		flushText = func() {
			if textBuf.Len() == 0 {
				return
			}
			_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeText, Text: textBuf.String()})
			textBuf.Reset()
		}
	)

	// 收尾：flush 全部 tool calls + 发送 finish chunk
	emitFinish := func(reason llm.FinishReason, usage *llm.Usage) {
		flushText()
		for _, tb := range tools {
			args := json.RawMessage(tb.Arguments.String())
			if !json.Valid(args) {
				args = json.RawMessage("null")
			}
			_ = w.Send(ctx, llm.StreamChunk{
				Type: llm.StreamChunkTypeToolCall,
				ToolCall: &llm.ToolCall{
					ID:   tb.ID,
					Name: tb.Name,
					Args: args,
				},
			})
		}
		_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeFinish, FinishReason: reason, Usage: usage})
	}

	var lastFinish llm.FinishReason

	for stream.Next() {
		chunk := stream.Current()

		// token 统计：DeepSeek 在最后一个 chunk（choices 为空）的顶层 usage 字段填值；
		// 中间 chunk 的 usage 字段为 0。直接以 TotalTokens > 0 作为"有效 usage"标志。
		if u := chunk.Usage; u.TotalTokens > 0 {
			lastUsage = usageFromPromptCompletion(u.PromptTokens, u.CompletionTokens, u.TotalTokens)
		}

		// 文本/工具调用增量
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				textBuf.WriteString(c.Delta.Content)
			}
			// DeepSeek 思考模式：delta.reasoning_content 暂不暴露，统一类型不支持。
			// 直接忽略；详见 README "已知限制"。
			for _, dtc := range c.Delta.ToolCalls {
				buf, ok := tools[dtc.Index]
				if !ok {
					buf = &toolBuf{}
					tools[dtc.Index] = buf
				}
				if dtc.ID != "" {
					buf.ID = dtc.ID
				}
				if dtc.Function.Name != "" {
					buf.Name = dtc.Function.Name
				}
				if dtc.Function.Arguments != "" {
					buf.Arguments.WriteString(dtc.Function.Arguments)
				}
			}
			if c.FinishReason != "" {
				lastFinish = mapChatFinishReason(c.FinishReason)
			}
		}
		// 整流：每个 chunk 立即 flush 文本以降低延迟
		flushText()
	}

	if err := stream.Err(); err != nil {
		// 流式错误：以 FinishReasonError 收尾；详细错误经 [asyncrw] 的 Err 通道返回
		_ = w.Send(ctx, llm.StreamChunk{
			Type:         llm.StreamChunkTypeFinish,
			FinishReason: llm.FinishReasonError,
		})
		return
	}

	emitFinish(lastFinish, lastUsage)
}
