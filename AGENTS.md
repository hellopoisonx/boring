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

- 三家协议共享逻辑（client 构造、错误归一化、Schema 解析） →
  [`app/internal/llm/provider/common.go`](./app/internal/llm/provider/common.go)
- OpenAI Chat Completions →
  [`app/internal/llm/provider/openai_chat.go`](./app/internal/llm/provider/openai_chat.go)
- OpenAI Responses →
  [`app/internal/llm/provider/openai_response.go`](./app/internal/llm/provider/openai_response.go)
- Anthropic Messages →
  [`app/internal/llm/provider/anthropic_message.go`](./app/internal/llm/provider/anthropic_message.go)

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

- LLMConfig / Model / Sdk 枚举 →
  [`app/internal/config/config.go`](./app/internal/config/config.go)

### 2.6 入口

- 调试脚本（**非产品代码**）→ [`main.go`](./main.go)
- 未来 CLI 入口占位 → [`cmd/`](./cmd/)

---

## 3. 测试索引

- Provider 集成测试（httptest mock）→
  [`app/internal/llm/provider/`](./app/internal/llm/provider/)
- 工具测试 →
  [`app/internal/llm/tools/builtin/`](./app/internal/llm/tools/builtin/)

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
- 已知限制与未来改进 → 同上两份 README 的"已知限制"段

---

## 7. 沟通语言

简体中文。与 [`README.md`](./README.md)、
