// Package store 提供 SQLite 持久化层：租户与会话三表 + sqlc 生成的查询。
//
// 设计要点：
//   - 三表链路：user_tenant (分配 tenant_id) → tenant_info (1:1 持有元数据)
//     → tenant_conv (1:N 持有会话)。仅 tenant_conv.tenant_id 建 DB 层 FK。
//   - tenant_id 统一 INTEGER PRIMARY KEY（= SQLite rowid，64-bit）。
//   - 时间戳统一 unix epoch seconds（INTEGER），Go 端用 int64 表示。
//   - SQL 由 sqlc 生成（见仓库根 sqlc.yaml + app/internal/store/queries/*.sql）；
//     本文件不写任何业务 SQL。
//   - Open 时幂等建表并启用 PRAGMA foreign_keys=ON。
//
// 行模型与查询：
//   - 行模型：UserTenant / TenantInfo / TenantConv（见 models.go，sqlc 生成）
//   - 查询方法：*Queries 上的方法（见 *.sql.go，sqlc 生成）
//   - *Queries 通过 New(*sql.DB) 构造；Store 嵌入 *Queries 后调用方可直接
//     s.CreateUserTenant(...) / s.GetTenantConv(...) 调用。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/ncruces/go-sqlite3/driver"
)

//go:embed schema.sql
var schemaSQL string

// Store 持有底层 *sql.DB 并通过嵌入 *Queries 直接暴露所有 sqlc 生成的方法。
//
// 调用方既可以 s.CreateUserTenant(...)（走嵌入字段），
// 也可以 s.DB().Exec(...) 自定义 SQL，或 s.WithTx(tx) 在事务里跑 sqlc 查询。
type Store struct {
	*Queries
	db *sql.DB
}

// Open 打开 DSN 指定的 SQLite 数据库（DSN 走 ncruses/go-sqlite3 格式，
// 如 ":memory:" / "file:foo.db?..."），然后幂等建表 + 启用外键。
//
// 启用的 PRAGMA：
//   - foreign_keys = ON：FK 约束（包括 tenant_conv → tenant_info 的 CASCADE）必须显式开启
//   - busy_timeout = 5s：避免短时锁竞争立刻失败
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	// SQLite 在单连接下行为最可预期：写串行、PRAGMA 不被重置。
	// 这里把 pool 限制为 1 既能保 FK 行为，也避免多连接下 busy_timeout 退化。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Ping 一次触发实际连接，避免首次 Query 报 "database is closed"。
	pingCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	// 启用 FK + busy timeout。注意 PRAGMA 在多连接下不会跨连接保留，
	// 配合 SetMaxOpenConns(1) 一劳永逸。
	// ncruces/go-sqlite3 的 driver 一次只接受一条 statement，必须逐条 Exec。
	for _, pragma := range []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA busy_timeout = 5000;`,
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: pragma %q: %w", pragma, err)
		}
	}

	// 幂等建表。
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}

	return &Store{Queries: New(db), db: db}, nil
}

// DB 暴露底层 *sql.DB，给需要自定义 SQL / 事务的调用方。
// 大多数调用方应直接使用嵌入的 *Queries 方法。
func (s *Store) DB() *sql.DB { return s.db }

// Close 关闭底层连接。多次调用安全（database/sql 保证幂等）。
func (s *Store) Close() error { return s.db.Close() }
