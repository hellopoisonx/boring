// chat 是 boring 的对话 CLI。
//
// 单次调用：
//
//	go run ./app/cmd/chat --profile alice --prompt "你好"
//	go run ./app/cmd/chat --profile alice --config config.yaml --prompt "你好" --stream
//
// 交互式多轮对话（不传 --prompt）：
//
//	go run ./app/cmd/chat --profile alice
//	go run ./app/cmd/chat --profile alice --stream
//
// LLM 配置完全委托给 [config.Load]，优先级 flag > env (BORING_*) > config.yaml。
// DB 路径：--db > BORING_DB > storage.dsn (config.yaml) > ./boring.db。
// 租户（profile）必填；第一次使用自动创建 tenant 和 conv。
//
// 物理位置说明：本文件位于 app/cmd/chat/ 而非根 cmd/chat/。
// 因为 Go internal 规则限制 app/internal/... 只能被 app/... 子树访问；
// 见 AGENTS.md §2.7 索引说明。
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/hellopoisonx/boring/app/internal/agent"
	"github.com/hellopoisonx/boring/app/internal/config"
	"github.com/hellopoisonx/boring/app/internal/llm"
	"github.com/hellopoisonx/boring/app/internal/llm/provider"
	"github.com/hellopoisonx/boring/app/internal/store"
	"github.com/hellopoisonx/boring/app/shared/asyncrw"
)

// chatEnv 捆绑本轮 CLI 启动的租户/会话/模型上下文。
type chatEnv struct {
	convID        int64
	modelID       string
	modelProvider string
	convs         store.TenantConvStore
}

// persistUsage 把一轮 chat 的 token 用量累加到当前 conv。
// DB 写失败只打印 warning，不中断流程（LLM 响应已经成功返回，不应被 DB 错误吞掉）。
func (e *chatEnv) persistUsage(ctx context.Context, usage *llm.Usage) {
	if usage == nil || usage.TotalTokens == 0 {
		return
	}
	if err := e.convs.IncUsage(ctx, e.convID, int64(usage.TotalTokens), e.modelID, e.modelProvider); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] persist usage: %v\n", err)
	}
}

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
		prompt     = fs.String("prompt", "", "用户输入；留空进入交互式多轮对话模式")
		system     = fs.String("system", "", "可选的 system 提示")
		stream     = fs.Bool("stream", false, "使用流式输出（逐 token 打印到 stdout）")
		profile    = fs.String("profile", "", "用户/租户标识（≤255 字符），必填")
		dbDSN      = fs.String("db", "", "SQLite DSN（覆盖 yaml 的 storage.dsn 与 BORING_DB）")
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

	if *profile == "" {
		return errors.New("--profile 是必填参数")
	}

	// 走 config.Load 统一收口：flag > env (BORING_*) > file。
	loader, err := config.Load(*configPath, config.Options{
		EnvPrefix: "BORING",
		FlagSet:   fs,
	})
	if err != nil {
		return err
	}
	defer func() { _ = loader.Close() }()

	// ── DSN 解析：--db > BORING_DB > storage.dsn (yaml) > ./boring.db ──
	dsn := *dbDSN
	if dsn == "" {
		dsn = os.Getenv("BORING_DB")
	}
	if dsn == "" {
		dsn = loader.Viper().GetString("storage.dsn")
	}
	if dsn == "" {
		dsn = "file:boring.db"
	}

	// ── 打开 store、解析 profile → tenant_id ──
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	ut, ti, convs := st.UserTenants(), st.TenantInfos(), st.Convs()

	tid, err := ut.GetByUserID(ctx, *profile)
	if errors.Is(err, sql.ErrNoRows) {
		tid, err = ut.Create(ctx, *profile)
		if err != nil {
			return fmt.Errorf("create tenant for %q: %w", *profile, err)
		}
		if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
			return fmt.Errorf("upsert tenant_info for %q: %w", *profile, err)
		}
	} else if err != nil {
		return fmt.Errorf("resolve tenant for %q: %w", *profile, err)
	}

	// ── 复用或新建 conv ──
	conv, err := convs.LatestActiveByTenant(ctx, tid)
	if errors.Is(err, sql.ErrNoRows) {
		title := fmt.Sprintf("%s @ %s", *profile, time.Now().Format(time.RFC3339))
		newID, err := convs.Create(ctx, tid, title)
		if err != nil {
			return fmt.Errorf("create conv: %w", err)
		}
		conv.ID = newID
	} else if err != nil {
		return fmt.Errorf("lookup latest conv: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[conv] tenant_id=%d conv_id=%d\n", tid, conv.ID)

	// ── 模型信息 ──
	cfg := loader.Config()
	env := &chatEnv{
		convID:        conv.ID,
		modelID:       cfg.Model.ID,
		modelProvider: string(cfg.Provider),
		convs:         convs,
	}

	// ── LLM ──
	l := provider.NewLLM(*cfg)
	chat := agent.NewChat(l, agent.ChatOptions{System: *system})

	if *prompt != "" {
		// 单次调用模式
		if *stream {
			return runStream(ctx, chat, *prompt, env)
		}
		return runSync(ctx, chat, *prompt, env)
	}

	// 交互式多轮对话模式
	return runChat(ctx, chat, *stream, env)
}

