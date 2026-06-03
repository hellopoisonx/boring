// Package provider — OpenAI Responses 协议适配。
//
// 通过官方 [openai-go/v3] SDK 调用 /v1/responses（responses 子包）。
// 与 Chat Completions 的关键差异：
//   - system 提示走顶层 [responses.ResponseNewParams.Instructions]，不再混入 messages；
//   - 历史输入走 [responses.ResponseNewParamsInputUnion] 的 InputItemList 形态；
//   - 非流式响应没有 Choices，文本与工具调用都从 [responses.Response.Output] 数组中提取；
//   - 流式是事件流（response.created / response.output_text.delta /
//     response.function_call_arguments.delta / response.completed ...），需按 event.Type 路由。
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// OpenAIResponse 实现 [llm.LLM]，对接 OpenAI Responses 协议。
type OpenAIResponse struct {
	cfg    config.LLMConfig
	client openai.Client
}

// NewOpenAIResponse 用给定的 [config.LLMConfig] 构造 [OpenAIResponse]。
func NewOpenAIResponse(cfg config.LLMConfig) *OpenAIResponse {
	return &OpenAIResponse{
		cfg:    cfg,
		client: openai.NewClient(openaiClientOptions(cfg)...),
	}
}

// Compile-time 断言：OpenAIResponse 必须实现 [llm.LLM]。
var _ llm.LLM = (*OpenAIResponse)(nil)

// Generate 同步调用 Responses 协议。
func (p *OpenAIResponse) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	resp, err := p.client.Responses.New(ctx, params)
	if err != nil {
		return nil, wrapError(p.cfg, err)
	}
	return parseResponseOutput(resp.Output)
}

// GenerateWithStream 流式调用 Responses 协议；按事件类型路由产出 [llm.StreamChunk]。
func (p *OpenAIResponse) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, &llm.Error{Provider: string(p.cfg.Sdk), Message: err.Error(), Cause: err}
	}
	stream := p.client.Responses.NewStreaming(ctx, params)
	w := asyncrw.NewAsyncWriter[llm.StreamChunk](64)
	go p.consumeStream(ctx, stream, w)
	return w.ToReader(), nil
}

// buildParams 把统一的 [llm.GenerateRequest] 翻译为 [responses.ResponseNewParams]。
func (p *OpenAIResponse) buildParams(req llm.GenerateRequest) (responses.ResponseNewParams, error) {
	inputItems, err := buildResponseInputList(req.History, req.Input)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	tools, err := convertToolsResponse(req.Tools)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	params := responses.ResponseNewParams{
		Model:             responses.ResponsesModel(p.cfg.Model.ID),
		Input:             responses.ResponseNewParamsInputUnion{OfInputItemList: inputItems},
		Temperature:       param.NewOpt(defaultTemperature),
		MaxOutputTokens:   param.NewOpt(resolveMaxTokens(p.cfg)),
		Tools:             tools,
	}
	if req.System != "" {
		params.Instructions = param.NewOpt(req.System)
	}
	return params, nil
}

