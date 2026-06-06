-- name: CreateUserTenant :one
INSERT INTO user_tenant (user_id, tenant_id, created_at)
VALUES (
    ?,
    COALESCE((SELECT MAX(tenant_id) FROM user_tenant), 0) + 1,
    strftime('%s', 'now')
)
RETURNING tenant_id;

-- name: GetUserTenantByUserID :one
SELECT tenant_id FROM user_tenant WHERE user_id = ?;

-- name: GetUserTenantByTenantID :one
SELECT user_id FROM user_tenant WHERE tenant_id = ?;
