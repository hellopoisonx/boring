package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TenantConvStore 描述 tenant_conv 表的 DAO 行为。
//
// 幂等语义：Create 走 INSERT，重复 (conv_id 是 PK，所以"重复"基本不会发生)。
// tenant_id 必须存在于 tenant_info（DB 层 FK 约束生效）。
type TenantConvStore interface {
	Create(ctx context.Context, tenantID int64, title string) (convID int64, err error)
	GetByConvID(ctx context.Context, convID int64) (Conv, error)
	ListByTenant(ctx context.Context, tenantID int64) ([]Conv, error)
	ListByTenantAndStatus(ctx context.Context, tenantID int64, status string) ([]Conv, error)
	LatestActiveByTenant(ctx context.Context, tenantID int64) (Conv, error)
	UpdateStatus(ctx context.Context, convID int64, status string) error
	UpdateUsage(ctx context.Context, convID int64, totalTokens int64, modelID, modelProvider string) error
	IncUsage(ctx context.Context, convID int64, deltaTokens int64, modelID, modelProvider string) error
}

type tenantConvStore struct{ db *sql.DB }

const sqlNowTenantConv = `strftime('%s','now')`

func (s *tenantConvStore) Create(ctx context.Context, tenantID int64, title string) (int64, error) {
	const q = `
		INSERT INTO tenant_conv (tenant_id, title, total_tokens, model_id, model_provider, status, created_at, updated_at)
		VALUES (?, ?, 0, '', '', 'active', ` + sqlNowTenantConv + `, ` + sqlNowTenantConv + `)
		RETURNING conv_id;`
	var id int64
	if err := s.db.QueryRowContext(ctx, q, tenantID, title).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: tenant_conv.Create: %w", err)
	}
	return id, nil
}

func (s *tenantConvStore) GetByConvID(ctx context.Context, convID int64) (Conv, error) {
	const q = `
		SELECT conv_id, tenant_id, title, status, total_tokens, model_id, model_provider, created_at, updated_at
		FROM tenant_conv
		WHERE conv_id = ?;`
	var c Conv
	var created, updated int64
	if err := s.db.QueryRowContext(ctx, q, convID).Scan(
		&c.ID, &c.TenantID, &c.Title, &c.Status, &c.TotalTokens, &c.ModelID, &c.ModelProvider, &created, &updated,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Conv{}, fmt.Errorf("store: tenant_conv.GetByConvID %d: %w", convID, sql.ErrNoRows)
		}
		return Conv{}, fmt.Errorf("store: tenant_conv.GetByConvID: %w", err)
	}
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return c, nil
}

func (s *tenantConvStore) ListByTenant(ctx context.Context, tenantID int64) ([]Conv, error) {
	return s.list(ctx, `
		SELECT conv_id, tenant_id, title, status, total_tokens, model_id, model_provider, created_at, updated_at
		FROM tenant_conv
		WHERE tenant_id = ?
		ORDER BY conv_id ASC;`, tenantID)
}

func (s *tenantConvStore) ListByTenantAndStatus(ctx context.Context, tenantID int64, status string) ([]Conv, error) {
	return s.list(ctx, `
		SELECT conv_id, tenant_id, title, status, total_tokens, model_id, model_provider, created_at, updated_at
		FROM tenant_conv
		WHERE tenant_id = ? AND status = ?
		ORDER BY conv_id ASC;`, tenantID, status)
}

func (s *tenantConvStore) list(ctx context.Context, q string, args ...any) ([]Conv, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: tenant_conv.list: %w", err)
	}
	defer rows.Close()

	var out []Conv
	for rows.Next() {
		var c Conv
		var created, updated int64
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.Title, &c.Status, &c.TotalTokens, &c.ModelID, &c.ModelProvider, &created, &updated,
		); err != nil {
			return nil, fmt.Errorf("store: tenant_conv.list scan: %w", err)
		}
		c.CreatedAt = time.Unix(created, 0).UTC()
		c.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: tenant_conv.list rows: %w", err)
	}
	return out, nil
}

func (s *tenantConvStore) UpdateStatus(ctx context.Context, convID int64, status string) error {
	const q = `
		UPDATE tenant_conv
		SET status = ?, updated_at = ` + sqlNowTenantConv + `
		WHERE conv_id = ?;`
	res, err := s.db.ExecContext(ctx, q, status, convID)
	if err != nil {
		return fmt.Errorf("store: tenant_conv.UpdateStatus: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: tenant_conv.UpdateStatus rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: tenant_conv.UpdateStatus %d: %w", convID, sql.ErrNoRows)
	}
	return nil
}

func (s *tenantConvStore) UpdateUsage(ctx context.Context, convID int64, totalTokens int64, modelID, modelProvider string) error {
	const q = `
		UPDATE tenant_conv
		SET total_tokens = ?, model_id = ?, model_provider = ?, updated_at = ` + sqlNowTenantConv + `
		WHERE conv_id = ?;`
	res, err := s.db.ExecContext(ctx, q, totalTokens, modelID, modelProvider, convID)
	if err != nil {
		return fmt.Errorf("store: tenant_conv.UpdateUsage: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: tenant_conv.UpdateUsage rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: tenant_conv.UpdateUsage %d: %w", convID, sql.ErrNoRows)
	}
	return nil
}

// LatestActiveByTenant 返回指定租户下最后一个 status='active' 的 conv（按 conv_id DESC LIMIT 1）。
// 无 active conv 时返回 sql.ErrNoRows。
func (s *tenantConvStore) LatestActiveByTenant(ctx context.Context, tenantID int64) (Conv, error) {
	const q = `
		SELECT conv_id, tenant_id, title, status, total_tokens, model_id, model_provider, created_at, updated_at
		FROM tenant_conv
		WHERE tenant_id = ? AND status = 'active'
		ORDER BY conv_id DESC
		LIMIT 1;`
	var c Conv
	var created, updated int64
	if err := s.db.QueryRowContext(ctx, q, tenantID).Scan(
		&c.ID, &c.TenantID, &c.Title, &c.Status, &c.TotalTokens, &c.ModelID, &c.ModelProvider, &created, &updated,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Conv{}, fmt.Errorf("store: tenant_conv.LatestActiveByTenant %d: %w", tenantID, sql.ErrNoRows)
		}
		return Conv{}, fmt.Errorf("store: tenant_conv.LatestActiveByTenant: %w", err)
	}
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return c, nil
}

// IncUsage 原子累加 conv 的 total_tokens，并更新 model_id / model_provider 为最新值。
func (s *tenantConvStore) IncUsage(ctx context.Context, convID int64, deltaTokens int64, modelID, modelProvider string) error {
	const q = `
		UPDATE tenant_conv
		SET total_tokens   = total_tokens + ?,
		    model_id       = ?,
		    model_provider = ?,
		    updated_at     = ` + sqlNowTenantConv + `
		WHERE conv_id = ?;`
	res, err := s.db.ExecContext(ctx, q, deltaTokens, modelID, modelProvider, convID)
	if err != nil {
		return fmt.Errorf("store: tenant_conv.IncUsage: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: tenant_conv.IncUsage rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: tenant_conv.IncUsage %d: %w", convID, sql.ErrNoRows)
	}
	return nil
}
