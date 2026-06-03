// Package llm
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// LLM 大语言模型统一接口
//
// 实现 [LLM.Generate] / [LLM.GenerateWithStream] 即可接入新的供应商；
// 典型调用循环：
//
//	resp, err := llm.Generate(ctx, req)
//	for err == nil && resp.MsgType == MessageTypeToolCall {
//	    for _, call := range resp.ToolCalls() {
//	        req.History = append(req.History, *resp)
//	        req.History = append(req.History, *NewToolResultMessage(execute(call)))
//	    }
//	    resp, err = llm.Generate(ctx, req)
//	}
type LLM interface {
	// Generate 同步生成一次响应。
	//
	// 返回 [MessageTypeAssistant] 表示本轮自然结束；
	// 返回 [MessageTypeToolCall] 表示 LLM 决定调用工具，
	// 调用方需执行并以 [MessageTypeToolResult] 追加到 [GenerateRequest.History] 后再次调用。
	Generate(ctx context.Context, req GenerateRequest) (*Message, error)

	// GenerateWithStream 与 [LLM.Generate] 行为相同，但通过 [asyncrw.AsyncReader]
	// 流式输出 [StreamChunk]，调用方边读边处理。
	//
	// 结束标志为 [StreamChunkTypeFinish]（含 [FinishReason] / [Usage]）；
	// 错误经 [asyncrw.AsyncReader.Recv] 返回。
	GenerateWithStream(ctx context.Context, req GenerateRequest) (asyncrw.AsyncReader[StreamChunk], error)

	// DefaultConfig 返回 Provider name 和 Provider 默认的 [config.LLMConfig]
	DefaultConfig() (string, config.LLMConfig)
}

// GenerateRequest LLM 调用请求
type GenerateRequest struct {
	// History 已有对话历史（按时间顺序）
	History []Message

	// Input 最新一条用户/工具输入；为 nil 时表示「无新增输入」（仅基于 history 续写）
	Input *Message

	// System 系统提示
	System string

	// Tools 暴露给 LLM 的工具列表
	Tools []ToolInfo
}

// StreamChunkType 流式 chunk 种类
type StreamChunkType int

const (
	StreamChunkTypeText     StreamChunkType = iota // 文本增量
	StreamChunkTypeToolCall                        // 一个完整的工具调用
	StreamChunkTypeFinish                          // 流结束
)

// FinishReason LLM 响应结束原因
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"           // 自然结束
	FinishReasonToolCalls     FinishReason = "tool_calls"     // 要求调用工具
	FinishReasonLength        FinishReason = "length"         // 达到 token 上限
	FinishReasonContentFilter FinishReason = "content_filter" // 内容过滤
	FinishReasonError         FinishReason = "error"          // 异常结束
)

// Usage token 用量统计
type Usage struct {
	PromptTokens     uint32
	CompletionTokens uint32
	TotalTokens      uint32
}

// StreamChunk 流式输出的一块数据
//
// 一个完整 LLM 流式响应（按 Recv 顺序拼接）：
//   - 0..N 条 [StreamChunkTypeText]：[Text] 拼成 assistant 的最终正文
//   - 0..N 条 [StreamChunkTypeToolCall]：[ToolCall] 为本轮工具调用的完整声明
//   - 1 条 [StreamChunkTypeFinish]：[FinishReason] 表示结束原因，[Usage] 为 token 用量
type StreamChunk struct {
	Type StreamChunkType

	// Text 仅 [StreamChunkTypeText] 有意义
	Text string

	// ToolCall 仅 [StreamChunkTypeToolCall] 有意义
	ToolCall *ToolCall

	// FinishReason 仅 [StreamChunkTypeFinish] 有意义
	FinishReason FinishReason

	// Usage 仅 [StreamChunkTypeFinish] 有意义
	Usage *Usage
}

// Error LLM 调用错误
type Error struct {
	// Provider 供应商名（"openai" / "deepseek" / ...）
	Provider string

	// StatusCode HTTP 状态码；网络层错误时为 0
	StatusCode int

	// Message 错误描述
	Message string

	// Cause 原始错误（可经 [errors.Unwrap] 解开）
	Cause error
}

