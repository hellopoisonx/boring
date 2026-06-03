package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// TenantInfoStore 描述 tenant_info 表的 DAO 行为。
//
// 幂等语义：Upsert 是 `INSERT ... ON CONFLICT DO UPDATE`，
// 重复调用会把 info 与 updated_at 整体覆盖。写入是"全量替换"语义，
// 不做局部字段 patch。
//
// 注意：tenant_info.tenant_id 在 DB 层**不是** FK 引用 user_tenant，
// 调用方需保证 tenantID 对应的 user_tenant 行已存在（应用层约束）。
type TenantInfoStore interface {
	Upsert(ctx context.Context, tenantID int64, info json.RawMessage) error
	GetByTenantID(ctx context.Context, tenantID int64) (json.RawMessage, error)
	Update(ctx context.Context, tenantID int64, info json.RawMessage) error
}

type tenantInfoStore struct{ db *sql.DB }

const sqlNowTenantInfo = `strftime('%s','now')`

func (s *tenantInfoStore) Upsert(ctx context.Context, tenantID int64, info json.RawMessage) error {
	// RawMessage 可能为空（调用方传 nil）→ 视为 '{}'。
	if len(info) == 0 {
		info = json.RawMessage("{}")
	}
	const q = `
		INSERT INTO tenant_info (tenant_id, info, created_at, updated_at)
		VALUES (?, ?, ` + sqlNowTenantInfo + `, ` + sqlNowTenantInfo + `)
		ON CONFLICT(tenant_id) DO UPDATE SET
			info       = excluded.info,
			updated_at = excluded.updated_at;`
	if _, err := s.db.ExecContext(ctx, q, tenantID, string(info)); err != nil {
		return fmt.Errorf("store: tenant_info.Upsert: %w", err)
	}
	return nil
}

func (s *tenantInfoStore) GetByTenantID(ctx context.Context, tenantID int64) (json.RawMessage, error) {
	const q = `SELECT info FROM tenant_info WHERE tenant_id = ?;`
	var raw string
	if err := s.db.QueryRowContext(ctx, q, tenantID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: tenant_info.GetByTenantID %d: %w", tenantID, sql.ErrNoRows)
		}
		return nil, fmt.Errorf("store: tenant_info.GetByTenantID: %w", err)
	}
	return json.RawMessage(raw), nil
}

func (s *tenantInfoStore) Update(ctx context.Context, tenantID int64, info json.RawMessage) error {
	if len(info) == 0 {
		info = json.RawMessage("{}")
	}
	const q = `
		UPDATE tenant_info
		SET info = ?, updated_at = ` + sqlNowTenantInfo + `
		WHERE tenant_id = ?;`
	res, err := s.db.ExecContext(ctx, q, string(info), tenantID)
	if err != nil {
		return fmt.Errorf("store: tenant_info.Update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: tenant_info.Update rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: tenant_info.Update %d: %w", tenantID, sql.ErrNoRows)
	}
	return nil
}
