# 变更日志

本项目的所有显著变更都记录在此文件。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [未发布]

### 新增

- **SQLite 持久化层** (`app/internal/store/`)
  - 三表链路：`user_tenant`（AIM user_id → tenant_id）→ `tenant_info`（1:1 持有 JSON 元数据）→ `tenant_conv`（1:N 持有会话）
  - `tenant_info.tenant_id` 是租户 ID 的唯一来源；`user_tenant.tenant_id` 与之同值但**无 DB 层 FK**（应用层同步）
  - 唯一 DB 层 FK：`tenant_conv.tenant_id` → `tenant_info.tenant_id` ON DELETE CASCADE
  - `Open` 幂等建表 + `PRAGMA foreign_keys=ON` + `busy_timeout=5s`；连接池限制为 1
  - 三个 DAO 接口：`UserTenantStore` / `TenantInfoStore` / `TenantConvStore`，工厂函数挂在 `*Store` 上
  - `tenant_info` 用 SQLite JSON1 + `json_valid(info)` CHECK 约束；`tenant_info.Upsert` 走 `INSERT ... ON CONFLICT DO UPDATE`
  - `tenant_conv.status` 用 CHECK 约束 `('active','archived','deleted')`；时间戳统一 unix epoch seconds
  - 详细设计决策见 `plans/db-schema-v1.md`

- **LLM 统一抽象层** (`app/internal/llm/types.go`)
  - `LLM` 接口：`Generate` / `GenerateWithStream` 两种调用形态
  - 消息模型：`Message`（5 种 `MessageType`）、`ContentPart`（text / image）、
    `ToolCall` / `ToolResult` / `ToolInfo` / `ImageInfo`
  - 流式输出：`StreamChunk`（text 增量 / 完整 tool_call / finish 三态）
  - 错误统一：`*llm.Error`（含 `Provider` / `StatusCode` / `Message` / `Cause`）

- **四种 LLM 协议适配器** (`app/internal/llm/provider/`)
  - `OpenAIChat` —— OpenAI Chat Completions API（`openai-go/v3`）
  - `OpenAIResponse` —— OpenAI Responses API（`openai-go/v3/responses`）
  - `AnthropicMessage` —— Anthropic Messages API（`anthropic-sdk-go`）
  - `DeepSeekChat` —— DeepSeek（OpenAI 兼容 Chat Completions；复用 `openai-go/v3` + `WithBaseURL`）
  - 四个 Provider 共享 `common.go` 中的 client 构造、错误归一化、
    tool schema 解析逻辑；`DeepSeekChat` 额外复用 `openai_chat.go` 中所有
    Chat Completions 协议级转换函数（`convertHistoryMessage` / `convertToolsChat` /
    `parseChatResponse` 等）
  - 流式 token 统计：`DeepSeekChat` 显式带 `stream_options.include_usage=true`，
    在 finish chunk 携带 `Usage`（`PromptTokens` / `CompletionTokens` / `TotalTokens`）
  - 终止原因扩展：`mapChatFinishReason` 加 `insufficient_system_resource` → `FinishReasonError`（DeepSeek 特有）
  - 详细设计决策与跨协议对比表见 `app/internal/llm/provider/README.md`
  - 已知限制（仅 DeepSeek）：思考模式 `reasoning_content` 不暴露、
    `prompt_cache_*_tokens` / `completion_tokens_details.reasoning_tokens` 不透出

- **LLM 配置抽象** (`app/internal/config/config.go`)
  - `Sdk` 协议枚举：`openai-chat` / `openai-response` / `anthropic-message` / `deepseek`
  - `LLMConfig` / `Model` 结构 + `Sdk.DefaultBaseURL()` 缺省地址回退
  - 支持 `MaxResponse` / `MaxContext` 字段
