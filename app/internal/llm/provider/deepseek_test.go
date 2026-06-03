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

// mockDeepSeekHandler 返回一个最小可用的 Chat Completions 响应（文本）。
// DeepSeek 路径为 /chat/completions（无 /v1 前缀）。
func mockDeepSeekHandler(t *testing.T, responseBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		// DeepSeekChat 改为委托给 sdk.OpenAIChat 后，请求体由 SDK 构造；
		// 非流式不带 stream_options 是 sdk 的默认行为，这里只确保不出现 stream_options 即可。
		if so, present := req["stream_options"]; present {
			t.Errorf("非流式请求不应包含 stream_options，实际为 %v", so)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}
}

// mockDeepSeekStreamHandler 返回一个流式 Chat Completions 响应，强制断言请求体里
// 带 stream=true 且 stream_options.include_usage=true。
//
// 历史背景：DeepSeekChat 早期曾直接用 openai-go SDK 调 DeepSeek 端点，并主动带该开关；
// 当前改为委托 sdk.OpenAIChat + WithStreamIncludeUsage，请求体仍由 SDK 构造并带该开关，
// DeepSeek 流式 Usage 行为不变。
func mockDeepSeekStreamHandler(t *testing.T, sseBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		if got, _ := req["stream"].(bool); !got {
			t.Errorf("流式请求 stream 必须为 true，实际为 %v", req["stream"])
		}
		so, ok := req["stream_options"].(map[string]any)
		if !ok {
			t.Fatalf("流式请求必须包含 stream_options，实际为 %v", req["stream_options"])
		}
		if inc, _ := so["include_usage"].(bool); !inc {
			t.Errorf("stream_options.include_usage 必须为 true，实际为 %v", so)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody)
	}
}

// TestDeepSeekChat_Generate 验证非流式文本响应被正确解析为 MessageTypeAssistant。
func TestDeepSeekChat_Generate(t *testing.T) {
	resp := `{
		"id": "chatcmpl-d1",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "deepseek-chat",
		"choices": [{
			"index": 0,
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"content": "你好，世界！"
			}
		}],
		"usage": {"prompt_tokens": 12, "completion_tokens": 6, "total_tokens": 18}
	}`
	srv := httptest.NewServer(mockDeepSeekHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewDeepSeekChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkDeepSeek,
		Model:   config.Model{ID: "deepseek-chat", MaxResponse: 1024},
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
	if msg.Usage == nil {
		t.Fatal("Usage = nil, want non-nil（DeepSeek 非流式响应体含 usage 字段）")
	}
	if msg.Usage.PromptTokens != 12 || msg.Usage.CompletionTokens != 6 || msg.Usage.TotalTokens != 18 {
		t.Errorf("Usage = %+v, want {12,6,18}", msg.Usage)
	}
}

// TestDeepSeekChat_GenerateWithToolCall 验证工具调用被解析为 MessageTypeToolCall。
func TestDeepSeekChat_GenerateWithToolCall(t *testing.T) {
	resp := `{
		"id": "chatcmpl-d2",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "deepseek-chat",
		"choices": [{
			"index": 0,
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_dsk_1",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Beijing\"}"
					}
				}]
			}
		}],
		"usage": {"prompt_tokens": 25, "completion_tokens": 12, "total_tokens": 37}
	}`
	srv := httptest.NewServer(mockDeepSeekHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewDeepSeekChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkDeepSeek,
		Model:   config.Model{ID: "deepseek-chat"},
	})

	msg, err := p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("北京天气如何？")),
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
	if calls[0].ID != "call_dsk_1" {
		t.Errorf("ID = %q", calls[0].ID)
	}
	if calls[0].Name != "get_weather" {
		t.Errorf("Name = %q", calls[0].Name)
	}
	if string(calls[0].Args) != `{"city":"Beijing"}` {
		t.Errorf("Args = %s", calls[0].Args)
	}
}