// buildResponseInputList 把 history + input 翻译为 [responses.ResponseInputItemUnionParam] 列表。
//
// 重要差异 vs Chat：
//   - system 消息 → 不在这里（走 Instructions）；
//   - user 消息 → EasyInputMessage（纯文本）或 ResponseInputItemMessageParam（多模态）；
//   - assistant 消息含 ToolCall → ResponseFunctionToolCallParam（每个工具调用一条 item）+ 可选 output message；
//   - ToolResult → ResponseInputItemFunctionCallOutputParam。
func buildResponseInputList(history []llm.Message, input *llm.Message) (responses.ResponseInputParam, error) {
	var items responses.ResponseInputParam

	appendMessage := func(idx int, m llm.Message) error {
		switch m.MsgType {
		case llm.MessageTypeSystem:
			// system 已在 buildParams 顶层处理；这里跳过
			return nil
		case llm.MessageTypeUserInput:
			item, err := toResponsesUserInput(m.Content)
			if err != nil {
				return fmt.Errorf("history[%d] user: %w", idx, err)
			}
			items = append(items, item)
			return nil
		case llm.MessageTypeAssistant:
			// 助手消息：先放 output_message（带文本），再放 function_call items
			text := m.Text()
			if text != "" {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfOutputMessage: &responses.ResponseOutputMessageParam{
						Role: "assistant",
						Content: []responses.ResponseOutputMessageContentUnionParam{
							{OfOutputText: &responses.ResponseOutputTextParam{Text: text}},
						},
					},
				})
			}
			for _, c := range m.ToolCalls() {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfFunctionCall: &responses.ResponseFunctionToolCallParam{
						CallID:    c.ID,
						Name:      c.Name,
						Arguments: string(c.Args),
					},
				})
			}
			return nil
		case llm.MessageTypeToolCall:
			// 历史中纯 tool_call 消息（无文本）→ 直接逐个加入
			for _, c := range m.ToolCalls() {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfFunctionCall: &responses.ResponseFunctionToolCallParam{
						CallID:    c.ID,
						Name:      c.Name,
						Arguments: string(c.Args),
					},
				})
			}
			return nil
		case llm.MessageTypeToolResult:
			for _, r := range m.ToolResults() {
				items = append(items, responses.ResponseInputItemUnionParam{
					OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
						CallID: r.ID,
						Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
							OfString: param.NewOpt(r.Result),
						},
					},
				})
			}
			return nil
		}
		return fmt.Errorf("history[%d]: 未知消息类型 %s", idx, m.MsgType)
	}

	for i, m := range history {
		if err := appendMessage(i, m); err != nil {
			return nil, err
		}
	}
	if input != nil {
		if err := appendMessage(-1, *input); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// toResponsesUserInput 构造 user 输入项。纯文本走 [EasyInputMessageParam.OfString]；
// 多模态走 [ResponseInputItemMessageParam]（结构更严谨）。
func toResponsesUserInput(parts []*llm.ContentPart) (responses.ResponseInputItemUnionParam, error) {
	allText := true
	for _, p := range parts {
		if p.PartType != llm.ContentPartTypeText {
			allText = false
			break
		}
	}
	if allText {
		return responses.ResponseInputItemUnionParam{
			OfMessage: &responses.EasyInputMessageParam{
				Role:    responses.EasyInputMessageRoleUser,
				Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(joinTextParts(parts))},
			},
		}, nil
	}

	var contentList responses.ResponseInputMessageContentListParam
	for _, p := range parts {
		switch p.PartType {
		case llm.ContentPartTypeText:
			contentList = append(contentList, responses.ResponseInputContentParamOfInputText(p.Text()))
		case llm.ContentPartTypeImage:
			info := p.Image()
			if info.URL == "" {
				return responses.ResponseInputItemUnionParam{}, fmt.Errorf("图片片段缺少 URL")
			}
			contentList = append(contentList, responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto))
			// URL 在最后一条上覆盖（OfInputImage 是单例）
			last := &contentList[len(contentList)-1]
			last.OfInputImage.ImageURL = param.NewOpt(info.URL)
		default:
			return responses.ResponseInputItemUnionParam{}, fmt.Errorf("不支持的内容片段类型: %s", p.PartType)
		}
	}
	return responses.ResponseInputItemUnionParam{
		OfInputMessage: &responses.ResponseInputItemMessageParam{
			Role:    "user",
			Content: contentList,
		},
	}, nil
}

// convertToolsResponse 把 [llm.ToolInfo] 列表翻译为 responses 工具 union。
func convertToolsResponse(tools []llm.ToolInfo) ([]responses.ToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for i, t := range tools {
		schema, err := parseToolSchema(t.Schema)
		if err != nil {
			return nil, fmt.Errorf("tools[%d] %q: %w", i, t.Name, err)
		}
		tool := responses.FunctionToolParam{
			Name:       t.Name,
			Parameters: schema,
		}
		if t.Description != "" {
			tool.Description = param.NewOpt(t.Description)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: &tool})
	}
	return out, nil
}

