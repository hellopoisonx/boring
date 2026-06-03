// Package provider —— 跨协议 LLM 工厂。
//
// 详见 [README.md](../README.md)。
package provider

import (
	"fmt"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
)

// NewLLM 根据 [config.LLMConfig.Sdk] 工厂式构造 [llm.LLM] 实现。
//
// 支持的 sdk：
//   - [config.SdkOpenAIChat]       → [OpenAIChatCompatible]（委托 [sdk.OpenAIChat]）
//   - [config.SdkOpenAIResponse]   → [OpenAIResponseCompatible]（委托 [sdk.OpenAIResponse]）
//   - [config.SdkAnthropicMessage] → [AnthropicMessageCompatible]（委托 [sdk.AnthropicMessage]）
//   - [config.SdkDeepSeek]         → [DeepSeekChat]（直连 openai-go，无 SDK 委托）
//
// 未识别的 sdk 会 panic；这是"程序配置错误"而非"运行时错误"，
// 调用方应在解析 config 阶段确保 sdk 合法（[config.Load] 配合
// [config.Provider] 预设会自动校验显式 sdk 是否在 AllowedSdks 内）。
func NewLLM(cfg config.LLMConfig) llm.LLM {
	switch cfg.Sdk {
	case config.SdkOpenAIChat:
		return NewOpenAIChatCompatible(cfg)
	case config.SdkOpenAIResponse:
		return NewOpenAIResponseCompatible(cfg)
	case config.SdkAnthropicMessage:
		return NewAnthropicMessageCompatible(cfg)
	case config.SdkDeepSeek:
		return NewDeepSeekChat(cfg)
	default:
		panic(fmt.Sprintf(
			"provider: 未实现的 sdk %q（可用: openai-chat / openai-response / anthropic-message / deepseek）",
			cfg.Sdk,
		))
	}
}