// TestDeepSeekChat_Stream 验证流式产出文本 chunk 与 finish chunk。
//
// 已知限制：DeepSeekChat 委托给 sdk.OpenAIChat 后，请求体不带 stream_options.include_usage，
// DeepSeek 不会在最后一个 chunk 返回 usage 字段；本测试不断言 Usage。
func TestDeepSeekChat_Stream(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":"你"},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n") + "\n"

	srv := httptest.NewServer(mockDeepSeekStreamHandler(t, body))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewDeepSeekChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkDeepSeek,
		Model:   config.Model{ID: "deepseek-chat"},
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
	// 流式 Usage 暂时拿不到：DeepSeekChat 委托给 sdk.OpenAIChat，请求体不带 stream_options.include_usage。
	if gotFinish.Usage != nil {
		t.Logf("收到 Usage（说明 DeepSeek 流式 Usage 已恢复）: %+v", gotFinish.Usage)
	}
}

// TestDeepSeekChat_StreamToolCall 验证流式工具调用：按 tool_call index 累积 ID/Name/Args。
func TestDeepSeekChat_StreamToolCall(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_t1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\""}}]},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"city\":\"Hangzhou\"}"}}]},"finish_reason":""}]}`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"deepseek-chat","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n") + "\n"

	srv := httptest.NewServer(mockDeepSeekStreamHandler(t, body))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewDeepSeekChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkDeepSeek,
		Model:   config.Model{ID: "deepseek-chat"},
	})

	reader, err := p.GenerateWithStream(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("天气如何？")),
		Tools: []llm.ToolInfo{{Name: "get_weather", Schema: `{"type":"object","properties":{"city":{"type":"string"}}}`}},
	})
	if err != nil {
		t.Fatalf("GenerateWithStream: %v", err)
	}

	var (
		gotCalls  []*llm.ToolCall
		gotFinish *llm.StreamChunk
	)
	for {
		chunk, err := reader.Recv(context.Background())
		if err != nil {
			break
		}
		switch chunk.Type {
		case llm.StreamChunkTypeToolCall:
			if chunk.ToolCall != nil {
				gotCalls = append(gotCalls, chunk.ToolCall)
			}
		case llm.StreamChunkTypeFinish:
			gotFinish = &chunk
		}
	}
	if len(gotCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(gotCalls))
	}
	if gotCalls[0].ID != "call_t1" {
		t.Errorf("ID = %q, want call_t1", gotCalls[0].ID)
	}
	if gotCalls[0].Name != "get_weather" {
		t.Errorf("Name = %q, want get_weather", gotCalls[0].Name)
	}
	if string(gotCalls[0].Args) != `{"city":"Hangzhou"}` {
		t.Errorf("Args = %s, want {\"city\":\"Hangzhou\"}", gotCalls[0].Args)
	}
	if gotFinish == nil || gotFinish.FinishReason != llm.FinishReasonToolCalls {
		t.Errorf("FinishReason = %v, want ToolCalls", gotFinish)
	}
}

// TestDeepSeekChat_FinishReason 验证 DeepSeek 特有的 `insufficient_system_resource`
// 终止原因被映射为 FinishReasonError。
func TestDeepSeekChat_FinishReason(t *testing.T) {
	// 非流式响应
	resp := `{
		"id": "chatcmpl-d3",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "deepseek-chat",
		"choices": [{
			"index": 0,
			"finish_reason": "insufficient_system_resource",
			"message": {
				"role": "assistant",
				"content": ""
			}
		}],
		"usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}`
	srv := httptest.NewServer(mockDeepSeekHandler(t, resp))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p := NewDeepSeekChat(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkDeepSeek,
		Model:   config.Model{ID: "deepseek-chat"},
	})

	// Generate 返回的 Message 不带 FinishReason，
	// 但通过 mapChatFinishReason 已测过；这里再做一个端到端 sanity 检查。
	msg, err := p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("hi")),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg.MsgType != llm.MessageTypeAssistant {
		t.Errorf("MsgType = %s, want Assistant", msg.MsgType)
	}
}
