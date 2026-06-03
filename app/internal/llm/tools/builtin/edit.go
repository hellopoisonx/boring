package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hellopoisonx/boring/app/internal/llm"
)

// editSchema 是 edit 工具的 JSON Schema。
//
// 4 个 op 的字段约定见 [EditTool.Info] 的 Description；schema 主体用
// oneOf 列出 4 种合法组合，LLM 在 strict mode 下能直接收到结构化错误。
const editSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path relative to work dir, or absolute path inside work dir"
    },
    "edits": {
      "type": "array",
      "minItems": 1,
      "items": {
        "oneOf": [
          {
            "type": "object",
            "title": "replace",
            "properties": {
              "op":    { "type": "string", "enum": ["replace"] },
              "pos":   { "type": "string", "description": "LINE#HASH anchor (required)" },
              "end":   { "type": "string", "description": "LINE#HASH anchor for inclusive end (optional, default = pos)" },
              "lines": { "type": "array", "items": { "type": "string" }, "minItems": 1 }
            },
            "required": ["op", "pos", "lines"],
            "additionalProperties": false
          },
          {
            "type": "object",
            "title": "append",
            "properties": {
              "op":    { "type": "string", "enum": ["append"] },
              "pos":   { "type": "string", "description": "LINE#HASH anchor (optional, default = EOF)" },
              "lines": { "type": "array", "items": { "type": "string" }, "minItems": 1 }
            },
            "required": ["op", "lines"],
            "additionalProperties": false
          },
          {
            "type": "object",
            "title": "prepend",
            "properties": {
              "op":    { "type": "string", "enum": ["prepend"] },
              "pos":   { "type": "string", "description": "LINE#HASH anchor (optional, default = BOF)" },
              "lines": { "type": "array", "items": { "type": "string" }, "minItems": 1 }
            },
            "required": ["op", "lines"],
            "additionalProperties": false
          },
          {
            "type": "object",
            "title": "replace_text",
            "properties": {
              "op":      { "type": "string", "enum": ["replace_text"] },
              "oldText": { "type": "string", "description": "Exact substring to replace; must be unique" },
              "newText": { "type": "string" }
            },
            "required": ["op", "oldText", "newText"],
            "additionalProperties": false
          }
        ]
      }
    }
  },
  "required": ["path", "edits"],
  "additionalProperties": false
}`

// editOp 对应 schema 里的 4 种操作。
//
// 老格式（顶层 oldText/newText）也走 replace_text；不在结构上区分。
type editOp struct {
	Op      string   `json:"op"`
	Pos     string   `json:"pos,omitempty"`
	End     string   `json:"end,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	OldText string   `json:"oldText,omitempty"`
	NewText string   `json:"newText,omitempty"`
}

type editArgs struct {
	Path  string   `json:"path"`
	Edits []editOp `json:"edits"`
}

// EditTool 实现 hashline 协议的 edit 工具。
type EditTool struct{}

// NewEdit 构造 edit 工具实例。
func NewEdit() *EditTool { return &EditTool{} }

// Info 返回 edit 工具的 LLM 描述。
//
// 4 种 op 的语义都源自 [RimuruW/pi-hashline-edit]：
//   - replace       替换单行 (pos) 或闭区间 (pos..end)
//   - append        在 pos 后插入；缺省 pos 则追加到 EOF
//   - prepend       在 pos 前插入；缺省 pos 则插入到 BOF
//   - replace_text  全局唯一字符串替换（兼容老格式，hashline 模式下不推荐）
//
// 所有 edits 在同一次调用内共享一份 pre-edit snapshot，先按 anchor 位置
// 降序排序再依次应用，保证前面的 edit 不会影响后面 edit 的行号。
//
// 两条针对 LLM 的关键警告，必须保留在描述里——schema 限制不住、
// 只靠测试也覆盖不到所有 LLM 行为模式：
//   - duplicate pos in same call: only the last edit wins（同一次调用里
//     重复使用同一个 pos 的多次 edit，后续 edit 会覆盖前面的；这是
//     bottom-up 排序的天然行为）；
//   - 文件不存在或为空（0 行）时拒绝执行，提示改用 `write` 工具
//     创建内容——edit 依赖锚点，无锚点即无意义。
func (t *EditTool) Info() llm.ToolInfo {
	return llm.ToolInfo{
		Name: EditToolName,
		Description: "Edit a text file using line anchors from the `read` tool. " +
			"Each `edits[i].pos` is a `LINE#HASH` anchor from a prior `read` call; " +
			"the hash check guarantees the file has not changed since you read it. " +
			"Supports four ops: `replace` (one line or a range), `append` (after pos / EOF), " +
			"`prepend` (before pos / BOF), and `replace_text` (unique substring). " +
			"Multiple edits in one call are applied bottom-up so line numbers stay stable. " +
			"WARNING: duplicate pos in same call: only the last edit wins. " +
			"NOTE: this tool refuses to operate on a non-existent or empty file; use `write` to create new content.",
		Schema: editSchema,
	}
}