// Error 实现 [error] 接口
func (e *Error) Error() string {
	switch {
	case e.StatusCode > 0:
		return fmt.Sprintf("llm: %s: %d %s", e.Provider, e.StatusCode, e.Message)
	case e.Provider != "":
		return fmt.Sprintf("llm: %s: %s", e.Provider, e.Message)
	default:
		return "llm: " + e.Message
	}
}

// Unwrap 实现 errors.Unwrap 协议
func (e *Error) Unwrap() error {
	return e.Cause
}

// MessageType 消息种类
type MessageType int

const (
	MessageTypeSystem     MessageType = iota // 系统提示
	MessageTypeUserInput                     // 用户输入
	MessageTypeToolCall                      // LLM 发出的工具调用
	MessageTypeToolResult                    // 工具调用结果，回喂给 LLM
	MessageTypeAssistant                     // LLM 助手回复
)

// String 返回消息种类的可读名称（用于日志/调试）
func (t MessageType) String() string {
	switch t {
	case MessageTypeSystem:
		return "system"
	case MessageTypeUserInput:
		return "user"
	case MessageTypeToolCall:
		return "tool_call"
	case MessageTypeToolResult:
		return "tool_result"
	case MessageTypeAssistant:
		return "assistant"
	}
	return fmt.Sprintf("MessageType(%d)", int(t))
}

// Message 一条对话消息
//
// 根据 [Message.MsgType] 将 [Message.Content] (反)序列化：
//
//	[MessageTypeSystem]     => markdown
//	[MessageTypeUserInput]  => markdown
//	[MessageTypeToolCall]   => json([ToolCall])
//	[MessageTypeToolResult] => json([ToolResult])
//	[MessageTypeAssistant]  => markdown
type Message struct {
	MsgType MessageType
	Content []*ContentPart

	// Usage 本次响应的 token 用量；可能为 nil（provider 未在响应中带回）。
	// 与流式路径的 [StreamChunkTypeFinish].Usage 同源同型，方便调用方在两条路径间共享统计逻辑。
	Usage *Usage
}

// NewSystemMessage 构造 [MessageTypeSystem] 消息
func NewSystemMessage(text string) *Message {
	return &Message{
		MsgType: MessageTypeSystem,
		Content: []*ContentPart{NewTextContent(text)},
	}
}

// NewUserMessage 构造 [MessageTypeUserInput] 消息
func NewUserMessage(parts ...*ContentPart) *Message {
	return &Message{
		MsgType: MessageTypeUserInput,
		Content: parts,
	}
}

// NewAssistantMessage 构造 [MessageTypeAssistant] 消息
func NewAssistantMessage(text string) *Message {
	return &Message{
		MsgType: MessageTypeAssistant,
		Content: []*ContentPart{NewTextContent(text)},
	}
}

// NewToolCallMessage 构造 [MessageTypeToolCall] 消息
func NewToolCallMessage(calls ...*ToolCall) *Message {
	body, _ := json.Marshal(calls)
	return &Message{
		MsgType: MessageTypeToolCall,
		Content: []*ContentPart{{
			PartType: ContentPartTypeText,
			Body:     body,
		}},
	}
}

// NewToolResultMessage 构造 [MessageTypeToolResult] 消息
func NewToolResultMessage(results ...*ToolResult) *Message {
	body, _ := json.Marshal(results)
	return &Message{
		MsgType: MessageTypeToolResult,
		Content: []*ContentPart{{
			PartType: ContentPartTypeText,
			Body:     body,
		}},
	}
}

// Text 拼接所有 [ContentPartTypeText] 部分的文本；非文本部分被忽略
func (m *Message) Text() string {
	var sb strings.Builder
	for _, p := range m.Content {
		if p.PartType == ContentPartTypeText {
			sb.Write(p.Body)
		}
	}
	return sb.String()
}

// ToolCalls 仅对 [MessageTypeToolCall] 消息有效；其他类型返回 nil
func (m *Message) ToolCalls() []*ToolCall {
	if m.MsgType != MessageTypeToolCall || len(m.Content) == 0 {
		return nil
	}
	var calls []*ToolCall
	if err := json.Unmarshal(m.Content[0].Body, &calls); err != nil {
		return nil
	}
	return calls
}

