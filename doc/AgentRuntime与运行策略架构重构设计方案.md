# AgentRuntime 与运行策略架构重构设计方案

状态：**已确认并已实现**。核心代码位于 `agent/runtime.go`、`agent/runtime_builder.go`、`agent/strategy_factory.go`、`scope/`、`event/` 与 `strategy/toolloop/`。

## 1. 背景

当前运行编排主要由 `agent.ToolLoopAgent` 承担。它同时持有模型、工具、Prompt、Session、事件、上下文裁剪等能力，并直接实现完整运行流程。这种结构可以满足单一 ToolLoop 场景，但当运行策略增加后，会产生以下问题：

- 每种策略重复定义和持有相同的运行能力；
- 统一入口与具体编排实现耦合；
- 策略选择、运行状态和基础能力缺少清晰边界；
- 使用方难以注册自定义策略并通过同一个 Runtime 调用；
- `RunScope` 和 `RunEnv` 位于 `agent` 包中，难以作为独立、稳定的策略执行上下文复用。

本次重构不兼容现有 `agent.Agent` 和 `agent.ToolLoopAgent`，以新的 AgentRuntime、独立 Scope 包和策略工厂建立统一运行架构。

## 2. 已确认需求

1. 新增统一运行入口 `AgentRuntime`。
2. `AgentRuntime` 注册模型执行层、工具系统、Session、Prompt、EventSink、Trimmer 和策略工厂。
3. `Scope` 迁移到独立的 `scope` 包。
4. `Env` 同时迁入 `scope` 包，并由 `Scope` 持有。
5. `Scope` 必须通过 Builder 构建；Builder 是公开扩展 API。
6. Request 和 Result 是 `AgentRuntime.Run` 的输入、输出契约。
7. 策略工厂根据 Request 参数自行判断应使用的策略。
8. 策略工厂注册的是可复用的策略实例，不是策略构造函数。
9. Runtime 和策略工厂构建完成后冻结，不允许继续注册。
10. 不兼容现有 `agent.Agent` 和 `ToolLoopAgent`，允许破坏性重构。

## 3. 目标、非目标与成功标准

### 3.1 目标

- 为所有运行策略提供稳定、统一的执行入口。
- 将共享运行能力与单次运行状态统一收口到 Scope。
- 支持使用方注册自定义策略工厂和策略实例。
- 保证构建完成后的 Runtime 可被多个 goroutine 并发调用。
- 将 ToolLoop 从独立 Agent 重构为普通 Strategy 实现。
- 保持模块职责单一，避免 `agent`、`scope` 和具体策略之间循环依赖。

### 3.2 非目标

- 不兼容旧 `agent.Agent.Run(context.Context, message.Message)`。
- 不兼容旧 `ToolLoopAgent` 构造和调用 API。
- 不在本次重构中实现 Planner、Reflection、Multi-Agent 等新策略。
- 不引入通用 Step 解释器或复杂策略 DSL。
- 不允许运行期间动态注册、删除或替换策略。
- 不支持单次 Run 临时替换 Runtime 中的模型、工具等能力。

### 3.3 成功标准

- 使用方可以构建一个不可变 `AgentRuntime` 并调用统一 `Run`。
- 每次 `Run` 创建独立 Scope，并使用 Runtime 的不可变 Env。
- 默认策略工厂可以注册多个策略实例，并根据 Request 选择一个实例。
- 未匹配策略、重复注册、非法 Scope、冻结后注册等情况返回明确错误。
- 策略实例可安全地被多个 Run 并发复用。
- ToolLoop 行为迁移为 Strategy 后，原有正常、错误、取消、事件和统计语义有对应测试覆盖。
- `gofmt`、`go test ./...`、`go vet ./...` 和 `go test -race ./...` 通过。

## 4. 总体架构

```text
使用方
  │
  ├── 构建 Env 所需组件
  ├── 向 StrategyFactory 注册 Strategy 实例
  └── 通过 RuntimeBuilder 构建 AgentRuntime
                │
                ▼
       不可变 AgentRuntime
         ├── scope.Env
         └── StrategyFactory
                │
                ▼
AgentRuntime.Run(ctx, Request)
  ├── 校验 Context 和 Request
  ├── 使用 scope.Builder 创建本次 Scope
  ├── StrategyFactory.Select(ctx, Request)
  ├── Strategy.Run(ctx, Scope)
  └── 返回 Result
```