// Execute 应用一组 edits 到文件。
//
// 流程：路径沙箱 → per-canonical-path 互斥 → 读现状 → 拒绝（空/不存在）
// → 字节上限校验 → 校验 + 应用 全部 edits → 原子写 → 构造 diff preview
// + updated anchors 返回。
//
// 所有业务错误（hash 失配、oldText 不唯一、文件不存在/为空……）都通过
// [errResult] 返回给 LLM；只有环境类错误（沙箱配置缺失、IO 不可用）才
// 返回 error。
func (t *EditTool) Execute(_ context.Context, env Env, call *llm.ToolCall) (*llm.ToolResult, error) {
	if env.ReadOnly {
		return errResult(call, ErrReadOnly.Error()), nil
	}

	var args editArgs
	if err := parseArgs(call, &args); err != nil {
		return errResult(call, err.Error()), nil
	}
	if len(args.Edits) == 0 {
		return errResult(call, "edits is empty"), nil
	}

	absPath, relPath, err := env.Resolve(args.Path)
	if err != nil {
		return errResult(call, err.Error()), nil
	}

	// 解析 symlink 后再上锁；同 inode 的不同别名会拿到同一把锁
	canonical := absPath
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		canonical = resolved
	}
	defaultFileLocks.Lock(canonical)
	defer defaultFileLocks.Unlock(canonical)
	// 读现状
	exists := true
	data, err := os.ReadFile(canonical)
	if err != nil {
		if !os.IsNotExist(err) {
			return errResult(call, fmt.Sprintf("read: %s", err.Error())), nil
		}
		exists = false
		data = nil
	}

	// 硬约束：文件不存在或为空（0 字节）时拒绝执行 edit。
	//
	// 理由：edit 的核心是锚点（`LINE#HASH`），而锚点必须有"行"才能生成。
	// 0 字节文件或非存在文件无任何行可定位，让 LLM 在这种状态下使用 edit
	// 只会逼它猜——猜错又得改用 write 覆盖整个文件，绕开 hashline 安全网。
	//
	// 正确流程：创建/清空文件用 `write`；编辑现有内容用 `edit`（先 read
	// 拿锚点，再用锚点定位）。这条规则和 write 工具的 read-first 校验
	// 一起把 LLM 锁进"read → {edit, write}"的强制路径。
	if !exists || len(data) == 0 {
		return errResult(call,
			"attempt to edit on an empty file, use `write` tool instead."), nil
	}

	// 字节数上限（写之前再卡一次）
	if env.MaxBytes > 0 {
		estSize := int64(len(data))
		for _, e := range args.Edits {
			if e.Op == "replace_text" {
				estSize += int64(len(e.NewText)) - int64(len(e.OldText))
			} else {
				for _, l := range e.Lines {
					estSize += int64(len(l)) + 1
				}
			}
		}
		if estSize > env.MaxBytes {
			return errResult(call, fmt.Sprintf(
				"result would exceed env max bytes (%d > %d)", estSize, env.MaxBytes,
			)), nil
		}
	}

	// 应用 edits
	before := newLineCount(data)
	after, err := applyEdits(before, args.Edits)
	if err != nil {
		return errResult(call, err.Error()), nil
	}

	newData := joinLines(after)
	if err := atomicWrite(canonical, newData, 0o644); err != nil {
		return nil, fmt.Errorf("atomic write: %w", err)
	}

	return t.makeResult(call, relPath, before, after, args.Edits), nil
}

