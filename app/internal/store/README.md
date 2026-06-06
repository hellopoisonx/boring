# `app/internal/store` —— SQLite 持久化层

租户与会话三表的 SQL 存储层，**所有查询由 [sqlc](https://github.com/sqlc-dev/sqlc) 生成**。

> 同步索引：仓库根 `AGENTS.md` §2.6 / `plans/db-schema-v1.md`

---

## 1. 包结构

```
app/internal/store/
├── README.md                # 本文件
├── schema.sql               # 三表 DDL，被 embed 进 Open()
├── queries/                 # sqlc 读的源（手写）
│   ├── user_tenant.sql
│   ├── tenant_info.sql
│   └── tenant_conv.sql
├── model.go                 # 手写：ConvStatus* 状态常量
├── store.go                 # 手写：*Store、Open()、DB()、Close()
├── db.go                    # sqlc 生成：DBTX / New / WithTx（不要手改）
├── models.go                # sqlc 生成：UserTenant / TenantInfo / TenantConv 行模型
├── user_tenant.sql.go       # sqlc 生成：CreateUserTenant / GetUserTenantByUserID / ...
├── tenant_info.sql.go       # sqlc 生成
├── tenant_conv.sql.go       # sqlc 生成
└── store_test.go            # 集成测试
```

仓库根 `sqlc.yaml` 是 sqlc 的配置文件。

---

## 2. sqlc 工作流

任何 **改 schema / 改 query** 的 PR 都要跑 `sqlc generate`，并把生成产物一起 commit。

### 2.1 加新表 / 改 DDL

1. 改 `app/internal/store/schema.sql`
2. 跑 `sqlc generate`（在仓库根）—— 重新生成 `models.go` / `db.go`
3. 在 `queries/<新表>.sql` 里加查询语句
4. 再跑一次 `sqlc generate` —— 重新生成 `*.sql.go`
5. 在 `model.go` 加对应 CHECK 约束的 Go 常量
6. 跑 `go test ./app/internal/store/`

### 2.2 加新查询

1. 在对应 `queries/*.sql` 里加一个 query 注释 + SQL：

   ```sql
   -- name: GetTenantConvsByTitle :many
   SELECT * FROM tenant_conv
   WHERE tenant_id = ? AND title LIKE ?
   ORDER BY conv_id ASC;
   ```

2. 跑 `sqlc generate` —— 在对应 `*.sql.go` 出现 `GetTenantConvsByTitle` 方法
3. 在 `store_test.go` 加覆盖用例
4. 跑 `go test ./app/internal/store/`

### 2.3 sqlc 注解的 5 种返回类型

| 注解 | 行为 |
|---|---|
| `:one` | 返回单个对象 + error；行不存在时 `err == sql.ErrNoRows` |
| `:many` | 返回 `[]T` + error；空结果时返回空切片（`emit_empty_slices: true`） |
| `:exec` | 不返回行，只返回 error |
| `:execresult` | 返回 `sql.Result`（可拿 RowsAffected / LastInsertId） |
| `:execrows` | 返回受影响行数 |

### 2.4 sqlc 不生成的部分

- **CHECK 约束的 Go 常量**（如 `status IN ('active','archived','deleted')`）——
  手写在 `model.go` 里的 `ConvStatus*`
- **JSON / 时间戳等业务类型转换**——
  sqlc 把 `TEXT` 映射成 `string`、`INTEGER` 映射成 `int64`；
  业务侧需要 `time.Time` / `json.RawMessage` 时由调用方转换
- **0 行 Affected 检查**——
  `:exec` 类型不读 `RowsAffected()`，行不存在时不会返回 `sql.ErrNoRows`；
  需要此语义时请改用 `:execrows` 或显式 SELECT 验证
- **错误包装**——
  sqlc 直接透传 `*sqlite3.Error` / `sql.ErrNoRows` 等驱动错误；
  不要在调用方再 `errors.Is(err, store.ErrNotFound)`，没有这层

---

## 3. 怎么用

### 3.1 打开 / 关闭

```go
ctx := context.Background()
st, err := store.Open(ctx, "file:boring.db")   // :memory: / file:foo.db?...
if err != nil { return err }
defer st.Close()
```

`Open` 内部会：
- `sql.Open("sqlite3", dsn)`
- `SetMaxOpenConns(1)` / `SetMaxIdleConns(1)`（单连接保 FK + busy_timeout 行为）
- 启用 `PRAGMA foreign_keys = ON` / `PRAGMA busy_timeout = 5000`
- 跑一遍 `schema.sql`（`IF NOT EXISTS` 幂等）

### 3.2 直接调 sqlc 生成的方法

`*Store` 嵌入了 `*Queries`，所以可以**直接调**所有 sqlc 方法：

```go
tid, err := st.GetUserTenantByUserID(ctx, "alice")
if errors.Is(err, sql.ErrNoRows) { /* 没找到 */ }

err = st.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
    TenantID: tid,
    Info:     `{"plan":"pro"}`,   // 注意是 string，不是 json.RawMessage
})

conv, err := st.GetLatestActiveTenantConv(ctx, tid)
```

### 3.3 拿原始 `*sql.DB`

```go
// 自定义 SQL / 跑 migration
rows, err := st.DB().QueryContext(ctx, `SELECT ...`)
```

### 3.4 事务

```go
tx, err := st.DB().BeginTx(ctx, nil)
if err != nil { return err }
defer tx.Rollback()

q := st.WithTx(tx)
if err := q.CreateUserTenant(ctx, "alice"); err != nil { return err }
if err := q.UpsertTenantInfo(ctx, ...); err != nil { return err }

return tx.Commit()
```

---

## 4. 已知限制

- **没有 store.ErrNotFound**：sqlc 直接透传 `sql.ErrNoRows`，调用方用 `errors.Is(err, sql.ErrNoRows)` 识别
- **没有软删**：`status='deleted'` 只是状态位，没有 `deleted_at` 时间戳
- **没有迁移框架**：schema.sql 用 `IF NOT EXISTS` 幂等；后续字段增加会手写 v2 SQL 并按版本号分发
- **没有 chromem-go 集成**：向量记忆是后续步骤
- **Info 字段是 string**：sqlc 不感知 `json_valid()` CHECK，调用方需保证传入合法 JSON 字符串（DB 会拦截非法值）
- **时间戳是 int64（unix epoch seconds）**：sqlc 不感知业务层 `time.Time`；调用方按需 `time.Unix(sec, 0).UTC()` 转换
- **`:exec` 0 行不报错**：行不存在时 `RowsAffected() == 0` 但 `ExecContext` 不返回 error；写路径默认假设 `WHERE pk = ?` 一定命中
