// Package builtin 提供 LLM 直接可调用的内置文件工具：read / edit / write。
//
// 三个工具的设计遵循 [RimuruW/pi-hashline-edit] 的 hashline 协议：
// read 输出形如 `LINE#HASH: content` 的带锚点文本，edit 通过 `LINE#HASH` 锚点
// 精确定位行；任何在 read 与 edit 之间发生的文件变更都会导致 hash 失配并被
// 拒绝，迫使 LLM 重新读取最新内容，杜绝"基于陈旧上下文写错文件"。
//
// 后期多租户隔离：所有 IO 都经过 [Env.Resolve] 路径沙箱校验，落点必须位于
// [Env.WorkDir] 之内；切换到真正的进程级 chroot 只需把 Env 的实现替换为带
// syscall.Chroot 的版本，业务工具代码无需改动。
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hellopoisonx/boring/app/internal/llm"
)

// ReadToolName / EditToolName / WriteToolName 是暴露给 LLM 的工具名常量。
const (
	ReadToolName  = "read"
	EditToolName  = "edit"
	WriteToolName = "write"
)

// ErrReadOnly 在 [Env.ReadOnly] 为 true 时被 edit/write 工具返回给 LLM。
//
// 用途：chroot 阶段可将 WorkDir 以只读方式挂载；现阶段可在演示/审计场景
// 用 ReadOnly=true 强制所有写操作拒绝。
var ErrReadOnly = errors.New("execution environment is read-only")

// Env 工具执行环境。
//
// Env 是工具与外部世界的唯一接口：租户隔离、沙箱路径、资源上限、只读开关
// 全部通过 Env 注入，工具实现本身不感知多租户/沙箱的存在。
//
// 当前阶段：Env 主要承担"软沙箱"职责——把任何传入路径都解析并校验必须落在
// [Env.WorkDir] 之内（防 ../ 越权），并通过 [Env.MaxBytes] / [Env.ReadOnly]
// 限制单次操作的资源消耗。
//
// 后期多租户：调度器为每个请求创建独立 Env，
//   - [Env.TenantID] 用于审计日志/限流/链路追踪关联；
//   - [Env.WorkDir] 指向 chroot 内的子目录 (e.g. /tenants/{id}/workspace)，
//     工具代码无需任何改动即可享受进程级文件系统隔离。
type Env struct {
	// TenantID 租户标识；为 "" 表示单租户/本地开发模式。
	TenantID string

	// WorkDir 租户工作根目录；所有相对路径都基于此解析，所有绝对路径必须
	// 落在此目录之内。WorkDir 必须为绝对路径；为 "" 时 Env 拒绝所有路径
	// 访问（fail-closed）。
	WorkDir string

	// MaxBytes 读/写工具允许处理的最大字节数；0 表示不限制。
	//
	// 设这个上限的动机：避免 LLM 一次把几 GB 的日志/二进制塞进 context 窗口
	// 导致 OOM；具体阈值由调度方按租户配额决定。
	MaxBytes int64

	// ReadOnly 只读模式；true 时 edit/write 拒绝执行。
	//
	// 现阶段可用于"演示模式"/"审计快照"等场景；chroot 化后可与
	// 只读挂载 (mount -o ro) 配合作为双重保险。
	ReadOnly bool

	// Tracker 文件读状态追踪器，跨工具调用共享。read 成功后把文件标记
	// 为"已读"，write 在覆盖非空文件前会校验"是否已 read"。
	//
	// 用途：防止 LLM 在 edit 撞到 stale anchor 后改用 write 强行覆盖整个
	// 文件——即"绕开 hashline 安全网"反模式。强制 read-before-overwrite
	// 让 LLM 必须先看到最新内容才能 write。
	//
	// 为 nil 时不追踪（write 不会卡 read-first 校验），用于无状态执行
	// 和单测场景。
	Tracker *FileState
}

// FileState 跟踪本对话中"文件被 read 过"的状态。
//
// 并发：所有方法内部上锁；Env 通过 *FileState 指针共享状态（Env 本身按值
// 传递）。典型用法：调度器在创建 Env 时 new(FileState)，read/edit/write
// 共享同一个实例。
//
// 设计取舍：只记"是否 read 过"，不记 mtime/版本号。理由——
//   - 目标场景是 LLM 短对话 + 同步 read→write，单回合内 mtime 漂移
//     概率低到不值得追踪；
//   - 不增加 IO（每次 read 都要 stat 拿 mtime）；
//   - 真实长流程的 stale 检测由 edit 的 hash 失配负责（更精确）。
type FileState struct {
	mu    sync.Mutex
	reads map[string]struct{} // absPath -> 标记
}

// MarkRead 记录某文件在本对话中已被 read。
//
// 调用方应传入 symlink 解析后的 canonical 路径，让指向同一文件的不同
// 别名共享 read 状态。
func (s *FileState) MarkRead(absPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads == nil {
		s.reads = make(map[string]struct{})
	}
	s.reads[absPath] = struct{}{}
}

// WasRead 报告某文件是否在本对话中已被 read 过。
//
// 调用方应传入与 [FileState.MarkRead] 同样的 canonical 路径。
func (s *FileState) WasRead(absPath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.reads[absPath]
	return ok
}

