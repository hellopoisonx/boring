package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHashline_StableAfterContentEdit_PreservesAnchor 验证一个微妙的场景：
// 行号没变（没人插入/删除行），只是某一行的内容被外部改了——同一个行号
// 的 hash 必然失配，但其它行的 hash 应当不受影响。
func TestHashline_StableAfterContentEdit_PreservesAnchor(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")

	// 第一次 read
	r1, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{"path": "f.txt"}))
	lines := strings.Split(strings.TrimRight(r1.Result, "\n"), "\n")
	// 解析出 line 2 (beta) 的 anchor
	anchor2, _, _ := strings.Cut(lines[1], ":")
	_, hash2, _ := strings.Cut(anchor2, "#")

	// 外部把 line 1 (alpha) 改成 ALPHA，行号不变
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	modified := strings.Replace(string(data), "alpha", "ALPHA", 1)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte(modified), 0o644)

	// 第二次 read
	r2, _ := NewRead().Execute(t.Context(), env, call("c2", 1, map[string]any{"path": "f.txt"}))
	lines2 := strings.Split(strings.TrimRight(r2.Result, "\n"), "\n")
	anchor2b, _, _ := strings.Cut(lines2[1], ":")
	_, hash2b, _ := strings.Cut(anchor2b, "#")

	// line 2 (beta) 的 hash 应当仍然一致（内容没改）
	if hash2 != hash2b {
		t.Errorf("line 2 hash changed: %q → %q (should be stable when content unchanged)", hash2, hash2b)
	}

	// 用旧 anchor 改 line 2：应当成功
	res, _ := NewEdit().Execute(t.Context(), env, call("c3", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "2#" + hash2,
			"lines": []string{"BETA"},
		}},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Errorf("edit with stable hash should succeed, got %+v", res)
	}
}

// TestHashline_RejectedAfterLineShift 验证"行号漂移"时 hashline 正确拒绝
// 旧锚点（关键安全属性：不会改错文件）。
func TestHashline_RejectedAfterLineShift(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")

	// LLM read 一次
	r1, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{"path": "f.txt"}))
	lines := strings.Split(strings.TrimRight(r1.Result, "\n"), "\n")
	// line 2 (beta) 的旧 anchor
	anchor2, _, _ := strings.Cut(lines[1], ":")
	_, hash2, _ := strings.Cut(anchor2, "#")

	// 外部在 line 1 之前插入新行
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	modified := "NEW\n" + string(data)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte(modified), 0o644)

	// 用旧 anchor 改 "line 2"：现在 line 2 是 alpha 而非 beta
	// hashline 应当拒绝（alpha 的 hash 与 beta 的 hash 不同）
	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "2#" + hash2,
			"lines": []string{"WRONG"},
		}},
	}))
	if res == nil || !strings.Contains(res.Result, "stale anchor") {
		t.Errorf("expected stale anchor rejection, got %+v", res)
	}

	// re-read 拿到新行号后应当成功
	r2, _ := NewRead().Execute(t.Context(), env, call("c3", 1, map[string]any{"path": "f.txt"}))
	lines2 := strings.Split(strings.TrimRight(r2.Result, "\n"), "\n")
	// beta 现在在 line 3
	anchor3, _, _ := strings.Cut(lines2[2], ":")
	_, hash3, _ := strings.Cut(anchor3, "#")

	res2, _ := NewEdit().Execute(t.Context(), env, call("c4", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{{
			"op":    "replace",
			"pos":   "3#" + hash3,
			"lines": []string{"BETA"},
		}},
	}))
	if res2 == nil || strings.HasPrefix(res2.Result, "Error: ") {
		t.Errorf("edit with fresh anchor should succeed, got %+v", res2)
	}
}

// TestHashline_MultiEdit_BottomUp_IndependentLines 验证 multi-edit 中
// 改不同行时 bottom-up 排序的正确性。
func TestHashline_MultiEdit_BottomUp_IndependentLines(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\ne\nf\ng\nh\n")

	// read 拿 4 个独立的 anchor
	r1, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{"path": "f.txt"}))
	lines := strings.Split(strings.TrimRight(r1.Result, "\n"), "\n")
	get := func(idx int) string {
		anchor, _, _ := strings.Cut(lines[idx], ":")
		_, hash, _ := strings.Cut(anchor, "#")
		return fmt.Sprintf("%d#%s", idx+1, hash)
	}
	h2, h5, h7 := get(1), get(4), get(6)

	// 一次调用改 3 个不连续的行
	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"op": "replace", "pos": h2, "lines": []string{"B"}},
			{"op": "replace", "pos": h5, "lines": []string{"E"}},
			{"op": "replace", "pos": h7, "lines": []string{"G"}},
		},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("multi-edit failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\nB\nc\nd\nE\nf\nG\nh\n" {
		t.Errorf("file = %q", data)
	}
}

