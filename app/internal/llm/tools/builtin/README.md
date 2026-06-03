# 内置 LLM 工具：read / edit / write

> 本目录实现三个 LLM 直接可调用的文件工具，遵循
> [RimuruW/pi-hashline-edit](https://github.com/RimuruW/pi-hashline-edit)
> 的 **hashline 协议**：read 输出带 `LINE#HASH:` 锚点的文本，edit 通过
> 锚点精确定位行；hash 失配会被拒绝，杜绝"基于陈旧上下文写错文件"。

## 工具一览

| 工具    | 用途                       | 协议        | 风险 |
| ------- | -------------------------- | ----------- | ---- |
| `read`  | 读文件，按行渲染 hashline | 哈希锚点   | 低   |
| `edit`  | 基于锚点的精确行编辑       | 哈希锚点   | 低   |
| `write` | 覆盖写整个文件             | 无锚点     | 中（会覆盖现有内容） |

### read

读文件，输出形如：

```
  1#MJ:function hello() {
  2#YR:  console.log("world");
  3#RM:}
```

- `LINE` 是 1-indexed 行号（左补空格对齐）
- `HASH` 是 2 字符内容指纹（取自 `ZPMQVRWSNKTXJBYH` 字母表，16 字符
  编码 4 bit × 2 = 8 bit 空间）
- 编辑时 LLM 直接把 `LINE#HASH` 字符串作为锚点回传给 `edit`

参数：
- `path` (必填)：相对 WorkDir 或 WorkDir 内的绝对路径
- `offset` (选填)：1-indexed 起始行，默认 1
- `limit` (选填)：最多返回行数，默认 2000

### edit

支持 4 种 op：

| Op              | 必填字段                    | 行为 |
| --------------- | --------------------------- | ---- |
| `replace`       | `pos`, `lines`              | 替换 `pos` 那一行 |
| `replace` (范围)| `pos`, `end`, `lines`       | 替换 `pos..end` 闭区间 |
| `append`        | `lines`（`pos` 选填）       | 在 `pos` 之后插入；`pos` 为空则追加到 EOF |
| `prepend`       | `lines`（`pos` 选填）       | 在 `pos` 之前插入；`pos` 为空则插入到 BOF |
| `replace_text`  | `oldText`, `newText`        | 全局唯一字符串替换（兼容老格式） |

**关键约束**：
- 所有 `pos`/`end` 必须是 read 输出的 `LINE#HASH` 锚点，hash 失配会
  拒绝（返回 fresh anchors 供 LLM 重试）
- 同一次调用多个 edits 共享 pre-edit snapshot，**bottom-up 排序**后
  依次应用（后面的 edit 不会影响前面 edit 的行号）
- **同一次调用内重复使用同一个 `pos`：只有最后一次 edit 生效**（bottom-up
  排序的天然行为——同一锚点的多次 edit 会按出现顺序叠加，但 line 相同的
  多次覆盖最终以最后一次为准）。LLM 应该把同一行的多次修改合并成
  单次 `replace`/`replace_text`
- **文件不存在或为空（0 字节）时拒绝执行**，并提示改用 `write` 工具
  创建内容。edit 依赖锚点，0 行文件没有锚点可定位；新建/清空文件
  用 `write`，编辑现有内容用 `edit`（先 `read` 拿锚点）

### write

- 覆盖写整个文件
- 自动创建父目录
- 写操作总是返回纯文本成功消息（"wrote N bytes to path"）
- **read-first 强约束**：覆盖已存在的非空文件前，本对话中必须先
  `read` 过该文件（由 [`Env.Tracker`](#filestate-文件读追踪) 追踪）。
  目的：阻止 LLM 在 edit 撞到 stale anchor 后改用 `write` 强行覆盖
  整个文件（此时 LLM 没有当前内容，write 极易丢内容/覆盖别人修改）。
  例外：写新文件 / 覆盖空文件无需 read（无现有内容可丢）

## 执行环境 Env

所有工具的 IO 都经过 [`Env`](./tool.go) 注入的参数管控。`Env` 的字段：

| 字段        | 类型         | 作用 |
| ----------- | ------------ | ---- |
| `TenantID`  | `string`     | 租户标识；为 `""` 时表示单租户/开发模式。后期多租户时用于审计、限流、日志关联 |
| `WorkDir`   | `string`     | 工作根目录；所有路径访问的**沙箱根**。必须为绝对路径，否则 fail-closed |
| `MaxBytes`  | `int64`      | 单次读/写字节上限；0 表示不限制。防止 LLM 一次性塞进几 GB 文本 |
| `ReadOnly`  | `bool`       | true 时 `edit`/`write` 拒绝执行 |
| `Tracker`   | `*FileState` | 文件读追踪器，nil 时不启用 read-first 校验（详见下文） |

### 路径沙箱

`Env.Resolve(path)` 在每次 IO 前把用户传入的路径解析并校验**必须落在
WorkDir 子树内**，任何 `../` 越界或 WorkDir 外部的绝对路径都会被拒绝
（fail-closed）。这层校验是**软沙箱**——靠路径语义而非内核 syscall。

### FileState 文件读追踪

`Env.Tracker` 是 `*FileState` 类型的可选字段，用于实现 "read-before-
overwrite" 安全策略：

- `read` 工具成功后，调用 `Tracker.MarkRead(canonical)` 把 symlink
  解析后的真实路径标记为"已读"
- `write` 工具在覆盖"已存在 + 非空"的文件前，会调用
  `Tracker.WasRead(canonical)` 校验"本对话中是否已 read 过"；未 read
  则拒绝（"refusing to overwrite existing non-empty file without
  prior `read`"）
- 软链（symlink）别名共享同一份 read 状态（都用 canonical 记录/查询）

**典型用途**：防止 LLM 在 `edit` 撞到 stale anchor（hash 失配）后
改用 `write` 强行覆盖整个文件——此时 LLM 没有当前文件内容，write
极易丢内容、覆盖别人修改。强制 read-first 让 LLM 必须先看到最新
内容才能 write。

**`Tracker` 为 `nil` 时的行为**：write 不做 read-first 校验，相当于
关闭此安全网。用于无状态执行、单测场景；生产/多租户环境应总是设
`Tracker = &FileState{}`。

### 租户隔离演进路径

| 阶段     | `Env.WorkDir` 取值                              | 隔离强度 |
| -------- | ----------------------------------------------- | -------- |
| 现状     | 调用方传入（开发模式默认 `os.Getwd()`）         | 软沙箱：靠 `filepath.Rel` 校验 |
| 短期     | 每个请求用独立子目录 `/tenants/{id}/workspace`  | 软沙箱 + 进程级独立 |
| 中期     | chroot(2) + bind mount 上述子目录                | 内核级：进程不可见其它租户 |
| 长期     | 用户命名空间 + mount namespace (unshare)        | 完全容器级：无需 root |

工具代码本身不需要任何改动——`Env` 抽象把"路径沙箱"和"物理隔离"都吸收
了，业务工具只看到 `Env.WorkDir` 这个绝对路径，把它当作 `root` 使用。

## 并发模型

- 每次 `edit` / `write` 都会获取 [per-canonical-path 互斥锁](./filelock.go)：
  - 锁挂在 symlink 解析后的真实路径上，`/a/b` 和 `/a/c` 同 inode
    也能互斥
  - 同一文件的所有写操作串行化，避免 read-then-write 区间竞态
- `read` 不上锁（多 reader 无害）

## 原子写

[`atomicWrite`](./atomic.go) 用 `temp file + rename` 保证：

1. 失败时不会留下半截文件
2. 读者要么看到旧内容、要么看到新内容（POSIX rename 原子性）
3. 保留原文件权限（chmod 0600 的私密文件不会被改成 0644）
4. symlink 始终写到解析后的真实路径，**保留软链结构**
   （不会把软链接替换成普通文件）

## 已知限制 / 未来改进

- **hash 碰撞**：2 字符 hash = 8 bit 空间，理论上每 256 行有 1 个碰撞。
  碰撞会被误判为 stale context，LLM re-read 一次即可（重新拿到
  fresh hash）。生产场景若出现大规模碰撞可考虑扩到 4 字符
- **diff preview**：当前只展示"行数差 + 头尾 5 行 fresh anchors"，
  未来可加 +/- 标记的精细 diff（[pi-hashline-edit] 已实现，可借鉴）
- **审计/钩子**：Env 当前不暴露 pre/post 钩子。后续需要审计/限流
  时可在 Env 上加 `OnRead` / `OnWrite` 回调而不动工具代码
- **chroot 集成**：在 Env 上加 `chroot()` 方法或独立的
  `NewChrootEnv(tenantID)` 构造器即可，工具代码无需改动
