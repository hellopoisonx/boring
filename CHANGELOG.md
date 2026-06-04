# 变更日志

本项目的所有显著变更都记录在此文件。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [未发布]

### 新增

- **CLI 接入租户隔离与用量统计** (`app/cmd/chat/chat.go` + `app/internal/store/`)
  - `chat` CLI 新增 `--profile` flag（必填，直当 `user_tenant.user_id`），启动时解析 profile → tenant_id → conv_id
  - 同 profile 下复用最后一个 `status='active'` 的 conv；不存在则自动 Create
  - 每轮 LLM 响应结束后立即通过 `IncUsage` 将 token 用量原子累加到当前 `tenant_conv`
  - DB 路径 `--db` flag > env `BORING_DB` > `config.yaml` `storage.dsn` > 默认 `./boring.db`
  - `config` 包新增 `StorageConfig` 类型；loader 新增 `storage.dsn` 解析与 env / flag 绑定
  - `TenantConvStore` 接口新增 `LatestActiveByTenant` / `IncUsage` 方法
  - 配套测试：`TestConv_LatestActiveByTenant` / `TestConv_IncUsage` / `TestStorage_DSN_*` (3 个)
  - `defaultTemplateYAML` 新增 `storage` 段

- **tenant_conv 表新增 usage 字段** (`app/internal/store/`)
  - `tenant_conv` 表新增 `total_tokens INTEGER NOT NULL DEFAULT 0`、`model_id VARCHAR(255) NOT NULL DEFAULT ''`、`model_provider VARCHAR(64) NOT NULL DEFAULT ''` 三列
  - `Conv` 领域类型新增 `TotalTokens` / `ModelID` / `ModelProvider` 字段
  - `TenantConvStore` 接口新增 `UpdateUsage(ctx, convID, totalTokens, modelID, modelProvider) error` 方法，用于 LLM 调用结束后回写用量与模型信息
  - 配套测试新增 `TestConv_UpdateUsage`，已有 Conv 用例补齐新字段默认值断言

- **chat agent 新增同一会话内的上下文** (`app/internal/agent/chat.go`)
  - `Chat` 结构体新增 `history []llm.Message` 字段，同一实例内自动维护会话历史
  - `Reply`：调用成功后将本轮 user 消息与 assistant 回复追加到 history，下次调用时作为 `GenerateRequest.History` 传入
  - `ReplyStream`：通过 `streamHistoryCollector` 包装 `AsyncReader`，在 finish chunk 到达时收集完整 assistant 文本并追加
  - 约束：不加锁（调用方保证单 goroutine 使用），不提供 ClearHistory / 条数上限等管理方法
  - 配套测试新增 `TestChat_Reply_MultiTurn` / `TestChat_Reply_ErrorNotAppendHistory` / `TestChat_ReplyStream_MultiTurn`
  - 文档更新：AGENTS.md §2.8 从"单轮 chat agent"改为"chat agent"，§6 约束从"无状态原则"改为"会话历史"
- **CLI 新增交互式多轮对话入口** (`app/cmd/chat/chat.go`)
  - `--prompt` 留空时自动进入交互式多轮对话模式，同一 `Chat` 实例自动维护会话历史
  - 从 stdin 逐行读取输入，空行跳过，`/exit` / `/quit` 或 Ctrl+D 退出
  - 支持 `--stream` 流式输出（逐 token 打印）
  - 每轮错误打印到 stderr，不中断循环；usage 信息打印到 stderr

### 修复

- **OpenAI Chat 流式路径丢失 usage 信息**
  - `sdk.OpenAIChat.consumeStream` 原先只捕获 `FinishReason`，忽略了 chunk 中的 `Usage` 字段
  - 在流式循环中新增 usage 提取逻辑：当 `chunk.Usage.TotalTokens > 0` 时赋值给 `lastUsage`
  - 修复后 `runStream` 可正常输出 `[usage] prompt=... completion=... total=...`

### 新增

### 新增

