# 会话 SQLite 持久化存储设计方案

## 1. 背景与定位

`session` 包已有面向 Agent 运行层的逻辑会话抽象 `session.Store`（`session/session.go`：以 `(Scope,ID)` 为会话、`Load` 返回整段历史 Snapshot、`Append(…, expectedRevision, batch)` 做乐观并发追加，ToolLoop/Agent 只依赖接口）。此前唯一实现是纯内存的 `session.MemoryStore`，进程退出即丢失，不满足"把它接到真实后端、做成能在生产的 Agent 应用持久化对话"的定位需要。

本方案在 `session` 下新增 `session/sqlite` 子包，作为 `session.Store` 的**持久化参考实现**：进程重启后仍能通过同一 DSN 读回历史与 revision，打通工具可组合、可替换的价值缺口。

## 2. 依赖取舍（重要、经用户确认）

按用户明确选择的方向 A，SQLite 后端作为 **agent-go module 的正式子包** `session/sqlite`，并为 module 引入首个第三方运行时依赖 **`modernc.org/sqlite`**（纯 Go、无 CGO 的 SQLite 移植）。

后果与理由，已在实现前与用户确认：
- **导入 agent-go 即会带入 `modernc.org/sqlite`**（即使不使用该子包）。这是"正式库内子包"定位的固有代价。
- 选 `modernc`（非 `mattn/go-sqlite3`）是为保住 CGO-free、跨平台与 CI 矩阵（Linux/macOS/Windows + `-race`）—— `modernc` 以 `CGO_ENABLED=0` 即可编译验证。
- driver 名为 `sqlite`。

## 3. 数据模型

单表、每（会话）一行、整段历史存一列：

```sql
CREATE TABLE IF NOT EXISTS conversations (
    scope      TEXT    NOT NULL,
    id         TEXT    NOT NULL,
    revision   INTEGER NOT NULL,
    payload    BLOB    NOT NULL,   -- JSON 序列化的 []message.Message（整段历史）
    updated_at INTEGER NOT NULL,   -- 仅信息性
    PRIMARY KEY (scope, id)
);
```

序列化复用 `message.Message` 既有 JSON tag（含 `ContentToolCall.ToolCall.Arguments []byte`，JSON 中表现为 base64，往返无损，实测验证）。

## 4. API

`session/sqlite` 公开构造与两个接口方法 + 生命周期：

```go
package sqlite

type Config struct {
    DSN          string // SQLite 数据源，如 file:agent.db?_pragma=busy_timeout(5000) ；:memory: 用于测试
    MaxOpenConns int    // 0=驱动默认
}

func Open(cfg Config) (*Store, error)  // 打开/建库并建(缺)表；Ping 校验。调用方拥有生命周期须 Close
func (s *Store) Close() error
func (s *Store) Load(ctx, session.Key) (session.Snapshot, error)      // 满足 session.Store
func (s *Store) Append(ctx, session.Key, session.Revision, []message.Message) (session.Revision, error)
```

- `Load`：无会话返回 revision 0 与 nil 消息；读回后 `message.CloneSlice` 深拷贝（隔离）。
- 空 key / 空 batch：reuse `session` 包 sentinel（`ErrInvalidKey` / `ErrInvalidMessages`）；nil/已取消 context 分别按 `ErrInvalidContext` / `ctx.Err()`。

## 5. 乐观并发（CAS，单事务）

`Append` 语义与 `session.MemoryStore` 保持一致：只有 `expectedRevision == 当前` 才提交成功，否则返回 `%w` 于 `session.ErrConflict` 的错误。实现通过单事务内**以 `expected` 为键的条件写**保证，不依赖任何进程内锁（跨 goroutine 亦安全），并天然防"丢失更新"：

- `expected == 0`（视为新会话）：`INSERT`；若因并发他方已先建行而撞到主键，则在同事务回查该行存在性 → `ErrConflict`。
- `expected > 0`：在事务内按 `WHERE scope=? AND id=? AND revision=expected` 读 payload → 追加新消息 → `UPDATE … SET revision=? AND payload=? WHERE … AND revision=expected`，`RowsAffected==1` 才 `Commit`；否则 `ErrConflict`。SQLite 串行化写者，并发双写时后写者因 revision 已前进而命中 0 行 → 冲突，不会覆盖对方已提交的消息。
- revision 溢出判为 `ErrRevisionExhausted`。

## 6. 已知取舍与边界

- **整段历史单列 ⇒ 每次 Append 都重写整行**：单次 O(N)、长会话累积 O(N²)。对"单次 Run 提交一批、消息量通常不大"的 agent 场景可接受；作为可读代码/文档边界列出，未来若需要更优扩展，可改为"每消息一行 + append log"，仍保持同一 `session.Store` 契约（属扩展，本方案不做）。
- **`:memory:` 与连接池**：数据库/sql 连接池下，`:memory:` 数据库对每个连接各自独立（建表只在一连接上）。故测试与明确用内存时需 `MaxOpenConns:1`（详见 doc.go 与测试 helper注释）。真实文件 DSN 无此问题。
- **写入并发与忙锁**：多连接并发写同一文件时，SQLite 会短暂返回 `SQLITE_BUSY`；生产 DSN 应带 `?_pragma=busy_timeout(…)` 让写者在锁上等待而非立即失败（文档与测试均如此使用；`Open` 不擅自替调用方加 pragma，保持纯透传）。
- 无缓存、无进程内存态：所有读写直接落库；多个 `*session/sqlite.Store` 指向同一文件即多进程/重启用同一历史。

## 7. 测试策略

- 单连接内存库（串行验证语义）：缺会话、Append/Load 往返、tool call 往返、乐观冲突、对已存在会话用 expected=0 冲突、空 key/batch 校验、Load 返回深拷贝隔离、context 取消、Close 后报错。
- 文件库（生产形状，pool 多连接）：并发写不同 key、并发写**同一新会话**恰一个成功其余 `ErrConflict`（single winner）；**跨 reopen** 持久化（Close 后以同 DSN 重开仍读回唯一正确历史）。
- `gofmt`、`go vet`、`go test ./...`；`CGO_ENABLED=0 go build` 确证纯 Go。
- `go test -race ./session/sqlite/`：因本机无 gcc（无 CGO）无法本地执行，需在含 gcc 的 Linux CI runner 上补跑（既有工具链限制）。

## 8. 兼容与演进 / 影响

- 纯新增：新增 `session/sqlite` 子包与导出 `Config/Store/Open/Close`；不占既有名字，不改 `session.Store` 契约、不改 `agent`/ToolLoop、不改 `MemoryStore`。
- 唯一可见影响：`go.mod` 新增 direct `require modernc.org/sqlite` 及其间接依赖集合（属用户确认接受的方向 A 代价）。
- 未来可扩展：`ReadOnlyStore`/`DecoratedStore`（指标、日志、加密、TTL）可在外层包实现或沿用 `session.Store`；更细颗粒持久化、归档、游标分页读取均为演进位，本方案不引入。

## 9. 实施结果（已完成并验证）

- 新增 `session/sqlite/sqlite.go` 与 `session/sqlite/sqlite_test.go`，`go.mod` 引入 `modernc.org/sqlite v1.58.0`（direct）。
- 验证：`gofmt` 干净、`go vet ./...`、`go test ./...` 全绿、`CGO_ENABLED=0 go build ./session/sqlite/` 通过（纯 Go）、并发/乐观锁/跨 reopen 单测通过。
