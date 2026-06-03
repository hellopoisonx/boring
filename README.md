# boring

一个 Go 写的 LLM 代理核心抽象层：把多家 LLM 协议（OpenAI Chat Completions /
OpenAI Responses / Anthropic Messages）屏蔽在同一接口之后，让上层业务只用
关心"对话 + 工具调用"两件事。

## 模块地图

| 路径 | 作用 |
|---|---|
| `app/internal/llm/` | LLM 统一接口与基础类型（`LLM` / `Message` / `ToolCall` / `StreamChunk`） |
| `app/internal/llm/provider/` | 三家协议适配层（OpenAI Chat / OpenAI Responses / Anthropic Messages） |
| `app/internal/llm/tools/builtin/` | LLM 可直接调用的内置文件工具（hashline read / edit / write） |
| `app/internal/config/` | YAML 配置类型（`LLMConfig` / `Model` / `Sdk` 协议枚举） |
| `app/shared/asyncrw/` | 泛型异步 I/O 抽象（流式 chunk 通过它暴露给调用方） |

每个子目录的 `README.md` 描述详细的架构与设计决策。

## 30 秒上手

```go
import (
    "github.com/hellopoisonx/boring/app/internal/config"
    "github.com/hellopoisonx/boring/app/internal/llm"
    "github.com/hellopoisonx/boring/app/internal/llm/provider"
)

func NewLLM(cfg config.LLMConfig) llm.LLM {
    switch cfg.Sdk {
    case config.SdkOpenAIChat:        return provider.NewOpenAIChat(cfg)
    case config.SdkOpenAIResponse:    return provider.NewOpenAIResponse(cfg)
    case config.SdkAnthropicMessage:  return provider.NewAnthropicMessage(cfg)
    }
    panic("unsupported sdk: " + string(cfg.Sdk))
}
```

切换协议 = 切换 `config.yaml` 里的 `sdk` 字段，业务代码无需改动。

## 构建与测试

```bash
go build ./...
go test ./...
go test -race ./app/internal/llm/provider/
```

## 项目状态

早期：核心抽象已就位（统一接口 + 协议适配 + 内置文件工具 + 异步 I/O），
调度器 / 持久化 / 租户隔离层还在路上。详见 `CHANGELOG.md`。