- **LLM Provider 内置预设** (`app/internal/config/config.go` + `loader.go`)
  - 新增 `Provider` 枚举 (`openai` / `anthropic` / `deepseek`) 与 `providerSpecs` 内置表：
    每个 provider 对应一组 (BaseURL, DefaultSdk, DefaultModel, AllowedSdks)
  - `LLMConfig.Provider` 字段：选一个后未显式配置的 `baseUrl` / `sdk` / `model.id` 自动用
    provider 默认值填充；显式 `baseUrl` 仍可覆盖（自建代理场景）
  - 显式 `sdk` 必须落在 provider 允许的协议列表内，否则 fail-fast
  - `Provider` 留空时维持老行为（手动指定 `sdk` + `baseUrl`）
  - viper `IsSet` 语义在 SetDefault 之后会把 default 误判为「已 set」；用 SetDefault 之前
    快照的 yaml 扁平 key 集合 (`flattenViperKeys`) 替代，保证「显式」判断可靠
  - 同步更新默认模板 / `--provider` flag / `EnvPrefix_PROVIDER` env 绑定
  - 新增 7 个测试：默认值填充、baseUrl 覆盖、允许的 sdk 列表、冲突 fail-fast、
    未知 provider fail-fast、老路径兼容、env 与 provider 协同

- **viper 配置加载器** (`app/internal/config/loader.go`)
  - `Load(path, Options)` 入口；零值 Options 即可工作
  - Unmarshal 到 `LLMConfig`（mapstructure hook 复用 yaml tag，并自动 `string → url.URL`）
  - `Options.WriteTemplate`：配置文件不存在时落盘带注释的默认模板
  - `Options.EnvPrefix`：启用环境变量覆盖（`PREFIX_BORING_APIKEY` 形式）
  - `Options.FlagSet` + pflag：命令行 flag 覆盖；优先级 flag > env > file
  - `Options.Watch` + `OnConfigChange`：fsnotify 热加载；解析失败保留旧值，100ms debounce
  - 配套测试覆盖：基本解析、模板落盘、env/flag 覆盖优先级、热加载触发

- **泛型异步 I/O 抽象** (`app/shared/asyncrw/asyncrw.go`)
  - `AsyncReader[T]` / `AsyncWriter[T]`，流式 chunk 通过它异步暴露
  - 修复了初版 closed-channel race：`Recv` 用 `, ok := <-ch` 检测 EOF；
    `Send` 用 `defer recover()` 兜底 send-on-closed panic

- **依赖管理** (`go.mod` / `go.sum`)
  - `github.com/anthropics/anthropic-sdk-go v1.46.0`
  - `github.com/ncruces/go-sqlite3 v0.34.3`
  - `github.com/openai/openai-go/v3 v3.38.0`
  - `github.com/spf13/viper v1.21.0`（配置加载器）
  - `github.com/spf13/pflag v1.0.10`（命令行 flag 覆盖）

- **调试脚本** (`main.go`)
  - 用 `httptest` mock Anthropic SSE 流，验证 `bufio.ScanLines` 解析行为

- **测试覆盖**
  - `app/internal/llm/provider/`：5 份 `*_test.go`，覆盖四家协议的非流式
    （文本 + 工具调用）+ 流式路径；DeepSeek 额外验证 token 统计（`Usage`）与
  - `app/internal/llm/provider/README.md` 给出 race detector 多次跑的命令
  - `app/internal/store/store_test.go`：15 个用例，覆盖三表 CRUD、Upsert、
    FK CASCADE、JSON CHECK、status 过滤、并发安全（`go test -race` 通过）
  - `app/internal/config/loader_test.go`：7 个用例，覆盖基本解析、文件不存在、
    模板落盘、env 覆盖、flag > env 优先级、fsnotify 热加载触发、watch 必传回调

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
- **viper 复用 yaml tag**：mapstructure hook 把 `TagName` 切到 `yaml`，
  并自定义 `string → url.URL` 解码，保留现有结构体不改动

[未发布]: https://github.com/hellopoisonx/boring