核心职责：

| 组件 | 职责 |
|---|---|
| `AgentRuntime` | 统一入口、构建 Scope、选择并调用策略 |
| `RuntimeBuilder` | 注册运行能力和策略工厂，校验后构建不可变 Runtime |
| `scope.Env` | 保存所有策略共享的运行能力 |
| `scope.Scope` | 保存一次运行的 Env、输入快照、身份、统计和事件序号 |
| `scope.Builder` | 构建合法且完整的 Scope |
| `StrategyFactory` | 注册策略实例、冻结注册表、根据 Request 选择策略 |
| `Strategy` | 实现具体运行编排 |

## 5. 建议包结构

```text
agent/
  runtime.go             # AgentRuntime
  runtime_builder.go     # RuntimeBuilder
  request.go             # Request / Result
  strategy.go            # Strategy / StrategyFactory 公共契约
  strategy_factory.go    # 默认工厂实现

event/
  event.go               # Event、EventType 和事件数据
  sink.go                # EventSink、FanoutSink

scope/
  env.go                 # Env
  scope.go               # Scope、Meta、Stats
  builder.go             # 公开 Scope Builder

strategy/
  toolloop/
    strategy.go          # ToolLoop Strategy
    options.go           # ToolLoop 策略参数
```

`strategy/toolloop` 是具体实现包，它可以依赖 `agent` 和 `scope`；`agent` 不得反向导入该包。使用方通过注册完成装配。

推荐依赖方向：

```text
agent ───────────────> scope
agent ───────────────> event
scope ───────────────> model/tool/prompt/session/trim/event
strategy/toolloop ───> agent/scope/event
```

## 6. EventSink 迁移

`scope.Env` 需要持有 EventSink，而当前 EventSink 和 Event 都定义在 `agent` 包中。如果 `scope` 继续引用 `agent.EventSink`，会形成：

```text
agent -> scope -> agent
```

因此建议将以下契约迁入独立 `event` 包：

- `EventType`；
- `Event`；
- 各类事件数据；
- `EventSink`；
- `FanoutSink` 及订阅契约。

Scope 只依赖 `event.Sink`，AgentRuntime 和具体策略也依赖 `event` 包，从而消除循环依赖。

## 7. Request 与 Result

Request 和 Result 定义在 `agent` 包，是 AgentRuntime 的公开运行契约：

```go
type Request struct {
    Strategy     string
    SessionKey   session.Key
    PromptValues prompt.Values
    Input        message.Message
}

type Result struct {
    Message  message.Message
    Revision session.Revision
    Stats    scope.Stats
}
```

### 7.1 Request 语义

- `Strategy` 是提供给策略工厂的路由参数；具体解释权属于工厂。
- 默认工厂将 `Strategy` 解释为已注册策略名称。
- `Strategy == ""` 时，默认工厂选择已配置的默认策略。
- 非空但没有匹配策略时返回 `ErrStrategyNotFound`，不得静默回退。
- `SessionKey` 在 Env 配置 Session Store 时必须有效；无 Session Store 时忽略。
- `PromptValues` 和 `Input` 会复制或标准化后写入 Scope，避免外部在运行期间修改可变数据。

### 7.2 Result 语义

- `Message` 是策略最终输出。
- `Revision` 在未配置 Session Store 时为零值。
- `Stats` 是运行结束时的 Scope 统计快照。
- 运行失败时默认返回零值 Result 和带上下文的错误。

## 8. Scope 包设计

### 8.1 Env

```go
package scope

type Env struct {
    Executor  model.Executor
    Tools     tool.Service
    Session   session.Store
    Prompt    prompt.Renderer
    EventSink event.Sink
    Trimmer   trim.Trimmer
}
```

约束：

- `Executor` 是必需能力；其余能力可选。
- Env 构建后只读，不支持 per-run override。
- Env 中的接口实现必须允许并发调用，或者在实现内部自行同步。
- Scope 返回 Env 时只能返回值拷贝，不暴露可替换内部 Env 的入口。

### 8.2 Scope 中的运行输入

`scope` 包不能依赖 `agent.Request`，否则形成循环依赖。Runtime 应把 Request 中策略执行所需的数据转换为 Scope 自己的中立输入结构：

```go
type Input struct {
    SessionKey   session.Key
    PromptValues prompt.Values
    Message      message.Message
}
```

