# 变更日志

本项目的所有显著变更都记录在此文件。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [未发布]

### 新增

- **LLM 统一抽象层** (`app/internal/llm/types.go`)
  - `LLM` 接口：`Generate` / `GenerateWithStream` 两种调用形态
  - 消息模型：`Message`（5 种 `MessageType`）、`ContentPart`（text / image）、
    `ToolCall` / `ToolResult` / `ToolInfo` / `ImageInfo`
  - 流式输出：`StreamChunk`（text 增量 / 完整 tool_call / finish 三态）
  - 错误统一：`*llm.Error`（含 `Provider` / `StatusCode` / `Message` / `Cause`）

- **三种 LLM 协议适配器** (`app/internal/llm/provider/`)
  - `OpenAIChat` —— OpenAI Chat Completions API（`openai-go/v3`）
  - `OpenAIResponse` —— OpenAI Responses API（`openai-go/v3/responses`）
  - `AnthropicMessage` —— Anthropic Messages API（`anthropic-sdk-go`）
  - 三个 Provider 共享 `common.go` 中的 client 构造、错误归一化、
    tool schema 解析逻辑
  - 详细设计决策与跨协议对比表见 `app/internal/llm/provider/README.md`

- **LLM 配置抽象** (`app/internal/config/config.go`)
  - `Sdk` 协议枚举：`openai-chat` / `openai-response` / `anthropic-message`
  - `LLMConfig` / `Model` 结构 + `Sdk.DefaultBaseURL()` 缺省地址回退
  - 支持 `MaxResponse` / `MaxContext` 字段

- **泛型异步 I/O 抽象** (`app/shared/asyncrw/asyncrw.go`)
  - `AsyncReader[T]` / `AsyncWriter[T]`，流式 chunk 通过它异步暴露
  - 修复了初版 closed-channel race：`Recv` 用 `, ok := <-ch` 检测 EOF；
    `Send` 用 `defer recover()` 兜底 send-on-closed panic

- **依赖管理** (`go.mod` / `go.sum`)
  - `github.com/anthropics/anthropic-sdk-go v1.46.0`
  - `github.com/openai/openai-go/v3 v3.38.0`

- **调试脚本** (`main.go`)
  - 用 `httptest` mock Anthropic SSE 流，验证 `bufio.ScanLines` 解析行为

- **测试覆盖**
  - `app/internal/llm/provider/`：4 份 `*_test.go`，覆盖三家协议的非流式
    （文本 + 工具调用）+ 流式路径，包含 httptest mock 集成测试
  - `app/internal/llm/provider/README.md` 给出 race detector 多次跑的命令

### 设计亮点

- **三种协议对上层透明**：调用方只看到 `llm.LLM` 接口和统一 `Message` /
  `StreamChunk` 类型；切换协议 = 切换 `config.yaml` 里的 `sdk` 字段
- **不重写 SDK**：Provider 不手写 HTTP / 签名 / JSON 编码，所有协议细节
  委托给官方 `openai.NewClient` / `anthropic.NewClient`
- **Tool ID 统一为 `string`**：免去三家协议"string ↔ uint64"映射，跨调用可
  正确回传
- **错误统一为 `*llm.Error`**：通过 `errors.As` 识别 `openai.Error` /
  `anthropic.Error`，提取 HTTP 状态码；网络层错误归一化为 `StatusCode=0`
- **流式走 `asyncrw.AsyncReader`**：避免阻塞主流程；与工具调度解耦，
  调用方通过 `Recv(ctx)` 拉取 chunk

[未发布]: https://github.com/hellopoisonx/boring
