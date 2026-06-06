# AGENTS.md

> 项目级指针索引。**不展开具体内容**——每条规则/约定都指向具体文件，
> 详情请直接读目标文件。

---

## 0. 项目身份 → [`README.md`](./README.md)

项目是什么、归属、数据存储、依赖等基础信息均在 [`README.md`](./README.md)。

---

## 1. 文档索引（必读）

| 主题 | 文件 |
|---|---|
| 项目概览 / 功能 / 存储 | [`README.md`](./README.md) |
| 变更日志（**新代码必须更新**） | [`CHANGELOG.md`](./CHANGELOG.md) |
| Provider 适配层设计 | [`app/internal/llm/provider/README.md`](./app/internal/llm/provider/README.md) |
| hashline 文件工具设计 | [`app/internal/llm/tools/builtin/README.md`](./app/internal/llm/tools/builtin/README.md) |
| asyncrw race fix 说明 | [`app/shared/asyncrw/asyncrw.go`](./app/shared/asyncrw/asyncrw.go) |

---

## 2. 代码索引

### 2.1 LLM 统一抽象

- 接口与类型 → [`app/internal/llm/types.go`](./app/internal/llm/types.go)

### 2.2 Provider 适配层

- OpenAI Chat Completions 兼容 provider（委托 sdk.OpenAIChat） →
  [`app/internal/llm/provider/openai_chat_compatible.go`](./app/internal/llm/provider/openai_chat_compatible.go)
- OpenAI Responses 兼容 provider（委托 sdk.OpenAIResponse） →
  [`app/internal/llm/provider/openai_response_compatible.go`](./app/internal/llm/provider/openai_response_compatible.go)
- Anthropic Messages 兼容 provider（委托 sdk.AnthropicMessage） →
  [`app/internal/llm/provider/anthropic_message_compatible.go`](./app/internal/llm/provider/anthropic_message_compatible.go)
- DeepSeek（OpenAI 兼容 Chat Completions，委托 sdk.OpenAIChat + `.WithStreamIncludeUsage()`）→
  [`app/internal/llm/provider/deepseek.go`](./app/internal/llm/provider/deepseek.go)
- 跨协议 LLM 工厂 `provider.NewLLM(cfg) llm.LLM` →
  [`app/internal/llm/provider/llm.go`](./app/internal/llm/provider/llm.go)

### 2.3 内置文件工具

- Env / FileState / Tool 接口 / 行拆分工具 →
  [`app/internal/llm/tools/builtin/tool.go`](./app/internal/llm/tools/builtin/tool.go)
- read 工具 →
  [`app/internal/llm/tools/builtin/read.go`](./app/internal/llm/tools/builtin/read.go)
- edit 工具（hashline 锚点编辑）→
  [`app/internal/llm/tools/builtin/edit.go`](./app/internal/llm/tools/builtin/edit.go)
- write 工具（覆盖写 + read-first 校验）→
  [`app/internal/llm/tools/builtin/write.go`](./app/internal/llm/tools/builtin/write.go)
- 行哈希算法 →
  [`app/internal/llm/tools/builtin/hashline.go`](./app/internal/llm/tools/builtin/hashline.go)
- 原子写（temp + rename）→
  [`app/internal/llm/tools/builtin/atomic.go`](./app/internal/llm/tools/builtin/atomic.go)
- per-canonical-path 互斥锁 →
  [`app/internal/llm/tools/builtin/filelock.go`](./app/internal/llm/tools/builtin/filelock.go)

### 2.4 共享工具

- 泛型 AsyncReader / AsyncWriter →
  [`app/shared/asyncrw/asyncrw.go`](./app/shared/asyncrw/asyncrw.go)

### 2.5 配置
- LLMConfig / Model / Sdk 枚举 / Provider 枚举 →
  [`app/internal/config/config.go`](./app/internal/config/config.go)
  - 各 provider 的 `DefaultConfig()` 定义完整默认配置，通过 `init()` 注册到 `config.RegisterProviderDefaults`
  - `Provider.AllowsSdk(s Sdk) bool`：该 provider 是否允许指定 sdk（委托注册表）
  - `Sdk.DefaultBaseURL() string`：sdk 协议官方默认 BaseURL
  - `StorageConfig`：本地持久化配置（SQLite DSN），三层优先级同 LLM 配置
- viper 配置加载器（flag/env/file 三层优先级 + fsnotify 热加载）→
  [`app/internal/config/loader.go`](./app/internal/config/loader.go)
### 2.6 持久化层（sqlc 生成）
- sqlc 配置文件（engine: sqlite, package: store）→ 仓库根 [`sqlc.yaml`](./sqlc.yaml)
- 三表 DDL（被 embed 进 `Open`）→ [`app/internal/store/schema.sql`](./app/internal/store/schema.sql)
- sqlc 源查询（手写注释 + SQL，按表拆）→ [`app/internal/store/queries/`](./app/internal/store/queries/)
  - `user_tenant.sql` / `tenant_info.sql` / `tenant_conv.sql`
- sqlc 生成产物（提交到 git，禁手改）→ `app/internal/store/`
  - `models.go`（行模型 `UserTenant` / `TenantInfo` / `TenantConv`）
  - `db.go`（`DBTX` / `Queries` / `WithTx`）
  - `user_tenant.sql.go` / `tenant_info.sql.go` / `tenant_conv.sql.go`（查询方法）
