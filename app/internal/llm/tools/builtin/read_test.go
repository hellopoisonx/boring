package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/llm"
)

func TestReadTool_Info(t *testing.T) {
	info := NewRead().Info()
	if info.Name != ReadToolName {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Schema == "" {
		t.Error("Schema empty")
	}
	if !strings.Contains(info.Schema, `"path"`) {
		t.Error("Schema missing path")
	}
}

func TestRead_BasicOutput(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "a.txt", "hello\nworld\nfoo\n")

	res, err := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "a.txt",
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res == nil {
		t.Fatal("nil res")
	}
	// 必须有 3 行 hashline 输出
	lines := strings.Split(strings.TrimRight(res.Result, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), res.Result)
	}
	// 形如 "1#AB:hello"
	for i, l := range lines {
		if !strings.Contains(l, ":") {
			t.Errorf("line %d missing #: %q", i, l)
		}
		parts := strings.SplitN(l, ":", 2)
		if len(parts) != 2 || parts[0] == "" {
			t.Errorf("line %d malformed: %q", i, l)
		}
		anchor := parts[0]
		if !strings.Contains(anchor, "#") {
			t.Errorf("line %d missing # in anchor: %q", i, anchor)
		}
	}
}

func TestRead_RejectsDirectory(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "sub/file.txt", "x")
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "sub",
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("expected Error result, got %+v", res)
	}
	if !strings.Contains(res.Result, "directory") {
		t.Errorf("error msg should mention directory, got %q", res.Result)
	}
}

func TestRead_RejectsMissingFile(t *testing.T) {
	env, dir := withWorkdir(t)
	_ = dir
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "no-such.txt",
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("expected Error result, got %+v", res)
	}
	if !strings.Contains(res.Result, "not found") {
		t.Errorf("error msg should mention 'not found', got %q", res.Result)
	}
}

func TestRead_RejectsBinary(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "bin.dat", "hello\x00world")
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "bin.dat",
	}))
	if res == nil || !strings.Contains(res.Result, "binary") {
		t.Errorf("expected binary error, got %+v", res)
	}
}

func TestRead_RespectsMaxBytes(t *testing.T) {
	env, dir := withWorkdir(t)
	env.MaxBytes = 10
	writeFile(t, dir, "big.txt", "this file is way too long to be allowed")
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "big.txt",
	}))
	if res == nil || !strings.Contains(res.Result, "too large") {
		t.Errorf("expected too-large error, got %+v", res)
	}
}

func TestRead_RejectsPathEscape(t *testing.T) {
	env, _ := withWorkdir(t)
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "../escape",
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Errorf("expected error for path escape, got %+v", res)
	}
}

func TestRead_OffsetAndLimit(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\ne\n")

	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":   "f.txt",
		"offset": 2,
		"limit":  2,
	}))
	if res == nil {
		t.Fatal("nil res")
	}
	lines := strings.Split(strings.TrimRight(res.Result, "\n"), "\n")
	// 期望首行是 2, 含"b"；截断提示可能独立成行
	if !strings.HasPrefix(lines[0], "2#") {
		t.Errorf("first line should start with 2#, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "b") {
		t.Errorf("first line should contain 'b', got %q", lines[0])
	}
}

func TestRead_EmptyFileAdvisory(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "empty.txt", "")
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "empty.txt",
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Errorf("empty file should give advisory error, got %+v", res)
	}
	if !strings.Contains(res.Result, "prepend") || !strings.Contains(res.Result, "append") {
		t.Errorf("advisory should suggest prepend/append, got %q", res.Result)
	}
}

func TestRead_OffsetBeyondEof(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\n")
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":   "f.txt",
		"offset": 100,
	}))
	if res == nil || !strings.Contains(res.Result, "exceeds") {
		t.Errorf("expected exceeds error, got %+v", res)
	}
}

func TestRead_TrailingNewline(t *testing.T) {
	env, dir := withWorkdir(t)
	// 文件带尾部 \n，应当看到 N 行而不是 N+1 行
	writeFile(t, dir, "f.txt", "a\nb\nc\n")
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	if res == nil {
		t.Fatal("nil res")
	}
	if strings.Contains(res.Result, "#:") {
		// 不应出现 4#X: 这样的第 4 行
		// 直接检查行数
		trimmed := strings.TrimRight(res.Result, "\n")
		lines := strings.Split(trimmed, "\n")
		if len(lines) != 3 {
			t.Errorf("expected 3 lines for trailing-newline file, got %d:\n%s", len(lines), res.Result)
		}
	}
}

func TestRead_HashlineForEdit(t *testing.T) {
	// 端到端：read 出 hashline → 用 hashline 调用 edit（模拟 LLM 流程）
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")

	readRes, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	// 解析第二行的 anchor
	secondLine := strings.Split(strings.TrimRight(readRes.Result, "\n"), "\n")[1]
	anchor, _, _ := strings.Cut(secondLine, ":") // "2#XX"
	_, hash, _ := strings.Cut(anchor, "#")

	editRes, err := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "2#" + hash,
			"lines": []string{"BETA"},
		}},
	}))
	if err != nil {
		t.Fatalf("edit err: %v", err)
	}
	if editRes == nil || strings.HasPrefix(editRes.Result, "Error: ") {
		t.Fatalf("edit failed: %+v", editRes)
	}
	// 文件内容
	data := readFile(t, dir, "f.txt")
	if !strings.Contains(string(data), "BETA") || strings.Contains(string(data), "beta") {
		t.Errorf("expected BETA replacement, got:\n%s", data)
	}
}

// TestRead_MarksFileAsRead 验证：read 成功后会在 [Env.Tracker] 里
// 标记 canonical 路径，供后续 write 工具的 read-first 校验使用。
func TestRead_MarksFileAsRead(t *testing.T) {
	env, dir := withWorkdir(t)
	env.Tracker = &FileState{}
	writeFile(t, dir, "f.txt", "hello\n")

	canonical, _ := filepath.EvalSymlinks(filepath.Join(dir, "f.txt"))
	if env.Tracker.WasRead(canonical) {
		t.Fatal("tracker should be empty before read")
	}

	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "f.txt",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("read failed: %+v", res)
	}

	if !env.Tracker.WasRead(canonical) {
		t.Errorf("tracker should mark canonical path %q as read after successful read", canonical)
	}
}

// TestRead_SymlinkMarksCanonical 验证：通过 symlink 读取时，Tracker
// 标记的是 symlink 解析后的 canonical 路径——确保 write 通过另一别名
// 解析到同一 canonical 时能查到 read 标记。
func TestRead_SymlinkMarksCanonical(t *testing.T) {
	env, dir := withWorkdir(t)
	env.Tracker = &FileState{}
	writeFile(t, dir, "real.txt", "hi\n")
	if err := symlink(dir+"/real.txt", dir+"/link.txt"); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	canonical, _ := filepath.EvalSymlinks(filepath.Join(dir, "real.txt"))
	res, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path": "link.txt",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("read failed: %+v", res)
	}
	// 标记的是 canonical 而非 link 本身
	if !env.Tracker.WasRead(canonical) {
		t.Errorf("Tracker should mark canonical %q, not the symlink alias", canonical)
	}
	if env.Tracker.WasRead(filepath.Join(dir, "link.txt")) {
		t.Errorf("Tracker should NOT mark the symlink alias itself")
	}
}

// 避免 readFile 重复，写在公共位置
func readFile(t *testing.T, dir, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	return data
}

// 触发 llm 包 import 不被 "unused" 警告
var _ = llm.MessageTypeToolCall
