package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)
// fakeLLM 是测试用的 [llm.LLM] 实现。
// 不会触发任何外部 HTTP 调用，仅用于验证 [Chat] 的协议透传与错误处理。
type fakeLLM struct {
	// Generate 行为
	generateResp *llm.Message
	generateErr  error

	// GenerateWithStream 行为
	streamChunks []llm.StreamChunk
	streamErr    error

	// 录下的最近一次请求，便于断言透传字段
	lastGenReq llm.GenerateRequest
	lastStrReq llm.GenerateRequest

	// 调用次数
	genCalls int
	strCalls int
}

func (f *fakeLLM) Generate(_ context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	f.genCalls++
	f.lastGenReq = req
	if f.generateErr != nil {
		return nil, f.generateErr
	}
	return f.generateResp, nil
}

func (f *fakeLLM) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	f.strCalls++
	f.lastStrReq = req
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	chunks := f.streamChunks
	w := asyncrw.NewAsyncWriter[llm.StreamChunk](16)
	go func() {
		defer w.Close()
		for _, ch := range chunks {
			if err := w.Send(ctx, ch); err != nil {
				return
			}
		}
	}()
	return w.ToReader(), nil
}

// DefaultConfig 是 [llm.LLM] 接口要求的占位实现；fakeLLM 不需要真实的默认配置。
func (f *fakeLLM) DefaultConfig() (string, config.LLMConfig) {
	return "fake", config.LLMConfig{}
}

// ---------- Reply ----------

func TestChat_Reply_AssistantText(t *testing.T) {
	fake := &fakeLLM{generateResp: llm.NewAssistantMessage("你好，我是助手")}
	c := NewChat(fake, ChatOptions{System: "你很有礼貌"})

	got, gotUsage, err := c.Reply(context.Background(), "你好")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got != "你好，我是助手" {
		t.Errorf("got %q, want %q", got, "你好，我是助手")
	}
	if gotUsage != nil {
		t.Errorf("Usage = %+v, want nil (LLM 未返回 usage)", gotUsage)
	}
	// 透传校验
	if fake.lastGenReq.System != "你很有礼貌" {
		t.Errorf("System = %q, want %q", fake.lastGenReq.System, "你很有礼貌")
	}
	if fake.lastGenReq.Input == nil {
		t.Fatal("Input 为 nil")
	}
	if fake.lastGenReq.Input.MsgType != llm.MessageTypeUserInput {
		t.Errorf("Input.MsgType = %s, want user", fake.lastGenReq.Input.MsgType)
	}
	if fake.lastGenReq.Input.Text() != "你好" {
		t.Errorf("Input.Text = %q, want %q", fake.lastGenReq.Input.Text(), "你好")
	}
	if fake.genCalls != 1 {
		t.Errorf("Generate 调用次数 = %d, want 1", fake.genCalls)
	}
}

func TestChat_Reply_PropagatesUsage(t *testing.T) {
	want := &llm.Usage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33}
	fake := &fakeLLM{generateResp: &llm.Message{
		MsgType: llm.MessageTypeAssistant,
		Content: []*llm.ContentPart{llm.NewTextContent("hi")},
		Usage:   want,
	}}
	c := NewChat(fake, ChatOptions{})

	_, gotUsage, err := c.Reply(context.Background(), "x")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if gotUsage != want {
		t.Errorf("Usage = %+v, want %+v", gotUsage, want)
	}
}

func TestChat_Reply_EmptySystem(t *testing.T) {
	fake := &fakeLLM{generateResp: llm.NewAssistantMessage("ok")}
	c := NewChat(fake, ChatOptions{}) // system 留空

	if _, _, err := c.Reply(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if fake.lastGenReq.System != "" {
		t.Errorf("System = %q, want empty", fake.lastGenReq.System)
	}
}

func TestChat_Reply_EmptyPrompt(t *testing.T) {
	c := NewChat(&fakeLLM{}, ChatOptions{})
	_, _, err := c.Reply(context.Background(), "")
	if !errors.Is(err, ErrEmptyPrompt) {
		t.Errorf("err = %v, want ErrEmptyPrompt", err)
	}
}

func TestChat_Reply_LLMError(t *testing.T) {
	upstream := errors.New("llm boom")
	fake := &fakeLLM{generateErr: upstream}
	c := NewChat(fake, ChatOptions{})

	_, _, err := c.Reply(context.Background(), "x")
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want 透传 upstream", err)
	}
}

