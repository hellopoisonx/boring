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

func mockAnthropicHandler(t *testing.T, responseBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		if _, hasSystem := req["system"]; !hasSystem {
			t.Logf("note: 请求未包含 system 字段（调用方未设置）")
		}
		if _, ok := req["max_tokens"]; !ok {
			t.Errorf("expected max_tokens 字段")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}
}

func TestAnthropicMessage_Generate(t *testing.T) {
	resp := `{
		"id": "msg_1",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{"type": "text", "text": "Hello from Claude!", "citations": []}
		],
		"stop_reason": "end_turn",
		"stop_sequence": null,
		"container": null,
		"usage": {
			"input_tokens": 10,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 0,
			"cache_creation": null,
			"output_tokens": 5,
			"output_tokens_details": null,
			"inference_geo": "",
			"server_tool_use": null,
			"service_tier": "standard"
		}
	}`
	srv := httptest.NewServer(mockAnthropicHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewAnthropicMessage(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkAnthropicMessage,
		Model:   config.Model{ID: "claude-3-5-sonnet-20241022"},
	})

	msg, err := p.Generate(context.Background(), llm.GenerateRequest{
		System: "你是助手",
		Input:  llm.NewUserMessage(llm.NewTextContent("hi")),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.MsgType != llm.MessageTypeAssistant {
		t.Errorf("MsgType = %s", msg.MsgType)
	}
	if got := msg.Text(); got != "Hello from Claude!" {
		t.Errorf("Text = %q", got)
	}
}

func TestAnthropicMessage_ToolCall(t *testing.T) {
	resp := `{
		"id": "msg_2",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-20241022",
		"content": [
			{"type": "text", "text": "我来查一下", "citations": []},
			{"type": "tool_use", "id": "toolu_01", "name": "get_weather", "input": {"city": "Beijing"}, "caller": null}
		],
		"stop_reason": "tool_use",
		"stop_sequence": null,
		"container": null,
		"usage": {
			"input_tokens": 10, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
			"cache_creation": null, "output_tokens": 5, "output_tokens_details": null,
			"inference_geo": "", "server_tool_use": null, "service_tier": "standard"
		}
	}`
	srv := httptest.NewServer(mockAnthropicHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewAnthropicMessage(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkAnthropicMessage,
		Model:   config.Model{ID: "claude-3-5-sonnet-20241022"},
	})

	msg, err := p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("天气如何？")),
		Tools: []llm.ToolInfo{{Name: "get_weather", Schema: `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`}},
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
	if calls[0].ID != "toolu_01" {
		t.Errorf("ID = %q", calls[0].ID)
	}
	if calls[0].Name != "get_weather" {
		t.Errorf("Name = %q", calls[0].Name)
	}
	if string(calls[0].Args) != `{"city":"Beijing"}` {
		t.Errorf("Args = %s", calls[0].Args)
	}
}

// TestAnthropicMessage_Stream 使用极简 body 验证流式解析。
// 详细事件链由 TestAnthropicMessage_Stream_Full 覆盖。
func TestAnthropicMessage_Stream(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"container":null,"usage":{}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"container":null},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewAnthropicMessage(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkAnthropicMessage,
		Model:   config.Model{ID: "claude-3-5-sonnet-20241022"},
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
			break
		}
		switch chunk.Type {
		case llm.StreamChunkTypeText:
			gotText.WriteString(chunk.Text)
		case llm.StreamChunkTypeFinish:
			gotFinish = &chunk
		}
	}
	if got := gotText.String(); got != "hi" {
		t.Errorf("stream text = %q", got)
	}
	if gotFinish == nil {
		t.Fatal("未收到 finish chunk")
	}
	if gotFinish.FinishReason != llm.FinishReasonStop {
		t.Errorf("FinishReason = %s", gotFinish.FinishReason)
	}
}