`Request.Strategy` 只用于工厂路由，默认不进入 Scope。若具体策略确实需要读取该参数，可以由 Scope Builder 提供通用只读 Metadata，但不建议把完整 `agent.Request` 作为 `any` 放入 Scope。

### 8.3 Scope

```go
type Scope struct {
    env   Env
    input Input
    meta  Meta

    mu    sync.Mutex
    seq   uint64
    stats Stats
}
```

公开能力建议包括：

```go
func (s *Scope) Env() Env
func (s *Scope) Input() Input
func (s *Scope) Meta() Meta
func (s *Scope) Stats() Stats
func (s *Scope) NextSequence() uint64
func (s *Scope) RecordTurn()
func (s *Scope) RecordStep()
func (s *Scope) RecordToolCall()
func (s *Scope) RecordUsage(model.Usage)
func (s *Scope) RecordRetry()
```

Scope 必须遵守：

- 每次 Run 创建一个实例；
- 不跨 Run 复用；
- 不保存到 `context.Context`；
- 不由 Strategy 长期持有；
- Stats、Meta 和 Input 对外返回快照；
- 内部计数操作支持并发安全。

### 8.4 公开 Builder

```go
runScope, err := scope.NewBuilder().
    Env(env).
    Input(input).
    Meta(meta).
    Build()
```

建议 Builder API：

```go
type Builder struct { /* 私有字段 */ }

func NewBuilder() *Builder
func (b *Builder) Env(Env) *Builder
func (b *Builder) Input(Input) *Builder
func (b *Builder) Meta(Meta) *Builder
func (b *Builder) Build() (*Scope, error)
```

Builder 行为：

- 缺少 Executor 时返回明确错误；
- 未提供 RunID 时自动生成；
- 未提供 CreatedAt 时使用当前时间；
- 对 Message、PromptValues 等可变输入执行必要复制；
- 不接受 nil Builder 或重复 Build 后隐式共享可变数据；
- Build 返回的新 Scope 与 Builder 后续修改相互隔离。

为了让测试不依赖真实时间和随机数，Builder 内部应支持受控扩展，例如可选 `IDGenerator` 和 `Clock`。这些能力可以作为高级公开选项或包内注入点，具体形式在实施前确定。

## 9. 策略契约

```go
package agent

type Strategy interface {
    Run(context.Context, *scope.Scope) (Result, error)
}
```

策略实例是共享对象，必须满足以下契约：

- 注册后不可变；
- `Run` 可被多个 goroutine 并发调用；
- 不把 Context、Scope、history、turn 等单次状态保存到实例字段；
- 单次状态只能位于 Scope 或 `Run` 的局部变量中；
- 必须响应 Context 取消和超时；
- 不关闭由 Env 提供且不归策略所有的共享资源；
- 返回错误时使用 `%w` 保留错误链；
- 不在同一层重复记录并返回相同错误。

具体策略可以保存不可变配置，例如 ToolLoop 的：

```go
type Options struct {
    MaxTurns            int
    MaxToolCallsPerTurn int
    StopOnToolError     bool
    TrimBudget          int
}
```

工具、模型、Session 等共享能力不再放入策略 Options，统一从 `scope.Env` 获取。

## 10. StrategyFactory 设计

### 10.1 公共接口

由于工厂注册的是策略实例，其核心语义是注册和选择，而不是创建：

```go
type StrategyFactory interface {
    Register(name string, strategy Strategy) error
    Default(name string) error
    Freeze() error
    Select(context.Context, Request) (Strategy, error)
}
```

虽然名称保留为 `StrategyFactory`，方法使用 `Select` 而不是 `Create`，避免误导为“每次构造策略”。

### 10.2 默认实现

```go
type DefaultStrategyFactory struct {
    mu          sync.RWMutex
    strategies  map[string]Strategy
    defaultName string
    frozen      bool
}
```

注册规则：

- name 不能为空；
- strategy 不能为 nil；
- 同名注册返回 `ErrStrategyAlreadyRegistered`；
- 默认策略必须已经注册；
- 冻结后调用 Register 或 Default 返回 `ErrStrategyFactoryFrozen`；
- Freeze 应幂等；
- Select 不修改内部状态。

选择规则：

