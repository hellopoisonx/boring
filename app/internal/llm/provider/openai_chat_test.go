package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// mockChatHandler 返回一个最小可用的 Chat Completions 响应（文本）。
func mockChatHandler(t *testing.T, responseBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		// 验证请求是合法 JSON
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		// 验证 stream 字段（如果开了流式则为 true）
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}
}

// TestOpenAIChat_Generate 验证非流式文本响应被正确解析为 MessageTypeAssistant。
func TestOpenAIChat_Generate(t *testing.T) {
	resp := `{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"content": "你好，世界！",
				"refusal": null
			}
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	srv := httptest.NewServer(mockChatHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewOpenAIChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIChat,
		Model:   config.Model{ID: "gpt-4o", MaxResponse: 1024},
	})

	msg, err := p.Generate(context.Background(), llm.GenerateRequest{
		System: "你是助手",
		Input:  llm.NewUserMessage(llm.NewTextContent("hello")),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.MsgType != llm.MessageTypeAssistant {
		t.Errorf("MsgType = %s, want Assistant", msg.MsgType)
	}
	if got := msg.Text(); got != "你好，世界！" {
		t.Errorf("Text = %q", got)
	}
}

// TestOpenAIChat_GenerateWithToolCall 验证工具调用被解析为 MessageTypeToolCall。
func TestOpenAIChat_GenerateWithToolCall(t *testing.T) {
	resp := `{
		"id": "chatcmpl-2",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": null,
				"refusal": null,
				"tool_calls": [{
					"id": "call_abc",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"SF\"}"
					}
				}]
			}
		}],
		"usage": {"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}
	}`
	srv := httptest.NewServer(mockChatHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewOpenAIChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIChat,
		Model:   config.Model{ID: "gpt-4o"},
	})

	msg, err := p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("天气如何？")),
		Tools: []llm.ToolInfo{{Name: "get_weather", Schema: `{"type":"object","properties":{"city":{"type":"string"}}}`}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.MsgType != llm.MessageTypeToolCall {
		t.Fatalf("MsgType = %s, want ToolCall", msg.MsgType)
	}
	calls := msg.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].ID != "call_abc" {
		t.Errorf("ID = %q", calls[0].ID)
	}
	if calls[0].Name != "get_weather" {
		t.Errorf("Name = %q", calls[0].Name)
	}
	if string(calls[0].Args) != `{"city":"SF"}` {
		t.Errorf("Args = %s", calls[0].Args)
	}
}

// TestOpenAIChat_Stream 验证流式产出文本 chunk 与 finish chunk。
func TestOpenAIChat_Stream(t *testing.T) {
	// 流式响应：分多个 SSE 块返回文本
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"你"},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n") + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewOpenAIChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIChat,
		Model:   config.Model{ID: "gpt-4o"},
	})

	reader, err := p.GenerateWithStream(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("hi")),
	})
	if err != nil {
		t.Fatalf("GenerateWithStream: %v", err)
	}

	var (
		gotText   strings.Builder
		gotFinish *llm.StreamChunk
	)
	for {
		chunk, err := reader.Recv(context.Background())
		if err != nil {
			if err.Error() == "async reader has been closed" {
				break
			}
			t.Fatalf("Recv: %v", err)
		}
		switch chunk.Type {
		case llm.StreamChunkTypeText:
			gotText.WriteString(chunk.Text)
		case llm.StreamChunkTypeFinish:
			gotFinish = &chunk
		}
	}
	if got := gotText.String(); got != "你好" {
		t.Errorf("stream text = %q, want 你好", got)
	}
	if gotFinish == nil {
		t.Fatal("未收到 finish chunk")
	}
	if gotFinish.FinishReason != llm.FinishReasonStop {
		t.Errorf("FinishReason = %s, want Stop", gotFinish.FinishReason)
	}
}
