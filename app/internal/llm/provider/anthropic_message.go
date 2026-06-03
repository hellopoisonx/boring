// Package provider — Anthropic Messages 协议适配。
//
// 通过官方 [anthropic-sdk-go] SDK 调用 /v1/messages。
// 与 OpenAI 协议的关键差异：
//   - system 走顶层 [anthropic.MessageNewParams.System]（不是 messages 之一）；
//   - MaxTokens 必填；
//   - 消息内容是 ContentBlock 列表而非字符串/ContentPart 列表；
//   - 工具调用以 [ToolUseBlock] 形式出现在响应 Content[] 中；
//   - 流式是事件流（message_start / content_block_start / content_block_delta /
//     content_block_stop / message_delta / message_stop），需按事件类型路由。
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// AnthropicMessage 实现 [llm.LLM]，对接 Anthropic Messages 协议。
type AnthropicMessage struct {
	cfg    config.LLMConfig
	client anthropic.Client
}

// NewAnthropicMessage 用给定的 [config.LLMConfig] 构造 [AnthropicMessage]。
func NewAnthropicMessage(cfg config.LLMConfig) *AnthropicMessage {
	return &AnthropicMessage{
		cfg:    cfg,
		client: anthropic.NewClient(anthropicClientOptions(cfg)...),
	}
}

// Compile-time 断言：AnthropicMessage 必须实现 [llm.LLM]。
var _ llm.LLM = (*AnthropicMessage)(nil)

// Generate 同步调用 Messages 协议。
func (p *AnthropicMessage) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, wrapError(p.cfg, err)
	}
	return parseAnthropicResponse(resp)
}

// GenerateWithStream 流式调用 Messages 协议。
func (p *AnthropicMessage) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	stream := p.client.Messages.NewStreaming(ctx, params)
	w := asyncrw.NewAsyncWriter[llm.StreamChunk](64)
	go p.consumeStream(ctx, stream, w)
	return w.ToReader(), nil
}

// buildParams 把统一的 [llm.GenerateRequest] 翻译为 [anthropic.MessageNewParams]。
func (p *AnthropicMessage) buildParams(req llm.GenerateRequest) (anthropic.MessageNewParams, error) {
	messages, err := buildAnthropicMessages(req.History, req.Input)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	tools, err := convertToolsAnthropic(req.Tools)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(p.cfg.Model.ID),
		Messages:    messages,
		MaxTokens:   resolveMaxTokens(p.cfg),
		Temperature: param.NewOpt(defaultTemperature),
		Tools:       tools,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	return params, nil
}

