// Package agent 实现多轮 chat agent。
//
// Chat 在同一实例内维护会话历史，每次 Reply / ReplyStream 自动将
// 上下轮用户输入与 assistant 回复追加到内部 history，后续调用时透明携带。
// 不压缩上下文、不读取跨会话记忆、不主动裁剪历史。
// 如需工具调用等复杂编排，请直接使用 [llm.LLM] 接口。
//
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// Chat 是多轮对话 agent。
//
// 同一实例内自动维护会话历史，每次调用 Reply / ReplyStream 将本轮的
// user 输入与 assistant 回复追加到内部 history，后续调用时作为 [GenerateRequest.History] 传入。
// 不压缩上下文、不读取跨会话记忆。
type Chat struct {
	llm     llm.LLM
	system  string
	history []llm.Message
}

// ChatOptions 配置 [Chat]。
type ChatOptions struct {
	// System 是可选的 system 提示；留空则不下发 system 字段。
	System string
}

// NewChat 用给定的 [llm.LLM] 与选项构造 [Chat]。
func NewChat(l llm.LLM, opts ChatOptions) *Chat {
	return &Chat{
		llm:    l,
		system: opts.System,
	}
}

// ErrEmptyPrompt 是 [Chat.Reply] / [Chat.ReplyStream] 在 prompt 为空时返回的错误。
var ErrEmptyPrompt = errors.New("agent: chat: prompt 不能为空")

// ErrToolCallNotSupported 是 [Chat.Reply] 在 LLM 决定调用工具时返回的错误。
// 单轮 agent 不执行工具调用；如需工具支持请直接使用 [llm.LLM] 接口。
type ErrToolCallNotSupported struct {
	// Calls 是 LLM 发出的工具调用名列表（按 LLM 返回顺序）。
	Calls []string
}

// Error 实现 [error] 接口。
func (e *ErrToolCallNotSupported) Error() string {
	return fmt.Sprintf("agent: chat: LLM 决定调用工具，单轮 agent 不支持；tools=%v", e.Calls)
}

// Reply 同步执行一次对话：把 prompt 作为 user 输入，携带当前会话历史，返回 assistant 的纯文本回复与 token 用量。
//
// 调用成功后自动将本轮 user 消息与 assistant 回复追加到会话历史。
// LLM 决定调用工具时返回 [*ErrToolCallNotSupported]（[errors.As] 识别）。
// usage 可能为 nil（provider 未在响应中带回 usage；如某些 OpenAI 兼容服务不返回 usage 字段）。
func (c *Chat) Reply(ctx context.Context, prompt string) (string, *llm.Usage, error) {
	if prompt == "" {
		return "", nil, ErrEmptyPrompt
	}
	userMsg := llm.NewUserMessage(llm.NewTextContent(prompt))
	msg, err := c.llm.Generate(ctx, llm.GenerateRequest{
		System:  c.system,
		Input:   userMsg,
		History: c.history,
	})
	if err != nil {
		return "", nil, err
	}
	switch msg.MsgType {
	case llm.MessageTypeAssistant:
		c.history = append(c.history, *userMsg, *msg)
		return msg.Text(), msg.Usage, nil
	case llm.MessageTypeToolCall:
		calls := msg.ToolCalls()
		names := make([]string, 0, len(calls))
		for _, tc := range calls {
			names = append(names, tc.Name)
		}
		return "", msg.Usage, &ErrToolCallNotSupported{Calls: names}
	default:
		return "", msg.Usage, fmt.Errorf("agent: chat: 不期望的响应类型 %s", msg.MsgType)
	}
}

// ReplyStream 流式执行一次对话。
//
// 返回的 [asyncrw.AsyncReader] 拉取顺序为：
//   - 0..N 条 [llm.StreamChunkTypeText]：[Text] 拼成 assistant 的最终正文
//   - 0..N 条 [llm.StreamChunkTypeToolCall]：agent 不处理，调用方按需取用
//   - 1 条 [llm.StreamChunkTypeFinish]：[FinishReason] / [Usage]
//
// 流正常结束时 [asyncrw.AsyncReader.Recv] 返回 [asyncrw.ErrAsyncReaderClosed]。
// finish chunk 到达后自动将本轮 user 消息与 assistant 回复追加到会话历史。
func (c *Chat) ReplyStream(ctx context.Context, prompt string) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	if prompt == "" {
		return nil, ErrEmptyPrompt
	}
	userMsg := llm.NewUserMessage(llm.NewTextContent(prompt))
	reader, err := c.llm.GenerateWithStream(ctx, llm.GenerateRequest{
		System:  c.system,
		Input:   userMsg,
		History: c.history,
	})
	if err != nil {
		return nil, err
	}
	c.history = append(c.history, *userMsg)
	return &streamHistoryCollector{
		inner:  reader,
		onDone: func(text string) {
			c.history = append(c.history, *llm.NewAssistantMessage(text))
		},
	}, nil
}

// streamHistoryCollector 包装 AsyncReader[llm.StreamChunk]，收集流式输出文本并在 finish 时追加到 history。
type streamHistoryCollector struct {
	inner  asyncrw.AsyncReader[llm.StreamChunk]
	buf    strings.Builder
	done   bool
	onDone func(text string)
}

func (s *streamHistoryCollector) Recv(ctx context.Context) (llm.StreamChunk, error) {
	if s.done {
		return llm.StreamChunk{}, asyncrw.ErrAsyncReaderClosed
	}
	chunk, err := s.inner.Recv(ctx)
	if err != nil {
		s.done = true
		return chunk, err
	}
	switch chunk.Type {
	case llm.StreamChunkTypeText:
		s.buf.WriteString(chunk.Text)
	case llm.StreamChunkTypeFinish:
		s.done = true
		if s.onDone != nil {
			s.onDone(s.buf.String())
		}
	}
	return chunk, nil
}
