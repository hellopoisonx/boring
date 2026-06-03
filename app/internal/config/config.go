// Package config
package config

import (
	"net/url"
	"slices"
)

type (
	Sdk      string
	Provider string
)

const (
	SdkOpenAIChat       Sdk = "openai-chat"
	SdkOpenAIResponse   Sdk = "openai-response"
	SdkAnthropicMessage Sdk = "anthropic-message"
	// SdkDeepSeek：DeepSeek 提供与 OpenAI Chat Completions 完全兼容的协议族，
	// 本项目为它分配独立的 Sdk 字符串（与"走 SdkOpenAIChat 协议 + 自定义 BaseURL"的实现路线并存）。
	SdkDeepSeek Sdk = "deepseek"

	// ProviderOpenAI / ProviderAnthropic / ProviderDeepSeek 是程序内置的 LLM 厂商预设。
	// 每个 provider 对应一组默认配置 (BaseURL + 默认 Sdk + 默认 Model + 允许的 Sdk 列表)，
	// 见 [providerSpecs]。
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderDeepSeek  Provider = "deepseek"
)

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
//
// 注：DefaultModel string 字段保留仅作向后兼容占位（早期 README / CHANGELOG 文档引用），
// 实际取值请走 Model.ID；本字段不参与 [Provider.Spec] 默认值填充。
type LLMConfig struct {
	Provider     Provider `yaml:"provider,omitempty"`
	BaseURL      url.URL  `yaml:"baseUrl,omitempty"`
	APIKey       string   `yaml:"apiKey"`
	Sdk          Sdk      `yaml:"sdk"`
	DefaultModel string   `yaml:"defaultModel"`
	Model        Model    `yaml:"model"`
	Models       []Model  `yaml:"models"`
}

// providerSpec 描述一个内置厂商预设的完整默认配置。
// 表驱动：见 [providerSpecs]。
type providerSpec struct {
	BaseURL      string // 官方默认 baseUrl（不含末尾斜杠）
	DefaultSdk   Sdk    // provider 的默认 sdk
	DefaultModel string // provider 的默认 model id
	AllowedSdks  []Sdk  // provider 允许的 sdk 列表；显式指定必须在此集合内
}

// providerSpecs 内置厂商预设表。增删 provider 必须同步更新本表。
//
// 默认 model id 选取依据：
//   - openai:    gpt-4o（业界事实标准 chat 模型，openai 官方在多个 SDK 示例中使用）
//   - anthropic: claude-3-5-sonnet-20241022（与 sdk/anthropic_message_test.go 的测试样例对齐）
//   - deepseek:  deepseek-v4-flash（与 app/internal/config/loader_test.go 期望对齐）
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
		DefaultModel: "claude-3-5-sonnet-20241022",
		AllowedSdks:  []Sdk{SdkAnthropicMessage},
	},
	ProviderDeepSeek: {
		BaseURL:      "https://api.deepseek.com",
		DefaultSdk:   SdkDeepSeek, // DeepSeek 独立 Sdk 枚举；底层仍走 openai-go 兼容协议
		DefaultModel: "deepseek-v4-flash",
		AllowedSdks:  []Sdk{SdkDeepSeek}, // 显式允许 DeepSeek 独立 Sdk 字符串
	},
}

// Spec 返回 (provider 的内置 spec, 是否识别该 provider)。
// 未识别的 provider 返回 (zero, false)；调用方应据此 fail-fast。
func (p Provider) Spec() (providerSpec, bool) {
	spec, ok := providerSpecs[p]
	return spec, ok
}

// AllowsSdk 返回 provider 是否允许使用给定 sdk。
// 未识别的 provider 一律返回 false。
func (p Provider) AllowsSdk(s Sdk) bool {
	spec, ok := p.Spec()
	if !ok {
		return false
	}
	return slices.Contains(spec.AllowedSdks, s)
}

// DefaultBaseURL 返回该 sdk 协议的官方默认 BaseURL。
// 未识别的 sdk 返回空字符串（与 BaseURL.Host 为空等价，调用方应回退到 provider 默认）。
func (s Sdk) DefaultBaseURL() string {
	switch s {
	case SdkOpenAIChat, SdkOpenAIResponse:
		return "https://api.openai.com/v1"
	case SdkAnthropicMessage:
		return "https://api.anthropic.com"
	case SdkDeepSeek:
		return "https://api.deepseek.com"
	}
	return ""
}
