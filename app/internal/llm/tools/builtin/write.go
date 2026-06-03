package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hellopoisonx/boring/app/internal/llm"
)

// writeSchema 是 write 工具的 JSON Schema。
const writeSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path relative to work dir, or absolute path inside work dir. Parent directories are created automatically."
    },
    "content": {
      "type": "string",
      "description": "Full file content (overwrites any existing file)"
    }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteTool 实现覆盖写文件的 write 工具。
type WriteTool struct{}

// NewWrite 构造 write 工具实例。
func NewWrite() *WriteTool { return &WriteTool{} }

// Info 返回 write 工具的 LLM 描述。
//
// 设计权衡：write 是"硬覆盖"，不预防 LLM 误覆盖。理由：
//  1. 与 read/edit 配合：read 看到现状 → LLM 决策 → write 替换，是
//     Claude Code/Cursor 等主流 Agent 的标准工作流；
//  2. 复杂确认机制（"are you sure?"）会污染 LLM 上下文、增加回合成本；
//  3. 真正需要保护的文件应通过 [Env.ReadOnly] 或 chroot 只读挂载
//     在系统层做，工具层不承担。
//
// 重要约束（防止 LLM 绕开 hashline 安全网）：write 在覆盖一个"非空
// 且已存在"的文件前，会要求该文件在本对话中已被 read 过（[Env.Tracker]
// 追踪）。目的是阻止 LLM 在 edit 撞到 stale anchor 后改用 write 强
// 行覆盖整个文件——此时 LLM 没有当前内容，write 出来的东西极易
// 丢内容、覆盖别人修改。
// 例外：写新文件、覆盖空文件无需 read（无内容可丢）。
func (t *WriteTool) Info() llm.ToolInfo {
	return llm.ToolInfo{
		Name: WriteToolName,
		Description: "Overwrite a file with the given content. " +
			"Parent directories are created automatically. " +
			"Use this for new files or when you have already read the existing file and decided to replace it entirely; " +
			"for surgical changes prefer the `edit` tool. " +
			"Atomic: the file is never observed half-written. " +
			"CONSTRAINT: overwriting an existing non-empty file requires a prior `read` in this conversation; " +
			"if you have not read the file yet (or anchors drifted), call `read` first, " +
			"then prefer `edit` (with fresh anchors) or `write` (for full replacement).",
		Schema: writeSchema,
	}
}

// Execute 覆盖写文件。
//
// 流程：路径沙箱 → 字节上限校验 → 解析 symlink → read-first 校验
// （仅覆盖非空文件时）→ 串行化（per-canonical-path 锁）→ 创建父目录
// → 原子写（temp + rename）→ 返回结果。
//
// 关键安全规则：覆盖已存在的非空文件前必须先 read（[Env.Tracker]），
// 详见 [WriteTool.Info]。
func (t *WriteTool) Execute(_ context.Context, env Env, call *llm.ToolCall) (*llm.ToolResult, error) {
	if env.ReadOnly {
		return errResult(call, ErrReadOnly.Error()), nil
	}

	var args writeArgs
	if err := parseArgs(call, &args); err != nil {
		return errResult(call, err.Error()), nil
	}
	if args.Path == "" {
		return errResult(call, "path is required"), nil
	}

	absPath, relPath, err := env.Resolve(args.Path)
	if err != nil {
		return errResult(call, err.Error()), nil
	}

	// 字节上限：覆盖写一次性把整文件塞给 LLM 后端，太大就拒绝
	if env.MaxBytes > 0 && int64(len(args.Content)) > env.MaxBytes {
		return errResult(call, fmt.Sprintf(
			"content too large: %d bytes (env max %d)", len(args.Content), env.MaxBytes,
		)), nil
	}

	// 解析 symlink 后上锁（写软链 → 写真实目标，保留软链结构）
	canonical := absPath
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		canonical = resolved
	}

	// 写前 read-first 校验：阻止 LLM 绕开 hashline 安全网。
	//
	// 触发条件全部成立才拒绝：
	//  1. [Env.Tracker] 非 nil（明启用 read 追踪）；
	//  2. 目标文件已存在且非空；
	//  3. 本对话中尚未 read 过此文件。
	//
	// 豁免：
	//  - Tracker == nil → 不做校验（无状态执行的向后兼容，详见
	//    [FileState] 文档）；
	//  - 写新文件 / 覆盖空文件 → 无现有内容可丢，无需 read。
	if env.Tracker != nil {
		if existing, readErr := os.ReadFile(canonical); readErr == nil && len(existing) > 0 {
			if !env.Tracker.WasRead(canonical) {
				return errResult(call,
					"refusing to overwrite existing non-empty file without prior `read`; "+
						"call `read` first to see the current content, "+
						"then prefer `edit` (with fresh anchors) or `write` (for full replacement)."), nil
			}
		}
	}

	defaultFileLocks.Lock(canonical)
	defer defaultFileLocks.Unlock(canonical)

	// 父目录：必须用 canonical 的父目录（如果 symlink 已解析，dir 跟着
	// 走；如果路径不存在，canonical==absPath，dir 是路径声明的父目录）
	dir := filepath.Dir(canonical)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent: %w", err)
		}
	}

	if err := atomicWrite(canonical, []byte(args.Content), 0o644); err != nil {
		return nil, fmt.Errorf("atomic write: %w", err)
	}

	return &llm.ToolResult{
		ID:     call.ID,
		ToolID: call.ToolID,
		Result: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), relPath),
	}, nil
}
