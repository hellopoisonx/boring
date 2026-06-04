package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"

	"github.com/hellopoisonx/boring/app/internal/store"
)

// openMem 返回一个 in-memory Store，每个用例独立。
func openMem(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Open in-memory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}


// 用例 1：Open + 建表
func TestOpen_CreatesAllTables(t *testing.T) {
	s := openMem(t)

	want := map[string]bool{
		"user_tenant": false,
		"tenant_info": false,
		"tenant_conv": false,
	}
	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type='table';`)
	if err != nil {
		t.Fatalf("query master: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected table %q to exist", name)
		}
	}
}

// 用例 2：PRAGMA foreign_keys = ON 已生效
func TestOpen_ForeignKeysEnabled(t *testing.T) {
	s := openMem(t)
	var on int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys;`).Scan(&on); err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	if on != 1 {
		t.Errorf("foreign_keys = %d, want 1", on)
	}
}

// 用例 3：同一文件二次 Open 幂等
func TestOpen_TwiceOnSameFile_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "store.db")

	s1, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// 写一行再开第二次，验证不丢数据
	if _, err := s1.UserTenants().Create(context.Background(), "u-1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = s1.Close()

	s2, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	id, err := s2.UserTenants().GetByUserID(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if id == 0 {
		t.Errorf("tenant_id should be > 0, got %d", id)
	}
}

// 用例 4：UserTenant Create + Get 双向
func TestUserTenant_CreateAndGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut := s.UserTenants()

	id, err := ut.Create(ctx, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatalf("tenant_id should be > 0, got %d", id)
	}

	got, err := ut.GetByUserID(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if got != id {
		t.Errorf("GetByUserID = %d, want %d", got, id)
	}

	uid, err := ut.GetByTenantID(ctx, id)
	if err != nil {
		t.Fatalf("GetByTenantID: %v", err)
	}
	if uid != "alice" {
		t.Errorf("GetByTenantID = %q, want %q", uid, "alice")
	}
}

// 用例 5：UserTenant 重复 Create → UNIQUE 错误
func TestUserTenant_CreateDuplicate_ReturnsUniqueError(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut := s.UserTenants()

	if _, err := ut.Create(ctx, "dup"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := ut.Create(ctx, "dup")
	if err == nil {
		t.Fatal("expected error on duplicate Create, got nil")
	}
	var sqlErr *sqlite3.Error
	if !errors.As(err, &sqlErr) {
		t.Fatalf("expected *sqlite3.Error, got %T: %v", err, err)
	}
	if sqlErr.Code() != sqlite3.CONSTRAINT {
		t.Errorf("sqlite err code = %d, want CONSTRAINT", sqlErr.Code())
	}
}

// 用例 6：TenantInfo Upsert 新建
func TestTenantInfo_Upsert_Insert(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti := s.UserTenants(), s.TenantInfos()

	tid, err := ut.Create(ctx, "alice")
	if err != nil {
		t.Fatalf("Create user_tenant: %v", err)
	}

	payload := json.RawMessage(`{"display_name":"Alice","plan":"pro"}`)
	if err := ti.Upsert(ctx, tid, payload); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := ti.GetByTenantID(ctx, tid)
	if err != nil {
		t.Fatalf("GetByTenantID: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("info = %s, want %s", got, payload)
	}
}

// 用例 7：TenantInfo Upsert 已存在 → info 覆盖、updated_at 推进
func TestTenantInfo_Upsert_Update(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti := s.UserTenants(), s.TenantInfos()

	tid, err := ut.Create(ctx, "bob")
	if err != nil {
		t.Fatalf("Create user_tenant: %v", err)
	}

	first := json.RawMessage(`{"v":1}`)
	if err := ti.Upsert(ctx, tid, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// 拿原始 created_at / updated_at
	var created1, updated1 int64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT created_at, updated_at FROM tenant_info WHERE tenant_id = ?`, tid,
	).Scan(&created1, &updated1); err != nil {
		t.Fatalf("select timestamps: %v", err)
	}

	// 等 1 秒确保 unix second 推进
	time.Sleep(1100 * time.Millisecond)

	second := json.RawMessage(`{"v":2,"new":"yes"}`)
	if err := ti.Upsert(ctx, tid, second); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := ti.GetByTenantID(ctx, tid)
	if err != nil {
		t.Fatalf("GetByTenantID: %v", err)
	}
	if string(got) != string(second) {
		t.Errorf("info = %s, want %s", got, second)
	}

	var created2, updated2 int64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT created_at, updated_at FROM tenant_info WHERE tenant_id = ?`, tid,
	).Scan(&created2, &updated2); err != nil {
		t.Fatalf("select timestamps 2: %v", err)
	}
	if created1 != created2 {
		t.Errorf("created_at changed: %d → %d", created1, created2)
	}
	if updated2 <= updated1 {
		t.Errorf("updated_at not advanced: %d → %d", updated1, updated2)
	}
}

// 用例 8：TenantInfo 非法 JSON → CHECK 约束错误
func TestTenantInfo_Upsert_RejectsInvalidJSON(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti := s.UserTenants(), s.TenantInfos()

	tid, err := ut.Create(ctx, "carol")
	if err != nil {
		t.Fatalf("Create user_tenant: %v", err)
	}

	err = ti.Upsert(ctx, tid, json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected CHECK constraint error, got nil")
	}
	var sqlErr *sqlite3.Error
	if !errors.As(err, &sqlErr) {
		t.Fatalf("expected *sqlite3.Error, got %T: %v", err, err)
	}
	if sqlErr.Code() != sqlite3.CONSTRAINT {
		t.Errorf("sqlite err code = %d, want CONSTRAINT", sqlErr.Code())
	}
}

// 用例 9：Conv Create 前未建 tenant_info → FK 错误
func TestConv_Create_FKRequiresTenantInfo(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	convs := s.Convs()

	// 用了 tenantID = 999 但 tenant_info 没有对应行
	_, err := convs.Create(ctx, 999, "no info row")
	if err == nil {
		t.Fatal("expected FK error, got nil")
	}
	var sqlErr *sqlite3.Error
	if !errors.As(err, &sqlErr) {
		t.Fatalf("expected *sqlite3.Error, got %T: %v", err, err)
	}
	if sqlErr.Code() != sqlite3.CONSTRAINT {
		t.Errorf("sqlite err code = %d, want CONSTRAINT", sqlErr.Code())
	}
}

// 用例 10：Conv Create + Get
func TestConv_CreateAndGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	id, err := convs.Create(ctx, tid, "first chat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := convs.GetByConvID(ctx, id)
	if err != nil {
		t.Fatalf("GetByConvID: %v", err)
	}
	if got.TenantID != tid {
		t.Errorf("TenantID = %d, want %d", got.TenantID, tid)
	}
	if got.Title != "first chat" {
		t.Errorf("Title = %q, want %q", got.Title, "first chat")
	}
	if got.Status != store.ConvStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, store.ConvStatusActive)
	}
	if got.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0", got.TotalTokens)
	}
	if got.ModelID != "" {
		t.Errorf("ModelID = %q, want empty", got.ModelID)
	}
	if got.ModelProvider != "" {
		t.Errorf("ModelProvider = %q, want empty", got.ModelProvider)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps zero: %+v", got)
	}
}


