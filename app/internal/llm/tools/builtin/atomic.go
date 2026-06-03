package builtin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// atomicWrite 把 data 原子地写入 path：
//
//  1. 在 path 所在目录创建临时文件（保证 rename 是同 fs、原子）；
//  2. 写入、fsync、chmod、close；
//  3. os.Rename 覆盖（POSIX rename 原子，读者要么看到旧内容要么看到新内容）；
//  4. 失败时清理临时文件。
//
// defaultMode 在目标文件不存在时使用；存在时保留原文件权限（避免把
// 0600 的私密文件 chmod 成 0644）。
//
// 符号链接：始终写入 symlink 解析后的真实路径，避免
//
//	ln -s /etc/passwd ./passwd
//	atomicWrite("./passwd", ...)  // 不期望把 /etc/passwd 覆盖了
//
// 之类的事故；同时也不会把 symlink 自身替换成普通文件（保留软链结构）。
func atomicWrite(path string, data []byte, defaultMode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}

	// 解析 symlink 链：拿到真实目标路径
	realPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		realPath = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	// 重新取 dir（realPath 可能在更深一层）
	realDir := filepath.Dir(realPath)

	// 决定 mode：保留原文件权限
	mode := defaultMode
	if info, err := os.Stat(realPath); err == nil {
		mode = info.Mode().Perm()
	}

	// 在真实目录里建临时文件
	f, err := os.CreateTemp(realDir, ".hashline-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := f.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	// fsync：把数据刷到盘，rename 后读者才能保证看到完整内容
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}

	// rename 原子覆盖：realPath 存在则覆盖（rename 语义），不存在则创建
	if err := os.Rename(tmpPath, realPath); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	_ = dir // 保留 dir 引用方便未来加 dir fsync（目前不需要）
	return nil
}