func TestChat_Reply_ToolCallNotSupported(t *testing.T) {
	fake := &fakeLLM{
		generateResp: llm.NewToolCallMessage(
			&llm.ToolCall{ID: "c1", Name: "get_weather"},
			&llm.ToolCall{ID: "c2", Name: "get_time"},
		),
	}
	c := NewChat(fake, ChatOptions{})

	_, _, err := c.Reply(context.Background(), "x")
	var toolErr *ErrToolCallNotSupported
	if !errors.As(err, &toolErr) {
		t.Fatalf("err = %v, want *ErrToolCallNotSupported", err)
	}
	if len(toolErr.Calls) != 2 || toolErr.Calls[0] != "get_weather" || toolErr.Calls[1] != "get_time" {
		t.Errorf("Calls = %v, want [get_weather get_time]", toolErr.Calls)
	}
}

func TestChat_Reply_UnexpectedMessageType(t *testing.T) {
	// 模拟 SDK 返回了一个非 Assistant / ToolCall 的 MessageType。
	// 单轮 agent 应明确报错而不是默默吞掉。
	fake := &fakeLLM{
		generateResp: &llm.Message{MsgType: llm.MessageTypeToolResult},
	}
	c := NewChat(fake, ChatOptions{})

	_, _, err := c.Reply(context.Background(), "x")
	if err == nil {
		t.Fatal("应报错")
	}
	if errors.Is(err, ErrEmptyPrompt) {
		t.Errorf("err = %v, want 不等于 ErrEmptyPrompt", err)
	}
}

// ---------- ReplyStream ----------

func TestChat_ReplyStream_TextChunks(t *testing.T) {
	fake := &fakeLLM{
		streamChunks: []llm.StreamChunk{
			{Type: llm.StreamChunkTypeText, Text: "你"},
			{Type: llm.StreamChunkTypeText, Text: "好"},
			{Type: llm.StreamChunkTypeFinish, FinishReason: llm.FinishReasonStop, Usage: &llm.Usage{
				PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
			}},
		},
	}
	c := NewChat(fake, ChatOptions{System: "sys"})

	reader, err := c.ReplyStream(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}

	var (
		text         string
		gotFinish    llm.FinishReason
		gotUsage     *llm.Usage
		gotToolCalls int
	)
	for {
		chunk, err := reader.Recv(context.Background())
		if err != nil {
			if errors.Is(err, asyncrw.ErrAsyncReaderClosed) {
				break
			}
			t.Fatalf("Recv: %v", err)
		}
		switch chunk.Type {
		case llm.StreamChunkTypeText:
			text += chunk.Text
		case llm.StreamChunkTypeToolCall:
			gotToolCalls++
		case llm.StreamChunkTypeFinish:
			gotFinish = chunk.FinishReason
			gotUsage = chunk.Usage
		}
	}

	if text != "你好" {
		t.Errorf("text = %q, want %q", text, "你好")
	}
	if gotFinish != llm.FinishReasonStop {
		t.Errorf("finishReason = %q, want %q", gotFinish, llm.FinishReasonStop)
	}
	if gotUsage == nil || gotUsage.TotalTokens != 3 {
		t.Errorf("usage = %+v, want TotalTokens=3", gotUsage)
	}
	if gotToolCalls != 0 {
		t.Errorf("ToolCall 计数 = %d, want 0", gotToolCalls)
	}

	// 透传校验
	if fake.lastStrReq.System != "sys" {
		t.Errorf("System = %q, want sys", fake.lastStrReq.System)
	}
	if fake.lastStrReq.Input == nil || fake.lastStrReq.Input.Text() != "hi" {
		t.Errorf("Input 透传错误: %+v", fake.lastStrReq.Input)
	}
}

func TestChat_ReplyStream_EmptyPrompt(t *testing.T) {
	c := NewChat(&fakeLLM{}, ChatOptions{})
	_, err := c.ReplyStream(context.Background(), "")
	if !errors.Is(err, ErrEmptyPrompt) {
		t.Errorf("err = %v, want ErrEmptyPrompt", err)
	}
}

func TestChat_ReplyStream_LLMError(t *testing.T) {
	upstream := errors.New("stream boom")
	fake := &fakeLLM{streamErr: upstream}
	c := NewChat(fake, ChatOptions{})

	_, err := c.ReplyStream(context.Background(), "x")
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want 透传 upstream", err)
	}
}
