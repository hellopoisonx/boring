package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hellopoisonx/boring/app/internal/llm"
)

// 平台特定 syscall 辅助：避免 write_test.go 写两遍 platform-specific 代码
func chmod(path string, mode os.FileMode) error        { return os.Chmod(path, mode) }
func symlink(oldname, newname string) error            { return os.Symlink(oldname, newname) }
func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}
func isSymlink(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info.Mode()&os.ModeSymlink != 0
}

// withWorkdir 在 t.TempDir() 里建一个 Env，简化测试代码。
func withWorkdir(t *testing.T) (Env, string) {
	t.Helper()
	dir := t.TempDir()
	return Env{WorkDir: dir}, dir
}

// writeFile 辅助：往 work dir 写一个文件（不走工具代码，避免测试自身依赖实现）。
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// call 是构造 *llm.ToolCall 的辅助，args 会被 json.Marshal。
func call(id string, toolID uint64, args any) *llm.ToolCall {
	body, _ := json.Marshal(args)
	return &llm.ToolCall{ID: id, ToolID: toolID, Args: body}
}

func TestEnv_Resolve_RejectEmptyPath(t *testing.T) {
	env, _ := withWorkdir(t)
	if _, _, err := env.Resolve(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestEnv_Resolve_RejectEscape(t *testing.T) {
	env, _ := withWorkdir(t)
	cases := []string{
		"../escape",
		"../../etc/passwd",
		"/etc/passwd",
		"foo/../../bar",
	}
	for _, c := range cases {
		_, _, err := env.Resolve(c)
		if err == nil {
			t.Errorf("expected error for %q, got nil", c)
		}
	}
}

func TestEnv_Resolve_AcceptRelative(t *testing.T) {
	env, dir := withWorkdir(t)
	abs, rel, err := env.Resolve("sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if abs != filepath.Join(dir, "sub", "file.txt") {
		t.Errorf("abs = %q", abs)
	}
	if rel != filepath.Join("sub", "file.txt") {
		t.Errorf("rel = %q", rel)
	}
}

func TestEnv_Resolve_AcceptAbsoluteInside(t *testing.T) {
	env, dir := withWorkdir(t)
	abs, rel, err := env.Resolve(filepath.Join(dir, "foo.txt"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if abs != filepath.Join(dir, "foo.txt") {
		t.Errorf("abs = %q", abs)
	}
	if rel != "foo.txt" {
		t.Errorf("rel = %q", rel)
	}
}

func TestEnv_Resolve_EmptyWorkDirIsFailClosed(t *testing.T) {
	env := Env{}
	if _, _, err := env.Resolve("foo"); err == nil {
		t.Fatal("expected error for empty work dir")
	}
}

func TestEnv_Resolve_CleanDotDot(t *testing.T) {
	env, dir := withWorkdir(t)
	// ./foo 等价于 foo；不应当被拒
	abs, rel, err := env.Resolve("./foo")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if abs != filepath.Join(dir, "foo") {
		t.Errorf("abs = %q", abs)
	}
	if rel != "foo" {
		t.Errorf("rel = %q", rel)
	}
}

func TestErrResult_PrefixesError(t *testing.T) {
	r := errResult(call("c1", 7, nil), "boom")
	if r == nil {
		t.Fatal("nil result")
	}
	if r.ID != "c1" || r.ToolID != 7 {
		t.Errorf("bad echo: %+v", r)
	}
	if got, want := r.Result, "Error: boom"; got != want {
		t.Errorf("Result = %q, want %q", got, want)
	}
}

func TestNewLineCount_Empty(t *testing.T) {
	if got := newLineCount(nil); got != nil {
		t.Errorf("empty input: want nil, got %v", got)
	}
}

func TestNewLineCount_RoundTrip(t *testing.T) {
	// joinLines 永远末尾加 "\n"；newLineCount 把单个 trailing \n 视为分隔符。
	// 所谓"round-trip"：read → newLineCount → joinLines 应当与"原文件 + 单个 trailing \n"等价。
	cases := []struct {
		name string
		in   string
		want string // joinLines(newLineCount(in)) 的期望值
	}{
		{"empty", "", ""},
		{"single no nl", "a", "a\n"},
		{"single with nl", "a\n", "a\n"},
		{"two no nl", "a\nb", "a\nb\n"},
		{"two with nl", "a\nb\n", "a\nb\n"},
		{"four", "a\nb\nc\nd", "a\nb\nc\nd\n"},
		{"only nl", "\n", "\n"},
		{"two nl", "\n\n", "\n\n"},
		{"trailing empty", "a\nb\n\n", "a\nb\n\n"},
	}
	for _, c := range cases {
		got := string(joinLines(newLineCount([]byte(c.in))))
		if got != c.want {
			t.Errorf("%s: in=%q got=%q want=%q", c.name, c.in, got, c.want)
		}
	}
}

func TestFileState_MarkAndWasRead(t *testing.T) {
	s := &FileState{}
	// 初始：未 read
	if s.WasRead("/tmp/x") {
		t.Error("fresh FileState should report unread")
	}
	// 标记后：可查
	s.MarkRead("/tmp/x")
	if !s.WasRead("/tmp/x") {
		t.Error("after MarkRead, WasRead should return true")
	}
	// 不影响其他路径
	if s.WasRead("/tmp/y") {
		t.Error("MarkRead on one path should not leak to others")
	}
	// 并发：同实例并发读写不 race detector 报错即通过
	s2 := &FileState{}
	done := make(chan struct{}, 10)
	for i := 0; i < 5; i++ {
		go func(i int) {
			s2.MarkRead("/p")
			_ = s2.WasRead("/p")
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 5; i++ {
		<-done
	}
}