// TestHashline_MultiEdit_DeleteThenEditSurvives 验证"删除前面的行 +
// 修改后面的行"在 bottom-up 排序下都正确。
func TestHashline_MultiEdit_DeleteThenEditSurvives(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\ne\n")

	r1, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{"path": "f.txt"}))
	lines := strings.Split(strings.TrimRight(r1.Result, "\n"), "\n")
	// 替换 line 1-2 为 [X]（变成 4 行：[X, c, d, e]），同时把 line 5 (e) 改成 [E]
	get := func(idx int) string {
		anchor, _, _ := strings.Cut(lines[idx], ":")
		_, hash, _ := strings.Cut(anchor, "#")
		return fmt.Sprintf("%d#%s", idx+1, hash)
	}

	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"op": "replace", "pos": get(0), "end": get(1), "lines": []string{"X"}},
			{"op": "replace", "pos": get(4), "lines": []string{"E"}},
		},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("multi-edit failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "X\nc\nd\nE\n" {
		t.Errorf("file = %q", data)
	}
}

// TestHashline_InsertInMiddle_PreservesLaterAnchors 验证在中间插入后，
// 后续 op 仍能基于原 read 的 anchor 精准定位（因为 bottom-up 排序先做
// 行号大的 edit）。
func TestHashline_InsertInMiddle_PreservesLaterAnchors(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\nd\n")

	r1, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{"path": "f.txt"}))
	lines := strings.Split(strings.TrimRight(r1.Result, "\n"), "\n")
	get := func(idx int) string {
		anchor, _, _ := strings.Cut(lines[idx], ":")
		_, hash, _ := strings.Cut(anchor, "#")
		return fmt.Sprintf("%d#%s", idx+1, hash)
	}

	// 在 line 2 之后插入 [X, Y]（让 line 3 (c) 变 line 5），然后改 line 3 (c→C)
	// LLM 不知道插入会让 c 变成 line 5，但 bottom-up 排序让 insert 先做，改 c 后做
	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"op": "replace", "pos": get(2), "lines": []string{"C"}}, // c→C（行号小的先报，bottom-up 后做）
			{"op": "append", "pos": get(1), "lines": []string{"X", "Y"}}, // 在 b 之后插 [X, Y]
		},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("multi-edit failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\nb\nX\nY\nC\nd\n" {
		t.Errorf("file = %q", data)
	}
}

// TestHashline_SamePosTwice_SecondWins 记录当前 multi-edit 同 pos 的行为：
// 两次都通过，第二次覆盖。LLM 不会得到错误（和 pi-hashline-edit 行为一致）。
func TestHashline_SamePosTwice_SecondWins(t *testing.T) {
	env, dir := withWorkdir(t)
	writeFile(t, dir, "f.txt", "a\nb\nc\n")

	r1, _ := NewRead().Execute(t.Context(), env, call("c1", 1, map[string]any{"path": "f.txt"}))
	lines := strings.Split(strings.TrimRight(r1.Result, "\n"), "\n")
	anchor2, _, _ := strings.Cut(lines[1], ":")
	_, hash2, _ := strings.Cut(anchor2, "#")

	res, _ := NewEdit().Execute(t.Context(), env, call("c2", 1, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"op": "replace", "pos": "2#" + hash2, "lines": []string{"FIRST"}},
			{"op": "replace", "pos": "2#" + hash2, "lines": []string{"SECOND"}},
		},
	}))
	if res == nil || strings.HasPrefix(res.Result, "Error: ") {
		t.Fatalf("multi-edit failed: %+v", res)
	}
	data := readFile(t, dir, "f.txt")
	if string(data) != "a\nSECOND\nc\n" {
		t.Errorf("file = %q (expected SECOND to win)", data)
	}
}
