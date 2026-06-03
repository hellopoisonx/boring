package builtin

import (
	"hash/fnv"
	"strconv"
	"strings"
	"unicode"
)

// hashAlphabet 是 hashline 协议规定的 16 字符输出字母表。
//
// 来源：[RimuruW/pi-hashline-edit] 的 README "Hashing" 章节明确给出
// `ZPMQVRWSNKTXJBYH`，理由是：剔除十六进制数字、常见元音字母、
// 视觉易混字母（D/G/I/L/O），使 "5#MQ" 这类锚点永远不会和源码中的
// hex 字面量、英文单词、视觉相近字符混淆。
//
// 16 字符 = 4 bit/字符，因此 32-bit hash 输出 8 bit 即可编码 2 字符。
// 任意 32-bit 哈希函数都可以用；本实现选 [hash/fnv] (FNV-1a 32)，
// 理由：标准库、零依赖、跨平台一致、对短字符串分布尚可。
const hashAlphabet = "ZPMQVRWSNKTXJBYH"

// lineHash 返回给定行的 2 字符 hashline 锚点。
//
// 对于"几乎全是结构性字符（括号/逗号/分号等）"的行——例如孤立的 "}" —
// FNV-1a 容易对结构相同的多行给出相同 hash（这是 FNV 的已知弱点）。
// pi-hashline-edit 用"行号 seed"绕过：若一行不含任何字母数字字符，
// 就把行号作为额外前缀混入哈希。沿用同一策略。
//
// 重要：hash 不要求 bit-perfect 兼容 pi-hashline-edit（用的 xxHash32），
// 只要在同一工具实现内"对相同输入给出相同输出、对不同行给出不同输出"。
// LLM 看到的只是 2 字符标识符，不参与任何计算。
func lineHash(line string, lineNum int) string {
	h := fnv.New32a()
	if !hasAlnum(line) {
		// 行号 seed；"ln:N:" 前缀是为了在结构相同的多行之间引入差异
		_, _ = h.Write([]byte("ln:" + strconv.Itoa(lineNum) + ":"))
	}
	_, _ = h.Write([]byte(line))
	sum := h.Sum32()

	// 取高 8 bit 作索引，每 4 bit 查表得到 2 字符。
	// 高位优先 + nibble 拆分避免低位偏差的常见做法。
	c0 := hashAlphabet[(sum>>4)&0xF]
	c1 := hashAlphabet[sum&0xF]
	return string([]byte{c0, c1})
}

// hasAlnum 报告字符串是否至少含一个 Unicode 字母/数字字符。
//
// 中文/希腊字母等会被识别为 alnum，从而走"内容 hash"路径（避免被误认为
// 全是结构字符而走行号 seed）；源码里这些字符出现概率低，即使被识别为
// alnum 也不影响 hashline 协议的正确性。
func hasAlnum(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// formatLine 把一行渲染为 "LINE#HASH: content" 格式。
//
// LINE 字段左补空格到 lineWidth，便于 LLM 视觉对齐读取（cat -n 风格）。
// 末尾不加 "\n"——由调用方决定是否换行。
func formatLine(lineNum int, lineWidth int, hash, content string) string {
	line := strconv.Itoa(lineNum)
	if pad := lineWidth - len(line); pad > 0 {
		line = strings.Repeat(" ", pad) + line
	}
	return line + "#" + hash + ":" + content
}

// lineWidthFor 给定最大行号，返回渲染时该补齐到的最小宽度。
//
// 规则：位数 ≥ 4 时按实际位数补齐（>9999 行的文件用 5 位、>99999 用 6 位）；
// 位数 < 4 时按实际位数补齐（<10 行用 1 位、<100 行用 2 位）。这样既能
// 避免 1~9 行的 " 1" 和 10~99 行的 " 10" 视觉差太大，又不会无谓地
// 把 5 行文件撑到 "   1"。
func lineWidthFor(maxLineNum int) int {
	w := 1
	for n := maxLineNum; n >= 10; n /= 10 {
		w++
	}
	if w < 4 {
		return w
	}
	return w
}
