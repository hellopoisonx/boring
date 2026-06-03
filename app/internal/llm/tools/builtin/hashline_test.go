package builtin

import (
	"strconv"
	"testing"
)

func TestLineHash_Deterministic(t *testing.T) {
	// 相同输入应给出相同输出
	cases := []struct {
		line    string
		lineNum int
	}{
		{"hello", 1},
		{"hello", 2}, // 行号参与 hash
		{"world", 1},
		{"", 1},
		{"}", 1},
		{"}", 2}, // 行号 seed 应使两行 "}" 给出不同 hash
	}
	for _, c := range cases {
		h1 := lineHash(c.line, c.lineNum)
		h2 := lineHash(c.line, c.lineNum)
		if h1 != h2 {
			t.Errorf("non-deterministic: %q @ %d → %q vs %q", c.line, c.lineNum, h1, h2)
		}
		if len(h1) != 2 {
			t.Errorf("hash must be 2 chars, got %q (len %d)", h1, len(h1))
		}
		// 必须落在字母表内
		for _, c1 := range h1 {
			if !contains(hashAlphabet, byte(c1)) {
				t.Errorf("hash char %q not in alphabet %q", c1, hashAlphabet)
			}
		}
	}
}

func TestLineHash_StructOnlyLinesDifferByLineNumber(t *testing.T) {
	// 两个纯结构字符的行（"{" 和 "}"）应通过行号 seed 给出不同 hash
	h1 := lineHash("{", 1)
	h2 := lineHash("}", 1)
	if h1 == h2 {
		t.Errorf("expected different hashes for { and }, got %q == %q", h1, h2)
	}
	h3 := lineHash("{", 1)
	h4 := lineHash("{", 2)
	if h3 == h4 {
		t.Errorf("expected different hashes for two { on different lines, got %q == %q", h3, h4)
	}
}

func TestLineHash_DifferentContent(t *testing.T) {
	// FNV-1a 32 + 8 bit 输出 = 256 种 hash；1000 行必然碰撞。
	// 验证：
	//   1. 至少出现 100+ 个不同 hash（理论期望 252，100 留出余量）
	//   2. 同一行号下，相同内容 → 相同 hash（确定性）
	seen := make(map[string]string)
	distinct := make(map[string]struct{})
	for i := range 1000 {
		content := "line " + strconv.Itoa(i)
		h := lineHash(content, i+1)
		if prev, ok := seen[h]; ok && prev == content {
			t.Errorf("non-deterministic: %q → %q twice", content, h)
		}
		seen[h] = content
		distinct[h] = struct{}{}
	}
	if len(distinct) < 100 {
		t.Errorf("hash distribution too poor: only %d distinct hashes for 1000 inputs", len(distinct))
	}
}

func TestHasAlnum(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"{", false},
		{"}", false},
		{"a", true},
		{"0", true},
		{"中文", true},
		{"a{}", true},
		{"// comment", true},
	}
	for _, c := range cases {
		if got := hasAlnum(c.in); got != c.want {
			t.Errorf("hasAlnum(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLineWidthFor(t *testing.T) {
	cases := []struct {
		max  int
		want int
	}{
		{1, 1},
		{9, 1},
		{10, 2},
		{99, 2},
		{100, 3},
		{999, 3},
		{1000, 4}, // 4 位以上按实际
		{9999, 4},
		{10000, 5},
	}
	for _, c := range cases {
		if got := lineWidthFor(c.max); got != c.want {
			t.Errorf("lineWidthFor(%d) = %d, want %d", c.max, got, c.want)
		}
	}
}

func TestFormatLine_PadsLineNumber(t *testing.T) {
	got := formatLine(8, 3, "AB", "hello")
	want := "  8#AB:hello"
	if got != want {
		t.Errorf("formatLine = %q, want %q", got, want)
	}
	// 不补（位数足够）
	got2 := formatLine(1234, 4, "AB", "x")
	want2 := "1234#AB:x"
	if got2 != want2 {
		t.Errorf("formatLine(no pad) = %q, want %q", got2, want2)
	}
}

func contains(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}
