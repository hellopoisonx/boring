package store

import "time"

// Conv 是 tenant_conv 表的领域类型。
//
// 时间戳字段在 DB 层存 unix epoch seconds（INTEGER），
// 与 DB 的交互由 DAO 负责 time.Time ↔ int64 转换；调用方拿到的是 time.Time。
type Conv struct {
	ID        int64
	TenantID  int64
	Title     string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Conv 状态枚举：与 schema.sql 里 CHECK 约束保持一致。
// DAO 不做应用层强制，只在文档层声明；写脏值会被 DB 拒。
const (
	ConvStatusActive   = "active"
	ConvStatusArchived = "archived"
	ConvStatusDeleted  = "deleted"
)
