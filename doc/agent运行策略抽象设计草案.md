# Agent 运行策略抽象设计（方案 1 落地稿）

状态：**已被 [AgentRuntime与运行策略架构重构设计方案.md](AgentRuntime与运行策略架构重构设计方案.md) 取代**。以下内容仅保留为历史决策记录。相关代码：`agent/run.go`（新增公共契约层）、`agent/toolloop.go`（构造收敛）、`agent/run_test.go`（新增）。实现过程中发现并记录一项 Go 关键约束，导致"内嵌 RunEnv 到 ToolLoopOptions"不可行的修正（见 §5.4）。

## 1. 背景与问题

`agent-go` 当前以组件化、可组合为核心。`ToolLoop` 只是众多"运行策略"（ReAct、Planner-Act、Reflection、Single-Turn 等）中的第一类。用户预期未来会有很多运行策略实现，因此希望**先在更上层抽象出各策略共享的公共契约与入口**，避免为每个策略重复建模。

当前事实：

- 各组件的能力接口**已稳定且可复用**：`model.Executor`、`tool.Service`、`prompt.Renderer`、`session.Store`、`agent.EventSink`，以及 `message`/`event.go` 的数据契约。这是所有策略共享的公共能力集。
- 第一个具体策略 `ToolLoopAgent` 已经是多个组件的事实组合器，代表了一段既定运行契约：
  - 构造期注入能力 + 策略参数（`ToolLoopOptions`），构造结果不可变、可并发；
  - 运行期按调用传入会话上下文（`RunRequest{SessionKey, PromptValues, Input}`）；
  - 返回 `RunResult{Message, Revision}` 与领域事件。
- 既有结论（见 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) §11.1、[AgentToolLoop编排设计方案.md](AgentToolLoop编排设计方案.md) §5.2 / §27 / §11）已确认：`ToolLoopAgent` **不**满足现有最小 `agent.Agent` 接口（该接口接收单个 `message.Message`），且**已明确不修改该接口**。原因：`ToolLoopAgent` 需并发服务多个会话/租户，`SessionKey` 必须按调用传入而非绑死在构造参数。

一句话难点："未来需要很多运行策略"是**推测性需求**；但"各策略要共享统一契约、能被业务代码统一调用"是**跨策略稳定、可预判**的结构性需求。二者要加以区分，才能避免过度抽象（遵循 AGENTS.md §4.7 / CLAUDE.md "抽象必须由真实变化驱动"）。

## 2. 目标 / 非目标 / 约束 / 成功标准

### 2.1 目标

- 抽象并把 `RunRequest`/`RunResult` 上提为**所有运行策略共享的运行契约**（公共层）。
- 把 5 个能力字段正视化为公共的 `RunEnv`（能力承载层），作为各策略的统一能力契约与规范字段集（单一字段事实来源），供未来多策略复用、避免各策略 Options 的能力字段形态自说自话。ToolLoop 的 `ToolLoopOptions` 平铺字段与其一一对应（§5.4 说明取舍）。
- 为"多策略被业务代码统一调用/替换"提供一条**最低成本的路径**：遵循"接口由使用方按需定义"，先用**使用方局部接口**承载统一入口，等出现第 2 个真实策略再上提为包内正式接口。
- 唯一确定要满足 `model.Executor` 不可缺省的构造期校验，抽为可复用的构造校验辅助。

### 2.2 非目标

- **不**导出全局性的"运行策略接口"（`Runner`/`RunStrategy`），也不抽象 `Step`/骨架解释器（见 §4 方案取舍）。
- **不**修改现有最小 `agent.Agent` 接口。
- **不**做 per-run 的能力注入（见 §7.2 能力绑定时机）。
- 不做上下文裁剪、并行 ToolCall、多 Agent 路由、Reflection/Judge 模型——这些各自是独立后续方案（沿用 ToolLoop 方案 §2.2/§8 既定结论）。
- 本草案不修改各组件的公开能力接口。

### 2.3 约束

