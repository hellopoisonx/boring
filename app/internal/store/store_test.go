package store_test

import (
	"context"
	"database/sql"
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
	if _, err := s1.CreateUserTenant(context.Background(), "u-1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = s1.Close()

	s2, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	id, err := s2.GetUserTenantByUserID(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("GetUserTenantByUserID: %v", err)
	}
	if id == 0 {
		t.Errorf("tenant_id should be > 0, got %d", id)
	}
}

// 用例 4：UserTenant Create + Get 双向
func TestUserTenant_CreateAndGet(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	id, err := s.CreateUserTenant(ctx, "alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Fatalf("tenant_id should be > 0, got %d", id)
	}

	got, err := s.GetUserTenantByUserID(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUserTenantByUserID: %v", err)
	}
	if got != id {
		t.Errorf("GetUserTenantByUserID = %d, want %d", got, id)
	}

	uid, err := s.GetUserTenantByTenantID(ctx, id)
	if err != nil {
		t.Fatalf("GetUserTenantByTenantID: %v", err)
	}
	if uid != "alice" {
		t.Errorf("GetUserTenantByTenantID = %q, want %q", uid, "alice")
	}
}

// 用例 5：UserTenant 重复 Create → UNIQUE 错误
func TestUserTenant_CreateDuplicate_ReturnsUniqueError(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	if _, err := s.CreateUserTenant(ctx, "dup"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := s.CreateUserTenant(ctx, "dup")
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

	tid, err := s.CreateUserTenant(ctx, "alice")
	if err != nil {
		t.Fatalf("Create user_tenant: %v", err)
	}

	payload := `{"display_name":"Alice","plan":"pro"}`
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     payload,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.GetTenantInfo(ctx, tid)
	if err != nil {
		t.Fatalf("GetTenantInfo: %v", err)
	}
	if got.Info != payload {
		t.Errorf("info = %s, want %s", got.Info, payload)
	}
}

// 用例 7：TenantInfo Upsert 已存在 → info 覆盖、updated_at 推进
func TestTenantInfo_Upsert_Update(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	tid, err := s.CreateUserTenant(ctx, "bob")
	if err != nil {
		t.Fatalf("Create user_tenant: %v", err)
	}

	first := `{"v":1}`
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     first,
	}); err != nil {
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

	second := `{"v":2,"new":"yes"}`
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     second,
	}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := s.GetTenantInfo(ctx, tid)
	if err != nil {
		t.Fatalf("GetTenantInfo: %v", err)
	}
	if got.Info != second {
		t.Errorf("info = %s, want %s", got.Info, second)
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

	tid, err := s.CreateUserTenant(ctx, "carol")
	if err != nil {
		t.Fatalf("Create user_tenant: %v", err)
	}

	err = s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "not json",
	})
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

	// 用了 tenantID = 999 但 tenant_info 没有对应行
	_, err := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: 999,
		Title:    "no info row",
	})
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

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	id, err := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: tid,
		Title:    "first chat",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetTenantConv(ctx, id)
	if err != nil {
		t.Fatalf("GetTenantConv: %v", err)
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
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Errorf("timestamps zero: %+v", got)
	}
}

// 用例 11：Conv ListByTenant 数量、顺序（按 conv_id 升序）
func TestConv_ListByTenant(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 另一个租户，故意搅一下
	otherTid, _ := s.CreateUserTenant(ctx, "bob")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: otherTid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert bob: %v", err)
	}

	wantIDs := make([]int64, 0, 3)
	for i, title := range []string{"a", "b", "c"} {
		id, err := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
			TenantID: tid,
			Title:    title,
		})
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		wantIDs = append(wantIDs, id)
	}
	// 给 bob 加一个，不应出现在结果里
	if _, err := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: otherTid,
		Title:    "bob's chat",
	}); err != nil {
		t.Fatalf("Create bob: %v", err)
	}

	got, err := s.ListTenantConvsByTenant(ctx, tid)
	if err != nil {
		t.Fatalf("ListTenantConvsByTenant: %v", err)
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("len = %d, want %d", len(got), len(wantIDs))
	}
	for i, c := range got {
		if c.ConvID != wantIDs[i] {
			t.Errorf("got[%d].ConvID = %d, want %d", i, c.ConvID, wantIDs[i])
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

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ids := make([]int64, 0, 3)
	for range 3 {
		id, _ := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
			TenantID: tid,
			Title:    "x",
		})
		ids = append(ids, id)
	}
	// 把第二个改成 archived
	if err := s.UpdateTenantConvStatus(ctx, store.UpdateTenantConvStatusParams{
		ConvID: ids[1],
		Status: store.ConvStatusArchived,
	}); err != nil {
		t.Fatalf("UpdateTenantConvStatus: %v", err)
	}

	got, err := s.ListTenantConvsByTenantAndStatus(ctx, store.ListTenantConvsByTenantAndStatusParams{
		TenantID: tid,
		Status:   store.ConvStatusActive,
	})
	if err != nil {
		t.Fatalf("ListTenantConvsByTenantAndStatus active: %v", err)
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

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	id, _ := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: tid,
		Title:    "x",
	})
	before, _ := s.GetTenantConv(ctx, id)

	time.Sleep(1100 * time.Millisecond)
	if err := s.UpdateTenantConvStatus(ctx, store.UpdateTenantConvStatusParams{
		ConvID: id,
		Status: store.ConvStatusDeleted,
	}); err != nil {
		t.Fatalf("UpdateTenantConvStatus: %v", err)
	}
	after, _ := s.GetTenantConv(ctx, id)

	if after.Status != store.ConvStatusDeleted {
		t.Errorf("Status = %q, want deleted", after.Status)
	}
	if after.UpdatedAt <= before.UpdatedAt {
		t.Errorf("UpdatedAt not advanced: %d → %d", before.UpdatedAt, after.UpdatedAt)
	}
}

