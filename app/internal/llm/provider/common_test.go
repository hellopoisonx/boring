package provider

import (
	"errors"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// TestParseToolSchema 验证 schema 解析与边界情况。
func TestParseToolSchema(t *testing.T) {
	t.Run("空 schema 走默认 object", func(t *testing.T) {
		got, err := parseToolSchema("")
		if err != nil {
			t.Fatalf("意外错误: %v", err)
		}
		if got["type"] != "object" {
			t.Errorf("空 schema 应等价于 {type: object}，得到 %v", got)
		}
	})
	t.Run("合法 JSON 透传", func(t *testing.T) {
		in := `{"type":"object","properties":{"x":{"type":"number"}}}`
		got, err := parseToolSchema(in)
		if err != nil {
			t.Fatalf("意外错误: %v", err)
		}
		if got["type"] != "object" {
			t.Errorf("type 字段被改: %v", got["type"])
		}
	})
	t.Run("非法 JSON 报错", func(t *testing.T) {
		if _, err := parseToolSchema("{invalid}"); err == nil {
			t.Fatal("应当返回错误")
		}
	})
}

// TestWrapError 验证错误包装路径。
func TestWrapError(t *testing.T) {
	cfg := config.LLMConfig{Sdk: config.SdkOpenAIChat}

	t.Run("nil 透传", func(t *testing.T) {
		if wrapError(cfg, nil) != nil {
			t.Error("nil 应返回 nil")
		}
	})

	t.Run("已经是 llm.Error 避免重复包装", func(t *testing.T) {
		orig := &llm.Error{Provider: "x", Message: "y"}
		out := wrapError(cfg, orig)
		if out != orig {
			t.Errorf("应原样返回，got %v", out)
		}
	})

	t.Run("普通 error 包装为 StatusCode=0", func(t *testing.T) {
		out := wrapError(cfg, errors.New("net fail"))
		e, ok := out.(*llm.Error)
		if !ok {
			t.Fatalf("应返回 *llm.Error，得到 %T", out)
		}
		if e.StatusCode != 0 {
			t.Errorf("网络错误 StatusCode 应为 0，得到 %d", e.StatusCode)
		}
		if e.Provider != "openai-chat" {
			t.Errorf("Provider 应来自 cfg.Sdk，得到 %s", e.Provider)
		}
		if !errors.Is(out, errors.Unwrap(out)) {
			t.Error("Cause 应能通过 errors.Is 找回原 error")
		}
	})
}

// TestMapChatFinishReason 验证 openai Chat finish_reason → llm.FinishReason 映射。
func TestMapChatFinishReason(t *testing.T) {
	cases := map[string]llm.FinishReason{
		"stop":           llm.FinishReasonStop,
		"tool_calls":     llm.FinishReasonToolCalls,
		"length":         llm.FinishReasonLength,
		"content_filter": llm.FinishReasonContentFilter,
		"unknown":        llm.FinishReason("unknown"),
	}
	for in, want := range cases {
		if got := mapChatFinishReason(in); got != want {
			t.Errorf("mapChatFinishReason(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestMapResponsesStatusFields 验证 Responses status+reason → FinishReason 映射。
func TestMapResponsesStatusFields(t *testing.T) {
	cases := []struct {
		status, reason string
		want           llm.FinishReason
	}{
		{"completed", "", llm.FinishReasonStop},
		{"failed", "", llm.FinishReasonError},
		{"cancelled", "", llm.FinishReasonError},
		{"incomplete", "max_output_tokens", llm.FinishReasonLength},
		{"incomplete", "content_filter", llm.FinishReasonContentFilter},
		{"incomplete", "其他原因", llm.FinishReasonError},
		{"unknown", "", llm.FinishReason("")},
		{"", "", llm.FinishReason("")},
	}
	for _, c := range cases {
		if got := mapResponsesStatusFields(c.status, c.reason); got != c.want {
			t.Errorf("mapResponsesStatusFields(%q,%q) = %s, want %s", c.status, c.reason, got, c.want)
		}
	}
}

// TestMapAnthropicStopReason 验证 anthropic StopReason → llm.FinishReason 映射。
func TestMapAnthropicStopReason(t *testing.T) {
	cases := map[string]llm.FinishReason{
		"end_turn":      llm.FinishReasonStop,
		"tool_use":      llm.FinishReasonToolCalls,
		"max_tokens":    llm.FinishReasonLength,
		"stop_sequence": llm.FinishReasonStop,
		"refusal":       llm.FinishReasonContentFilter,
		"":              llm.FinishReason(""),
		"weird":         llm.FinishReasonError,
	}
	for in, want := range cases {
		if got := mapAnthropicStopReason(in); got != want {
			t.Errorf("mapAnthropicStopReason(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestJoinTextParts 验证多 part 拼接。
func TestJoinTextParts(t *testing.T) {
	parts := []*llm.ContentPart{
		llm.NewTextContent("hello "),
		llm.NewImageContent(llm.ImageInfo{URL: "http://x"}), // 非 text 应被忽略
		llm.NewTextContent("world"),
	}
	if got := joinTextParts(parts); got != "hello world" {
		t.Errorf("got %q", got)
	}
}

// TestUsageFromPromptCompletion 验证零值返回 nil。
func TestUsageFromPromptCompletion(t *testing.T) {
	if got := usageFromPromptCompletion(0, 0, 0); got != nil {
		t.Errorf("全零应返回 nil，got %+v", got)
	}
	u := usageFromPromptCompletion(10, 20, 30)
	if u == nil || u.PromptTokens != 10 || u.CompletionTokens != 20 || u.TotalTokens != 30 {
		t.Errorf("got %+v", u)
	}
}