- 能力在**构造期**绑定，`Run` 只接收会话上下文，保持 `ToolLoopAgent` 的不可变 + 并发安全语义（TToolLoop 方案 §7.3）。
- 不破坏现有公开调用写法：`ToolLoopOptions` 现有字段在 composite literal 里的平铺用法必须继续可用（测试中大量 `ToolLoopOptions{Executor:…, Store:…}`）。
- 文档层面同步修订既有 ToolLoop 方案，避免两处契约描述不一致。

### 2.4 成功标准

- `RunRequest`/`RunResult` 能被 ToolLoop 及未来策略直接引用，已在公共契约文件 `agent/run.go` 中定义。
- `RunEnv` 作为公共能力承载导出；`NewToolLoopAgent` 在构造期把 `ToolLoopOptions` 能力字段组装为 `RunEnv` 并经 `validateRunEnv` 校验（“内嵌 RunEnv”的取舍修正见 §5.4）。**所有既有测试无需改动照常通过**。
- 业务代码不依赖 `agent` 包内 L3 接口即可“统一运行多策略”（通过局部接口断言）。
- `gofmt`、`go test ./...`、`go vet ./...` 全绿（`-race` 受当前环境无 gcc/CGO 限制，见 §5.5）。

## 3. 总体架构与分层

运行策略抽象的语义应区分三层，不要混为一谈：

| 层 | 命名（草案） | 表达 | 现状 | 本草案动作 |
|---|---|---|---|---|
| L1 能力承载 | `RunEnv` | 一次 run 会用到哪些能力/依赖（Executor / Tools / Renderer / Store / Sink） | 散落在 `ToolLoopOptions` | **提出为公共结构体** |
| L2 运行契约 | `RunRequest` + `RunResult` | 跑一轮需要什么、返回什么（会话 key / prompt 变量 / 最终消息 / 会话乐观锁） | 定义在 toolloop.go，但语义是公共的 | **上提到公共文件** |
| L3 统一入口 | `Runner`（暂不导出） | 让不同策略被同一段代码调用、替换 | 无；`Agent` 接口太小且已定不改 | **由使用方局部定义**，暂不 in agent |

依赖方向（沿用 ToolLoop 方案 §3 既定结论，不变）：

```text
agent ──> model
agent ──> tool
agent ──> prompt
agent ──> session
agent ──> message
```

文件位置：新增 `agent/run.go`，与 `agent.go`（接口）、`event.go`/`eventsink.go`（事件）、`toolloop.go`（ToolLoop 实现）**同包**。这与仓库惯例一致（prompt 包 `Renderer` 与 `Static`/`Template` 同包、tool 包 `Service` 与 `ToolRuntime` 同包、session 包 `Store` 与 `MemoryStore` 同包）。

## 4. 候选方案对比与取舍

本轮讨论出现四种思路，取舍如下：

- **方案 1（推荐）**：上提 L1+L2 为公共契约，L3 在用方局部定义，**现在不导出统一接口**。见 §5。
- 方案 2（更保守）：不新增文件/接口，只文档化 + 可选字段上提。缺点：多策略 Options 会各自平铺一遍能力字段（重复），抽象并未真正"放在上层"。适用于判断短期只有 1~2 个策略的分支。
- 方案 3（激进）：抽 `Step`/骨架 + 解释器，把 ToolLoop/ReAct/Planner 全表达为可组合步骤。**本草案明确不建议现在做**：现有工具/事件/prompt/session/ToolLoop 方案的演进路线都是"以具体策略逐步抽象 + 真实需求驱动"；当前可运行策略只有 1 个，立刻抽 Steps 骨架属于为还不存在的多个策略预埋骨架，属过度抽象。记录为演进目标，等真实策略 ≥2 且出现共性时再评估。
- 方案 4（伪需求）：仅为了"看起来抽象"把不同输入（Planner/Executor 分开、Judge 模型等）压平成统一 Options。**不建议**：会破坏专一性。

本草案采用**方案 1**，其核心是：**现在固化共享契约（L1+L2），把统一接口（L3）的导出门槛设为"第 2 个真实策略出现且确有统一调度需求"**，为未来扩展预留低成本通道而非空接口。理由是"接口由使用方按需、小而专一"（AGENTS.md §4.3）；单一实现的接口不导出可避免 `interface` anti-pattern。

