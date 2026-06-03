// chat 是 boring 的单轮对话 CLI。
//
// 用法：
//
//	go run ./app/cmd/chat --prompt "你好"
//	go run ./app/cmd/chat --config config.yaml --prompt "你好" --stream
//
// LLM 配置完全委托给 [config.Load]，优先级 flag > env (BORING_*) > config.yaml。
// 第一次跑需要先准备 config.yaml（参考 app/internal/config/loader.go 注释）。
//
// 物理位置说明：本文件位于 app/cmd/chat/ 而非根 cmd/chat/。
// 因为 Go internal 规则限制 app/internal/... 只能被 app/... 子树访问；
// 见 AGENTS.md §2.7 索引说明。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/hellopoisonx/boring/app/internal/agent"
	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/internal/llm/provider"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := pflag.NewFlagSet("chat", pflag.ContinueOnError)
	fs.SortFlags = false
	var (
		configPath = fs.String("config", "config.yaml", "LLM 配置文件路径")
		prompt     = fs.String("prompt", "", "用户输入（必填）")
		system     = fs.String("system", "", "可选的 system 提示")
		stream     = fs.Bool("stream", false, "使用流式输出（逐 token 打印到 stdout）")
	)
	// 注册 config 包会绑定的 5 个 LLM flag，使 --help 完整、CLI 可直接覆盖。
	// 见 [config.Options.FlagSet] 与 bindFlags。
	fs.String("provider", "", "LLM provider 内置预设: openai / anthropic / deepseek")
	fs.String("base-url", "", "LLM provider base URL")
	fs.String("api-key", "", "LLM provider API key")
	fs.String("sdk", "", "SDK 协议: openai-chat / openai-response / anthropic-message / deepseek")
	fs.String("model-id", "", "Model ID (如 gpt-4o)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return err
	}
	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: chat --prompt <text> [--config path] [--system text] [--stream]")
		return errors.New("--prompt 为必填")
	}

	// 走 config.Load 统一收口：flag > env (BORING_*) > file。
	// --api-key / --sdk / --base-url / --model-id / --provider 也由 config 包识别（详见 bindFlags）。
	loader, err := config.Load(*configPath, config.Options{
		EnvPrefix: "BORING",
		FlagSet:   fs,
	})
	if err != nil {
		return err
	}
	defer func() { _ = loader.Close() }()

	l := provider.NewLLM(*loader.Config())
	chat := agent.NewChat(l, agent.ChatOptions{System: *system})

	// SIGINT / SIGTERM 触发 ctx cancel，传给 LLM SDK 走优雅退出
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *stream {
		return runStream(ctx, chat, *prompt)
	}
	return runSync(ctx, chat, *prompt)
}

func runSync(ctx context.Context, chat *agent.Chat, prompt string) error {
	text, usage, err := chat.Reply(ctx, prompt)
	if err != nil {
		return err
	}
	fmt.Printf("assistant: %s\n", text)
	if usage != nil {
		fmt.Fprintf(os.Stderr, "[usage] prompt=%d completion=%d total=%d\n",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
	return nil
}

func runStream(ctx context.Context, chat *agent.Chat, prompt string) error {
	reader, err := chat.ReplyStream(ctx, prompt)
	if err != nil {
		return err
	}
	fmt.Print("assistant: ")

	var (
		finishReason llm.FinishReason
		finishUsage  *llm.Usage
	)
	for {
		chunk, err := reader.Recv(ctx)
		if err != nil {
			if errors.Is(err, asyncrw.ErrAsyncReaderClosed) {
				break
			}
			return err
		}
		switch chunk.Type {
		case llm.StreamChunkTypeText:
			fmt.Print(chunk.Text)
		case llm.StreamChunkTypeToolCall:
			// 单轮 agent 不执行工具调用；吞掉，finish 阶段如有错误会单独报
		case llm.StreamChunkTypeFinish:
			finishReason = chunk.FinishReason
			finishUsage = chunk.Usage
		}
	}
	fmt.Println()

	if finishReason == llm.FinishReasonError {
		return errors.New("llm stream ended with error")
	}
	if finishUsage != nil {
		fmt.Fprintf(os.Stderr, "[usage] prompt=%d completion=%d total=%d\n",
			finishUsage.PromptTokens, finishUsage.CompletionTokens, finishUsage.TotalTokens)
	}
	return nil
}
