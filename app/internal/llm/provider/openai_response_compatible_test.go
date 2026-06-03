package provider

import (
	"context"
	"net/url"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// TestOpenAIResponseCompatible_ImplementsLLM 编译期断言 + 运行期类型断言。
func TestOpenAIResponseCompatible_ImplementsLLM(t *testing.T) {
	var _ llm.LLM = (*OpenAIResponseCompatible)(nil)
}

// TestOpenAIResponseCompatible_DefaultConfig 验证 DefaultConfig 返回的 name 与 cfg。
//
// cfg 填充该 provider 的完整默认值（Provider / Sdk / BaseURL / Model.ID），APIKey 留空。
func TestOpenAIResponseCompatible_DefaultConfig(t *testing.T) {
	p := NewOpenAIResponseCompatible(config.LLMConfig{Sdk: config.SdkOpenAIResponse})
	name, cfg := p.DefaultConfig()
	if name != string(config.SdkOpenAIResponse) {
		t.Errorf("name = %q, want %q", name, config.SdkOpenAIResponse)
	}
	if cfg.Provider != config.ProviderOpenAI {
		t.Errorf("cfg.Provider = %q, want %q", cfg.Provider, config.ProviderOpenAI)
	}
	if cfg.Sdk != config.SdkOpenAIResponse {
		t.Errorf("cfg.Sdk = %q, want %q", cfg.Sdk, config.SdkOpenAIResponse)
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

// TestOpenAIResponseCompatible_NewSucceeds 验证构造不 panic。
func TestOpenAIResponseCompatible_NewSucceeds(t *testing.T) {
	u, _ := url.Parse("https://api.openai.com/v1")
	p := NewOpenAIResponseCompatible(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIResponse,
		Model:   config.Model{ID: "gpt-4o"},
	})
	if p == nil {
		t.Fatal("NewOpenAIResponseCompatible 返回 nil")
	}
	if p.sdk == nil {
		t.Error("内部 sdk 字段为 nil")
	}
}

// TestOpenAIResponseCompatible_GeneratePassesThrough 验证 Generate 透传到 sdk 包。
func TestOpenAIResponseCompatible_GeneratePassesThrough(t *testing.T) {
	u, _ := url.Parse("https://api.openai.com/v1")
	p := NewOpenAIResponseCompatible(config.LLMConfig{
		BaseURL: *u,
		APIKey:  "test-key",
		Sdk:     config.SdkOpenAIResponse,
		Model:   config.Model{ID: "gpt-4o"},
	})
	_, _ = p.Generate(context.Background(), llm.GenerateRequest{
		Input: llm.NewUserMessage(llm.NewTextContent("hi")),
	})
}
