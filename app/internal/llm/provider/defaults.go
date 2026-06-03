// Package provider —— Provider 默认配置注册。
//
// 各 provider 的完整默认配置由 [llm.LLM.DefaultConfig] 定义；
// 本文件在 init() 中将它们注册到 [config.RegisterProviderDefaults]，
// 供 config 包的 [resolveProviderDefaults] 查询，避免导入循环。
package provider

import (
	"github.com/hellopoisonx/boring/app/internal/config"
)

func init() {
	// OpenAIChatCompatible 作为 OpenAI provider 的主协议，
	// 其 DefaultConfig 定义了 Provider=openai 的全部默认值。
	_, openaiCfg := (&OpenAIChatCompatible{}).DefaultConfig()
	config.RegisterProviderDefaults(config.ProviderOpenAI, openaiCfg,
		[]config.Sdk{config.SdkOpenAIChat, config.SdkOpenAIResponse})

	// Anthropic
	_, anthropicCfg := (&AnthropicMessageCompatible{}).DefaultConfig()
	config.RegisterProviderDefaults(config.ProviderAnthropic, anthropicCfg,
		[]config.Sdk{config.SdkAnthropicMessage})

	// DeepSeek
	_, deepseekCfg := (&DeepSeekChat{}).DefaultConfig()
	config.RegisterProviderDefaults(config.ProviderDeepSeek, deepseekCfg,
		[]config.Sdk{config.SdkDeepSeek})
}

// AllowedSdks 返回指定 provider 允许的 sdk 列表及 provider 是否被识别。
//
// 未识别的 provider 返回 (nil, false)。
func AllowedSdks(p config.Provider) ([]config.Sdk, bool) {
	switch p {
	case config.ProviderOpenAI:
		return []config.Sdk{config.SdkOpenAIChat, config.SdkOpenAIResponse}, true
	case config.ProviderAnthropic:
		return []config.Sdk{config.SdkAnthropicMessage}, true
	case config.ProviderDeepSeek:
		return []config.Sdk{config.SdkDeepSeek}, true
	default:
		return nil, false
	}
}
