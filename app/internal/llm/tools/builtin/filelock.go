package builtin

import "sync"

// fileLockTable 是 "per-canonical-path" 互斥表：同一底层文件（解析 symlink
// 后的真实路径）的所有写操作串行化，避免以下两类竞态：
//
//  1. 同文件并发写：read 旧内容 → edit 算 hash → write；并发执行时两次
//     edit 的 read-then-write 区间交错，前一次 edit 写入的内容被后一次
//     edit 覆盖（"lost update"）。
//
//  2. 软链/硬链绕开路径沙箱检查：写 /a/b、/a/c 都最终落到同一 inode，
//     锁必须挂在解析 symlink 后的 canonical 路径上，跨别名调用也能互斥。
//
// 内存泄漏：理论上每遇到一个新路径都会在 map 里留下一把锁。实际使用中
// 路径集合有限（同一进程能同时打开的文件数有上限），可忽略；如需严格
// 控制可加 LRU 淘汰，但目前没有触发场景。
type fileLockTable struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// newFileLockTable 构造一个空表。
func newFileLockTable() *fileLockTable {
	return &fileLockTable{locks: make(map[string]*sync.Mutex)}
}

// Lock 阻塞直到获得指定 path 的互斥锁。
func (t *fileLockTable) Lock(path string) {
	t.mu.Lock()
	m, ok := t.locks[path]
	if !ok {
		m = &sync.Mutex{}
		t.locks[path] = m
	}
	t.mu.Unlock()
	m.Lock()
}

// Unlock 释放指定 path 的互斥锁；与 Lock 成对调用。
func (t *fileLockTable) Unlock(path string) {
	t.mu.Lock()
	m := t.locks[path]
	t.mu.Unlock()
	m.Unlock()
}

// defaultFileLocks 是进程级共享的锁表。
//
// 暴露为包级变量供 edit/write 直接使用，避免每次调用方都构造一份
// （同一进程内的多 goroutine 共享锁表才有意义）。
var defaultFileLocks = newFileLockTable()
