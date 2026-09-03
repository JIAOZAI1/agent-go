# Agent 运行 Scope 状态管理设计方案

状态：**已确认并已实现**（`agent/runscope.go` 新增；`toolloop.go` 以 scope 收口；`RunResult` 新增 `Stats`；配套单测/集成断言均绿）。核心取舍采纳讨论结论：
- Scope **独立文件** `agent/runscope.go`（过程对象），`agent/run.go` 只保留纯数据契约与运行入口契约；
- `RunMeta`/`RunStats` 作为 Scope 的身份面与统计面，统一承载在 Scope 内；
- Turn = 一次循环；Step = 循环内由运行策略自定义粒度的步骤；Scope 只保存 StepCount 整数，不规定 Step 语义；
- Scope 是一次 Run 调用内私有对象，绝不跨 Run/gouroutine/context 共享（并发安全根基）。

本方案引用既有术语与边界：
- [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md)：`RunID`/`Sequence` 语义、终态约束；
- [AgentToolLoop编排设计方案.md](AgentToolLoop编排设计方案.md)：ToolLoopAgent 每次 `Run` 的 per-run 局部状态模型（§7.3）；
- [agent运行策略抽象设计草案.md](agent运行策略抽象设计草案.md)：`RunRequest`/`RunResult` 公共契约。

## 1. 背景与问题

`ToolLoopAgent.Run` 当前的进程内"运行状态"是**游离的散落参数**：

```go
runID, err := newRunID()   // string
var seq uint64             // 事件序号，指针自增
a.publish(ctx, runID, &seq, EventRunStarted, ...) // runID/&seq 逐参数贯穿
return a.fail(ctx, runID, &seq, "render_failed", err)
```

实际贯穿面（grep 实测）：`runID string` + `seq *uint64` 在 `Run` / `fail` / `publish` / `observe` 上作为独立形参各多处出现；事件信封每处手工拼 `Event{RunID, Sequence: *seq, OccurredAt,...}`；序号 `*seq++` 用指针自增（非值属、易误用）。

同时用户需要**一次运行的状态账本**（身份 + 统计）：

```go
type RunMeta struct {
    RunID       string
    ParentRunID string
    SessionID   string
    CreatedAt   time.Time
}

type RunStats struct {
    TurnCount     int
    StepCount     int
    ToolCallCount int
    InputTokens   int
    OutputTokens  int
    TotalTokens   int
    RetryCount    int
}
```

这些目前是**未建模的游离量**：没有 RunMeta/ParentRun 概念；token 只在 `ModelGenerationFinished` 事件里透传却从不聚合；Turn/ToolCall 计数无累积点；事件序号与身份沿方法签名四处乱传。

本方案把这些纳入一个 `RunScope`：**一次 Run 调用内专有、逐步推进、只被该次 Run 独占变更的运行状态承载对象**。存活期 = 一次 `Run()` 从创建到返回；它在并发的意义上属于"每个调用一个实例"的局部状态（沿承 ToolLoop §7.3），而非全局/共享/池化账本。

## 2. 目标 / 非目标

### 2.1 目标
- 提供独立 `RunScope`，把 Run 的身份（RunMeta)、统计（RunStats）与事件序号集中化，替代散落的 `runID`/`seq`。
- 提供一套 `RecordXxx()` 方法，使策略在相应环节推进统计；结束时可导出 `RunStats` 快照。
- 供各类运行策略（ToolLoop、未来的 Planner/ReAct 等）共享同一生命周期状态语义。
- 缺省/默认计数器：turn、toolCall、usage（token）；Step 与 Retry 的口径交由策略/后续决定与调用。

### 2.2 非目标
- **不**把 history、snapshot、turn 变量等"业务中间推进"装进 Scope（那属于更高层 step 解释器，见 §5.3）。
- **不**引入 RunPhase 终态 state machine（这属于第二期，先做最小记账，已讨论一致）。
- **不**在 v1 嵌入/改造领域事件结构（`RunCompleted` 仍只有 Message；如需将 Stats 并入事件，另开兼容会议讨论）。
- **不**自动重试或为 RetryCount 引入不存在于当前循环的语义。
- 不改 `model.Executor` / `tool.Service` / `session.Store` 等组件能力接口。

## 3. 分层与文件放置

- `agent/run.go`（已存在）：纯数据契约 —— `RunEnv`/`RunRequest`/`RunResult`/`runRunner`。**保持纯数据**。
- **新增 `agent/runscope.go`**：`RunMeta`、`RunStats`、`RunScope` 及其推进方法。Scope 是"过程对象"，与纯契约文件分开，便于边界清晰、文档聚焦"如何推进一次 run"。
- `agent/eventsink.go` 事件信封 `Event` 结构**不**改。

依赖方向（不变）：`agent` → model/tool/session/message & 标准库。

## 4. 核心数据模型

