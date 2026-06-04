-- 1) 用户-租户映射表（AIM user_id → 内部 tenant_id）
--    tenant_id 在这里分配（user_id 插入时生成或预留）。
CREATE TABLE IF NOT EXISTS user_tenant (
    user_id    VARCHAR(255) PRIMARY KEY,
    tenant_id  INTEGER      UNIQUE NOT NULL,
    created_at INTEGER      NOT NULL
);

-- 2) 租户信息表（一个 tenant 一份 JSON info）
--    tenant_id 是租户 ID 的唯一来源（不再 FK 引用 user_tenant）。
--    应用层负责保证 user_tenant.tenant_id 与 tenant_info.tenant_id 同步。
CREATE TABLE IF NOT EXISTS tenant_info (
    tenant_id  INTEGER PRIMARY KEY,
    info       TEXT         NOT NULL DEFAULT '{}'
               CHECK (json_valid(info)),
    created_at INTEGER      NOT NULL,
    updated_at INTEGER      NOT NULL
);

-- 3) 租户-会话表（一个租户可拥有多个会话）
--    引用 tenant_info 而非 user_tenant：保证"有 info 才有 conv"的不变量。
CREATE TABLE IF NOT EXISTS tenant_conv (
    conv_id        INTEGER PRIMARY KEY,
    tenant_id      INTEGER      NOT NULL,
    title          VARCHAR(255) NOT NULL DEFAULT '',
    status         VARCHAR(16)  NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active','archived','deleted')),
    total_tokens   INTEGER      NOT NULL DEFAULT 0,
    model_id       VARCHAR(255) NOT NULL DEFAULT '',
    model_provider VARCHAR(64)  NOT NULL DEFAULT '',
    created_at     INTEGER      NOT NULL,
    updated_at     INTEGER      NOT NULL,
    FOREIGN KEY (tenant_id) REFERENCES tenant_info(tenant_id) ON DELETE CASCADE
);


-- 索引
CREATE INDEX IF NOT EXISTS idx_tenant_conv_tenant
    ON tenant_conv(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_conv_status
    ON tenant_conv(tenant_id, status);