// 用例 11：Conv ListByTenant 数量、顺序（按 conv_id 升序）
func TestConv_ListByTenant(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 另一个租户，故意搅一下
	otherTid, _ := ut.Create(ctx, "bob")
	if err := ti.Upsert(ctx, otherTid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}

	wantIDs := make([]int64, 0, 3)
	for i, title := range []string{"a", "b", "c"} {
		id, err := convs.Create(ctx, tid, title)
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		wantIDs = append(wantIDs, id)
	}
	// 给 bob 加一个，不应出现在结果里
	if _, err := convs.Create(ctx, otherTid, "bob's chat"); err != nil {
		t.Fatalf("Create bob: %v", err)
	}

	got, err := convs.ListByTenant(ctx, tid)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("len = %d, want %d", len(got), len(wantIDs))
	}
	for i, c := range got {
		if c.ID != wantIDs[i] {
			t.Errorf("got[%d].ID = %d, want %d", i, c.ID, wantIDs[i])
		}
		if c.TenantID != tid {
			t.Errorf("got[%d].TenantID = %d, want %d", i, c.TenantID, tid)
		}
	}
}

// 用例 12：Conv ListByTenantAndStatus
func TestConv_ListByTenantAndStatus(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ids := make([]int64, 0, 3)
	for range 3 {
		id, _ := convs.Create(ctx, tid, "x")
		ids = append(ids, id)
	}
	// 把第二个改成 archived
	if err := convs.UpdateStatus(ctx, ids[1], store.ConvStatusArchived); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, err := convs.ListByTenantAndStatus(ctx, tid, store.ConvStatusActive)
	if err != nil {
		t.Fatalf("ListByTenantAndStatus active: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("active count = %d, want 2", len(got))
	}
	for _, c := range got {
		if c.Status != store.ConvStatusActive {
			t.Errorf("status = %q, want active", c.Status)
		}
	}
}

// 用例 13：Conv UpdateStatus + 再 Get
func TestConv_UpdateStatus(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	id, _ := convs.Create(ctx, tid, "x")
	before, _ := convs.GetByConvID(ctx, id)

	time.Sleep(1100 * time.Millisecond)
	if err := convs.UpdateStatus(ctx, id, store.ConvStatusDeleted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	after, _ := convs.GetByConvID(ctx, id)

	if after.Status != store.ConvStatusDeleted {
		t.Errorf("Status = %q, want deleted", after.Status)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("UpdatedAt not advanced: %v → %v", before.UpdatedAt, after.UpdatedAt)
	}
}

// 用例 14：FK CASCADE — 删 tenant_info 行级联 tenant_conv
func TestFK_CascadeOnTenantInfoDelete(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	cid, _ := convs.Create(ctx, tid, "x")

	// 直接 DELETE 绕过 DAO，验证 DB 层 CASCADE
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM tenant_info WHERE tenant_id = ?`, tid); err != nil {
		t.Fatalf("delete tenant_info: %v", err)
	}

	// tenant_conv 应被级联删除
	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tenant_conv WHERE conv_id = ?`, cid,
	).Scan(&n); err != nil {
		t.Fatalf("count tenant_conv: %v", err)
	}
	if n != 0 {
		t.Errorf("tenant_conv row not cascaded, count = %d", n)
	}

	// user_tenant 行仍在（无 FK 引用）
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_tenant WHERE tenant_id = ?`, tid,
	).Scan(&n); err != nil {
		t.Fatalf("count user_tenant: %v", err)
	}
	if n != 1 {
		t.Errorf("user_tenant row should remain (no FK), count = %d", n)
	}
}

// 用例 15：边界 — 空 title
func TestConv_EmptyTitle(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, err := convs.Create(ctx, tid, "")
	if err != nil {
		t.Fatalf("Create with empty title: %v", err)
	}
	got, err := convs.GetByConvID(ctx, id)
	if err != nil {
		t.Fatalf("GetByConvID: %v", err)
	}
	if got.Title != "" {
		t.Errorf("Title = %q, want empty", got.Title)
	}
}

// 用例 16：UpdateUsage 写入后 GetByConvID 验证字段值
func TestConv_UpdateUsage(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, _ := convs.Create(ctx, tid, "x")

	// 写入 usage
	if err := convs.UpdateUsage(ctx, id, 12345, "gpt-4o", "openai"); err != nil {
		t.Fatalf("UpdateUsage: %v", err)
	}

	got, err := convs.GetByConvID(ctx, id)
	if err != nil {
		t.Fatalf("GetByConvID: %v", err)
	}
	if got.TotalTokens != 12345 {
		t.Errorf("TotalTokens = %d, want 12345", got.TotalTokens)
	}
	if got.ModelID != "gpt-4o" {
		t.Errorf("ModelID = %q, want gpt-4o", got.ModelID)
	}
	if got.ModelProvider != "openai" {
		t.Errorf("ModelProvider = %q, want openai", got.ModelProvider)
	}
}

// 用例 17：LatestActiveByTenant — 取最后 active conv；无 active 时返回 ErrNoRows
func TestConv_LatestActiveByTenant(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 无 active conv → ErrNoRows
	_, err := convs.LatestActiveByTenant(ctx, tid)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("期望 ErrNoRows，实际: %v", err)
	}

	// 建 3 条，把第 2 条 archive
	ids := make([]int64, 3)
	for i := range 3 {
		ids[i], _ = convs.Create(ctx, tid, "x")
	}
	if err := convs.UpdateStatus(ctx, ids[1], store.ConvStatusArchived); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// 应返回 conv_id 最大的 active 行（ids[2]）
	got, err := convs.LatestActiveByTenant(ctx, tid)
	if err != nil {
		t.Fatalf("LatestActiveByTenant: %v", err)
	}
	if got.ID != ids[2] {
		t.Errorf("LatestActiveByTenant conv_id = %d, want %d", got.ID, ids[2])
	}

	// 把 ids[2] 也 archive → 应回退到 ids[0]
	if err := convs.UpdateStatus(ctx, ids[2], store.ConvStatusArchived); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, err = convs.LatestActiveByTenant(ctx, tid)
	if err != nil {
		t.Fatalf("LatestActiveByTenant after archive: %v", err)
	}
	if got.ID != ids[0] {
		t.Errorf("LatestActiveByTenant after archive conv_id = %d, want %d", got.ID, ids[0])
	}
}

// 用例 18：IncUsage 累加 total_tokens，model_id / model_provider 被最新值覆盖
func TestConv_IncUsage(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	ut, ti, convs := s.UserTenants(), s.TenantInfos(), s.Convs()

	tid, _ := ut.Create(ctx, "alice")
	if err := ti.Upsert(ctx, tid, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, _ := convs.Create(ctx, tid, "x")

	// 第一次累加
	if err := convs.IncUsage(ctx, id, 100, "gpt-4o", "openai"); err != nil {
		t.Fatalf("IncUsage #1: %v", err)
	}
	got, _ := convs.GetByConvID(ctx, id)
	if got.TotalTokens != 100 {
		t.Errorf("第1次后 TotalTokens = %d, want 100", got.TotalTokens)
	}
	if got.ModelID != "gpt-4o" {
		t.Errorf("第1次后 ModelID = %q, want gpt-4o", got.ModelID)
	}
	if got.ModelProvider != "openai" {
		t.Errorf("第1次后 ModelProvider = %q, want openai", got.ModelProvider)
	}

	// 第二次累加，model 改成别的
	if err := convs.IncUsage(ctx, id, 250, "claude-3", "anthropic"); err != nil {
		t.Fatalf("IncUsage #2: %v", err)
	}
	got, _ = convs.GetByConvID(ctx, id)
	if got.TotalTokens != 350 {
		t.Errorf("第2次后 TotalTokens = %d, want 350", got.TotalTokens)
	}
	if got.ModelID != "claude-3" {
		t.Errorf("第2次后 ModelID = %q, want claude-3", got.ModelID)
	}
	if got.ModelProvider != "anthropic" {
		t.Errorf("第2次后 ModelProvider = %q, want anthropic", got.ModelProvider)
	}
}


// 错误 sql.ErrNoRows 上抛：避免业务侧自己 unwrap
func TestErrors_NotWrapped(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	_, err := s.UserTenants().GetByUserID(ctx, "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected errors.Is(err, sql.ErrNoRows) = true, err = %v", err)
	}
}