// indexedEdit 把 edit 和它的原始下标绑在一起，用于排序后还原顺序输出。

type indexedEdit struct {
	op            editOp
	originalIndex int
	line          int // 用于排序的起始行号；-1 表示无 pos（如 append-to-EOF/replace_text）
}

// applyEdits 在 lines 上应用一组 edits，返回新的 lines 切片。
//
// 排序策略：所有 edits 按"影响行号"降序排序后依次应用——后应用的 edit
// 在更靠前的位置，先应用的 edit 在更靠后的位置，互不干扰。
//
// 排序 key 的获取：
//   - replace/append(pos)/prepend(pos)：取 pos 解析出的行号；
//   - append("") 追加到 EOF：行号 = len(lines)+1，固定最大；
//   - prepend("") 插入到 BOF：行号 = 0，固定最小；
//   - replace_text：没有锚点，按原 edits 顺序处理（不会改变行号）。
func applyEdits(lines []string, edits []editOp) ([]string, error) {
	indexed := make([]indexedEdit, len(edits))
	for i, op := range edits {
		var line int = -1
		switch op.Op {
		case "replace", "append", "prepend":
			if op.Pos != "" {
				ln, _, err := parseAnchor(op.Pos)
				if err != nil {
					return nil, fmt.Errorf("edits[%d]: %w", i, err)
				}
				line = ln
			} else if op.Op == "append" {
				line = len(lines) + 1
			} else if op.Op == "prepend" {
				line = 0
			}
		}
		indexed[i] = indexedEdit{op: op, originalIndex: i, line: line}
	}

	// 排序：
	//   - replace_text 不影响行号，按 originalIndex 稳定排序即可
	//   - 其他按 line 降序（line 大的先做，行号不漂移）
	sort.SliceStable(indexed, func(i, j int) bool {
		if indexed[i].op.Op == "replace_text" && indexed[j].op.Op == "replace_text" {
			return indexed[i].originalIndex < indexed[j].originalIndex
		}
		if indexed[i].op.Op == "replace_text" {
			return false
		}
		if indexed[j].op.Op == "replace_text" {
			return true
		}
		if indexed[i].line != indexed[j].line {
			return indexed[i].line > indexed[j].line
		}
		return indexed[i].originalIndex < indexed[j].originalIndex
	})

	cur := make([]string, len(lines))
	copy(cur, lines)

	for _, ie := range indexed {
		if _, _, _, _, err := applyOne(&cur, ie.op, lines); err != nil {
			return nil, fmt.Errorf("edits[%d]: %w", ie.originalIndex, err)
		}
	}
	return cur, nil
}

