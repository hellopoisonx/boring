// Package sdk — OpenAI Chat Completions 协议适配。
//
// 通过官方 [openai-go/v3] SDK 调用 /v1/chat/completions。
// 系统提示通过 messages[0] = role=system 注入；工具定义走
// [openai.ChatCompletionFunctionToolParam]；流式 chunk 走
// [ssestream.Stream]。
package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// OpenAIChat 实现 [llm.LLM]，对接 OpenAI Chat Completions 协议。
type OpenAIChat struct {
	cfg    config.LLMConfig
	client openai.Client

	// streamIncludeUsage 控制流式请求是否带 stream_options.include_usage=true。
	// 默认 false（OpenAI 协议下流式一般不需要 usage，最后一个 chunk 的 usage 字段不保证填充）。
	// 某些 OpenAI-兼容 provider（如 DeepSeek）需要该开关才能在流式响应里拿到 usage。
	// 设置入口：[OpenAIChat.WithStreamIncludeUsage]。
	streamIncludeUsage bool
}

// NewOpenAIChat 用给定的 [config.LLMConfig] 构造 [OpenAIChat]。
func NewOpenAIChat(cfg config.LLMConfig) *OpenAIChat {
	return &OpenAIChat{
		cfg:    cfg,
		client: openai.NewClient(openaiClientOptions(cfg)...),
	}
}

// WithStreamIncludeUsage 打开流式请求的 stream_options.include_usage=true。
// 链式调用：sdk.NewOpenAIChat(cfg).WithStreamIncludeUsage()。
// 供 [provider.DeepSeekChat] 等需要服务端在最后一个 chunk 返回 usage 的场景使用。
func (p *OpenAIChat) WithStreamIncludeUsage() *OpenAIChat {
	p.streamIncludeUsage = true
	return p
}

// Compile-time 断言：OpenAIChat 必须实现 [llm.LLM]。


// DefaultConfig 返回 (Sdk 字符串, Sdk 零值 LLMConfig)。
// SDK 自身不绑定 BaseURL/APIKey/Model 等业务字段，故仅返回协议标识。
func (p *OpenAIChat) DefaultConfig() (string, config.LLMConfig) {
	return string(config.SdkOpenAIChat), config.LLMConfig{Sdk: config.SdkOpenAIChat}
}


// Generate 同步调用 Chat Completions；返回 [llm.Message]。
func (p *OpenAIChat) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
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

// GenerateWithStream 流式调用；通过 [asyncrw.AsyncReader] 暴露 [llm.StreamChunk]。
func (p *OpenAIChat) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	// DeepSeek 等 OpenAI-兼容 provider 需要 stream_options.include_usage=true
	// 才能在最后一个 chunk 返回 usage 字段；通过 [OpenAIChat.WithStreamIncludeUsage] 启用。
	if p.streamIncludeUsage {
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	w := asyncrw.NewAsyncWriter[llm.StreamChunk](64)
	go p.consumeStream(ctx, stream, w)
	return w.ToReader(), nil
}

// buildParams 把统一的 [llm.GenerateRequest] 翻译为 [openai.ChatCompletionNewParams]。
func (p *OpenAIChat) buildParams(req llm.GenerateRequest) (openai.ChatCompletionNewParams, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.History)+1)

	// 1. system 提示作为第一条消息
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
	}
	return params, nil
}

// convertHistoryMessage 把一条 [llm.Message] 翻译为若干个 openai 消息。
func convertHistoryMessage(idx int, m llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	switch m.MsgType {
	case llm.MessageTypeSystem:
		text := m.Text()
		if text == "" {
			return nil, nil
		}
		return []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(text)}, nil

	case llm.MessageTypeUserInput:
		um, err := toOpenAIUserMessage(m.Content)
		if err != nil {
			return nil, fmt.Errorf("history[%d] user: %w", idx, err)
		}
		return []openai.ChatCompletionMessageParamUnion{um}, nil

	case llm.MessageTypeAssistant:
		calls := m.ToolCalls()
		if len(calls) > 0 {
			return []openai.ChatCompletionMessageParamUnion{buildAssistantWithToolCalls(m.Text(), calls)}, nil
		}
		return []openai.ChatCompletionMessageParamUnion{openai.AssistantMessage(m.Text())}, nil

	case llm.MessageTypeToolCall:
		calls := m.ToolCalls()
		if len(calls) == 0 {
			return nil, nil
		}
		return []openai.ChatCompletionMessageParamUnion{buildAssistantWithToolCalls("", calls)}, nil

	case llm.MessageTypeToolResult:
		results := m.ToolResults()
		out := make([]openai.ChatCompletionMessageParamUnion, 0, len(results))
		for _, r := range results {
			out = append(out, openai.ToolMessage(r.Result, r.ID))
		}
		return out, nil
	}
	return nil, fmt.Errorf("history[%d]: 未知消息类型 %s", idx, m.MsgType)
}

// convertInputMessage 把 req.Input（必须是 UserInput 或 ToolResult）翻译为单条 openai 消息。
func convertInputMessage(m *llm.Message) (openai.ChatCompletionMessageParamUnion, error) {
	switch m.MsgType {
	case llm.MessageTypeUserInput:
		return toOpenAIUserMessage(m.Content)
	case llm.MessageTypeToolResult:
		results := m.ToolResults()
		if len(results) == 1 {
			return openai.ToolMessage(results[0].Result, results[0].ID), nil
		}
		if len(results) == 0 {
			return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("ToolResult 消息为空")
		}
		// 多结果合并：取首条 id 作为 tool_call_id（不严谨但覆盖典型场景）
		merged := ""
		for i, r := range results {
			if i > 0 {
				merged += "\n"
			}
			merged += r.Result
		}
		return openai.ToolMessage(merged, results[0].ID), nil
	}
	return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("input 必须是 UserInput 或 ToolResult，得到 %s", m.MsgType)
}

