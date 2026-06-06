-- name: CreateTenantConv :one
INSERT INTO tenant_conv (tenant_id, title, total_tokens, model_id, model_provider, status, created_at, updated_at)
VALUES (?, ?, 0, '', '', 'active', strftime('%s', 'now'), strftime('%s', 'now'))
RETURNING conv_id;

-- name: GetTenantConv :one
SELECT * FROM tenant_conv WHERE conv_id = ?;

-- name: ListTenantConvsByTenant :many
SELECT * FROM tenant_conv
WHERE tenant_id = ?
ORDER BY conv_id ASC;

-- name: ListTenantConvsByTenantAndStatus :many
SELECT * FROM tenant_conv
WHERE tenant_id = ? AND status = ?
ORDER BY conv_id ASC;

-- name: GetLatestActiveTenantConv :one
SELECT * FROM tenant_conv
WHERE tenant_id = ? AND status = 'active'
ORDER BY conv_id DESC
LIMIT 1;

-- name: UpdateTenantConvStatus :exec
UPDATE tenant_conv
SET status = ?, updated_at = strftime('%s', 'now')
WHERE conv_id = ?;

-- name: UpdateTenantConvUsage :exec
UPDATE tenant_conv
SET total_tokens = ?, model_id = ?, model_provider = ?, updated_at = strftime('%s', 'now')
WHERE conv_id = ?;

-- name: IncTenantConvUsage :exec
UPDATE tenant_conv
SET total_tokens   = total_tokens + ?,
    model_id       = ?,
    model_provider = ?,
    updated_at     = strftime('%s', 'now')
WHERE conv_id = ?;
