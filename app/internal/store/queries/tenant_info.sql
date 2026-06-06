-- name: UpsertTenantInfo :exec
INSERT INTO tenant_info (tenant_id, info, created_at, updated_at)
VALUES (?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
ON CONFLICT(tenant_id) DO UPDATE SET
    info       = excluded.info,
    updated_at = excluded.updated_at;

-- name: GetTenantInfo :one
SELECT * FROM tenant_info WHERE tenant_id = ?;

-- name: UpdateTenantInfo :exec
UPDATE tenant_info
SET info = ?, updated_at = strftime('%s', 'now')
WHERE tenant_id = ?;