// DefaultEnv 返回一个面向本地开发用的默认 Env：以调用方 cwd 为工作目录、
// 无字节限制、可写。生产/多租户场景请显式构造 Env。
func DefaultEnv() Env {
	wd, _ := os.Getwd()
	return Env{WorkDir: wd}
}

// Resolve 把用户传入的路径转换为绝对路径，并校验必须落在 env.WorkDir 之内。
//
// 行为：
//   - path 为空 → 报错；
//   - 相对路径 → 拼到 env.WorkDir；
//   - 绝对路径 → 校验其解析后必须位于 env.WorkDir 子树内（防 ../ 越权）。
//
// 返回：abs=绝对路径（用于真实 IO）、rel=相对 WorkDir 的显示路径（用于返回给 LLM）。
// 任何越界都返回 error（fail-closed），不会 fallback 到 host 根目录。
func (e Env) Resolve(path string) (abs string, rel string, err error) {
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("path is empty")
	}
	if e.WorkDir == "" {
		return "", "", errors.New("env work dir is empty (refusing to resolve path)")
	}

	absWorkDir, err := filepath.Abs(e.WorkDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve work dir: %w", err)
	}
	absWorkDir = filepath.Clean(absWorkDir)

	var absPath string
	if filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	} else {
		absPath = filepath.Join(absWorkDir, path)
	}

	relPath, err := filepath.Rel(absWorkDir, absPath)
	if err != nil {
		return "", "", fmt.Errorf("path %q escapes work dir %q: %w", path, absWorkDir, err)
	}
	// filepath.Rel 在路径位于外部时会返回 ".." 或 "../xxx"；同时拒绝
	// 形如 ".." 的相对路径（哪怕 WorkDir 本身有问题）。
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes work dir %q", path, absWorkDir)
	}

	return absPath, relPath, nil
}

// Tool 所有 LLM 工具的统一接口。
//
// 约定：
//   - 业务级错误（"文件不存在"、"hash 失配"、"oldText 不唯一"等）→
//     通过返回的 [llm.ToolResult.Result] 字符串传达（"Error: ..." 前缀），
//     同时 error = nil。LLM 看到 Result 后可自我修正。
//   - 系统级错误（Env 配置缺失、IO 不可用、沙箱逃逸等）→
//     返回 nil, error，由调用方决定是否回退/告警。
//
// 这样区分是因为 LLM 看到 "Error: ..." 后能继续对话；而真正的环境问题
// 越早暴露给上层越好，绝不能让 LLM 误以为"这是任务的一部分"。
type Tool interface {
	// Info 返回工具描述（暴露给 LLM）。
	Info() llm.ToolInfo
	// Execute 执行一次工具调用。
	Execute(ctx context.Context, env Env, call *llm.ToolCall) (*llm.ToolResult, error)
}

// errResult 把业务错误包装成 *llm.ToolResult 返回给 LLM。
//
// "Error: " 前缀让 LLM 一眼区分结果/错误，且便于在 trace 中按前缀检索。
func errResult(call *llm.ToolCall, msg string) *llm.ToolResult {
	return &llm.ToolResult{
		ID:     call.ID,
		ToolID: call.ToolID,
		Result: "Error: " + msg,
	}
}

// parseArgs 是 [json.Unmarshal] 的薄封装，把错误统一为业务错误返回。
// tool 文件里到处用到，集中起来减少样板。
func parseArgs(call *llm.ToolCall, dst any) error {
	if len(call.Args) == 0 {
		return errors.New("args is empty (missing required parameters)")
	}
	if err := json.Unmarshal(call.Args, dst); err != nil {
		return fmt.Errorf("args parse error: %w", err)
	}
	return nil
}

// newLineCount 把字节切片拆成"行"切片，行号 1-indexed 语义保持一致。
//
// 拆分语义（与 [bufio.Scanner] 按行迭代一致）：
//   - ""           → nil           (0 行)
//   - "a"          → ["a"]         (1 行)
//   - "a\n"        → ["a"]         (1 行；trailing \n 是分隔符，不是空行)
//   - "a\nb"       → ["a", "b"]    (2 行；末尾无 \n 不算空行)
//   - "a\nb\n"     → ["a", "b"]    (2 行)
//   - "\n"         → [""]          (1 个空行)
//   - "\n\n"       → ["", ""]      (2 个空行)
//   - "a\nb\n\n"   → ["a", "b", ""]  (a, b, 1 个空行)
//
// 与 strings.Split 的差异：strings.Split 会在 trailing \n 处额外产生一个
// 空 token，违背"人类视角的行数"。本函数把"单个 trailing \n"当作
// 分隔符剥离，但保留多出的 trailing 空 token（中间或多个 \n 留下的）。
//
// joinLines 是反向操作，永远末尾加 "\n"，保证 read → edit → write 不丢字节。
func newLineCount(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	// 把单个 trailing \n 当作分隔符剥离（不影响中间的 \n）
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		// 原 data 是一串 \n：按 \n 个数产出空行
		n := strings.Count(string(data), "\n")
		return make([]string, n)
	}
	return strings.Split(s, "\n")
}

// joinLines 把行切片拼回字节切片，末尾永远加 "\n"（除非本身为空）。
// 是 [newLineCount] 的反向操作，保证 read → edit → write 路径上的字节
// 序列对齐 read 前看到的字节序列。
func joinLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
