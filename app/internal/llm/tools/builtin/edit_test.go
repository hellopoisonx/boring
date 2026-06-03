package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTool_Info(t *testing.T) {
	info := NewEdit().Info()
	if info.Name != EditToolName {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(info.Schema, `"replace"`) ||
		!strings.Contains(info.Schema, `"append"`) ||
		!strings.Contains(info.Schema, `"prepend"`) ||
		!strings.Contains(info.Schema, `"replace_text"`) {
		t.Errorf("Schema missing one of 4 ops:\n%s", info.Schema)
	}
}

func TestEdit_Replace_OneLine(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")

	// 走 read 拿到正确 hash
	readRes, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	lines := strings.Split(strings.TrimRight(readRes.Result, "\n"), "\n")
	anchor2, _, _ := strings.Cut(lines[1], ":")
	_, hash2, _ := strings.Cut(anchor2, "#")

	res, err := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "2#" + hash2,
			"lines": []string{"BETA_NEW"},
		}},
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "alpha\nBETA_NEW\ngamma\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_Replace_Range(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\n")
	readRes, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	lines := strings.Split(strings.TrimRight(readRes.Result, "\n"), "\n")
	anchor1, _, _ := strings.Cut(lines[0], ":")
	_, hash1, _ := strings.Cut(anchor1, "#")
	anchor3, _, _ := strings.Cut(lines[2], ":")
	_, hash3, _ := strings.Cut(anchor3, "#")

	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "1#" + hash1,
			"end":   "3#" + hash3,
			"lines": []string{"X", "Y"},
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "X\nY\nd\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_Append_AfterPos(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\n")
	readRes, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	lines := strings.Split(strings.TrimRight(readRes.Result, "\n"), "\n")
	anchor1, _, _ := strings.Cut(lines[0], ":")
	_, hash1, _ := strings.Cut(anchor1, "#")

	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"pos":   "1#" + hash1,
			"lines": []string{"inserted"},
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\ninserted\nb\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_Append_ToEOF(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"lines": []string{"c", "d"},
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\nb\nc\nd\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_Prepend_ToBOF(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "prepend",
			"lines": []string{"x", "y"},
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "x\ny\na\nb\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_ReplaceText_Success(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "hello world\nfoo bar\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":      "replace_text",
			"oldText": "hello world",
			"newText": "hi",
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "hi\nfoo bar\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_ReplaceText_NotUnique(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "foo foo foo\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":      "replace_text",
			"oldText": "foo",
			"newText": "bar",
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "matches") {
		t.Errorf("expected non-unique error, got %+v", res)
	}
}

func TestEdit_StaleAnchor(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\n")
	// 故意用错的 hash
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "2#XX",
			"lines": []string{"B"},
		}},
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("expected error for stale anchor, got %+v", res)
	}
	if !strings.Contains(res.Result, "stale anchor") {
		t.Errorf("expected 'stale anchor' message, got %q", res.Result)
	}
}

func TestEdit_MultipleEdits_BottomUp(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\ne\n")
	readRes, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	lines := strings.Split(strings.TrimRight(readRes.Result, "\n"), "\n")
	// 一次操作：替换第 2 行、替换第 4 行；都基于同一份 snapshot
	anchor2, _, _ := strings.Cut(lines[1], ":")
	_, hash2, _ := strings.Cut(anchor2, "#")
	anchor4, _, _ := strings.Cut(lines[3], ":")
	_, hash4, _ := strings.Cut(anchor4, "#")

	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"op": "replace", "pos": "2#" + hash2, "lines": []string{"B"}},
			{"op": "replace", "pos": "4#" + hash4, "lines": []string{"D"}},
		},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\nB\nc\nD\ne\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_RespectsReadOnly(t *testing.T) {
	env, _ := withWorkdir(t)
	env.ReadOnly = true
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"lines": []string{"x"},
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "read-only") {
		t.Errorf("expected read-only error, got %+v", res)
	}
}

func TestEdit_RespectsMaxBytes(t *testing.T) {
	env, dir := withWorkdir(t)
	env.MaxBytes = 10
	writeFile(t, dir, "f.txt", "a\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"lines": []string{"x", "y", "z", "longer line that blows up the size"},
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "exceed") {
		t.Errorf("expected exceed error, got %+v", res)
	}
}

func TestEdit_RefusesNonExistentFile(t *testing.T) {
	env, dir := withWorkdir(t)
	_ = dir
	// 文件不存在 → 不管 op 类型，都拒绝并提示用 write
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "new/file.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"lines": []string{"hello"},
		}},
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("expected error, got %+v", res)
	}
	if !strings.Contains(res.Result, "empty file") || !strings.Contains(res.Result, "write") {
		t.Errorf("expected 'empty file, use write' hint, got %+v", res)
	}
	// 文件确实没被创建
	if _, err := os.Stat(filepath.Join(dir, "new/file.txt")); !os.IsNotExist(err) {
		t.Errorf("file should not exist after refused edit, stat err = %v", err)
	}
}

