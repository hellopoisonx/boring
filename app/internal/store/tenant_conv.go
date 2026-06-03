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
	UpdateStatus(ctx context.Context, convID int64, status string) error
}

type tenantConvStore struct{ db *sql.DB }

const sqlNowTenantConv = `strftime('%s','now')`

func (s *tenantConvStore) Create(ctx context.Context, tenantID int64, title string) (int64, error) {
	const q = `
		INSERT INTO tenant_conv (tenant_id, title, status, created_at, updated_at)
		VALUES (?, ?, 'active', ` + sqlNowTenantConv + `, ` + sqlNowTenantConv + `)
		RETURNING conv_id;`
	var id int64
	if err := s.db.QueryRowContext(ctx, q, tenantID, title).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: tenant_conv.Create: %w", err)
	}
	return id, nil
}

func (s *tenantConvStore) GetByConvID(ctx context.Context, convID int64) (Conv, error) {
	const q = `
		SELECT conv_id, tenant_id, title, status, created_at, updated_at
		FROM tenant_conv
		WHERE conv_id = ?;`
	var c Conv
	var created, updated int64
	if err := s.db.QueryRowContext(ctx, q, convID).Scan(
		&c.ID, &c.TenantID, &c.Title, &c.Status, &created, &updated,
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
		SELECT conv_id, tenant_id, title, status, created_at, updated_at
		FROM tenant_conv
		WHERE tenant_id = ?
		ORDER BY conv_id ASC;`, tenantID)
}

func (s *tenantConvStore) ListByTenantAndStatus(ctx context.Context, tenantID int64, status string) ([]Conv, error) {
	return s.list(ctx, `
		SELECT conv_id, tenant_id, title, status, created_at, updated_at
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
			&c.ID, &c.TenantID, &c.Title, &c.Status, &created, &updated,
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
