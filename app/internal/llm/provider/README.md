# LLM Provider 适配层

本目录实现三种 LLM 协议到 `app/internal/llm.LLM` 统一接口的适配层：

| Provider 名称 | 协议 | 官方 SDK |
|---|---|---|
| `OpenAIChat` | OpenAI Chat Completions API | `github.com/openai/openai-go/v3` |
| `OpenAIResponse` | OpenAI Responses API | `github.com/openai/openai-go/v3/responses` |
| `AnthropicMessage` | Anthropic Messages API | `github.com/anthropics/anthropic-sdk-go` |

三个 Provider 都实现同一接口，可按 `config.LLMConfig.Sdk` 字段工厂式创建：

```go
import (
    "github.com/hellopoisonx/boring/app/internal/config"
    "github.com/hellopoisonx/boring/app/internal/llm"
    "github.com/hellopoisonx/boring/app/internal/llm/provider"
)

func NewLLM(cfg config.LLMConfig) llm.LLM {
    switch cfg.Sdk {
    case config.SdkOpenAIChat:
        return provider.NewOpenAIChat(cfg)
    case config.SdkOpenAIResponse:
        return provider.NewOpenAIResponse(cfg)
    case config.SdkAnthropicMessage:
        return provider.NewAnthropicMessage(cfg)
    default:
        panic("unsupported sdk: " + string(cfg.Sdk))
    }
}
```

## 调用示例

### 非流式

```go
msg, err := llm.Generate(ctx, llm.GenerateRequest{
    System: "你是一名助手。",
    Tools:  []llm.ToolInfo{ {Name: "get_weather", Schema: `{"type":"object","properties":{"city":{"type":"string"}}}`} },
    Input:  llm.NewUserMessage(llm.NewTextContent("北京天气如何？")),
})
// msg.MsgType == MessageTypeToolCall → 执行工具，回喂结果
// msg.MsgType == MessageTypeAssistant → 直接展示 msg.Text()
```

### 流式

```go
reader, err := llm.GenerateWithStream(ctx, req)
for {
    chunk, err := reader.Recv(ctx)
    if err != nil { break } // io.EOF 或 closed
    switch chunk.Type {
    case llm.StreamChunkTypeText:
        fmt.Print(chunk.Text)
    case llm.StreamChunkTypeToolCall:
        // 一次完整的工具调用声明
    case llm.StreamChunkTypeFinish:
        // 收尾：chunk.FinishReason + chunk.Usage
    }
}
```

## 关键设计决策

### 1. 严格不重写 SDK

三个 Provider 文件**禁止**手写 HTTP 请求、签名构造、JSON 编解码。所有协议细节都委托给 `openai.NewClient(...)` / `anthropic.NewClient(...)` 返回的官方 SDK 客户端。Provider 的全部职责是：

1. 把统一的 `llm.GenerateRequest` 翻译为对应 SDK 的 `…NewParams`
2. 调 `client.Xxx.New(ctx, params)` 或 `client.Xxx.NewStreaming(ctx, params)`
3. 把 SDK 响应解析为统一的 `llm.Message` / `llm.StreamChunk`
4. 错误归一化为 `*llm.Error`

### 2. 屏蔽 Chat Completions 与 Responses 的请求/响应结构差异

| 维度 | Chat Completions | Responses |
|---|---|---|
| system 提示 | `messages[0] = {role: "system", ...}` | 顶层 `instructions` 字段（不再混入 messages） |
| 历史输入 | `messages: [...]` | `input: { type: "message", ... }[]`（item 列表） |
| assistant 工具调用回喂 | `assistant message + tool_calls` | `function_call` item + 可选 `message` item |
| 工具结果回喂 | `role: "tool"` 消息（每条结果一条） | `function_call_output` item（每条结果一条） |
| 非流式响应位置 | `choices[0].message.content` | `output[]` 数组（按 type 路由） |
| 流式形态 | 增量 delta（连续） | 离散事件（`response.output_text.delta` / `response.function_call_arguments.delta` / `response.completed`） |
| 状态/结束 | `choices[].finish_reason` 字符串 | `response.status`（completed / failed / incomplete）+ `incomplete_details.reason` |