// applyOne 在 *out 上应用单次 edit。
//
// 原地修改 *out 的长度（缩/扩），避免每次都返回新切片导致 caller 难处理。
// snapshot 是 pre-edit 的行快照（用于 hash 验证，必须保持不变）。
func applyOne(out *[]string, op editOp, snapshot []string) (startLine, endLine, delLines, insLines int, err error) {
	if out == nil {
		return 0, 0, 0, 0, errors.New("applyOne: out is nil")
	}
	switch op.Op {
	case "replace":
		if op.Pos == "" {
			return 0, 0, 0, 0, errors.New("replace: pos is required")
		}
		if len(op.Lines) == 0 {
			return 0, 0, 0, 0, errors.New("replace: lines is required and must be non-empty")
		}
		posLine, posHash, err := parseAnchor(op.Pos)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if err := verifyHash(snapshot, posLine, posHash); err != nil {
			return 0, 0, 0, 0, err
		}
		startIdx := posLine - 1

		endIdx := startIdx
		if op.End != "" {
			endLine, endHash, err := parseAnchor(op.End)
			if err != nil {
				return 0, 0, 0, 0, err
			}
			if endLine < posLine {
				return 0, 0, 0, 0, fmt.Errorf("end %q is before pos %q", op.End, op.Pos)
			}
			if err := verifyHash(snapshot, endLine, endHash); err != nil {
				return 0, 0, 0, 0, err
			}
			endIdx = endLine - 1
		}

		cur := *out
		if startIdx < 0 || endIdx >= len(cur) {
			return 0, 0, 0, 0, fmt.Errorf("replace range %d..%d out of file bounds (file has %d lines)", posLine, endIdx+1, len(cur))
		}

		// 拼接：cur[:startIdx] + op.Lines + cur[endIdx+1:]
		newOut := make([]string, 0, len(cur)+(len(op.Lines)-(endIdx-startIdx+1)))
		newOut = append(newOut, cur[:startIdx]...)
		newOut = append(newOut, op.Lines...)
		newOut = append(newOut, cur[endIdx+1:]...)
		*out = newOut

		return posLine, posLine + len(op.Lines) - 1, endIdx - startIdx + 1, len(op.Lines), nil

	case "append":
		if len(op.Lines) == 0 {
			return 0, 0, 0, 0, errors.New("append: lines is required and must be non-empty")
		}
		cur := *out
		if op.Pos == "" {
			// 追加到 EOF
			newOut := make([]string, 0, len(cur)+len(op.Lines))
			newOut = append(newOut, cur...)
			newOut = append(newOut, op.Lines...)
			*out = newOut
			return len(cur) + 1, len(cur) + len(op.Lines), 0, len(op.Lines), nil
		}
		posLine, posHash, err := parseAnchor(op.Pos)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if err := verifyHash(snapshot, posLine, posHash); err != nil {
			return 0, 0, 0, 0, err
		}
		idx := posLine
		if idx < 0 || idx >= len(cur) {
			return 0, 0, 0, 0, fmt.Errorf("append pos %d out of file bounds (file has %d lines)", posLine, len(cur))
		}
		newOut := make([]string, 0, len(cur)+len(op.Lines))
		newOut = append(newOut, cur[:idx]...)
		newOut = append(newOut, op.Lines...)
		newOut = append(newOut, cur[idx:]...)
		*out = newOut
		return posLine + 1, posLine + len(op.Lines), 0, len(op.Lines), nil

	case "prepend":
		if len(op.Lines) == 0 {
			return 0, 0, 0, 0, errors.New("prepend: lines is required and must be non-empty")
		}
		cur := *out
		if op.Pos == "" {
			// 插入到 BOF
			newOut := make([]string, 0, len(cur)+len(op.Lines))
			newOut = append(newOut, op.Lines...)
			newOut = append(newOut, cur...)
			*out = newOut
			return 1, len(op.Lines), 0, len(op.Lines), nil
		}
		posLine, posHash, err := parseAnchor(op.Pos)
		if err != nil {
			return 0, 0, 0, 0, err
		}
		if err := verifyHash(snapshot, posLine, posHash); err != nil {
			return 0, 0, 0, 0, err
		}
		idx := posLine - 1
		if idx < 0 || idx >= len(cur) {
			return 0, 0, 0, 0, fmt.Errorf("prepend pos %d out of file bounds (file has %d lines)", posLine, len(cur))
		}
		newOut := make([]string, 0, len(cur)+len(op.Lines))
		newOut = append(newOut, cur[:idx]...)
		newOut = append(newOut, op.Lines...)
		newOut = append(newOut, cur[idx:]...)
		*out = newOut
		return posLine, posLine + len(op.Lines) - 1, 0, len(op.Lines), nil

	case "replace_text":
		if op.OldText == "" {
			return 0, 0, 0, 0, errors.New("replace_text: oldText is required")
		}
		cur := *out
		joined := strings.Join(cur, "\n")
		count := strings.Count(joined, op.OldText)
		if count == 0 {
			return 0, 0, 0, 0, errors.New("replace_text: oldText not found in file (whitespace matters)")
		}
		if count > 1 {
			return 0, 0, 0, 0, fmt.Errorf("replace_text: oldText matches %d times; must be unique (use a longer substring or switch to hashline mode)", count)
		}
		oldLines := strings.Count(op.OldText, "\n") + 1
		newJoined := strings.Replace(joined, op.OldText, op.NewText, 1)
		*out = newLineCount([]byte(newJoined))
		return 0, 0, oldLines, strings.Count(op.NewText, "\n") + 1, nil

	default:
		return 0, 0, 0, 0, fmt.Errorf("unknown op: %q", op.Op)
	}
}