1. 如果 `Request.Strategy` 非空，按名称精确查找；
2. 如果为空，选择默认策略；
3. 没有默认策略或找不到指定策略时返回 `ErrStrategyNotFound`；
4. 不静默选择任意已注册策略。

自定义 StrategyFactory 可以使用租户、输入类型或其他 Request 字段实现不同选择逻辑，但必须满足冻结后不可变和并发安全契约。

### 10.3 冻结责任

`RuntimeBuilder.Build` 必须调用 `StrategyFactory.Freeze()`。只有 Freeze 成功后才返回 AgentRuntime。

这样 Runtime 的并发安全不依赖使用方记得手动冻结：

```go
factory.Register("tool-loop", toolLoop)
factory.Default("tool-loop")

runtime, err := agent.NewRuntimeBuilder().
    StrategyFactory(factory).
    Build() // 内部冻结 factory
```

Build 成功后，持有 factory 的其他代码也无法继续注册策略。

## 11. RuntimeBuilder 与 AgentRuntime

### 11.1 RuntimeBuilder

建议 API：

```go
runtime, err := agent.NewRuntimeBuilder().
    Executor(executor).
    Tools(tools).
    Session(store).
    Prompt(renderer).
    EventSink(sink).
    Trimmer(trimmer).
    StrategyFactory(factory).
    Build()
```

Builder 负责：

- 汇总 Env；
- 校验 Executor；
- 校验 StrategyFactory；
- 冻结 StrategyFactory；
- 创建不可变 AgentRuntime；
- 复制必要配置，避免与 Builder 共享可变状态。

### 11.2 AgentRuntime

```go
type AgentRuntime struct {
    env             scope.Env
    strategyFactory StrategyFactory
}

func (r *AgentRuntime) Run(
    ctx context.Context,
    request Request,
) (Result, error)
```

AgentRuntime 构建后不提供 Register、Set、Replace 等修改方法。

### 11.3 Run 流程

```go
func (r *AgentRuntime) Run(ctx context.Context, request Request) (Result, error) {
    if ctx == nil {
        return Result{}, ErrInvalidContext
    }
    if err := ctx.Err(); err != nil {
        return Result{}, err
    }
    if err := validateRequest(request, r.env); err != nil {
        return Result{}, err
    }

    runScope, err := scope.NewBuilder().
        Env(r.env).
        Input(scope.Input{
            SessionKey:   request.SessionKey,
            PromptValues: request.PromptValues,
            Message:      request.Input,
        }).
        Build()
    if err != nil {
        return Result{}, fmt.Errorf("build run scope: %w", err)
    }

    strategy, err := r.strategyFactory.Select(ctx, request)
    if err != nil {
        return Result{}, fmt.Errorf("select run strategy: %w", err)
    }
    if strategy == nil {
        return Result{}, ErrNilStrategy
    }

    result, err := strategy.Run(ctx, runScope)
    if err != nil {
        return Result{}, fmt.Errorf("run strategy: %w", err)
    }
    return result, nil
}
```

## 12. 生命周期与事件边界

建议 AgentRuntime 负责所有策略共有的生命周期边界：

- 创建 RunID 和 Scope；
- 发布 `run.started`；
- 策略选择失败时发布 `run.failed`；
- 策略返回成功时发布 `run.completed`；
- Context 取消时发布 `run.cancelled`；
- 保证一次 Run 最多发布一个终态事件。

Strategy 负责策略内部事件：

- 模型生成开始、增量和完成；
- 工具调用请求、执行开始和完成；
- 策略特有步骤事件。

需要在实施时将现有 ToolLoop 中的终态事件逻辑上移至 Runtime，避免 Runtime 与 Strategy 重复发布终态。

EventSink 继续采用 best-effort 还是传播错误，应保持现有约定：事件发布失败不影响主运行结果，可靠投递由具体 Sink 负责。

## 13. 并发与资源语义

### 13.1 并发安全

- AgentRuntime 构建后不可变，可并发 Run。
- StrategyFactory 冻结后只读，可并发 Select。
- Strategy 实例会被并发复用，必须并发安全。
- Scope 每次 Run 独立创建；内部计数允许受控并发更新。
- Env 中各组件由多个策略实例和 Run 共享，其实现必须满足对应并发契约。

### 13.2 资源所有权

- Runtime 不自动关闭外部传入的 Executor、Store、EventSink 等资源。
- 创建资源的调用方负责关闭。
- 如果后续需要 Runtime 托管资源，应单独设计 `Close` 和所有权规则，不能隐式关闭调用方资源。
- Strategy 不得关闭 Env 中的共享资源。