- **Provider 默认配置迁移至 `provider.DefaultConfig()`** (`app/internal/llm/provider/` + `app/internal/config/`)
  - 各 provider 的 `DefaultConfig()` 从仅返回 `Sdk` 扩展为返回完整默认配置（Provider / BaseURL / Model.ID）
  - provider 包通过 `init()` 将默认配置注册到 config 包的内部注册表（`RegisterProviderDefaults`），打破 config ↔ provider 导入循环
  - `config.providerSpec` / `providerSpecs` / `Provider.Spec()` 已移除
  - `loader.go` 的 `resolveProviderDefaults()` 从注册表读取默认配置
  - `Provider.AllowsSdk()` 改为委托注册表实现
- **三种 SDK 兼容 provider** (`app/internal/llm/provider/`)
  - `OpenAIChatCompatible` / `OpenAIResponseCompatible` / `AnthropicMessageCompatible`：
    分别包装 [sdk.OpenAIChat] / [sdk.OpenAIResponse] / [sdk.AnthropicMessage]，
    委托所有 LLM 接口方法。唯一职责是补齐 [llm.LLM.DefaultConfig] 方法，
    返回 `("openai-chat" / "openai-response" / "anthropic-message", Sdk 零值 LLMConfig)`。
  - 设计动机：sdk 包专注协议适配，provider 包专注工厂与发现元信息。
    所有协议级 free function（`convertHistoryMessage` / `parseChatResponse` / `mapChatFinishReason` 等）仅在 sdk 包写一份。
  - 跨协议 LLM 工厂 `provider.NewLLM(cfg) llm.LLM`：按 `cfg.Sdk` 派发到上述三个 Compatible + `DeepSeekChat`。
    未识别 sdk panic（程序配置错误，fail-fast）。


- **llm 包结构调整** (`app/internal/llm/`)
  - 新增 `sdk` 子包（`app/internal/llm/sdk/`）：
    `OpenAIChat` / `OpenAIResponse` / `AnthropicMessage` 三种官方 SDK 适配层，
    `common.go` 共享 client options / 错误归一化 / tool schema 解析；
    `docs.go` 包级文档。
  - 同步删除 `provider/` 下同名旧文件（`openai_chat.go` / `openai_response.go` /
    `anthropic_message.go` / `common.go` 及其测试），把协议实现集中到 sdk 包；
    provider 包不再包含协议级代码。


- **单轮 chat agent + CLI 入口** (`app/internal/agent/` + `app/cmd/chat/`)
  - `agent.Chat`：`NewChat(llm, ChatOptions)` 构造；`Reply(ctx, prompt) (string, *Usage, error)`
    与 `ReplyStream(ctx, prompt) (AsyncReader[StreamChunk], error)` 两种调用形态。
    不维护历史 / 不压缩 / 不读取记忆；调用方按需叠加上下文。
  - `app/cmd/chat/chat.go`：单轮对话 CLI 入口。
    `go run ./app/cmd/chat --prompt "你好"`：读 `config.yaml` → 构造 LLM → 单轮调用 → 打印 `assistant: <text>`。
    支持 `--config / --prompt / --system / --stream` cmd flag，
    显式注册 config 包的 5 个 LLM flag（`--provider / --base-url / --api-key / --sdk / --model-id`）。
    LLM 配置走 `config.Load` 三层优先级：flag > env (`BORING_*`) > file。
    流式模式逐 token 打印 stdout，遇 SIGINT/SIGTERM 优雅取消；finish 阶段 stderr 打印 `[usage] prompt=X completion=Y total=Z`。


- **LLM 接口契约补齐** (`app/internal/llm/types.go` + `app/internal/llm/sdk/*` + `app/internal/llm/provider/*`)
  - [llm.LLM] 接口增加 `DefaultConfig() (string, config.LLMConfig)` 方法。
  - sdk 包三个 SDK (`OpenAIChat` / `OpenAIResponse` / `AnthropicMessage`) 补上 `DefaultConfig`，返回与 Sdk 字符串对齐的零值 cfg。
  - provider 包的 [DeepSeekChat] 补上 `DefaultConfig` (为协议标识统一)。
  - [llm.Message] 增 `Usage *Usage` 字段（与流式路径 [StreamChunkTypeFinish.Usage] 同源同型），补齐非流式调用路径的 token 用量提取。