## 5. 具体设计（方案 1）

### 5.1 L1：公共能力承载 `RunEnv`

新增于 `agent/run.go`：

```go
package agent

// RunEnv is the shared capability set held by every run strategy. Concrete
// strategies (e.g. ToolLoopAgent) read it at construction time and keep an
// immutable copy; sharing RunEnv across strategies removes duplicated
// capability fields from each options struct. Capabilities are bound at
// construction time, never overridden per Run call.
type RunEnv struct {
    Executor  model.Executor   // required
    Tools     tool.Service     // optional; nil means no tool capability
    Renderer  prompt.Renderer  // optional; nil means no system prompt
    Store     session.Store    // optional; nil means stateless single-turn
    EventSink EventSink        // optional; nil means no events
}
```

配套一个构造期校验辅助，供所有 `NewXxx` 复用：

```go
// validateRunEnv validates a RunEnv; only Executor is required.
// It is shared by every strategy constructor.
func validateRunEnv(env RunEnv) error {
    if env.Executor == nil {
        return ErrNilExecutor
    }
    return nil
}
```

`ErrNilExecutor` 保留现有定义与文案，避免破坏错误契约。

### 5.2 L2：公共运行契约 `RunRequest` / `RunResult`

从 toolloop.go **上提语义**到 `agent/run.go`，作为公共契约（定义体不变，用途注释改为"所有运行策略共享"）：

```go
// RunRequest is the input contract for one run executed by any run strategy:
// it identifies the owning session, carries render variables, and the input.
// SessionKey is required when a Store is configured; it is ignored when
// Store is nil (stateless single-turn).
type RunRequest struct {
    SessionKey   session.Key
    PromptValues prompt.Values
    Input        message.Message
}

// RunResult is the output contract of one completed run.
type RunResult struct {
    Message  message.Message   // final reply
    Revision session.Revision  // zero when no Store is configured
}
```

上提不改变任何调用写法（字段/类型名/注释语义不变），仅使 ToolLoop 之外的策略可直接引用公共契约。

### 5.3 L3：统一入口接口 —— 由使用方定义，暂不导出

`agent` 包 **不导出** `Runner`/`RunStrategy`。业务代码在**自己包内**按需定义局部最小接口并对各策略做编译期断言即可统一运行：

```go
// 使用方包内
type RunStrategy interface {
    Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error)
}

var _ RunStrategy = (*agent.ToolLoopAgent)(nil) // 将来 *agent.XxxAgent 也在消费方加一行即入策略族
```

为将来"第 2 个策略出现后将 L3 上提"保留内部自检（不改导出面）：

```go
// agent/run.go 内
var _ interface{ Run(context.Context, RunRequest) (RunResult, error) } = (*ToolLoopAgent)(nil)
```

这保证 agent 包内部已知 ToolLoop 的方法集与未来可能要上提的 L3 形状一致，但对外仍不承诺一个空契约接口。

### 5.4 ToolLoop 的改造（含一处关键约束修正）

目标：能力字段归一到 `RunEnv` 的同时，保持现有平铺 literal 调用源兼容。

**实现时发现并接受的关键约束**：Go 不允许在 struct literal 中把**嵌入（promoted）字段**作为 key 使用（`ToolLoopOptions{RunEnv; Executor:…}` 中 `Executor` 若来自内嵌 `RunEnv` 会报 `unknown field`，经最小用例实测确认）。因此若把 `RunEnv` **内嵌**进 `ToolLoopOptions`，所有既有平铺调用点（`ToolLoopOptions{Executor: e, Store: s, …}`，可在 repo 测试中大量见到）都会失效，破坏承诺的“零迁移”。

据此采用修正后的落地形态——`ToolLoopOptions` 保持**显式平铺能力字段**（与 `RunEnv` 字段集一一对应），由 `NewToolLoopAgent` 在构造期组装为 `RunEnv` 并统一校验：