`OpenAIChat` 与 `OpenAIResponse` 的 `buildParams` / `consumeStream` / `parseResponseOutput` 各自处理，但**对外暴露完全一致的** `llm.Message` / `llm.StreamChunk` —— 调用方无感。

### 3. 工具 ID 统一为 string

- 统一接口的 `ToolCall.ID` / `ToolResult.ID` 由 `uint64` 改为 `string`（见 `app/internal/llm/types.go`）。
- 原因：OpenAI Chat/Responses 与 Anthropic 三家 API 返回的 tool call id 都是 string（`call_xxx` / `toolu_xxx`）。强制 string 让 Provider 无需在内部维护"string ↔ uint64"映射，跨调用也可以正确回传。
- `ToolInfo.ID` 保留为 `uint64`（业务侧 ID，与 `Name` 配对使用）。

### 4. Model / Temperature / MaxTokens 从 LLMConfig 注入

- 统一 `GenerateRequest` 不带 Model/Temperature 字段；
- Provider 构造时从 `config.LLMConfig.Model.ID` 取模型名、`Model.MaxResponse` 取 `max_tokens`（Anthropic 必填）、温度硬编码为 `1.0`（各家协议的中性默认）；
- BaseURL 缺省时回退到 `Sdk.DefaultBaseURL()` 返回的官方地址（OpenAI → `https://api.openai.com/v1`，Anthropic → `https://api.anthropic.com`）。

### 5. 错误统一为 `*llm.Error`

`common.go::wrapError` 通过 `errors.As` 识别 `openai.Error` / `anthropic.Error`（两者都是 `apierror.Error` 的公开 type alias），提取 `StatusCode` 与 `Message`；网络层 / ctx 错误归一化为 `StatusCode=0`。调用方用 `errors.As(err, &llmError)` 即可。

### 6. 流式通过 `asyncrw.AsyncReader` 暴露

`GenerateWithStream` 启动独立 goroutine 消费 SDK 的 `*ssestream.Stream`，把 chunk 写入 `asyncrw.AsyncWriter[StreamChunk]`；调用方通过 `AsyncReader.Recv(ctx)` 拉取，避免阻塞主流程。

> 关键实现细节：`asyncrw` 内部 `Recv` 用 `, ok := <-a.ch` 语法区分"buffered 数据"与"channel 已关闭"，避免 `Close` 时 select 随机选到 closed-read 而丢失数据。详见 `app/shared/asyncrw/asyncrw.go` 注释。

### 7. 三家协议的 Tool Schema 形式略有差异

| Provider | 工具定义字段 |
|---|---|
| OpenAI Chat | `tools[].function.parameters: map[string]any` |
| OpenAI Responses | `tools[].parameters: any`（同 Chat） |
| Anthropic | `tools[].input_schema: { properties, required, type:"object" }`（嵌套结构） |

`convertToolsChat` / `convertToolsResponse` 透传 `Properties: schema["properties"]` + `Required: schema["required"]`，所以**业务方只需写一个 JSON Schema 字符串**就能在三种协议间切换。

## 测试

```bash
# 跑全部 Provider 单元 + 集成测试（httptest mock HTTP）
go test ./app/internal/llm/provider/

# 含 race detector（推荐）
go test -race ./app/internal/llm/provider/

# 多次跑检测偶发 race
go test -count=30 -race ./app/internal/llm/provider/
```

`openai_chat_test.go` / `openai_response_test.go` / `anthropic_message_test.go` 覆盖非流式（文本 + 工具调用）+ 流式（多 chunk + finish）两种模式。

## 切换协议 = 切换 config

业务方无需修改 LLM 调用代码，仅需在 `config.yaml` 中切换 `sdk` 字段：

```yaml
llm:
  sdk: anthropic-message   # 或 openai-chat / openai-response
  apiKey: sk-...
  baseUrl: https://api.example.com   # 可选；缺省走官方默认
  model:
    id: claude-3-5-sonnet-20241022
    maxResponse: 4096
```

`NewLLM(cfg)` 工厂自动派发到对应 Provider；统一 `llm.GenerateRequest` 跨协议无差异。