- **`config` 包增量扩展** (`app/internal/config/config.go`)
  - 基于 [feat(config): 新增 Provider 内置预设字段 (cb38301)] 追加：
  - `Provider.AllowsSdk(s Sdk) bool` 辅助方法，基于 `providerSpecs.AllowedSdks` 判断是否允许；
  - `LLMConfig.DefaultModel string` 字段保留为向后兼容占位（早期 README / CHANGELOG 文档引用），实际取值请走 `LLMConfig.Model.ID`；
  - `LLMConfig.Models []Model` 字段补齐，对齐 SDK 测试样例里的多模型列表语法（`Models: []config.Model{{ID: "..."}}`）；
  - Anthropic provider 的默认 model id 同步为 `claude-3-5-sonnet-20241022`（与 sdk/anthropic_message_test.go 的测试样例对齐）。
  - 注：`ProviderOpenAI` / `ProviderAnthropic` 常量、`Provider.Spec()`、`Sdk.DefaultBaseURL()`、`providerSpecs` 表已由 cb38301 实现，本 commit 不重复。
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
- **`cmd/chat` 单轮对话 CLI 入口** (`app/cmd/chat/chat.go`)
  - `go run ./app/cmd/chat --prompt "你好"`：读取 `config.yaml` → 构造 LLM → 单轮调用 → 打印 `assistant: <text>`
  - 支持 `--config / --prompt / --system / --stream` 四个 cmd 入口 flag；
    另显式注册 config 包绑定的 5 个 LLM flag（`--provider / --base-url / --api-key / --sdk / --model-id`）
    使 `--help` 完整、CLI 可直接覆盖 yaml/env
  - LLM 配置走 `config.Load` 三层优先级：flag > env (`BORING_*`) > file
  - 流式模式逐 token 打印至 stdout，遇 SIGINT / SIGTERM 经 `signal.NotifyContext` 取消；
    finish 阶段若 `Usage` 非 nil 在 stderr 打印 `[usage] prompt=X completion=Y total=Z`
  - 物理位置说明：放在 `app/cmd/chat/` 而非根 `cmd/`，因为 Go `internal` 规则限制
    `app/internal/...` 只能被 `app/...` 子树访问（见 AGENTS.md §2.7）

- **单轮 chat agent** (`app/internal/agent/chat.go`)
  - `agent.Chat`：1 user message → 1 assistant message；不维护历史 / 不压缩 / 不读取记忆
  - `Chat.Reply(ctx, prompt) (string, error)`：同步；LLM 决定调工具时返回 `*agent.ErrToolCallNotSupported`（`errors.As` 识别）
  - `Chat.ReplyStream(ctx, prompt) (asyncrw.AsyncReader[llm.StreamChunk], error)`：流式；流结束以 `asyncrw.ErrAsyncReaderClosed` 表示
  - 错误：`ErrEmptyPrompt`（prompt 为空）、`*ErrToolCallNotSupported`（不支持工具）、`fmt.Errorf`（不期望的 MessageType）

- **provider 跨协议工厂** (`app/internal/llm/provider/llm.go`)
  - `provider.NewLLM(cfg) llm.LLM`：按 `cfg.Sdk` 派发到 `NewOpenAIChat` / `NewOpenAIResponse` / `NewAnthropicMessage` / `NewDeepSeekChat`
  - 未识别 sdk panic（程序配置错误，fail-fast；与 `app/internal/llm/provider/README.md` §调用示例 中的伪代码对齐）

- **依赖管理** (`go.mod` / `go.sum`)

- **调试脚本** (`main.go`)
  - 用 `httptest` mock Anthropic SSE 流，验证 `bufio.ScanLines` 解析行为