func runSync(ctx context.Context, chat *agent.Chat, prompt string, env *chatEnv) error {
	text, usage, err := chat.Reply(ctx, prompt)
	if err != nil {
		return err
	}
	fmt.Printf("assistant: %s\n", text)
	if usage != nil {
		fmt.Fprintf(os.Stderr, "[usage] prompt=%d completion=%d total=%d\n",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		env.persistUsage(ctx, usage)
	}
	return nil
}

func runStream(ctx context.Context, chat *agent.Chat, prompt string, env *chatEnv) error {
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
		env.persistUsage(ctx, finishUsage)
	}
	return nil
}

// ---------- 交互式多轮对话 ----------

// runChat 运行交互式多轮对话循环。
// 从 stdin 逐行读取用户输入，同一 Chat 实例自动维护会话历史。
// 输入 /exit 或 /quit 退出，Ctrl+D（EOF）退出，空行跳过。
func runChat(ctx context.Context, chat *agent.Chat, stream bool, env *chatEnv) error {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Print("> ")
			continue
		}
		switch line {
		case "/exit", "/quit":
			return nil
		}

		if stream {
			chatStream(ctx, chat, line, env)
		} else {
			chatSync(ctx, chat, line, env)
		}
		fmt.Print("> ")
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// EOF
	fmt.Println()
	return nil
}

// chatSync 同步执行一轮对话并打印结果。错误打印到 stderr，不中断循环。
func chatSync(ctx context.Context, chat *agent.Chat, userInput string, env *chatEnv) {
	text, usage, err := chat.Reply(ctx, userInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return
	}
	fmt.Println(text)
	if usage != nil {
		fmt.Fprintf(os.Stderr, "[usage] prompt=%d completion=%d total=%d\n",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		env.persistUsage(ctx, usage)
	}
}

// chatStream 流式执行一轮对话并逐 token 打印。错误打印到 stderr，不中断循环。
func chatStream(ctx context.Context, chat *agent.Chat, userInput string, env *chatEnv) {
	reader, err := chat.ReplyStream(ctx, userInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return
	}

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
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return
		}
		switch chunk.Type {
		case llm.StreamChunkTypeText:
			fmt.Print(chunk.Text)
		case llm.StreamChunkTypeFinish:
			finishReason = chunk.FinishReason
			finishUsage = chunk.Usage
		}
	}
	fmt.Println()

	if finishReason == llm.FinishReasonError {
		fmt.Fprintln(os.Stderr, "error: llm stream ended with error")
		return
	}
	if finishUsage != nil {
		fmt.Fprintf(os.Stderr, "[usage] prompt=%d completion=%d total=%d\n",
			finishUsage.PromptTokens, finishUsage.CompletionTokens, finishUsage.TotalTokens)
		env.persistUsage(ctx, finishUsage)
	}
}
