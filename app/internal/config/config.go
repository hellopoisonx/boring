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
	// 每个 provider 对应的默认配置由 provider 包通过 [RegisterProviderDefaults] 注册。
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
//     的内置默认值填充（由 provider 包通过 [RegisterProviderDefaults] 注册）；
//     显式 baseUrl 仍可覆盖（自建代理场景）；
//     显式 sdk 必须落在该 provider 允许的协议列表内，否则 fail-fast。
//   - 留空 Provider（不写）时维持老行为：baseUrl / sdk 全部由用户手写，
//     缺省 baseUrl 回退到 [Sdk.DefaultBaseURL]。
//
// 注：DefaultModel string 字段保留仅作向后兼容占位（早期 README / CHANGELOG 文档引用），
// 实际取值请走 Model.ID；本字段不参与默认值填充。
type LLMConfig struct {
	Provider     Provider `yaml:"provider,omitempty"`
	BaseURL      url.URL  `yaml:"baseUrl,omitempty"`
	APIKey       string   `yaml:"apiKey"`
	Sdk          Sdk      `yaml:"sdk"`
	DefaultModel string   `yaml:"defaultModel"`
	Model        Model    `yaml:"model"`
	Models       []Model  `yaml:"models"`
}

// AllowsSdk 返回 provider 是否允许使用给定 sdk。
// 未识别的 provider 一律返回 false。
//
// 注：本方法通过 [getAllowedSdks] 查询注册表，
// 对外暴露与旧版 [Provider.Spec] 兼容的 API。
func (p Provider) AllowsSdk(s Sdk) bool {
	allowed, ok := getAllowedSdks(p)
	if !ok {
		return false
	}
	return slices.Contains(allowed, s)
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