// buildAnthropicMessages 把 history + input 翻译为 [anthropic.MessageParam] 列表。
//
// 重要差异 vs OpenAI：
//   - system 消息已在 buildParams 顶层处理；这里跳过；
//   - user 消息的内容是 ContentBlockParamUnion 列表（text/image/tool_result）；
//   - assistant 消息含 ToolUseBlock → tool_use 块；
//   - ToolResult 消息 → user 消息中的 tool_result 块。
func buildAnthropicMessages(history []llm.Message, input *llm.Message) ([]anthropic.MessageParam, error) {
	var out []anthropic.MessageParam

	appendMsg := func(idx int, m llm.Message) error {
		switch m.MsgType {
		case llm.MessageTypeSystem:
			// 跳过；system 已走顶层
			return nil
		case llm.MessageTypeUserInput:
			blocks, err := toAnthropicUserContent(m.Content)
			if err != nil {
				return fmt.Errorf("history[%d] user: %w", idx, err)
			}
			out = append(out, anthropic.NewUserMessage(blocks...))
			return nil
		case llm.MessageTypeAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if text := m.Text(); text != "" {
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfText: &anthropic.TextBlockParam{Text: text},
				})
			}
			for _, c := range m.ToolCalls() {
				input := json.RawMessage(c.Args)
				if !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    c.ID,
						Name:  c.Name,
						Input: input,
					},
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}
			return nil
		case llm.MessageTypeToolCall:
			var blocks []anthropic.ContentBlockParamUnion
			for _, c := range m.ToolCalls() {
				input := json.RawMessage(c.Args)
				if !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    c.ID,
						Name:  c.Name,
						Input: input,
					},
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}
			return nil
		case llm.MessageTypeToolResult:
			// Anthropic: tool_result 块嵌入 user 消息
			var blocks []anthropic.ContentBlockParamUnion
			for _, r := range m.ToolResults() {
				isErr := param.NewOpt(false)
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: r.ID,
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfText: &anthropic.TextBlockParam{Text: r.Result}},
						},
						IsError: isErr,
					},
				})
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewUserMessage(blocks...))
			}
			return nil
		}
		return fmt.Errorf("history[%d]: 未知消息类型 %s", idx, m.MsgType)
	}

	for i, m := range history {
		if err := appendMsg(i, m); err != nil {
			return nil, err
		}
	}
	if input != nil {
		if err := appendMsg(-1, *input); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// convertToolsAnthropic 把 [llm.ToolInfo] 列表翻译为 anthropic 工具 union。
func convertToolsAnthropic(tools []llm.ToolInfo) ([]anthropic.ToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i, t := range tools {
		schema, err := parseToolSchema(t.Schema)
		if err != nil {
			return nil, fmt.Errorf("tools[%d] %q: %w", i, t.Name, err)
		}
		// 拆出 properties / required
		props, _ := schema["properties"].(map[string]any)
		var required []string
		if reqs, ok := schema["required"].([]any); ok {
			for _, r := range reqs {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		}
		if props == nil {
			props = map[string]any{}
		}
		tool := anthropic.ToolParam{
			Name: t.Name,
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: props,
				Required:   required,
			},
		}
		if t.Description != "" {
			tool.Description = param.NewOpt(t.Description)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

// parseAnthropicResponse 把非流式响应翻译为 [llm.Message]。
func parseAnthropicResponse(resp *anthropic.Message) (*llm.Message, error) {
	var (
		textParts []string
		toolCalls []*llm.ToolCall
	)
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			textParts = append(textParts, v.Text)
		case anthropic.ToolUseBlock:
			args := v.Input
			if !json.Valid(args) {
				args = json.RawMessage("null")
			}
			toolCalls = append(toolCalls, &llm.ToolCall{
				ID:   v.ID,
				Name: v.Name,
				Args: args,
			})
		}
	}
	if len(toolCalls) > 0 {
		body, _ := json.Marshal(toolCalls)
		return &llm.Message{
			MsgType: llm.MessageTypeToolCall,
			Content: []*llm.ContentPart{{PartType: llm.ContentPartTypeText, Body: body}},
		}, nil
	}
	return &llm.Message{
		MsgType: llm.MessageTypeAssistant,
		Content: []*llm.ContentPart{llm.NewTextContent(strings.Join(textParts, ""))},
	}, nil
}

// consumeStream 在独立 goroutine 中消费 anthropic 事件流。
//
// 关键事件：
//   - MessageStartEvent     → 记录 input tokens
//   - ContentBlockStartEvent → 标记当前块类型（text / tool_use）
//   - ContentBlockDeltaEvent → 追加文本或 input_json 增量
//   - ContentBlockStopEvent  → 块结束（text 立刻 flush 整段；tool_use 产出 tool_call）
//   - MessageDeltaEvent      → 更新 StopReason + OutputTokens
//   - MessageStopEvent       → 产出 StreamChunkTypeFinish
func (p *AnthropicMessage) consumeStream(ctx context.Context, stream *ssestream.Stream[anthropic.MessageStreamEventUnion], w asyncrw.AsyncWriter[llm.StreamChunk]) {

	defer w.Close()

	// 状态：按 content_block index 维护 buffer
	type textBuf struct {
		Text strings.Builder
	}
	type toolBuf struct {
		ID, Name string
		Input    strings.Builder
	}
	var (
		texts  = make(map[int64]*textBuf)
		tools  = make(map[int64]*toolBuf)
		flushText = func(idx int64) {
			b, ok := texts[idx]
			if !ok || b.Text.Len() == 0 {
				return
			}
			_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeText, Text: b.Text.String()})
			delete(texts, idx)
		}
		flushTool = func(idx int64) {
			b, ok := tools[idx]
			if !ok {
				return
			}
			input := json.RawMessage(b.Input.String())
			if !json.Valid(input) {
				input = json.RawMessage("null")
			}
			_ = w.Send(ctx, llm.StreamChunk{
				Type: llm.StreamChunkTypeToolCall,
				ToolCall: &llm.ToolCall{
					ID:   b.ID,
					Name: b.Name,
					Args: input,
				},
			})
			delete(tools, idx)
		}
	)

	var (
		inputTokens  int64
		outputTokens int64
		stopReason   llm.FinishReason
	)

	emitFinish := func() {
		// 防御：flush 残留 buffer（异常路径）
		for idx := range texts {
			flushText(idx)
		}
		for idx := range tools {
			flushTool(idx)
		}
		var usage *llm.Usage
		if total := inputTokens + outputTokens; total > 0 {
			usage = &llm.Usage{
				PromptTokens:     uint32(inputTokens),
				CompletionTokens: uint32(outputTokens),
				TotalTokens:      uint32(total),
			}
		}
		_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeFinish, FinishReason: stopReason, Usage: usage})
	}

	for stream.Next() {
		ev := stream.Current()

		switch e := ev.AsAny().(type) {




		case anthropic.MessageStartEvent:
			inputTokens = e.Message.Usage.InputTokens
		case anthropic.ContentBlockStartEvent:
			switch cb := e.ContentBlock.AsAny().(type) {
			case anthropic.TextBlock:
				texts[e.Index] = &textBuf{}
			case anthropic.ToolUseBlock:
				tools[e.Index] = &toolBuf{ID: cb.ID, Name: cb.Name}
			}
		case anthropic.ContentBlockDeltaEvent:
			switch d := e.Delta.AsAny().(type) {
			case anthropic.TextDelta:
				if b, ok := texts[e.Index]; ok {
					b.Text.WriteString(d.Text)
				}
			case anthropic.InputJSONDelta:
				if b, ok := tools[e.Index]; ok {
					b.Input.WriteString(d.PartialJSON)
				}
			}
		case anthropic.ContentBlockStopEvent:
			// 该 index 可能是 text 或 tool，按是否在两个 map 中判断
			if _, ok := texts[e.Index]; ok {
				flushText(e.Index)
			} else if _, ok := tools[e.Index]; ok {
				flushTool(e.Index)
			}
		case anthropic.MessageDeltaEvent:
			stopReason = mapAnthropicStopReason(string(e.Delta.StopReason))
			outputTokens = e.Usage.OutputTokens
		case anthropic.MessageStopEvent:
			emitFinish()
			return
		}
}

	if err := stream.Err(); err != nil {
		_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeFinish, FinishReason: llm.FinishReasonError})
		return
	}

	// 防御：流正常结束但未收到 message_stop
	emitFinish()
}

// mapAnthropicStopReason 把 anthropic 的 StopReason 字符串映射为 [llm.FinishReason]。
func mapAnthropicStopReason(s string) llm.FinishReason {
	switch s {
	case "end_turn":
		return llm.FinishReasonStop
	case "tool_use":
		return llm.FinishReasonToolCalls
	case "max_tokens":
		return llm.FinishReasonLength
	case "stop_sequence":
		return llm.FinishReasonStop
	case "refusal":
		return llm.FinishReasonContentFilter
	}
	if s == "" {
		return llm.FinishReason("")
	}
	return llm.FinishReasonError
}