### 13.3 超时和取消

- `Run` 不自行设置固定超时；调用方通过 Context 控制。
- Runtime、Factory、Strategy、模型、工具、Session 和事件发布都应传递同一个 Context。
- Context 已取消时应尽快返回 `ctx.Err()`。

### 13.4 幂等性

- `Run` 本身不承诺幂等，因为模型生成、工具调用和 Session 写入可能产生副作用。
- Request 暂不引入幂等键。
- 若未来需要重试整个 Run，应单独定义幂等键、工具副作用和 Session 提交策略。

## 14. 错误契约

建议提供可使用 `errors.Is` 判断的哨兵错误：

```go
var (
    ErrInvalidContext            = errors.New("agent: invalid context")
    ErrInvalidRequest            = errors.New("agent: invalid request")
    ErrNilExecutor               = errors.New("scope: nil executor")
    ErrNilStrategyFactory        = errors.New("agent: nil strategy factory")
    ErrNilStrategy               = errors.New("agent: nil strategy")
    ErrInvalidStrategyName       = errors.New("agent: invalid strategy name")
    ErrStrategyAlreadyRegistered = errors.New("agent: strategy already registered")
    ErrStrategyNotFound          = errors.New("agent: strategy not found")
    ErrStrategyFactoryFrozen     = errors.New("agent: strategy factory is frozen")
)
```

错误归属应跟随包职责：Scope 构建错误定义在 `scope` 包；策略注册和选择错误定义在 `agent` 包。

## 15. ToolLoop 迁移

现有 `ToolLoopAgent` 重构为 `strategy/toolloop.Strategy`：

```go
type Strategy struct {
    maxTurns            int
    maxToolCallsPerTurn int
    stopOnToolError     bool
    trimBudget          int
}

func (s *Strategy) Run(
    ctx context.Context,
    runScope *scope.Scope,
) (agent.Result, error)
```

迁移规则：

- Executor、Tools、Renderer、Store、EventSink、Trimmer 从 `runScope.Env()` 获取；
- SessionKey、PromptValues、输入消息从 `runScope.Input()` 获取；
- history、snapshot、turn 等保持方法局部变量；
- 统计通过 Scope 的 Record 方法更新；
- 策略实例只保存不可变 ToolLoop 参数；
- Runtime 负责通用起止事件，ToolLoop 只发布模型和工具事件；
- 工具能力缺失但模型请求工具时，保持明确错误；
- Session 乐观锁、上下文裁剪和工具失败策略保持现有行为。

## 16. 使用示例

```go
factory := agent.NewDefaultStrategyFactory()

loop, err := toolloop.New(toolloop.Options{
    MaxTurns:            8,
    MaxToolCallsPerTurn: 8,
})
if err != nil {
    return err
}

if err := factory.Register("tool-loop", loop); err != nil {
    return err
}
if err := factory.Default("tool-loop"); err != nil {
    return err
}

runtime, err := agent.NewRuntimeBuilder().
    Executor(executor).
    Tools(tools).
    Session(store).
    Prompt(renderer).
    EventSink(sink).
    Trimmer(trimmer).
    StrategyFactory(factory).
    Build()
if err != nil {
    return err
}

result, err := runtime.Run(ctx, agent.Request{
    Strategy: "tool-loop",
    SessionKey: session.Key{
        Scope: "tenant-a",
        ID:    "conversation-1",
    },
    Input: message.Text(message.RoleUser, "查询天气"),
})
```

Build 成功后，以下调用必须失败：

```go
err = factory.Register("react", reactStrategy)
// errors.Is(err, agent.ErrStrategyFactoryFrozen) == true
```

## 17. 兼容与迁移策略

本次是明确的破坏性重构：

- 删除或替换旧 `agent.Agent`；
- 删除或替换 `ToolLoopAgent`；
- `RunEnv`、`RunScope`、`RunMeta`、`RunStats` 迁入 `scope` 包并重命名为更简洁的 `Env`、`Scope`、`Meta`、`Stats`；
- `EventSink` 和事件类型迁入 `event` 包；
- ToolLoop 调用方改为构建策略、注册工厂、构建 Runtime；
- 示例、README 和全部设计文档同步更新。