- 手写代码 → [`app/internal/store/store.go`](./app/internal/store/store.go) + [`model.go`](./app/internal/store/model.go)
  - `*Store` 嵌入 `*Queries`，调用方可直接 `st.CreateUserTenant(...)` / `st.GetTenantConv(...)`
  - `model.go` 仅放 `ConvStatus*` 状态常量（sqlc 不生成 CHECK 约束的 Go 常量）
- sqlc 工作流 / 用法 / 已知限制 → [`app/internal/store/README.md`](./app/internal/store/README.md)
- 设计与决策记录 → [`plans/db-schema-v1.md`](./plans/db-schema-v1.md)

### 2.7 入口
- 调试脚本（**非产品代码**）→ [`main.go`](./main.go)
- `cmd/chat` 对话 CLI 入口（`--profile` 必填，`--db` 可选）→ [`app/cmd/chat/chat.go`](./app/cmd/chat/chat.go)
  - **物理位置在 `app/cmd/chat/` 而非根 `cmd/`**：
    Go `internal` 规则限制 `app/internal/...` 只能被 `app/...` 子树访问
    （参见 [Go 官方文档](https://pkg.go.dev/cmd/go#hdr-Internal_Directories)）。
    所有需要调用 `app/internal/...` 的可执行程序都应放在 `app/cmd/<name>/` 下。
  - LLM 配置走 [`app/internal/config`](./app/internal/config/) 统一收口，
    三层优先级：flag > env (`BORING_*`) > file
  - 租户隔离：`--profile` → `user_tenant` → `tenant_id`；同 profile 复用最后 active conv
  - DB 路径：`--db` > env `BORING_DB` > `storage.dsn` > `./boring.db`；每轮 finish 后 `IncUsage` 写库
  - DB 错误不中断 LLM 响应（仅 stderr warn）

### 2.8 chat agent

- `agent.Chat` / `ChatOptions` / `NewChat` / `Reply` / `ReplyStream` /
  `ErrEmptyPrompt` / `ErrToolCallNotSupported` →
  [`app/internal/agent/chat.go`](./app/internal/agent/chat.go)
- 原则：同一实例内自动维护会话历史（user + assistant），不压缩上下文、不读取跨会话记忆、不主动裁剪历史；
  工具调用等复杂编排请直接使用 [`llm.LLM`](./app/internal/llm/types.go) 接口
- 配套测试：fake LLM（不依赖外部 HTTP）→
  [`app/internal/agent/chat_test.go`](./app/internal/agent/chat_test.go)
---

## 3. 测试索引

- Provider 集成测试（httptest mock）→
  [`app/internal/llm/provider/`](./app/internal/llm/provider/)
- 工具测试 →
  [`app/internal/llm/tools/builtin/`](./app/internal/llm/tools/builtin/)
- 存储层集成测试 → [`app/internal/store/store_test.go`](./app/internal/store/store_test.go)
- 单轮 chat agent 测试（fake LLM，不依赖外部 HTTP）→
  [`app/internal/agent/chat_test.go`](./app/internal/agent/chat_test.go)
- 配置加载器测试 → [`app/internal/config/loader_test.go`](./app/internal/config/loader_test.go)

### 3.1 测试命令

- `go test ./...`
- `go test -race ./app/internal/llm/provider/`
- `go test -count=30 -race ./app/internal/llm/provider/`

> 详细测试风格、断言方式见各测试文件。

---

## 4. 依赖索引

- 模块声明 → [`go.mod`](./go.mod)
- 校验和 → [`go.sum`](./go.sum)

---

## 5. 改动流程

1. 读 §1 索引中对应的设计文档；
2. 读 §2 索引中对应的实现文件；
3. 改实现 + 改 §3 索引中对应的测试；
4. 跑 §3.1 测试命令，全部通过；
5. 更新 [`CHANGELOG.md`](./CHANGELOG.md) 的 `[未发布]` 段；
6. 若改了"索引指向"或新增/删除关键文件，**同步更新本文件**。

---

## 6. 约束（详见引文）

- 铁律 / 关键魔数 / 错误归一化 →
  [`app/internal/llm/provider/README.md`](./app/internal/llm/provider/README.md)
- Env 字段约定 / 业务 vs 系统错误边界 / 行数语义 / 锁粒度 →
  [`app/internal/llm/tools/builtin/README.md`](./app/internal/llm/tools/builtin/README.md)
- 三表链路 / `tenant_info` 唯一来源 / FK 行为 / JSON1 + CHECK 约束 →
  [`plans/db-schema-v1.md`](./plans/db-schema-v1.md)
- 已知限制与未来改进 → provider / builtin 两份 README 的"已知限制"段
- **chat agent 会话历史**：`agent.Chat` 在同一实例内维护 `[]llm.Message` 会话历史，
  每次 Reply / ReplyStream 成功后自动追加本轮 user + assistant 消息；
  不压缩上下文、不读取跨会话记忆、不主动裁剪历史。调用方保证单 goroutine 使用
- **cli 物理位置约束**：需要调用 `app/internal/...` 的可执行程序必须放在
  `app/cmd/<name>/` 下，不能在根 `cmd/`，原因见 §2.7

---

## 7. 沟通语言

简体中文。与 [`README.md`](./README.md)、

