// Package provider —— OpenAIChatCompatible 是 [sdk.OpenAIChat] 的 thin wrapper，
// 唯一作用是补齐 [llm.LLM] 接口的 [DefaultConfig] 方法。
//
// 为什么不直接用 [sdk.OpenAIChat] 充当 LLM？
//   - [llm.LLM] 接口要求 [DefaultConfig]；SDK 实现专注协议适配，不持有"自己是哪个 provider"的元数据。
//   - 把"协议实现"和"provider 元信息"分到两个包：sdk 是 HTTP 协议层，provider 是工厂/发现层。
//
// 实现策略：完全委托给 [sdk.OpenAIChat]，不重写任何 HTTP / 流式 / 工具调用逻辑。
package provider

import (
	"context"
	"net/url"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/internal/llm/sdk"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// OpenAIChatCompatible 把 [sdk.OpenAIChat] 包装为实现 [llm.LLM] 的 provider。
//
// 内部字段直接持有 SDK 实例，所有 [Generate] / [GenerateWithStream] 调用透传。
type OpenAIChatCompatible struct {
	sdk *sdk.OpenAIChat
}

// NewOpenAIChatCompatible 用给定的 [config.LLMConfig] 构造 [OpenAIChatCompatible]。
func NewOpenAIChatCompatible(cfg config.LLMConfig) *OpenAIChatCompatible {
	return &OpenAIChatCompatible{sdk: sdk.NewOpenAIChat(cfg)}
}

// Compile-time 断言：OpenAIChatCompatible 必须实现 [llm.LLM]。
var _ llm.LLM = (*OpenAIChatCompatible)(nil)

// Generate 透传到 [sdk.OpenAIChat.Generate]。
func (p *OpenAIChatCompatible) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	return p.sdk.Generate(ctx, req)
}

// GenerateWithStream 透传到 [sdk.OpenAIChat.GenerateWithStream]。
func (p *OpenAIChatCompatible) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	return p.sdk.GenerateWithStream(ctx, req)
}

// DefaultConfig 返回该 provider 的标识名与默认配置。
//
// 语义：name 等于 [config.Sdk] 字符串（"openai-chat"），与"协议名"对齐；
// cfg 填充该 provider 的完整默认值（Provider / Sdk / BaseURL / Model.ID），
// APIKey 留空由调用方填入。
func (p *OpenAIChatCompatible) DefaultConfig() (string, config.LLMConfig) {
	u, _ := url.Parse("https://api.openai.com/v1")
	return string(config.SdkOpenAIChat), config.LLMConfig{
		Provider: config.ProviderOpenAI,
		Sdk:      config.SdkOpenAIChat,
		BaseURL:  *u,
		Model:    config.Model{ID: "gpt-4o"},
	}
}
