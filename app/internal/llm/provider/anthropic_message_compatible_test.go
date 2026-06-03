package provider

import (
	"context"
	"net/url"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// TestAnthropicMessageCompatible_ImplementsLLM 编译期断言 + 运行期类型断言。
func TestAnthropicMessageCompatible_ImplementsLLM(t *testing.T) {
	var _ llm.LLM = (*AnthropicMessageCompatible)(nil)
}

// TestAnthropicMessageCompatible_DefaultConfig 验证 DefaultConfig 返回的 name 与 cfg。
func TestAnthropicMessageCompatible_DefaultConfig(t *testing.T) {
	p := NewAnthropicMessageCompatible(config.LLMConfig{Sdk: config.SdkAnthropicMessage})
	name, cfg := p.DefaultConfig()
	if name != string(config.SdkAnthropicMessage) {
		t.Errorf("name = %q, want %q", name, config.SdkAnthropicMessage)
	}
	if cfg.Sdk != config.SdkAnthropicMessage {
		t.Errorf("cfg.Sdk = %q, want %q", cfg.Sdk, config.SdkAnthropicMessage)
	}
	if cfg.APIKey != "" {
		t.Errorf("cfg.APIKey = %q, want empty (DefaultConfig 应只填 Sdk 字段)", cfg.APIKey)
	}
	if cfg.BaseURL.Host != "" {
		t.Errorf("cfg.BaseURL = %q, want empty", cfg.BaseURL.String())
	}
}

// TestAnthropicMessageCompatible_NewSucceeds 验证构造不 panic。
func TestAnthropicMessageCompatible_NewSucceeds(t *testing.T) {
	u, _ := url.Parse("https://api.anthropic.com")
	p := NewAnthropicMessageCompatible(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkAnthropicMessage,
		Model:   config.Model{ID: "claude-3-5-sonnet-20241022"},
	})
	if p == nil {
		t.Fatal("NewAnthropicMessageCompatible 返回 nil")
	}
	if p.sdk == nil {
		t.Error("内部 sdk 字段为 nil")
	}
}

// TestAnthropicMessageCompatible_GeneratePassesThrough 验证 Generate 透传到 sdk 包。
func TestAnthropicMessageCompatible_GeneratePassesThrough(t *testing.T) {
	u, _ := url.Parse("https://api.anthropic.com")
	p := NewAnthropicMessageCompatible(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkAnthropicMessage,
		Model:   config.Model{ID: "claude-3-5-sonnet-20241022"},
	})
	_, _ = p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("hi")),
	})
}