// 用例 14：FK CASCADE — 删 tenant_info 行级联 tenant_conv
func TestFK_CascadeOnTenantInfoDelete(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	cid, _ := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: tid,
		Title:    "x",
	})

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

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, err := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: tid,
		Title:    "",
	})
	if err != nil {
		t.Fatalf("Create with empty title: %v", err)
	}
	got, err := s.GetTenantConv(ctx, id)
	if err != nil {
		t.Fatalf("GetTenantConv: %v", err)
	}
	if got.Title != "" {
		t.Errorf("Title = %q, want empty", got.Title)
	}
}

// 用例 16：UpdateTenantConvUsage 写入后 GetTenantConv 验证字段值
func TestConv_UpdateUsage(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, _ := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: tid,
		Title:    "x",
	})

	// 写入 usage
	if err := s.UpdateTenantConvUsage(ctx, store.UpdateTenantConvUsageParams{
		ConvID:        id,
		TotalTokens:   12345,
		ModelID:       "gpt-4o",
		ModelProvider: "openai",
	}); err != nil {
		t.Fatalf("UpdateTenantConvUsage: %v", err)
	}

	got, err := s.GetTenantConv(ctx, id)
	if err != nil {
		t.Fatalf("GetTenantConv: %v", err)
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

// 用例 17：GetLatestActiveTenantConv — 取最后 active conv；无 active 时返回 ErrNoRows
func TestConv_LatestActiveByTenant(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// 无 active conv → ErrNoRows
	_, err := s.GetLatestActiveTenantConv(ctx, tid)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("期望 ErrNoRows，实际: %v", err)
	}

	// 建 3 条，把第 2 条 archive
	ids := make([]int64, 3)
	for i := range 3 {
		ids[i], _ = s.CreateTenantConv(ctx, store.CreateTenantConvParams{
			TenantID: tid,
			Title:    "x",
		})
	}
	if err := s.UpdateTenantConvStatus(ctx, store.UpdateTenantConvStatusParams{
		ConvID: ids[1],
		Status: store.ConvStatusArchived,
	}); err != nil {
		t.Fatalf("UpdateTenantConvStatus: %v", err)
	}

	// 应返回 conv_id 最大的 active 行（ids[2]）
	got, err := s.GetLatestActiveTenantConv(ctx, tid)
	if err != nil {
		t.Fatalf("GetLatestActiveTenantConv: %v", err)
	}
	if got.ConvID != ids[2] {
		t.Errorf("GetLatestActiveTenantConv conv_id = %d, want %d", got.ConvID, ids[2])
	}

	// 把 ids[2] 也 archive → 应回退到 ids[0]
	if err := s.UpdateTenantConvStatus(ctx, store.UpdateTenantConvStatusParams{
		ConvID: ids[2],
		Status: store.ConvStatusArchived,
	}); err != nil {
		t.Fatalf("UpdateTenantConvStatus: %v", err)
	}
	got, err = s.GetLatestActiveTenantConv(ctx, tid)
	if err != nil {
		t.Fatalf("GetLatestActiveTenantConv after archive: %v", err)
	}
	if got.ConvID != ids[0] {
		t.Errorf("GetLatestActiveTenantConv after archive conv_id = %d, want %d", got.ConvID, ids[0])
	}
}

// 用例 18：IncTenantConvUsage 累加 total_tokens，model_id / model_provider 被最新值覆盖
func TestConv_IncUsage(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	tid, _ := s.CreateUserTenant(ctx, "alice")
	if err := s.UpsertTenantInfo(ctx, store.UpsertTenantInfoParams{
		TenantID: tid,
		Info:     "{}",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, _ := s.CreateTenantConv(ctx, store.CreateTenantConvParams{
		TenantID: tid,
		Title:    "x",
	})

	// 第一次累加
	if err := s.IncTenantConvUsage(ctx, store.IncTenantConvUsageParams{
		ConvID:        id,
		TotalTokens:   100,
		ModelID:       "gpt-4o",
		ModelProvider: "openai",
	}); err != nil {
		t.Fatalf("IncTenantConvUsage #1: %v", err)
	}
	got, _ := s.GetTenantConv(ctx, id)
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
	if err := s.IncTenantConvUsage(ctx, store.IncTenantConvUsageParams{
		ConvID:        id,
		TotalTokens:   250,
		ModelID:       "claude-3",
		ModelProvider: "anthropic",
	}); err != nil {
		t.Fatalf("IncTenantConvUsage #2: %v", err)
	}
	got, _ = s.GetTenantConv(ctx, id)
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

	_, err := s.GetUserTenantByUserID(ctx, "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected errors.Is(err, sql.ErrNoRows) = true, err = %v", err)
	}
}
