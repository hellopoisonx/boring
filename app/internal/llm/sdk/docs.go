// Package sdk 实现三种 LLM 协议（OpenAI Chat Completions / OpenAI Responses / Anthropic Messages）
// 到 [llm.LLM] 统一接口的适配层。
//
// 所有 provider 都：
//   - 接收标准化的 [llm.GenerateRequest]；
//   - 内部转换为对应官方 SDK 的请求格式；
//   - 统一返回 [llm.Message]（非流式）或 [llm.StreamChunk]（流式）；
//   - 统一将 SDK 错误（apierror.Error）包装为 [llm.Error]。
package sdk
