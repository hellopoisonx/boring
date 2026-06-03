// Package config
package config

import "net/url"

type (
	Sdk      string
	Provider string
)

const (
	SdkOpenAIChat       Sdk = "openai-chat"
	SdkOpenAIResponse   Sdk = "openai-response"
	SdkAnthropicMessage Sdk = "anthropic-message"
	SdkDeepSeek         Sdk = "deepseek"

	// ProviderOpenAI / ProviderAnthropic / ProviderDeepSeek 是程序内置的 LLM 厂商预设。
	// 每个 provider 对应一组默认配置 (BaseURL + 默认 Sdk + 默认 Model + 允许的 Sdk 列表)，
	// 见 [providerSpecs]。
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderDeepSeek  Provider = "deepseek"
)

// DefaultBaseURL 返回指定 SDK 协议对应的官方默认 BaseURL。
// 用户在 [LLMConfig.BaseURL] 中显式配置时可覆盖。
func (s Sdk) DefaultBaseURL() string {
	switch s {
	case SdkOpenAIChat, SdkOpenAIResponse:
		return "https://api.openai.com/v1"
	case SdkAnthropicMessage:
		return "https://api.anthropic.com"
	case SdkDeepSeek:
		// DeepSeek 官方 base URL 不含 /v1 前缀（与 OpenAI 不同），
		// openai-go SDK 的 WithBaseURL 会自动处理路径斜杠。
		return "https://api.deepseek.com"
	}
	return ""
}

// providerSpec 是 [Provider] 的内置默认值集合。
//
//   - BaseURL      协议官方默认 base URL
//   - DefaultSdk   该 provider 下未显式指定 sdk 时的回退
//   - DefaultModel 该 provider 下未显式指定 model.id 时的回退
//   - AllowedSdks  显式指定 sdk 时必须落在此范围内
type providerSpec struct {
	BaseURL      string
	DefaultSdk   Sdk
	DefaultModel string
	AllowedSdks  []Sdk
}

// providerSpecs 是 [Provider] 的内置预设表。
// 增加新 provider 时：在此处加表项 + 上方 [Provider] 常量 + 同步更新 [LLMConfig] 默认模板注释。
var providerSpecs = map[Provider]providerSpec{
	ProviderOpenAI: {
		BaseURL:      "https://api.openai.com/v1",
		DefaultSdk:   SdkOpenAIChat,
		DefaultModel: "gpt-4o",
		AllowedSdks:  []Sdk{SdkOpenAIChat, SdkOpenAIResponse},
	},
	ProviderAnthropic: {
		BaseURL:      "https://api.anthropic.com",
		DefaultSdk:   SdkAnthropicMessage,
		DefaultModel: "claude-3-5-sonnet-latest",
		AllowedSdks:  []Sdk{SdkAnthropicMessage},
	},
	ProviderDeepSeek: {
		BaseURL:      "https://api.deepseek.com",
		DefaultSdk:   SdkDeepSeek,
		DefaultModel: "deepseek-v4-flash",
		AllowedSdks:  []Sdk{SdkDeepSeek},
	},
}

// Spec 返回 p 的内置默认值；未识别返回 false。
func (p Provider) Spec() (providerSpec, bool) {
	s, ok := providerSpecs[p]
	return s, ok
}

type Model struct {
	Name        string `yaml:"name"`
	ID          string `yaml:"id"`
	MaxResponse uint32 `yaml:"maxResponse"`
	MaxContext  uint32 `yaml:"maxContext"`
}

// LLMConfig 是 LLM provider 的统一配置。
//
//   - Provider: 程序内置的厂商预设 (openai / anthropic / deepseek)；
//     选一个后未显式配置的 baseUrl / sdk / model.id 字段会自动用该 provider
//     的内置默认值填充；显式 baseUrl 仍可覆盖（自建代理场景）；
//     显式 sdk 必须落在该 provider 允许的协议列表内，否则 fail-fast。
//   - 留空 Provider（不写）时维持老行为：baseUrl / sdk 全部由用户手写，
//     缺省 baseUrl 回退到 [Sdk.DefaultBaseURL]。
type LLMConfig struct {
	Provider Provider `yaml:"provider,omitempty"`
	BaseURL  url.URL  `yaml:"baseUrl,omitempty"`
	APIKey   string   `yaml:"apiKey"`
	Sdk      Sdk      `yaml:"sdk"`
	Model    Model    `yaml:"model"`
}
