# boring

[AIM](https://github.com/hellopoisonx/aim) 的附属项目

## 功能

- 闲聊
- 执行任务
  - 处理文档
  - 搜寻资料
  - ...(可调用外部工具)
- 租户隔离
- 跨会话记忆
- 自定义模型接入

## 数据存储

- 用户数据: [sqlite](https://github.com/ncruces/go-sqlite3)
- 向量数据库: [chromem-go](https://github.com/philippgille/chromem-go)
- 分层记忆: markdown (llm 直接调用 `grep` 工具)