// ToolResults 仅对 [MessageTypeToolResult] 消息有效；其他类型返回 nil
func (m *Message) ToolResults() []*ToolResult {
	if m.MsgType != MessageTypeToolResult || len(m.Content) == 0 {
		return nil
	}
	var res []*ToolResult
	if err := json.Unmarshal(m.Content[0].Body, &res); err != nil {
		return nil
	}
	return res
}

// ContentPartType 内容片段种类
type ContentPartType int

const (
	ContentPartTypeText  ContentPartType = iota // 纯文本
	ContentPartTypeImage                        // 图片
)

// String 返回内容片段种类的可读名称（用于日志/调试）
func (t ContentPartType) String() string {
	switch t {
	case ContentPartTypeText:
		return "text"
	case ContentPartTypeImage:
		return "image"
	}
	return fmt.Sprintf("ContentPartType(%d)", int(t))
}

// ContentPart 内容片段
//
// 根据 [ContentPart.PartType] 将 [ContentPart.Body] (反)序列化：
//
//	[ContentPartTypeText]  => plain text (utf-8)
//	[ContentPartTypeImage] => json([ImageInfo])
type ContentPart struct {
	PartType ContentPartType
	Body     []byte
}

// NewTextContent 构造文本片段
func NewTextContent(text string) *ContentPart {
	return &ContentPart{
		PartType: ContentPartTypeText,
		Body:     []byte(text),
	}
}

// NewImageContent 构造图片片段
func NewImageContent(info ImageInfo) *ContentPart {
	body, _ := json.Marshal(info)
	return &ContentPart{
		PartType: ContentPartTypeImage,
		Body:     body,
	}
}

// Text 返回文本片段的解码结果；非 [ContentPartTypeText] 返回空字符串
func (p *ContentPart) Text() string {
	if p.PartType != ContentPartTypeText {
		return ""
	}
	return string(p.Body)
}

// Image 返回图片片段的解码结果；非 [ContentPartTypeImage] 返回零值
func (p *ContentPart) Image() ImageInfo {
	if p.PartType != ContentPartTypeImage {
		return ImageInfo{}
	}
	var info ImageInfo
	_ = json.Unmarshal(p.Body, &info)
	return info
}

// ToolCall LLM 调用 Tool 的请求
type ToolCall struct {
	// ID 工具调用id（LLM 端生成，用于关联 [ToolResult]）
	ID string `json:"id"`

	// ToolID 工具id（指向 [ToolInfo.ID]）
	ToolID uint64 `json:"tool_id"`

	// Name 工具名（从 [ToolInfo.Name] 冗余拷贝，便于调用方无需再查表）
	Name string `json:"name"`

	// Args 工具参数（JSON 对象；保留原始字节避免重复解析）
	Args json.RawMessage `json:"args,omitempty"`
}

// ToolResult 返回给 LLM 的 Tool 调用结果
type ToolResult struct {
	// ID 工具调用id（对应 [ToolCall.ID]）
	ID string `json:"id"`

	// ToolID 工具id（对应 [ToolCall.ToolID]）
	ToolID uint64 `json:"tool_id"`

	// Result 工具执行结果（自由文本或 JSON，由 Tool 自行决定）
	Result string `json:"result"`
}

// ToolInfo 工具描述
type ToolInfo struct {
	// ID 工具id
	ID uint64 `json:"id"`

	// Name 工具名（暴露给 LLM 的人类可读标识）
	Name string `json:"name"`

	// Description 工具描述（给 LLM 看的功能说明；为空时省略）
	Description string `json:"description,omitempty"`

	// Schema 工具参数 JSON Schema
	//
	// https://json-schema.org
	Schema string `json:"schema"`
}

// ImageInfo 图片元数据
type ImageInfo struct {
	// URL 图片地址（http(s):// 或 data: URI）
	URL string `json:"url"`

	// MIME 媒体类型，如 "image/png"
	MIME string `json:"mime,omitempty"`

	// Width 像素宽
	Width uint32 `json:"width,omitempty"`

	// Height 像素高
	Height uint32 `json:"height,omitempty"`
}
