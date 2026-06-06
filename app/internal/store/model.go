package store

// Conv 状态枚举：与 schema.sql 里 CHECK 约束保持一致。
//
// sqlc 不会为 SQL CHECK 约束生成 Go 常量；这里手写并用包级 const 暴露，
// 避免调用方在多处硬编码字符串字面量。
const (
	ConvStatusActive   = "active"
	ConvStatusArchived = "archived"
	ConvStatusDeleted  = "deleted"
)