### 4.1 身份面 RunMeta（一次 run 内不变）
```go
type RunMeta struct {
    RunID       string      // 一次 run 稳定标识（当前 newRunID 16B hex）
    ParentRunID string      // 嵌套/子运行父级 id；无父为空
    SessionID   string      // 通用会话标识字符串（不绑死在 session 包）
    CreatedAt   time.Time   // 生命周期锚
}
```
取舍（§7.1）：`SessionID` 用**字符串**而非 `session.Key{Scope,ID}`，使 Run 契约保持通用、不被 session 具体化约束（ToolLoop 可在构造 Request 时自行把 `session.Key` 加工成字符串填入）。若后续确需强类型关联 Store，可加可选字段或由 store 层承载，不在一开始扩大 Scope 依赖。

### 4.2 统计面 RunStats（快照语义）
```go
type RunStats struct {
    TurnCount     int   // 一次循环
    StepCount     int   // 循环内由运行策略自定义粒度的步骤
    ToolCallCount int   // 已触发工具调用数
    InputTokens   int   // 来自 model.Usage 累加
    OutputTokens  int
    TotalTokens   int
    RetryCount    int   // v1 保留字段、保持 0；重试策略出现后由策略推进（见 §7.3）
}
```
RetryCount 取舍：仓库原则"不为不确定未来占位"倾向不走空字段；**但因用户模型列出 RetryCount 且属于 run 统计的自然成员，予以保留且 v1 不调、不写死语义** —— 见 §7.4 风险。此为面向可观测计数的一个刻意例外，在风格上对齐"RunStats 是一次 run 统计账本的既定点位"。

### 4.3 Scope 对象与方法
```go
// RunScope 是一次 Run 调用内私有、逐步推进的状态记账对象。
// 由某一次 Run 开头创建；结束导出一份 RunStats 快照后丢弃。
type RunScope struct {
    mu    sync.Mutex // 单 Run 内本已顺序；加锁为 R 未来 step 受控并行提前确立正确边界
    meta  RunMeta
    seq   uint64
    stats RunStats
}

func NewRunScope(meta RunMeta) *RunScope       // Run 开头一次
func (s *RunScope) Meta() RunMeta                // 只读 x 拷贝
func (s *RunScope) NextSequence() uint64         // 事件序号：从 1 开始单调
func (s *RunScope) Stats() RunStats              // 返回数值快照（供记录/日志）
func (s *RunScope) RecordTurn()                  // 一次循环完成
func (s *RunScope) RecordStep()                  // 一个策略自定义步骤完成
func (s *RunScope) RecordToolCall()              // 一次工具调用被触发
func (s *RunScope) RecordUsage(u model.Usage)    // 累加 input/output/total tokens
func (s *RunScope) RecordRetry()                 // 预留：重试策略就绪后推进
```
并发正确性（§7.2）：`mu sync.Mutex` 保护，允许（v1 不强制并行而正确的）未来 Step/工具受控并行。单 Run顺序版本无竞争亦无开销问题。

### 4.4 RunPublish 收口
在 `runscope.go` 或在 toolloop 内部 helper 归一。倾向在 helper 提供与既有一致的发布收口，但签名改用 Scope 取代 `runID,seq`：

```go
// publish 保留"best-effort 事件"语义（nil sink 或错误不阻塞 run），
// 但序号改从 scope 统一分配，不再靠外部传 *seq。
func (a *ToolLoopAgent) publish(ctx context.Context, scope *RunScope, eventType EventType, data any) {
    if a.sink == nil { return }
    seq := scope.NextSequence()
    _ = a.sink.Publish(ctx, Event{RunID: scope.Meta().RunID, Sequence: seq,
        OccurredAt: time.Now(), Type: eventType, Data: data})
}
```
fail 同理：
```go
func (a *ToolLoopAgent) fail(ctx context.Context, scope *RunScope, kind string, err error) (RunResult, error)
```

## 5. ToolLoop `Run` 集成

### 5.1 替换散落的身份与序号
- `runID string` 与 `var seq uint64` 两串游离态，改为在 `Run` 开头建 scope、后续所有 `publish`/`fail`/`observe` 都携 `scope`。
- 终局：方法签名不再出现 `runID string` / `seq *uint64` 两个并行形参，统一一个 `scope *RunScope`。

### 5.2 落点埋点（就地能真实跑、可测）
- 一次 turn：`for turn := 1; ...` 每轮进入/结束时 `scope.RecordTurn()`。**口径：每轮模型 Generate = 1 Turn**。
- Generate 后 token 聚合：在 `ModelGenerationFinished` 发布旁（拿到 `response.Usage`）执行 `scope.RecordUsage(response.Usage)`。
- Tool call：每次执行 `a.tools.Execute(...)` 处（ToolCallRequested 之后、Execute 调用前/成功收口）`scope.RecordToolCall()`。
- StepCount：ToolLoop 第一例口径已由用户拍板（§9.1）：每轮 Generate 或每次 Tool call 均计 1 Step。ToolLoop 依此在 Generate 与顺次 tool 执行处 `RecordStep`，无歧义。
- RetryCount：v1 不埋点(0)，仅在策略引入重试后推进（此字段为 RunStats 既定定点成员的刻意例外，见 §7.3）。
- RetryCount：v1 不埋点（0）。

