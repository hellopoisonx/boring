package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hellopoisonx/boring/app/internal/llm"
)

// defaultReadLimit read 工具单次默认最多返回的行数。
//
// 经验值：2000 行通常 ≈ 50KB 文本（按 25 字符/行），远低于常见 200K
// context 窗口的 0.1%，对 LLM 友好；超出部分 LLM 通过 offset 续读。
const defaultReadLimit = 2000

// readSchema 是 read 工具的 JSON Schema，直接拼到 [llm.ToolInfo.Schema] 里。
//
// 字段：
//   - path:   必填，相对 WorkDir 或绝对路径（必须落在 WorkDir 内）
//   - offset: 选填，1-indexed 起始行，默认 1
//   - limit:  选填，最多返回行数，默认 2000
const readSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path relative to work dir, or absolute path inside work dir"
    },
    "offset": {
      "type": "integer",
      "minimum": 1,
      "description": "1-indexed line number to start from; default 1"
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "description": "Maximum number of lines to return; default 2000"
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// ReadTool 实现 hashline 协议的 read 工具。
type ReadTool struct{}

// NewRead 构造 read 工具实例。
func NewRead() *ReadTool { return &ReadTool{} }

// Info 返回 read 工具的 LLM 描述。
func (t *ReadTool) Info() llm.ToolInfo {
	return llm.ToolInfo{
		Name: ReadToolName,
		Description: "Read a text file with line-anchored output. " +
			"Each line is prefixed with `LINE#HASH: content` where HASH is a 2-char " +
			"content fingerprint. Pass the `LINE#HASH` anchors to the `edit` tool " +
			"to target lines precisely; the hash check guarantees the file has not " +
			"changed since you read it. " +
			"NOTE: a successful read marks the file as 'seen' in this conversation; " +
			"`write` requires this mark before overwriting an existing non-empty file " +
			"(so you cannot bypass the hashline safety net by jumping straight to `write`).",
		Schema: readSchema,
	}
}

// Execute 读取文件并以 hashline 格式返回。
//
// 行为要点：
//   - 拒绝目录、二进制探测：若文件中存在 NUL 字节则报错（避免把二进制
//     渲染成 ANSI 乱码污染 LLM context）；
//   - 超出 limit 时截断并在末尾追加"续读提示"指明下一 offset；
//   - 空文件返回业务错误，提示 LLM 改用 edit prepend/append 或 write。
func (t *ReadTool) Execute(_ context.Context, env Env, call *llm.ToolCall) (*llm.ToolResult, error) {
	var args readArgs
	if err := parseArgs(call, &args); err != nil {
		return errResult(call, err.Error()), nil
	}

	absPath, relPath, err := env.Resolve(args.Path)
	if err != nil {
		return errResult(call, err.Error()), nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errResult(call, fmt.Sprintf("file not found: %s", relPath)), nil
		}
		return errResult(call, fmt.Sprintf("stat: %s", err.Error())), nil
	}
	if info.IsDir() {
		return errResult(call, fmt.Sprintf("is a directory (cannot read as file): %s", relPath)), nil
	}

	// 字节数硬上限
	if env.MaxBytes > 0 && info.Size() > env.MaxBytes {
		return errResult(call, fmt.Sprintf(
			"file too large: %d bytes (env max %d); consider editing it via shell if you only need a slice",
			info.Size(), env.MaxBytes,
		)), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errResult(call, fmt.Sprintf("read: %s", err.Error())), nil
	}

	// 二进制探测：第一个 NUL 字节即视为二进制
	if indexOfNUL(data) >= 0 {
		return errResult(call, fmt.Sprintf(
			"binary file (%d bytes); hashline protocol only supports text files", len(data),
		)), nil
	}

	// 拆行
	lines := newLineCount(data)
	if len(lines) == 0 {
		return errResult(call, fmt.Sprintf(
			"file is empty; use 'edit' with op=prepend or op=append to add content: %s", relPath,
		)), nil
	}

	// offset/limit 默认值与边界裁剪
	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}
	if offset > len(lines) {
		return errResult(call, fmt.Sprintf(
			"offset %d exceeds file length (%d lines)", offset, len(lines),
		)), nil
	}

	end := offset - 1 + limit
	truncated := false
	if end > len(lines) {
		end = len(lines)
	}
	if end-offset+1 < len(lines)-(offset-1) {
		// 还剩没读完的行
		truncated = (end < len(lines))
	}
	visible := lines[offset-1 : end]

	// 行号补齐宽度：按本块覆盖的最大行号决定
	width := lineWidthFor(offset + len(visible) - 1)

	var sb strings.Builder
	for i, content := range visible {
		lineNum := offset + i
		// 注意：newLineCount 永远至少返回 1 个元素；空文件返回 [""]，
		// 此时行号也按 1 走，LLM 看到的"line 1"指向唯一的空行。
		h := lineHash(content, lineNum)
		sb.WriteString(formatLine(lineNum, width, h, content))
		sb.WriteByte('\n')
	}

	// 截断提示
	if truncated {
		nextOffset := end + 1
		fmt.Fprintf(&sb, "... (file has %d more lines; use offset=%d to continue)\n", len(lines)-end, nextOffset)
	}

	// 标记"已读"：write 工具覆盖非空文件前会查这个标记，防止 LLM 在
	// edit 撞 stale anchor 后改用 write 强行覆盖整个文件。
	//
	// 用 canonical 路径（symlink 解析后）让指向同一文件的不同别名共享
	// read 状态；与 write 工具的查寻路径保持一致。
	if env.Tracker != nil {
		canonical := absPath
		if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
			canonical = resolved
		}
		env.Tracker.MarkRead(canonical)
	}

	return &llm.ToolResult{
		ID:     call.ID,
		ToolID: call.ToolID,
		Result: strings.TrimRight(sb.String(), "\n"),
	}, nil
}

// indexOfNUL 返回 data 中第一个 NUL 字节的下标；-1 表示无 NUL。
//
// 用 bytes.IndexByte 内联避免 import bytes（节省 1 行 + 编译时间）。
func indexOfNUL(data []byte) int {
	for i, b := range data {
		if b == 0 {
			return i
		}
	}
	return -1
}
