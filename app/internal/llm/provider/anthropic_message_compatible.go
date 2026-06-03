// Package provider —— AnthropicMessageCompatible 是 [sdk.AnthropicMessage] 的 thin wrapper，
// 唯一作用是补齐 [llm.LLM] 接口的 [DefaultConfig] 方法。
//
// 实现策略：完全委托给 [sdk.AnthropicMessage]，不重写任何 HTTP / 流式 / 工具调用逻辑。
// 详见 [openai_chat_compatible.go] 文件头注释。
package provider

import (
	"context"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/internal/llm/sdk"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// AnthropicMessageCompatible 把 [sdk.AnthropicMessage] 包装为实现 [llm.LLM] 的 provider。
type AnthropicMessageCompatible struct {
	sdk *sdk.AnthropicMessage
}

// NewAnthropicMessageCompatible 用给定的 [config.LLMConfig] 构造 [AnthropicMessageCompatible]。
func NewAnthropicMessageCompatible(cfg config.LLMConfig) *AnthropicMessageCompatible {
	return &AnthropicMessageCompatible{sdk: sdk.NewAnthropicMessage(cfg)}
}

// Compile-time 断言：AnthropicMessageCompatible 必须实现 [llm.LLM]。
var _ llm.LLM = (*AnthropicMessageCompatible)(nil)

// Generate 透传到 [sdk.AnthropicMessage.Generate]。
func (p *AnthropicMessageCompatible) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	return p.sdk.Generate(ctx, req)
}

// GenerateWithStream 透传到 [sdk.AnthropicMessage.GenerateWithStream]。
func (p *AnthropicMessageCompatible) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	return p.sdk.GenerateWithStream(ctx, req)
}

// DefaultConfig 返回该 provider 的标识名与零值默认配置。
//
// 语义：name 等于 [config.Sdk] 字符串（"anthropic-message"），与"协议名"对齐；
// cfg 只填 [config.LLMConfig.Sdk] 字段，其他（BaseURL / APIKey / Model）全部零值，
// 由调用方按需填入。
func (p *AnthropicMessageCompatible) DefaultConfig() (string, config.LLMConfig) {
	return string(config.SdkAnthropicMessage), config.LLMConfig{Sdk: config.SdkAnthropicMessage}
}
