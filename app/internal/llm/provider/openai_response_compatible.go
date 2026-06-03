// Package provider —— OpenAIResponseCompatible 是 [sdk.OpenAIResponse] 的 thin wrapper，
// 唯一作用是补齐 [llm.LLM] 接口的 [DefaultConfig] 方法。
//
// 实现策略：完全委托给 [sdk.OpenAIResponse]，不重写任何 HTTP / 流式 / 工具调用逻辑。
// 详见 [openai_chat_compatible.go] 文件头注释。
package provider

import (
	"context"
	"net/url"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/internal/llm/sdk"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// OpenAIResponseCompatible 把 [sdk.OpenAIResponse] 包装为实现 [llm.LLM] 的 provider。
type OpenAIResponseCompatible struct {
	sdk *sdk.OpenAIResponse
}

// NewOpenAIResponseCompatible 用给定的 [config.LLMConfig] 构造 [OpenAIResponseCompatible]。
func NewOpenAIResponseCompatible(cfg config.LLMConfig) *OpenAIResponseCompatible {
	return &OpenAIResponseCompatible{sdk: sdk.NewOpenAIResponse(cfg)}
}

// Compile-time 断言：OpenAIResponseCompatible 必须实现 [llm.LLM]。
var _ llm.LLM = (*OpenAIResponseCompatible)(nil)

// Generate 透传到 [sdk.OpenAIResponse.Generate]。
func (p *OpenAIResponseCompatible) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	return p.sdk.Generate(ctx, req)
}

// GenerateWithStream 透传到 [sdk.OpenAIResponse.GenerateWithStream]。
func (p *OpenAIResponseCompatible) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	return p.sdk.GenerateWithStream(ctx, req)
}

// DefaultConfig 返回该 provider 的标识名与默认配置。
//
// 语义：name 等于 [config.Sdk] 字符串（"openai-response"），与"协议名"对齐；
// cfg 填充该 provider 的完整默认值（Provider / Sdk / BaseURL / Model.ID），
// APIKey 留空由调用方填入。
func (p *OpenAIResponseCompatible) DefaultConfig() (string, config.LLMConfig) {
	u, _ := url.Parse("https://api.openai.com/v1")
	return string(config.SdkOpenAIResponse), config.LLMConfig{
		Provider: config.ProviderOpenAI,
		Sdk:      config.SdkOpenAIResponse,
		BaseURL:  *u,
		Model:    config.Model{ID: "gpt-4o"},
	}
}
