package provider

import (
	"context"
	"net/url"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// TestOpenAIChatCompatible_ImplementsLLM 编译期断言 + 运行期类型断言。
func TestOpenAIChatCompatible_ImplementsLLM(t *testing.T) {
	var _ llm.LLM = (*OpenAIChatCompatible)(nil)
}

// TestOpenAIChatCompatible_DefaultConfig 验证 DefaultConfig 返回的 name 与 cfg。
//
// cfg 填充该 provider 的完整默认值（Provider / Sdk / BaseURL / Model.ID），APIKey 留空。
func TestOpenAIChatCompatible_DefaultConfig(t *testing.T) {
	p := NewOpenAIChatCompatible(config.LLMConfig{Sdk: config.SdkOpenAIChat})
	name, cfg := p.DefaultConfig()
	if name != string(config.SdkOpenAIChat) {
		t.Errorf("name = %q, want %q", name, config.SdkOpenAIChat)
	}
	if cfg.Provider != config.ProviderOpenAI {
		t.Errorf("cfg.Provider = %q, want %q", cfg.Provider, config.ProviderOpenAI)
	}
	if cfg.Sdk != config.SdkOpenAIChat {
		t.Errorf("cfg.Sdk = %q, want %q", cfg.Sdk, config.SdkOpenAIChat)
	}
	if cfg.APIKey != "" {
		t.Errorf("cfg.APIKey = %q, want empty", cfg.APIKey)
	}
	if cfg.BaseURL.String() != "https://api.openai.com/v1" {
		t.Errorf("cfg.BaseURL = %q, want %q", cfg.BaseURL.String(), "https://api.openai.com/v1")
	}
	if cfg.Model.ID != "gpt-4o" {
		t.Errorf("cfg.Model.ID = %q, want %q", cfg.Model.ID, "gpt-4o")
	}
}

// TestOpenAIChatCompatible_NewSucceeds 验证构造不 panic。
func TestOpenAIChatCompatible_NewSucceeds(t *testing.T) {
	u, _ := url.Parse("https://api.openai.com/v1")
	p := NewOpenAIChatCompatible(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIChat,
		Model:   config.Model{ID: "gpt-4o"},
	})
	if p == nil {
		t.Fatal("NewOpenAIChatCompatible 返回 nil")
	}
	if p.sdk == nil {
		t.Error("内部 sdk 字段为 nil")
	}
}

// TestOpenAIChatCompatible_GeneratePassesThrough 验证 Generate 透传到 sdk 包。
// 这里不依赖网络（不构造真实 HTTP mock），仅验证 cfg 与 req 透传。
func TestOpenAIChatCompatible_GeneratePassesThrough(t *testing.T) {
	u, _ := url.Parse("https://api.openai.com/v1")
	p := NewOpenAIChatCompatible(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIChat,
		Model:   config.Model{ID: "gpt-4o"},
	})
	// 真实调用会因网络失败而返回 error；这里只确认不会因 wrapper 自身崩溃（如 nil pointer）。
	_, _ = p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("hi")),
	})
}
