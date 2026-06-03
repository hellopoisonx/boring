package provider

import (
	"testing"

	"github.com/hellopoisonx/boring/app/internal/config"
)

// TestRegisterProviderDefaults_OpenAI 验证 openai provider 的默认配置已通过 init() 注册。
func TestRegisterProviderDefaults_OpenAI(t *testing.T) {
	_, openaiCfg := (&OpenAIChatCompatible{}).DefaultConfig()
	if openaiCfg.Provider != config.ProviderOpenAI {
		t.Errorf("cfg.Provider = %q, want %q", openaiCfg.Provider, config.ProviderOpenAI)
	}
	if openaiCfg.Sdk != config.SdkOpenAIChat {
		t.Errorf("cfg.Sdk = %q, want %q", openaiCfg.Sdk, config.SdkOpenAIChat)
	}
	if openaiCfg.BaseURL.String() != "https://api.openai.com/v1" {
		t.Errorf("cfg.BaseURL = %q, want %q", openaiCfg.BaseURL.String(), "https://api.openai.com/v1")
	}
	if openaiCfg.Model.ID != "gpt-4o" {
		t.Errorf("cfg.Model.ID = %q, want %q", openaiCfg.Model.ID, "gpt-4o")
	}
	if openaiCfg.APIKey != "" {
		t.Errorf("cfg.APIKey = %q, want empty", openaiCfg.APIKey)
	}
}

func TestRegisterProviderDefaults_Anthropic(t *testing.T) {
	_, cfg := (&AnthropicMessageCompatible{}).DefaultConfig()
	if cfg.Provider != config.ProviderAnthropic {
		t.Errorf("cfg.Provider = %q, want %q", cfg.Provider, config.ProviderAnthropic)
	}
	if cfg.Sdk != config.SdkAnthropicMessage {
		t.Errorf("cfg.Sdk = %q, want %q", cfg.Sdk, config.SdkAnthropicMessage)
	}
	if cfg.BaseURL.String() != "https://api.anthropic.com" {
		t.Errorf("cfg.BaseURL = %q, want %q", cfg.BaseURL.String(), "https://api.anthropic.com")
	}
	if cfg.Model.ID != "claude-3-5-sonnet-20241022" {
		t.Errorf("cfg.Model.ID = %q, want %q", cfg.Model.ID, "claude-3-5-sonnet-20241022")
	}
}

func TestRegisterProviderDefaults_DeepSeek(t *testing.T) {
	_, cfg := (&DeepSeekChat{}).DefaultConfig()
	if cfg.Provider != config.ProviderDeepSeek {
		t.Errorf("cfg.Provider = %q, want %q", cfg.Provider, config.ProviderDeepSeek)
	}
	if cfg.Sdk != config.SdkDeepSeek {
		t.Errorf("cfg.Sdk = %q, want %q", cfg.Sdk, config.SdkDeepSeek)
	}
	if cfg.BaseURL.String() != "https://api.deepseek.com" {
		t.Errorf("cfg.BaseURL = %q, want %q", cfg.BaseURL.String(), "https://api.deepseek.com")
	}
	if cfg.Model.ID != "deepseek-v4-flash" {
		t.Errorf("cfg.Model.ID = %q, want %q", cfg.Model.ID, "deepseek-v4-flash")
	}
}

func TestAllowedSdks_KnownProviders(t *testing.T) {
	cases := []struct {
		provider config.Provider
		want     []config.Sdk
	}{
		{config.ProviderOpenAI, []config.Sdk{config.SdkOpenAIChat, config.SdkOpenAIResponse}},
		{config.ProviderAnthropic, []config.Sdk{config.SdkAnthropicMessage}},
		{config.ProviderDeepSeek, []config.Sdk{config.SdkDeepSeek}},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider), func(t *testing.T) {
			got, ok := AllowedSdks(tc.provider)
			if !ok {
				t.Fatal("known provider 应返回 ok=true")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i, s := range got {
				if s != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, s, tc.want[i])
				}
			}
		})
	}
}

func TestAllowedSdks_Unknown(t *testing.T) {
	_, ok := AllowedSdks("foo-bar")
	if ok {
		t.Fatal("未识别的 provider 应返回 ok=false")
	}
}
