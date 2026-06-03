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

// mockResponsesHandler 返回一个 Responses API 风格的 JSON 响应（文本）。
func mockResponsesHandler(t *testing.T, responseBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		// 验证 system 走了 instructions（不在 messages）
		if _, ok := req["instructions"]; !ok {
			t.Errorf("expected instructions 字段")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}
}

// TestOpenAIResponse_Generate 验证 Responses 协议的非流式解析。
func TestOpenAIResponse_Generate(t *testing.T) {
	resp := `{
		"id": "resp_1",
		"object": "response",
		"status": "completed",
		"created_at": 1700000000,
		"model": "gpt-4o",
		"instructions": "你是助手",
		"output": [{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"status": "completed",
			"content": [{
				"type": "output_text",
				"text": "hi from responses",
				"annotations": []
			}]
		}],
		"parallel_tool_calls": true,
		"temperature": 1.0,
		"tool_choice": "auto",
		"tools": [],
		"top_p": 1.0,
		"error": null,
		"incomplete_details": null,
		"metadata": null,
		"usage": {
			"input_tokens": 5,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens": 3,
			"output_tokens_details": {"reasoning_tokens": 0},
			"total_tokens": 8
		}
	}`
	srv := httptest.NewServer(mockResponsesHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewOpenAIResponse(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIResponse,
		Model:   config.Model{ID: "gpt-4o"},
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
	if got := msg.Text(); got != "hi from responses" {
		t.Errorf("Text = %q", got)
	}
}

// TestOpenAIResponse_Stream 验证 Responses 事件流被正确翻译为 StreamChunk。
func TestOpenAIResponse_Stream(t *testing.T) {
	body := strings.Join([]string{
		// event: response.created
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"r1","object":"response","status":"in_progress","created_at":1,"model":"gpt-4o","instructions":"","output":[],"parallel_tool_calls":true,"temperature":1.0,"tool_choice":"auto","tools":[],"top_p":1.0,"incomplete_details":null,"metadata":null,"usage":null}}`,
		``,
		// text delta
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"你好","logprobs":[]}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" world","logprobs":[]}`,
		``,
		// completed
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"r1","object":"response","status":"completed","created_at":1,"model":"gpt-4o","instructions":"","output":[],"parallel_tool_calls":true,"temperature":1.0,"tool_choice":"auto","tools":[],"top_p":1.0,"incomplete_details":null,"metadata":null,"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":3}}}`,
		``,
	}, "\n") + "\n"


	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewOpenAIResponse(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIResponse,
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
			t.Logf("stream end: %v", err)
			break
		}
		switch chunk.Type {
		case llm.StreamChunkTypeText:
			gotText.WriteString(chunk.Text)
		case llm.StreamChunkTypeFinish:
			gotFinish = &chunk
		}
	}
	if got := gotText.String(); got != "你好 world" {
		t.Errorf("stream text = %q", got)
	}
	if gotFinish == nil {
		t.Fatal("未收到 finish chunk")
	}
	if gotFinish.FinishReason != llm.FinishReasonStop {
		t.Errorf("FinishReason = %s", gotFinish.FinishReason)
	}
}