// parseAnchor 解析 "LINE#HASH" 锚点。
//
// 严格匹配格式：line 是 1-indexed 正整数，hash 是 2 字符。任意一边不
// 合法都返回错误，不做"宽松"猜测——和 pi-hashline-edit 行为一致。
func parseAnchor(anchor string) (line int, hash string, err error) {
	idx := strings.Index(anchor, "#")
	if idx <= 0 || idx >= len(anchor)-1 {
		return 0, "", fmt.Errorf("invalid anchor %q (expected LINE#HASH)", anchor)
	}
	lineStr := anchor[:idx]
	hash = anchor[idx+1:]
	line, err = strconv.Atoi(lineStr)
	if err != nil || line < 1 {
		return 0, "", fmt.Errorf("invalid anchor %q (line must be positive integer)", anchor)
	}
	if len(hash) != 2 {
		return 0, "", fmt.Errorf("invalid anchor %q (hash must be 2 chars)", anchor)
	}
	return line, hash, nil
}

// verifyHash 校验第 lineNum 行的 hash 是否匹配。
//
// 失败时附上 "fresh anchors" snippet（3 行上下文），便于 LLM 立即
// 用最新锚点重试。
func verifyHash(snapshot []string, lineNum int, expectedHash string) error {
	if lineNum < 1 || lineNum > len(snapshot) {
		return fmt.Errorf("anchor %d#%s out of range (file has %d lines)",
			lineNum, expectedHash, len(snapshot))
	}
	actual := lineHash(snapshot[lineNum-1], lineNum)
	if actual != expectedHash {
		from := max(lineNum-1, 0)
		to := min(lineNum+1, len(snapshot)-1)
		var sb strings.Builder
		fmt.Fprintf(&sb, "stale anchor: line %d expected hash %q, got %q; fresh anchors: ", lineNum, expectedHash, actual)
		for i := from; i <= to; i++ {
			if i > from {
				sb.WriteString(" | ")
			}
			fmt.Fprintf(&sb, "%d#%s", i+1, lineHash(snapshot[i], i+1))
		}
		return errors.New(sb.String())
	}
	return nil
}

// makeResult 构造成功响应：行数差异 + 头尾各 5 行的 fresh anchors。
func (t *EditTool) makeResult(call *llm.ToolCall, relPath string, before, after []string, ops []editOp) *llm.ToolResult {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Edited %s\n\n", relPath)

	if len(after) > len(before) {
		fmt.Fprintf(&sb, "+ %d line(s) added; file now has %d lines\n", len(after)-len(before), len(after))
	} else if len(after) < len(before) {
		fmt.Fprintf(&sb, "- %d line(s) removed; file now has %d lines\n", len(before)-len(after), len(after))
	} else {
		fmt.Fprintf(&sb, "File still has %d lines\n", len(after))
	}

	width := lineWidthFor(len(after))
	fmt.Fprintf(&sb, "\n--- Updated anchors ---\n")
	emit := func(start, end int) {
		for i := start; i <= end; i++ {
			if i < 0 || i >= len(after) {
				continue
			}
			fmt.Fprintf(&sb, "%s\n", formatLine(i+1, width, lineHash(after[i], i+1), after[i]))
		}
	}
	headN := 5
	if len(after) <= headN*2 {
		emit(0, len(after)-1)
	} else {
		emit(0, headN-1)
		fmt.Fprintf(&sb, "...\n")
		emit(len(after)-headN, len(after)-1)
	}
	_ = ops
	return &llm.ToolResult{
		ID:     call.ID,
		ToolID: call.ToolID,
		Result: sb.String(),
	}
}
