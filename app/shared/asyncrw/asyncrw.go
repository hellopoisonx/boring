// Package asyncrw 提供生产者-消费者模式的带缓冲异步读写抽象。
//
// 关键设计点（修复了初版的 race condition）：
//   - [Recv] 用 `, ok := <-a.ch` 检测 ch 关闭（close 后 ok=false）。
//   - [Close] 仅关闭 ch；reader 通过 ok=false 感知 EOF。
//   - [Send] 用 defer recover 兜底向 closed channel 发送的 panic。
package asyncrw

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrAsyncReaderClosed = errors.New("async reader has been closed")
	ErrAsyncWriterClosed = errors.New("async writer has been closed")
)

type AsyncReader[T any] interface {
	Recv(ctx context.Context) (T, error)
}

type AsyncWriter[T any] interface {
	Send(ctx context.Context, data T) error
	ToReader() AsyncReader[T]
	Close()
}

type asyncRW[T any] struct {
	bufSize uint32
	ch      chan T
	once    sync.Once
}

func (a *asyncRW[T]) Recv(ctx context.Context) (T, error) {
	for {
		// 优先尝试从 ch 读取（包括已 buffered 数据 + ch 已关闭场景）。
		select {
		case res, ok := <-a.ch:
			if !ok {
				return zero[T](), ErrAsyncReaderClosed
			}
			return res, nil
		default:
		}
		// ch 空：阻塞等待
		select {
		case res, ok := <-a.ch:
			if !ok {
				return zero[T](), ErrAsyncReaderClosed
			}
			return res, nil
		case <-ctx.Done():
			return zero[T](), ctx.Err()
		}
	}
}

func (a *asyncRW[T]) Send(ctx context.Context, data T) error {
	// 兜底：万一与 [Close] 并发，recover 关闭后 send 触发的 panic。
	defer func() { _ = recover() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a.ch <- data:
		return nil
	}
}

func (a *asyncRW[T]) Close() {
	a.once.Do(func() {
		close(a.ch)
	})
}

func (a *asyncRW[T]) ToReader() AsyncReader[T] {
	return a
}

func NewAsyncWriter[T any](bufSize uint32) AsyncWriter[T] {
	return &asyncRW[T]{
		bufSize: bufSize,
		ch:      make(chan T, bufSize),
	}
}

// zero 返回类型 T 的零值；用于 [Recv] 在 ch 关闭时返回。
func zero[T any]() T {
	var z T
	return z
}