```go
// toolloop.go：能力字段显式平铺，镜像 RunEnv 的规范字段集
//（注释需同步说明平铺字段内嵌会因 Go composite-literal 规则破坏源兼容）
type ToolLoopOptions struct {
    Executor            model.Executor
    Tools               tool.Service
    Renderer            prompt.Renderer
    Store               session.Store
    EventSink           EventSink
    MaxTurns            int  // <=0 => default 8
    MaxToolCallsPerTurn int  // <=0 => default 8
    StopOnToolError     bool // default false
}
```

```go
// NewToolLoopAgent 内
env := RunEnv{Executor: options.Executor, Tools: options.Tools,
    Renderer: options.Renderer, Store: options.Store, EventSink: options.EventSink}
if err := validateRunEnv(env); err != nil { return nil, err }
// … 读取 env.* 与 options.MaxTurns 等填入不可变 ToolLoopAgent
```

`ToolLoopAgent` struct 内部保持既有 7 个私有字段（改动局部，不夹带重构）。`RunRequest`/`RunResult` 因已上提，`Run` 签名引用公共类型即可；行内其它逻辑（fail/publish/observe/commit/轮限制/工具失败策略）一律不动。

**代价与后续**：`ToolLoopOptions` 目前与 `RunEnv` 存在字段平行（轻度重复）。这是为了保住“零迁移”而接受的取舍；当第 2 个策略出现、确有统一 Options 去重需求时，再评估把各策略 Options 改为内嵌 `RunEnv` 并在落地时一次性迁移调用点（公开破坏性变更，需在发布说明标注）。

### 5.5 测试

- 全部既有 toolloop 测试**零改动**继续通过（能力字段保持显式平铺）。
- 新增 `agent/run_test.go`：覆盖 `validateRunEnv`（nil Executor → `ErrNilExecutor`；非 nil 通过），并断言 `runRunner` 内部契约由 `*ToolLoopAgent` 满足（避免将来 L3 上提时方法集漂移）。
- 其它 L2 行为（SessionKey 必填语义 / Session 冲突）已由既有 ToolLoop 测试覆盖，run.go 不重复引入校验。
- 在含 gcc/CGO 的环境中执行 `go test -race ./...`（并发/取消/超限场景）；当前环境无 gcc，`-race` 无法运行，属工具链限制而非本改动问题。

## 6. 演进门槛（何时把 L3 正式导出）

不为单一实现导出统一接口。当以下**两个条件同时成立**时，才把 L3 在 `agent` 包内正式成文并调整各实现：

1. 出现 ≥2 个真实运行策略实现（如 Planner-Act / ReAct）；
2. 确有在策略间统一调度/替换的调用方需求。

届时仅需：在 run.go export `type Runner interface { Run(context.Context, RunRequest) (RunResult, error) }`，`ToolLoopAgent` 等实现因方法集已与之一致**无需功能改动**，仅加 `var _ Runner = (*ToolLoopAgent)(nil)` 断言与文档。此路径成本极低、无破坏性变更，是采用 client-side interface 的最大收益。

任何情况下，上下文裁剪、并行 ToolCall、多 Agent 路由都不并入本契约，仍为独立后续方案（沿用 ToolLoop 方案 §2.2 / §8）。

## 7. 关键设计取舍与风险

### 7.1 能力（L1）与运行（L2）分离

把"用什么能力"（RunEnv，构造期固定）与"跑哪个会话"（RunRequest，运行期传入）分开建模，是并发安全与可复用的根基：一次实例可并发服务任意会话。这正是 ToolLoopAgent 既定设计在抽象层的体现（ToolLoop 方案 §7.3）。

### 7.2 能力构造期绑定，不做 per-run override

`Run` 只接收会话上下文，不允许每次调用临时换 Executor/Tools/Store。原因：per-run override 会破坏实例不可变性、引入并发语义分歧、削弱领域事件的可观性与审计一致性。若未来确有"每次请求按租户选能力"需求，应作为独立设计（如运行时工厂注入 `RunEnv`）另行讨论，不在本契约开口子。

### 7.3 不现在导出一致性接口 vs 保守/激进取舍

