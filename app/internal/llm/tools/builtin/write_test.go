package builtin

import (
	"strings"
	"testing"
)

// withReadEnv 在 withWorkdir 基础上附带 [Env.Tracker]，模拟"真实对话中
// 多个工具共享同一 read 追踪"的场景。大部分覆盖写已存在文件的 write
// 测试需要这个 Tracker 才能过 read-first 校验。
func withReadEnv(t *testing.T) (Env, string) {
	env, dir := withWorkdir(t)
	env.Tracker = &FileState{}
	return env, dir
}

// readFirst 模拟"LLM 先 read 后 write"的对话步骤，标记 Tracker。
// 返回 ReadTool 的结果（调用方通常丢弃）。
func readFirst(t *testing.T, env Env, path string) {
	t.Helper()
	res, _ := NewRead().Execute(t.Context(), env, call("setup-read", 0, map[string]any{
		"path": path,
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("read-first setup failed for %s: %+v", path, res)
	}
}

func TestWriteTool_Info(t *testing.T) {
	info := NewWrite().Info()
	if info.Name != WriteToolName {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(info.Schema, `"path"`) || !strings.Contains(info.Schema, `"content"`) {
		t.Error("Schema missing path/content")
	}
}

func TestWrite_CreatesNewFile(t *testing.T) {
	env, dir := withWorkdir(t)
	res, err := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "new.txt",
		"content": "hello world",
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "new.txt")
	if string(data) != "hello world" {
		t.Errorf("file = %q", data)
	}
}

func TestWrite_CreatesParentDirs(t *testing.T) {
	env, dir := withWorkdir(t)
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "a/b/c/file.txt",
		"content": "deep",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "a/b/c/file.txt")
	if string(data) != "deep" {
		t.Errorf("file = %q", data)
	}
}

func TestWrite_OverwritesExisting(t *testing.T) {
	env, dir := withReadEnv(t)
	writeFile(t, dir, "f.txt", "old")
	readFirst(t, env, "f.txt")
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "new",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "new" {
		t.Errorf("file = %q", data)
	}
}

func TestWrite_RespectsReadOnly(t *testing.T) {
	env, _ := withWorkdir(t)
	env.ReadOnly = true
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "x",
	}))
	if res == nil || !strings.Contains(res.Result, "read-only") {
		t.Errorf("expected read-only error, got %+v", res)
	}
}

func TestWrite_RespectsMaxBytes(t *testing.T) {
	env, _ := withWorkdir(t)
	env.MaxBytes = 5
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "this is way too long",
	}))
	if res == nil || !strings.Contains(res.Result, "too large") {
		t.Errorf("expected too-large error, got %+v", res)
	}
}

func TestWrite_RejectsPathEscape(t *testing.T) {
	env, _ := withWorkdir(t)
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "../escape",
		"content": "x",
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Errorf("expected error, got %+v", res)
	}
}

func TestWrite_PreservesPermissions(t *testing.T) {
	env, dir := withReadEnv(t)
	writeFile(t, dir, "f.txt", "old")
	// chmod 0o600
	fullPath := dir + "/f.txt"
	if err := chmod(fullPath, 0o600); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	readFirst(t, env, "f.txt")
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "new",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	mode := statMode(t, fullPath)
	if mode.Perm() != 0o600 {
		t.Errorf("permissions not preserved: got %v, want 0600", mode.Perm())
	}
}

func TestWrite_FollowsSymlink(t *testing.T) {
	env, dir := withReadEnv(t)
	writeFile(t, dir, "real.txt", "old")
	if err := symlink(dir+"/real.txt", dir+"/link.txt"); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// 通过软链 read：Tracker 用 canonical 路径（real.txt）记录，
	// write 通过 link.txt 解析到同一 canonical，read-first 校验通过
	readFirst(t, env, "link.txt")
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "link.txt",
		"content": "new",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	// 软链应保留，目标文件被更新
	if isSymlink(t, dir+"/link.txt") != true {
		t.Error("symlink was replaced with regular file")
	}
	data := readFile(t, dir, "real.txt")
	if string(data) != "new" {
		t.Errorf("target file = %q", data)
	}
}

func TestWrite_EmptyContent(t *testing.T) {
	env, dir := withReadEnv(t)
	writeFile(t, dir, "f.txt", "non-empty")
	readFirst(t, env, "f.txt")
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if len(data) != 0 {
		t.Errorf("file should be empty, got %q", data)
	}
}

// TestWrite_OverwriteRequiresRead 验证：覆盖已存在的非空文件前必须
// 先 read，否则拒绝——这是阻止 LLM 绕开 hashline 安全网的关键保险。
func TestWrite_OverwriteRequiresRead(t *testing.T) {
	env, dir := withReadEnv(t)
	writeFile(t, dir, "f.txt", "existing content")

	// 不调用 readFirst，直接 write
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "new",
	}))
	if res == nil || !strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("expected error for unread overwrite, got %+v", res)
	}
	if !strings.Contains(res.Result, "prior `read`") {
		t.Errorf("error should mention 'prior read', got %q", res.Result)
	}
	// 文件未被覆盖
	if data := readFile(t, dir, "f.txt"); string(data) != "existing content" {
		t.Errorf("file should be unchanged, got %q", data)
	}
}

// TestWrite_NewFile_DoesNotRequireRead 验证：写新文件无需 read。
func TestWrite_NewFile_DoesNotRequireRead(t *testing.T) {
	env, dir := withReadEnv(t)
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "brand-new.txt",
		"content": "hello",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("new file write should succeed without read, got %+v", res)
	}
	if data := readFile(t, dir, "brand-new.txt"); string(data) != "hello" {
		t.Errorf("file = %q", data)
	}
}

// TestWrite_EmptyTarget_DoesNotRequireRead 验证：覆盖空文件无需 read
// （无现有内容可丢）。
func TestWrite_EmptyTarget_DoesNotRequireRead(t *testing.T) {
	env, dir := withReadEnv(t)
	writeFile(t, dir, "empty.txt", "")
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "empty.txt",
		"content": "content",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("empty file write should succeed without read, got %+v", res)
	}
	if data := readFile(t, dir, "empty.txt"); string(data) != "content" {
		t.Errorf("file = %q", data)
	}
}

// TestWrite_NoTracker_AllowsOverwrite 验证：Env.Tracker 为 nil 时
// （无状态执行场景）不卡 read-first，保持向后兼容。
func TestWrite_NoTracker_AllowsOverwrite(t *testing.T) {
	env, dir := withWorkdir(t) // 注意：Tracker = nil
	_ = env.Tracker
	writeFile(t, dir, "f.txt", "old")
	res, _ := NewWrite().Execute(t.Context(), env, call("c1", 1, map[string]any{
		"path":    "f.txt",
		"content": "new",
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("overwrite with no tracker should succeed, got %+v", res)
	}
	if data := readFile(t, dir, "f.txt"); string(data) != "new" {
		t.Errorf("file = %q", data)
	}
}

// TestWrite_InfoMentionsReadFirst 验证 write 工具描述明确写出了
// read-first 约束（这是给 LLM 看的关键安全提示）。
func TestWrite_InfoMentionsReadFirst(t *testing.T) {
	info := NewWrite().Info()
	if !strings.Contains(info.Description, "prior `read`") {
		t.Errorf("WriteTool.Info must mention read-first constraint, got: %s", info.Description)
	}
	if !strings.Contains(info.Description, "`edit`") {
		t.Errorf("WriteTool.Info should also nudge LLM towards `edit`, got: %s", info.Description)
	}
}
