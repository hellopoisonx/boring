package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func main() {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"container":null,"usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":null,"output_tokens":0,"output_tokens_details":null,"inference_geo":"","server_tool_use":null,"service_tier":"standard"}}}`,
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
	}, "\n") + "\n" // 末尾补一个 newline

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 模拟真实服务器：先 flush headers
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(body))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	// 同时手动测试 sse decoder
	fmt.Println("=== bufio.ScanLines behavior ===")
	scn := bufio.NewScanner(strings.NewReader(body))
	scn.Buffer(nil, 1024*1024)
	for scn.Scan() {
		fmt.Printf("LINE: %q\n", scn.Text())
	}

	fmt.Println("=== SDK stream ===")
	client := anthropic.NewClient(
		option.WithBaseURL(srv.URL),
		option.WithAPIKey("test"),
	)
	stream := client.Messages.NewStreaming(context.Background(), anthropic.MessageNewParams{
		Model:     "m",
		MaxTokens: 100,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
	for stream.Next() {
		ev := stream.Current()
		fmt.Printf("event type=%s\n", ev.Type)
	}
	if err := stream.Err(); err != nil {
		fmt.Println("ERR:", err)
	}
}
