package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UserTenantStore 描述 user_tenant 表的 DAO 行为。
//
// 幂等语义：Create 遇到 user_id 已存在时**直接返回 UNIQUE 约束错误**，
// 不自动 upsert。调用方若想"获取或创建"应显式 Get → 失败再 Create。
type UserTenantStore interface {
	Create(ctx context.Context, userID string) (tenantID int64, err error)
	GetByUserID(ctx context.Context, userID string) (tenantID int64, err error)
	GetByTenantID(ctx context.Context, tenantID int64) (userID string, err error)
}

type userTenantStore struct{ db *sql.DB }

const sqlNowUserTenant = `strftime('%s','now')`

func (s *userTenantStore) Create(ctx context.Context, userID string) (int64, error) {
	// tenant_id 由应用层在事务里预留：先 SELECT rowid 风格的最大空闲 ID，
	// 再 INSERT。SQLite 没有"INSERT ... RETURNING"（3.35+ 才有，ncruces
	// 0.34 内置 SQLite 3.x 已支持），所以可以直接用 RETURNING 一次拿。
	const q = `
		INSERT INTO user_tenant (user_id, tenant_id, created_at)
		VALUES (
			?,
			COALESCE((SELECT MAX(tenant_id) FROM user_tenant), 0) + 1,
			` + sqlNowUserTenant + `
		)
		RETURNING tenant_id;`
	var id int64
	if err := s.db.QueryRowContext(ctx, q, userID).Scan(&id); err != nil {
		return 0, fmt.Errorf("store: user_tenant.Create: %w", err)
	}
	return id, nil
}

func (s *userTenantStore) GetByUserID(ctx context.Context, userID string) (int64, error) {
	const q = `SELECT tenant_id FROM user_tenant WHERE user_id = ?;`
	var id int64
	if err := s.db.QueryRowContext(ctx, q, userID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("store: user_tenant.GetByUserID %q: %w", userID, sql.ErrNoRows)
		}
		return 0, fmt.Errorf("store: user_tenant.GetByUserID: %w", err)
	}
	return id, nil
}

func (s *userTenantStore) GetByTenantID(ctx context.Context, tenantID int64) (string, error) {
	const q = `SELECT user_id FROM user_tenant WHERE tenant_id = ?;`
	var uid string
	if err := s.db.QueryRowContext(ctx, q, tenantID).Scan(&uid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("store: user_tenant.GetByTenantID %d: %w", tenantID, sql.ErrNoRows)
		}
		return "", fmt.Errorf("store: user_tenant.GetByTenantID: %w", err)
	}
	return uid, nil
}