- 导出 vs 不导出：单一实现时导出一个永远满足的空接口属反模式，违背"接口由真实驱动"原则；用方法集相同的内部断言 + 使用方局部接口即可达成统一调用的当下目标，把导出门槛明确设给第 2 个策略。
- 净收益：现在获得稳定公共契约（多策略不再各写一遍能力字段），又不必为未到的多样性预埋接口或步骤骨架。
- 风险：若对"未来多策略"判断过慢达成一致，L3 导出会略滞后于第 2 个策略出现；缓解 = §6 的低成本渐变路径，滞后代价可忽略。

### 7.4 风险点（沿用既有未决项）

- ToolLoop 本身尚存在无上下文裁剪风险，由独立组件兜底，与本草案正交。
- 事件、Session、工具失败策略等均已稳定为既有契约，本草案只做上提/收敛，不重写其语义。

## 8. 兼容性与迁移

- `ToolLoopOptions` 能力字段保持**显式平铺**（未内嵌 `RunEnv`），因此对既有平铺 literal 调用**零迁移**。净新增仅 `RunEnv`/`validateRunEnv`（新导出符号），不占既有名字。
- `RunRequest`/`RunResult` 仅“定义位置”从 toolloop.go 移到 run.go，类型名与字段不变，无源码级迁移。
- 不删除/改名任何现有导出符号；不修改最小 `agent.Agent`。
- 验证命令：

```bash
gofmt -w .
go test ./...
go vet ./...
# 在含 gcc/CGO 环境执行：
go test -race ./...
```

## 9. 决策落定（本轮确认/已执行）

1. **时机观**：采纳——上提 L1 + L2，暂不导出 L3；L3 用使用方局部接口 + `agent` 内部 `runRunner` 自检断言承载，待第 2 个策略出现后再正式导出（见 §6）。
2. `ToolLoopAgent` struct 内部：保留既有的平铺私有字段，不改成持 `env RunEnv`（改动局部、不夹带重构）。
3. 能力**构造期绑定**，不含 per-run override（已落定并固化在 §7.2）。
4. 本设计方案文档转正为“方案 1 落地稿”；由于 `ToolLoopOptions` 公开字段与调用形态未变（仅类型定义位置迁往 run.go，外部不可见），`AgentToolLoop编排设计方案.md` 无需强制修订，后续可在顺手整理时同步指向 run.go 以便查阅。
5. **新增重要技术修正**：实施中发现 Go 不允许内嵌(promoted)字段作为 struct literal key（实测确认），故不用“内嵌 RunEnv 到 ToolLoopOptions”的方案（会破坏全部既有平铺调用点）；改用 ToolLoopOptions 显式平铺 + 构造期组装 `RunEnv`（§5.4），保住零迁移，代价是 Options 与 RunEnv 字段轻度平行，已在本文件注释为后续可选项。

## 10. 实施结果（已执行完成）

0. ToolLoop 已实现，方案纯为上提/收敛，不依赖新 provider 行为——满足。
1. 新增 `agent/run.go`：`RunEnv`、`validateRunEnv`、上提的 `RunRequest`/`RunResult`、`runRunner` 内部自检断言（`var _ runRunner = (*ToolLoopAgent)(nil)`）。
2. 修改 `agent/toolloop.go`：`RunRequest`/`RunResult` 定义迁往 run.go（同包引用即可）；`ToolLoopOptions` 保持显式平铺能力字段；`NewToolLoopAgent` 组装 `env` = 一份 `RunEnv` 并经 `validateRunEnv` 校验，再以其填充不可变字段；`Run` 签名引用公共契约类型。
3. 新增 `agent/run_test.go`：验证 `validateRunEnv`（nil/非 nil Executor）、并断言 `*ToolLoopAgent` 满足 `runRunner`。
4. 文档落定：本文件已在 §9.4 注明——由于 `ToolLoopOptions` 公开调用形态未变，既有 ToolLoop 设计文档无需强制修订；后续整理时再同步指向 run.go 便于查阅。
5. 验证：`gofmt` / `go test ./...` / `go vet ./...` 全绿；`-race` 因环境无 gcc/CGO 无法执行（非代码问题），应在含 toolchain 环境补跑。