// buildAssistantWithToolCalls 构造一条带 tool_calls 的 assistant 消息。
func buildAssistantWithToolCalls(text string, calls []*llm.ToolCall) openai.ChatCompletionMessageParamUnion {
	asst := openai.ChatCompletionAssistantMessageParam{Role: "assistant"}
	if text != "" {
		asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(text)}
	}
	for _, c := range calls {
		asst.ToolCalls = append(asst.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: c.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      c.Name,
					Arguments: string(c.Args),
				},
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &asst}
}

// convertToolsChat 把 [llm.ToolInfo] 列表翻译为 openai function tool 列表。
func convertToolsChat(tools []llm.ToolInfo) ([]openai.ChatCompletionToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for i, t := range tools {
		schema, err := parseToolSchema(t.Schema)
		if err != nil {
			return nil, fmt.Errorf("tools[%d] %q: %w", i, t.Name, err)
		}
		tool := openai.ChatCompletionFunctionToolParam{
			Type: "function",
			Function: shared.FunctionDefinitionParam{
				Name:       t.Name,
				Parameters: shared.FunctionParameters(schema),
			},
		}
		if t.Description != "" {
			tool.Function.Description = param.NewOpt(t.Description)
		}
		out = append(out, openai.ChatCompletionToolUnionParam{OfFunction: &tool})
	}
	return out, nil
}

// parseChatResponse 把 Chat Completions 协议的非流式响应转换为 [llm.Message]。
//
// 是 free function（不依赖 receiver），便于 DeepSeek 等同协议族 provider 直接复用。
// provider 字符串仅在异常路径（LLM 未返回 choice）时用于构造 [llm.Error.Provider]。
func parseChatResponse(provider string, resp *openai.ChatCompletion) (*llm.Message, error) {
	if len(resp.Choices) == 0 {
		return nil, &llm.Error{Provider: provider, Message: "LLM 未返回任何 choice"}
	}
	choice := resp.Choices[0]
	msg := choice.Message

	// 工具调用
	calls := make([]*llm.ToolCall, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		if fc := tc.AsFunction(); fc.ID != "" {
			args := json.RawMessage(fc.Function.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage("null")
			}
			calls = append(calls, &llm.ToolCall{
				ID:   fc.ID,
				Name: fc.Function.Name,
				Args: args,
			})
		}
	}
	usage := usageFromPromptCompletion(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)

	if len(calls) > 0 {
		return &llm.Message{
			MsgType: llm.MessageTypeToolCall,
			Content: []*llm.ContentPart{toolCallPart(calls)},
			Usage:   usage,
		}, nil
	}

	// 纯文本
	return &llm.Message{
		MsgType: llm.MessageTypeAssistant,
		Content: []*llm.ContentPart{llm.NewTextContent(msg.Content)},
		Usage:   usage,
	}, nil
}

// toolCallPart 把 ToolCall 列表 JSON 序列化后存入单个 ContentPart。
func toolCallPart(calls []*llm.ToolCall) *llm.ContentPart {
	body, _ := json.Marshal(calls)
	return &llm.ContentPart{PartType: llm.ContentPartTypeText, Body: body}
}

// consumeStream 在独立 goroutine 中消费 ssestream，把每个 chunk 翻译为 [llm.StreamChunk] 并写入 writer。
func (p *OpenAIChat) consumeStream(ctx context.Context, stream *ssestream.Stream[openai.ChatCompletionChunk], w asyncrw.AsyncWriter[llm.StreamChunk]) {
	defer w.Close()

	// 累积器：按 tool call index 拼接 ID/Name/Arguments；text 整段输出
	type toolBuf struct {
		ID        string
		Name      string
		Arguments strings.Builder
	}
	var (
		textBuf strings.Builder
		tools   = make(map[int64]*toolBuf)
		flushText = func() {
			if textBuf.Len() == 0 {
				return
			}
			_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeText, Text: textBuf.String()})
			textBuf.Reset()
		}
	)

	// 收尾：flush 全部 tool calls
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

	var (
		lastFinish llm.FinishReason
		lastUsage  *llm.Usage
	)

	for stream.Next() {
		chunk := stream.Current()
		// 文本增量
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				textBuf.WriteString(c.Delta.Content)
			}
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

// mapChatFinishReason 把 OpenAI / DeepSeek 等 Chat Completions 协议族的
// FinishReason 字符串映射为 [llm.FinishReason]。
//
// 包含 DeepSeek 特有的 `insufficient_system_resource`（系统推理资源不足）。
func mapChatFinishReason(s string) llm.FinishReason {
	switch s {
	case "stop":
		return llm.FinishReasonStop
	case "tool_calls":
		return llm.FinishReasonToolCalls
	case "length":
		return llm.FinishReasonLength
	case "content_filter":
		return llm.FinishReasonContentFilter
	case "insufficient_system_resource":
		return llm.FinishReasonError
	}
	return llm.FinishReason(s)
}