func TestEdit_RefusesEmptyFile(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "empty.txt", "")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "empty.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"lines": []string{"x"},
		}},
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("expected error, got %+v", res)
	}
	if !strings.Contains(res.Result, "empty file") {
		t.Errorf("expected 'empty file' message, got %q", res.Result)
	}
	// 文件保持空
	if data := readFile(t, dir, "empty.txt"); len(data) != 0 {
		t.Errorf("file should remain empty, got %q", data)
	}
}

func TestEdit_RefusesCreateForPosOps(t *testing.T) {
	env, dir := withWorkdir(t)
	_ = dir
	// 不管 op 是否带 pos，文件不存在都直接拒绝
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "new/file.txt",
		"edits": []map[string]any{{
			"op":    "append",
			"pos":   "1#XX",
			"lines": []string{"x"},
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "empty file") {
		t.Errorf("expected empty-file error, got %+v", res)
	}
}

func TestEdit_InfoWarnsDuplicatePos(t *testing.T) {
	info := NewEdit().Info()
	if !strings.Contains(info.Description, "duplicate pos in same call") {
		t.Errorf("EditTool.Info must warn about duplicate pos, got: %s", info.Description)
	}
	if !strings.Contains(info.Description, "only the last edit wins") {
		t.Errorf("EditTool.Info must state 'only the last edit wins', got: %s", info.Description)
	}
}

func TestEdit_InvalidAnchor(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "garbage",
			"lines": []string{"x"},
		}},
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Errorf("expected anchor parse error, got %+v", res)
	}
}
func TestEdit_EndBeforePos(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\n")
	readRes, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	lines := strings.Split(strings.TrimRight(readRes.Result, "\n"), "\n")
	anchor1, _, _ := strings.Cut(lines[0], ":")
	_, hash1, _ := strings.Cut(anchor1, "#")
	anchor3, _, _ := strings.Cut(lines[2], ":")
	_, hash3, _ := strings.Cut(anchor3, "#")

	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "3#" + hash3,
			"end":   "1#" + hash1,
			"lines": []string{"X"},
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "before pos") {
		t.Errorf("expected 'before pos' error, got %+v", res)
	}
}

func TestEdit_ReplaceText_MultiLine(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":      "replace_text",
			"oldText": "b\nc",
			"newText": "B1\nB2\nB3",
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\nB1\nB2\nB3\nd\n" {
		t.Errorf("file = %q", data)
	}
}

func TestEdit_EmptyEditArray(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{},
	}))
	if res == nil || !strings.Contains(res.Result, "empty") {
		t.Errorf("expected empty-edits error, got %+v", res)
	}
}

func TestEdit_UnknownOp(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\n")
	res, _ := NewEdit().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "wat",
			"lines": []string{"x"},
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "unknown op") {
		t.Errorf("expected unknown op error, got %+v", res)
	}
}

func TestEdit_ConcurrentSameFile(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "line0\n")
	const N = 20
	done := make(chan struct{}, N)
	for i := range N {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			_, _ = NewEdit().Execute(t.Context(), env, call("c", 1, map[string]any{
				"path": "f.txt",
				"edits": []map[string]any{{
					"op":    "append",
					"lines": []string{fmt.Sprintf("line%d", i)},
				}},
			}))
		}(i)
	}
	for range N {
		<-done
	}
	// 验证：文件包含 line0 + 20 个 lineN（顺序不一定，但全在）
	data := string(readFile(t, dir, "f.txt"))
	if !strings.Contains(data, "line0\n") {
		t.Errorf("missing line0: %q", data)
	}
	for i := range N {
		want := fmt.Sprintf("line%d", i)
		if !strings.Contains(data, want+"\n") {
			t.Errorf("missing %s: %q", want, data)
		}
	}
}

func TestRead_LineWidthPadding(t *testing.T) {
	// 100 行文件：行号应补齐到 3 位
	env, dir := withWorkdir(t)
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&sb, "L%d\n", i)
	}
	writeFile(t, dir, "f.txt", sb.String())

	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	if res == nil {
		t.Fatal("nil")
	}
	firstLine, _, _ := strings.Cut(res.Result, "\n")
	// 期望 "  1#XX:L1" 形如（行号左补空到 3 位）
	if len(firstLine) < len("  1#XX:") {
		t.Errorf("line number not padded to 3 digits: %q", firstLine)
	}
	if !strings.HasPrefix(firstLine, "  1#") {
		t.Errorf("expected '  1#' prefix, got %q", firstLine)
	}
}