// parseResponseOutput 把非流式响应的 Output 数组翻译为 [llm.Message]。
// 这是个 free function（与 chat provider 共享处理逻辑）。
func parseResponseOutput(output []responses.ResponseOutputItemUnion) (*llm.Message, error) {
	var (
		textParts   []string
		toolCalls   []*llm.ToolCall
	)

	for _, item := range output {
		switch item.Type {
		case "message":
			// ResponseOutputMessage：按 Content[] 收集 ResponseOutputText
			msg := item.AsMessage()
			for _, c := range msg.Content {
				if c.Type == "output_text" {
					textParts = append(textParts, c.Text)
				}
			}
		case "function_call":
			fc := item.AsFunctionCall()
			args := json.RawMessage(fc.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage("null")
			}
			toolCalls = append(toolCalls, &llm.ToolCall{
				ID:   fc.CallID,
				Name: fc.Name,
				Args: args,
			})
		default:
			// 其他类型（reasoning、web_search_call、file_search_call 等）忽略
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

// mapResponsesStatus 把 [responses.Response] 状态翻译为 [llm.FinishReason]。
func mapResponsesStatus(resp *responses.Response) llm.FinishReason {
	return mapResponsesStatusFields(string(resp.Status), string(resp.IncompleteDetails.Reason))
}

// mapResponsesStatusFields 是 mapResponsesStatus 的纯函数拆分（便于测试）。
func mapResponsesStatusFields(status, reason string) llm.FinishReason {
	switch status {
	case "completed":
		return llm.FinishReasonStop
	case "failed":
		return llm.FinishReasonError
	case "cancelled":
		return llm.FinishReasonError
	case "incomplete":
		switch reason {
		case "max_output_tokens":
			return llm.FinishReasonLength
		case "content_filter":
			return llm.FinishReasonContentFilter
		}
		return llm.FinishReasonError
	}
	return llm.FinishReason("")
}

// consumeStream 在独立 goroutine 中消费 responses 事件流。
//
// 关键事件类型（仅枚举我们关心的）：
//   - response.output_text.delta         → 文本增量
//   - response.function_call_arguments.delta → 工具参数增量
//   - response.output_item.added/done    → 工具调用项的生命周期边界
//   - response.completed                 → 含完整 Response
//   - response.failed / error            → 异常结束
func (p *OpenAIResponse) consumeStream(ctx context.Context, stream *ssestream.Stream[responses.ResponseStreamEventUnion], w asyncrw.AsyncWriter[llm.StreamChunk]) {
	defer w.Close()

	type toolBuf struct {
		ID, Name, Arguments string
	}
	var (
		textBuf strings.Builder
		tools   = make(map[string]*toolBuf) // keyed by ItemID
	)
	flushText := func() {
		if textBuf.Len() == 0 {
			return
		}
		_ = w.Send(ctx, llm.StreamChunk{Type: llm.StreamChunkTypeText, Text: textBuf.String()})
		textBuf.Reset()
	}
	emitFinish := func(reason llm.FinishReason, usage *llm.Usage) {
		flushText()
		for _, tb := range tools {
			args := json.RawMessage(tb.Arguments)
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

	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			textBuf.WriteString(ev.Delta)
			flushText()
		case "response.function_call_arguments.delta":
			buf, ok := tools[ev.ItemID]
			if !ok {
				buf = &toolBuf{}
				tools[ev.ItemID] = buf
			}
			buf.Arguments += ev.Arguments
		case "response.output_item.added":
			// 新工具调用项开始；记下 ItemID → CallID + Name
			if fc := ev.Item.AsFunctionCall(); fc.CallID != "" {
				tools[ev.ItemID] = &toolBuf{
					ID:   fc.CallID,
					Name: fc.Name,
				}
			}
		case "response.output_item.done":
			// 工具调用项结束；若未收到任何 delta 也能在此刻补全
			if buf, ok := tools[ev.ItemID]; ok && buf.Arguments == "" {
				if fc := ev.Item.AsFunctionCall(); fc.Arguments != "" {
					buf.Arguments = fc.Arguments
				}
			}
		case "response.completed":
			flushText()
			resp := ev.Response
			usage := usageFromPromptCompletion(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
			emitFinish(mapResponsesStatus(&resp), usage)
			return
		case "response.failed", "error", "response.incomplete":
			// 错误/异常结束
			emitFinish(llm.FinishReasonError, nil)
			return
		}
	}

	if err := stream.Err(); err != nil {
		emitFinish(llm.FinishReasonError, nil)
		return
	}

	// 防御：流正常结束但未收到 response.completed
	emitFinish(llm.FinishReasonStop, nil)
}
