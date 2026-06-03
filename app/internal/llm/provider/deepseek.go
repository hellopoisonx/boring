package provider

import (
	"context"

	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/internal/llm/sdk"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// DeepSeek 协议适配。
//
// DeepSeek 提供与 OpenAI Chat Completions 完全兼容的 HTTP API：
//   - BaseURL: https://api.deepseek.com（注意无 /v1 前缀）
//   - 路径:    POST /chat/completions
//   - 鉴权:    Authorization: Bearer <api_key>
//   - 工具调用、流式 SSE、Usage 字段格式与 OpenAI 一致
//
// 关键差异（相对 OpenAI Chat）：
//   - 终止原因多了 `insufficient_system_resource`（系统资源不足），已由
//     sdk/mapChatFinishReason 统一处理。
//   - 流式响应中需要在请求里显式带 `stream_options.include_usage=true`，
//     才能在最后一个 chunk（`choices: []`）拿到本次请求的 usage 统计。
//     当前实现通过 [sdk.OpenAIChat.WithStreamIncludeUsage] 启用该开关。
//   - 思考模式默认开启：delta 中除 `content` 外还可能有 `reasoning_content`，
//     当前不暴露给上层（统一 [llm.Message]/[llm.StreamChunk] 不含 reasoning
//     字段），直接丢弃；详见 "DeepSeek 已知限制" 段。
//
// 实现策略：完全委托给 [sdk.OpenAIChat]，由 [config.LLMConfig.BaseURL] 切到 DeepSeek 端点。
// DeepSeek 之前曾直接使用 openai-go SDK（绕过 sdk 包）以追求"流式 usage 开关可控"的精确性，
// 但这导致 DeepSeekChat 与 sdk 包的三方协议级 free function（convertHistoryMessage /
// parseChatResponse 等）有代码重复。本次重构改为 SDK 委托 + WithStreamIncludeUsage
// 选项叠加，零代码重复，流式 Usage 行为保持。

// DeepSeekChat 实现 [llm.LLM]，对接 DeepSeek（OpenAI 兼容 Chat Completions）。
//
// 内部委托给 [sdk.OpenAIChat]：DeepSeek 走 OpenAI Chat 协议，
// [config.LLMConfig.BaseURL] 指向 https://api.deepseek.com（由调用方在 cfg 中设置）。
type DeepSeekChat struct {
	cfg config.LLMConfig
	sdk *sdk.OpenAIChat
}

// NewDeepSeekChat 用给定的 [config.LLMConfig] 构造 [DeepSeekChat]。
func NewDeepSeekChat(cfg config.LLMConfig) *DeepSeekChat {
	return &DeepSeekChat{
		cfg: cfg,
		// 开启流式 usage：DeepSeek 要求 stream_options.include_usage=true 才能在最后一个 chunk 返回 usage。
		// 委托 [sdk.OpenAIChat] 加上该开关，保留 CHANGELOG 中"修复流式 Usage"的行为。
		sdk: sdk.NewOpenAIChat(cfg).WithStreamIncludeUsage(),
	}
}

// Compile-time 断言：DeepSeekChat 必须实现 [llm.LLM]。
var _ llm.LLM = (*DeepSeekChat)(nil)

// Generate 同步调用 DeepSeek Chat Completions；返回 [llm.Message]。
func (p *DeepSeekChat) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.Message, error) {
	return p.sdk.Generate(ctx, req)
}

// GenerateWithStream 流式调用 DeepSeek；通过 [asyncrw.AsyncReader] 暴露 [llm.StreamChunk]。
func (p *DeepSeekChat) GenerateWithStream(ctx context.Context, req llm.GenerateRequest) (asyncrw.AsyncReader[llm.StreamChunk], error) {
	return p.sdk.GenerateWithStream(ctx, req)
}

// DefaultConfig 返回该 provider 的标识名与零值默认配置。
//
// DeepSeek 不是 SDK 的 wrapper（直连 openai-go 改为 SDK 委托后仍持 cfg 便于扩展），
// 但为满足 [llm.LLM] 接口契约，DefaultConfig 仍按"name=Sdk 字符串, cfg 只填 Sdk 字段"统一语义返回。
func (p *DeepSeekChat) DefaultConfig() (string, config.LLMConfig) {
	return string(config.SdkDeepSeek), config.LLMConfig{Sdk: config.SdkDeepSeek}
}
