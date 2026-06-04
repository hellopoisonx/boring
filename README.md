# boring

[AIM](https://github.com/hellopoisonx/aim) 的附属项目

## 功能

- 闲聊
- 执行任务
  - 处理文档
  - 搜寻资料
  - ...(可调用外部工具)
- 租户隔离（CLI 通过 `--profile` 接入，行级 tenant_id 隔离，每次对话用量落库）
- 跨会话记忆
- 自定义模型接入

## 数据存储

- 用户数据: [sqlite](https://github.com/ncruces/go-sqlite3) — 三表 `user_tenant` / `tenant_info` / `tenant_conv`，详见 [`app/internal/store/schema.sql`](./app/internal/store/schema.sql) 与 [`plans/db-schema-v1.md`](./plans/db-schema-v1.md)。CLI 已接入：`--profile` 自动创建或复用租户及会话。
- 向量数据库: [chromem-go](https://github.com/philippgille/chromem-go)
