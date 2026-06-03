// Package agent 实现单轮 / 多轮 chat agent。
//
// 当前版本只提供 [Chat]：1 user message → 1 assistant message。
// 不维护会话历史、不做上下文压缩、不读取跨会话记忆。
// 如需多轮 / 工具调用，请直接使用 [llm.LLM] 接口自行编排。
package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// Chat 是单轮对话 agent。
//
// 一次调用接收 1 条用户输入，返回 1 条 assistant 回复。
// 不维护会话历史、不做上下文压缩、不读取跨会话记忆。
// 多轮 / 工具调用等场景请直接使用 [llm.LLM] 接口。
type Chat struct {
	llm    llm.LLM
	system string
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

// Reply 同步执行一次单轮对话：把 prompt 作为唯一 user 输入，返回 assistant 的纯文本回复与 token 用量。
//
// LLM 决定调用工具时返回 [*ErrToolCallNotSupported]（[errors.As] 识别）。
// usage 可能为 nil（provider 未在响应中带回 usage；如某些 OpenAI 兼容服务不返回 usage 字段）。
func (c *Chat) Reply(ctx context.Context, prompt string) (string, *llm.Usage, error) {
	if prompt == "" {
		return "", nil, ErrEmptyPrompt
	}
	msg, err := c.llm.Generate(ctx, llm.GenerateRequest{
		System: c.system,
		Input:  llm.NewUserMessage(llm.NewTextContent(prompt)),
	})
	if err != nil {
		return "", nil, err
	}
	switch msg.MsgType {
	case llm.MessageTypeAssistant:
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

// ReplyStream 流式执行一次单轮对话。
//
// 返回的 [asyncrw.AsyncReader] 拉取顺序为：
//   - 0..N 条 [llm.StreamChunkTypeText]：[Text] 拼成 assistant 的最终正文
//   - 0..N 条 [llm.StreamChunkTypeToolCall]：单轮 agent 不处理，调用方按需取用
//   - 1 条 [llm.StreamChunkTypeFinish]：[FinishReason] / [Usage]
//
// 流正常结束时 [asyncrw.AsyncReader.Recv] 返回 [asyncrw.ErrAsyncReaderClosed]。
func (c *Chat) ReplyStream(ctx context.Context, prompt string) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	if prompt == "" {
		return nil, ErrEmptyPrompt
	}
	return c.llm.GenerateWithStream(ctx, llm.GenerateRequest{
		System: c.system,
		Input:  llm.NewUserMessage(llm.NewTextContent(prompt)),
	})
}