- **测试覆盖**
  - `app/internal/agent/chat_test.go`：9 个用例（fake LLM，不依赖外部 HTTP），覆盖 `Reply` / `ReplyStream` 对 `System` + `Input.MsgType` + `Input.Text` 的透传、prompt 为空、LLM 错误透传、`tool_call` 不支持、不期望的 MessageType
  - `app/internal/llm/provider/`：5 份 `*_test.go`，覆盖四家协议的非流式
    （文本 + 工具调用）+ 流式路径；DeepSeek 额外验证 token 统计（`Usage`）与
    `app/internal/llm/provider/README.md` 给出 race detector 多次跑的命令
  - `app/internal/store/store_test.go`：15 个用例，覆盖三表 CRUD、Upsert、
    FK CASCADE、JSON CHECK、status 过滤、并发安全（`go test -race` 通过）
  - `app/internal/config/loader_test.go`：7 个用例，覆盖基本解析、文件不存在、
  - 模板落盘、env 覆盖、flag > env 优先级、fsnotify 热加载触发、watch 必传回调

### 修复

- **DeepSeek 非流式调用 400 错误** (`app/internal/llm/provider/deepseek.go`)
  - 现象：`go run ./app/cmd/chat --prompt "你好"` 返回 `400 invalid_request_error: stream_options should be set along with stream = true`
  - 根因：`DeepSeekChat.buildParams` 无条件设置 `StreamOptions`；`openai-go` SDK 的 `omitzero` 对带 `paramObj` 嵌入字段的 `ChatCompletionStreamOptionsParam` 不会自动省略，导致非流式请求 body 里依然带 `stream_options`，触发 DeepSeek 服务端校验
  - 修复：`buildParams` 加 `stream bool` 参数，仅在流式调用时设 `StreamOptions`；同时修正过时的注释（"非流式调用时 SDK 会忽略此字段" 是错的）
  - 测试：收紧 `mockDeepSeekHandler` 断言非流式 body 不含 `stream_options`；新增 `mockDeepSeekStreamHandler` 断言流式 body 同时带 `stream=true` 与 `stream_options.include_usage=true`；故意回滚修复时 3 个非流式用例稳定失败（"实际为 map[include_usage:true]"）

- **非流式调用丢失 token 用量** (`app/internal/llm/types.go` + `app/internal/llm/provider/*.go` + `app/internal/agent/chat.go` + `app/cmd/chat/chat.go`)
  - 现象：`go run ./app/cmd/chat --prompt "你好"`（默认非流式）不打印 `[usage] ...`，与 `--stream` 行为不一致
  - 根因：4 个 provider 的非流式解析（`parseChatResponse` / `parseResponseOutput` / `parseAnthropicResponse`）丢弃响应体里 `usage` 字段；`llm.Message` 也没有挂载点；`agent.Chat.Reply` 只返回 `(string, error)`
  - 修复：
    - `llm.Message` 加 `Usage *Usage` 字段，与流式路径 `StreamChunkTypeFinish.Usage` 同源同型
    - 3 个非流式解析点统一从响应里读 `usage` 并填到 `Message.Usage`；Anthropic 不给 total_tokens 时按流式路径同样语义自算
    - `Chat.Reply` 签名改为 `(string, *Usage, error)`；错误路径也透传 usage（让 CLI 能透到 stderr）
    - `cmd/chat.runSync` 复用 `runStream` 的 stderr 打印格式 `[usage] prompt=X completion=Y total=Z`
  - 破坏性变更：`agent.Chat.Reply` 签名变更（内部 API，目前只有 chat CLI 一处调用方）
  - 测试：`agent/chat_test.go` 现有 6 个 `Reply` 用例适配新签名 + 新增 `TestChat_Reply_PropagatesUsage`；4 家 provider 的非流式 `Generate` 用例都加 Usage 字段断言（值与响应体 `usage` 字段对齐）

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
- **chat agent 不引入状态**：`agent.Chat` 只持有 `llm.LLM` + `System` 字符串，
  无任何实例变量 / 全局状态；调用方拥有完整控制权，多轮 / 记忆 / 工具由调用方按需叠加
[未发布]: https://github.com/hellopoisonx/boring