### 5.3 不做：中间业务态不进 Scope
`history`、`snapshot`、`systemText` 维持 `Run` 内的局部变量，由每轮循环局部推进（§7.3 并发安全的根基），Scope 只负责身份 + 统计 + 序号，不承担解释器。

## 6. 接口契约与行为
- `NewRunScope(meta)`: 生成 scope, `seq=0`(随即 NextSequence 自 1)、stats 零值。
- `RecordXxx`：内部加锁后自增对应计数; usage 累加; all零同步方式安全。
- `NextSequence()`：供 publish 取唯一递增序号。
- `Stats()/Meta()` 均返回 de-referenced 值拷贝(数值即可,避免引用可变外泄)。

## 7. 关键取舍与风险

### 7.1 SessionID 字符串化
取舍通过 `session.Key` 与通用 run 契约解耦。ToolLoop 的 Request.SessionKey 若要进 meta，由调用方拼(如 `scope.Key()."scope/id"`)或仅在 ToolLoop 内把 key 转 string 填给 RunMeta。契约保持一致短（通用），实现层自便。

### 7.2 一把 mu 与"单个 run 一个实例"共存
单 run 顺序执行本是单 writer；加锁成本为零，且给未来 step/工具并行以确定正确语义。但 mu 属**进程内轻量**，绝不从 scope 掏出给别人。snapshot 出去的是值。

### 7.3 Retry 字段占位带来的语义悬空
- 当前无自动重试；为不影响 RunStats 定点成员而保留 0。若标准坚持"非未来空白字段"，可在实现期从 RunStats 摘除，待引入重试策略再加（文档留待 §9 拍板）。

### 7.4 事件与返回值
- 不破坏既有 `Event`/`RunCompleted`(仅 Message)。stats 需外传时优先采取扩展 `RunResult` 加可选 `Stats RunStats`（Go 加字段兼容）,不在 v1 触发事件结构变化。若你定在 RunResult 上带字段,调用方按需取用,兼容性友好。

## 8. 测试策略
- `runscope_test.go`（package agent_test 看外部接口，或同包测私有递增）:
  - New → seq 从 1；Meta 透传；Stats 零快照；
  - 依次 RecordTurn/ToolCall/Usage/Retry 各多次 → Stats 的累加正确；
  - RecordUsage 的 input/output/total 合并；
  - 并发：多 goroutine 同时 RecordXxx → `-race` 无竞争（在含 gcc 环境跑）；
  - snapshot 的值不被后续变更污染（记录两次之间的 Snapshot 隔离）。
- toolloop 既有测试补少量断言：若 `RunResult` 扩展 Stats 后可校验 turn/tool 与脚本对得上（若你选择在 RunResult 带 stats）。

## 9. 已确认决策（用户拍板）

1. **StepCount 口径**：每轮模型调用（Generate）或每次工具调用 = 一次 Step。即：单轮无工具“1 Turn / 1 Step”；一轮含 N 个顺次工具调用 → 该轮 1 Turn，步骤数 = 1(Generate) + N(Tool)，N 个工具同时计入 `ToolCallCount`。`Turn ≠ Step`：Turn 是循环次数，Step 是步骤明细数。
2. **RunResult 带 Stats**：`RunResult` 新增 `Stats RunStats` 字段（值），供调用方观测、测试断言 end-of-run 统计。兼容：纯新增字段，不删/改名。
3. **SessionID 用 string**（通用 run 契约，不绑 session 包）。
4. **ParentRunID 保字段、ToolLoop v1 置空**（子运行由上层策略填充）。
5. 测试：外部 `package agent_test` 看接口为主 + 必要的同包白盒测量。

## 10. 实现安排

1. 新增 `agent/runscope.go`：`RunMeta`/`RunStats`/`RunScope` 及方法；`RunResult` 增加 `Stats` 字段。
2. 改 `agent/toolloop.go`：以 `scope` 收口 `publish/fail/observe`（取代 runID/&seq 两个散落形参）；按 §9.1 口径在 Generate / Tool 执行处 `RecordStep`；每轮 `RecordTurn`；Generate 后 `RecordUsage(response.Usage)`；tool 执行 `RecordToolCall`；结束赋 `RunResult.Stats`。
3. 补测试 `runscope_test.go` + ToolLoop 的 turn/tool/stats 断言。
4. 验证 gofmt / go test / go vet / (含 gcc 环境) -race。

确认后我实现：新增 `runscope.go`、改 `toolloop.go` 用 scope 收口替代 runID/seq、按所选口径加埋点、补测试与文档。