由于不保留旧 API，建议作为主版本升级发布，并在发布说明中提供旧 API 到新 API 的迁移示例。

## 18. 测试策略

### 18.1 Scope

- Builder 必填字段和默认值；
- Env、Input、Meta 的快照隔离；
- RunID 和 CreatedAt 生成；
- Stats 累加和快照；
- Sequence 从 1 开始且单调；
- 并发 Record 和 NextSequence 无竞争；
- nil、非法 SessionKey 等错误路径。

### 18.2 StrategyFactory

- 正常注册和选择；
- 重复名称；
- 空名称和 nil Strategy；
- 默认策略选择；
- 指定策略不存在；
- Freeze 幂等；
- Freeze 后 Register/Default 失败；
- 并发 Select；
- 自定义工厂契约测试。

### 18.3 AgentRuntime

- Builder 缺少 Executor 或 Factory；
- Build 自动冻结 Factory；
- Run 创建独立 Scope；
- Request 正确传给 Factory；
- Factory 错误正确包装；
- nil Strategy 防御；
- Strategy 错误正确包装；
- Context 取消和超时；
- 多 goroutine 并发 Run；
- Runtime 统一终态事件且不重复。

### 18.4 ToolLoop Strategy

迁移现有测试并覆盖：

- 无工具单轮完成；
- 多轮工具调用；
- 最大轮数和单轮工具数限制；
- 工具不存在和工具执行失败；
- Session Load/Append 和 Revision 冲突；
- Prompt 渲染失败；
- Trimmer 行为；
- 模型流事件；
- Stats 统计；
- 并发复用同一策略实例。

### 18.5 验证命令

```bash
gofmt -w .
go test ./...
go vet ./...
go test -race ./...
```

## 19. 实施顺序

1. 新增独立 `event` 包并迁移事件契约。
2. 新增 `scope` 包，迁移 Env、Scope、Meta、Stats，并实现公开 Builder。
3. 在 `agent` 包定义 Request、Result、Strategy 和 StrategyFactory 契约。
4. 实现默认 StrategyFactory 及冻结机制。
5. 实现 RuntimeBuilder 和不可变 AgentRuntime。
6. 将 ToolLoopAgent 重构为 ToolLoop Strategy。
7. 迁移测试、示例、README 和相关设计文档。
8. 执行格式化、单测、静态检查和竞态检测。

每一步保持可编译并使用测试验证，避免一次性移动全部代码后难以定位问题。

## 20. 主要取舍与风险

### 20.1 注册策略实例

收益：

- 不需要每次 Run 创建策略；
- 策略成为稳定的编排规则对象；
- 注册和选择逻辑简单。

代价：

- 策略必须无单次运行状态并支持并发；
- 第三方策略若错误保存 Scope，可能出现数据竞争或内存滞留。

通过接口文档、并发测试和 `go test -race` 控制风险。

### 20.2 Scope 持有完整 Env

收益：策略只有一个运行参数，新增公共能力时无需修改所有 Strategy 方法签名。

代价：策略理论上可以访问所有能力，接口隔离弱于按需注入。当前各运行策略确实共享这些核心能力，因此接受该取舍；不应继续把无关服务无限加入 Env。

### 20.3 Factory 同时支持注册和选择

收益：符合使用方式，默认实现简单。

代价：Factory 名称与实际“注册表 + 路由器”职责不完全一致。为保持用户确定的术语，保留名称，但使用 `Select` 明确运行语义。

### 20.4 破坏性重构

不保留旧 API 可以获得更清晰的边界，但所有调用方必须迁移。应通过主版本升级、迁移文档和完整测试控制发布风险。

## 21. 实施决策

1. 采用独立 `event` 包解决 EventSink 循环依赖。
2. 默认工厂按 `Request.Strategy` 精确匹配，并支持一个默认策略。
3. Runtime 统一负责 run started/completed/failed/cancelled 终态事件。
4. Scope Builder 当前内部使用系统随机源和时间；可替换 Clock/IDGenerator 暂不扩大公开 API，测试通过显式 Meta 验证确定性场景。
5. 导出命名采用 `AgentRuntime`、`Builder`、`StrategyFactory`、`scope.Scope` 和 `scope.Env`。
6. 原有 Agent、ToolLoopAgent、RunEnv、RunScope 及 agent 包内事件实现已移除；ToolLoop 迁入 `strategy/toolloop`。
