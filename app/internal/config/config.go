// Package config
package config

import "net/url"

type (
	Sdk string
)

const (
	SdkOpenAIChat       Sdk = "openai-chat"
	SdkOpenAIResponse   Sdk = "openai-response"
	SdkAnthropicMessage Sdk = "anthropic-message"
)

// DefaultBaseURL 返回指定 SDK 协议对应的官方默认 BaseURL。
// 用户在 [LLMConfig.BaseURL] 中显式配置时可覆盖。
func (s Sdk) DefaultBaseURL() string {
	switch s {
	case SdkOpenAIChat, SdkOpenAIResponse:
		return "https://api.openai.com/v1"
	case SdkAnthropicMessage:
		return "https://api.anthropic.com"
	}
	return ""
}

type Model struct {
	Name        string `yaml:"name"`
	ID          string `yaml:"id"`
	MaxResponse uint32 `yaml:"maxResponse"`
	MaxContext  uint32 `yaml:"maxContext"`
}

type LLMConfig struct {
	BaseURL url.URL `yaml:"baseUrl"`
	APIKey  string  `yaml:"apiKey"`
	Sdk     Sdk     `yaml:"sdk"`
	Model   Model   `yaml:"model"`
}
