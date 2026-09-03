# Agent 运行领域事件设计方案

## 1. 目标

为 agent-go 定义统一的 Agent 运行领域事件，使运行策略能够向日志、指标、Tracing、Web UI、审计和调试组件提供结构化运行信息。

本方案重点解决两个问题：

1. 定义跨运行策略和跨模型 Provider 稳定的领域事件；
2. 支持多个旁路消费者独立消费事件，避免观测组件反向侵入 Agent 核心流程。

成功标准：

- Agent 运行层只依赖一个小型事件发布契约；
- 消费者不需要识别 OpenAI 等 Provider 的原始事件格式；
- 同一 Run 内的事件可通过序号稳定排序；
- 一个消费者变慢或失败时，不会默认影响其他消费者和 Agent 主流程；
- 测试、日志、指标和审计可以选择不同的投递可靠性。

## 2. 非目标

本方案暂不负责：

- 事件持久化和完整事件溯源；
- 跨进程或分布式消息队列；
- 事件重放服务；
- Agent 运行策略、ToolLoop 或模型路由的具体实现；
- Provider 原始请求、响应和错误对象透传；
- 自动重试、限流和熔断。

如果未来需要可靠审计、跨进程消费或事件重放，应在本方案的发布接口之下增加独立适配器，不改变领域事件结构。

## 3. 设计原则

### 3.1 生产与消费分离

Agent 运行层只产生领域事件，不直接调用日志、指标、数据库或消息队列实现。

旁路消费者通过事件发布器注册，消费者生命周期和失败策略由发布器管理。

### 3.2 事件不可变

事件发布后不得被生产者或其他消费者修改。包含 `message.Message`、`tool.Call`、`tool.Result` 等引用类型的数据时，在创建事件时复制引用背后的切片和字节数组。

### 3.3 运行级顺序

事件只保证同一个 `RunID` 内的顺序，不保证多个并发 Run 之间的全局顺序。

### 3.4 可靠性显式选择

指标、调试和 UI 通常允许丢失事件；审计、计费和合规记录通常不能静默丢失。不同用途应选择不同的 Sink 或投递策略，不能让一个默认策略满足所有场景。

### 3.5 不使用全局事件总线

事件发布器由 Agent 或运行时实例显式持有，避免全局状态、测试污染、隐式订阅和生命周期不清晰。

## 4. 分层边界与依赖关系

```text
Agent 运行策略
    │ 产生 Event
    ▼
agent.EventSink
    │
    ├── 同步 Sink：测试、可靠审计、事务性记录
    └── FanoutSink：异步旁路消费
            ├── 指标消费者
            ├── 日志/Tracing 消费者
            ├── UI 推送消费者
            └── 审计消费者
```

依赖方向：

```text
agent 运行策略 ──> agent 事件契约
agent FanoutSink ──> agent 事件契约
事件消费者 ───────> agent 事件契约
```

事件层不依赖具体 Provider、日志库、消息队列或数据库。

## 5. 事件信封

建议在 `agent/event.go` 中定义统一事件信封：

```go
type EventType string

type Event struct {
	RunID      string
	Sequence   uint64
	OccurredAt time.Time
	Type       EventType
	Data       any
}
```

字段契约：

- `RunID`：一次 Agent Run 的唯一标识，由运行策略创建并贯穿整个运行过程；不能为空；
- `Sequence`：单次 Run 内从 1 开始递增；由运行策略在产生事件时分配；
- `OccurredAt`：事件发生时间，使用带时区的 `time.Time`；
- `Type`：稳定的机器可读事件类型；
- `Data`：对应事件的强类型数据，只允许使用本方案定义的领域数据结构。

`Event` 是值类型，但 `Data` 可能包含引用类型。事件构造函数负责复制消息和字节切片，消费者不得修改 `Data`。

### 5.1 事件类型

```go
const (
	EventRunStarted              EventType = "run.started"
	EventInputReceived           EventType = "input.received"
	EventModelGenerationStarted  EventType = "model.generation_started"
	EventModelDelta              EventType = "model.delta"
	EventModelGenerationFinished EventType = "model.generation_finished"
	EventToolCallRequested       EventType = "tool.call_requested"
	EventToolExecutionStarted    EventType = "tool.execution_started"
	EventToolExecutionFinished   EventType = "tool.execution_finished"
	EventRunCompleted            EventType = "run.completed"
	EventRunFailed               EventType = "run.failed"
	EventRunCancelled            EventType = "run.cancelled"
)
```

命名使用 `领域.动作`，不包含厂商名称、HTTP 接口名称或实现类名称。

### 5.2 事件数据

```go
type RunStarted struct {
	Input message.Message
}

type InputReceived struct {
	Message message.Message
}

type ModelGenerationStarted struct {
	Model model.Ref
}

type ModelDelta struct {
	Model        model.Ref
	Delta        string
	FinishReason model.FinishReason
}

type ModelGenerationFinished struct {
	Model        model.Ref
	Message      message.Message
	Usage        model.Usage
	FinishReason model.FinishReason
}

type ToolCallRequested struct {
	Call tool.Call
}

type ToolExecutionStarted struct {
	Call tool.Call
}

type ToolExecutionFinished struct {
	Call     tool.Call
	Result   tool.Result
	Duration time.Duration
}

type RunCompleted struct {
	Message message.Message
}

type RunFailed struct {
	ErrorKind string
}

type RunCancelled struct{}
```

说明：

- `model.delta` 只表示通用模型增量，不暴露 Provider 原始 chunk；
- `model.generation_finished` 表示一次模型调用结束，不等同于整个 Agent Run 结束；
- `tool.call_requested` 表示模型提出工具调用意图，不表示工具已经执行；
- `tool.execution_finished` 的 `Result` 只在成功执行时存在，执行失败通过运行错误事件表达；
- `RunFailed.ErrorKind` 只保存稳定错误分类，不保存密钥、请求头、完整提示词或工具参数；
- 如需关联多次模型调用或工具调用，后续可在事件信封增加可选 `StepID`，第一阶段不提前增加。

## 6. 运行生命周期

典型单轮运行：

```text
run.started
  └─ input.received
      └─ model.generation_started
          ├─ model.delta ...
          └─ model.generation_finished
              └─ run.completed
```

典型 ToolLoop：

```text
run.started
  └─ input.received
      └─ model.generation_started
          ├─ model.delta ...
          └─ model.generation_finished
              └─ tool.call_requested
                  └─ tool.execution_started
                      └─ tool.execution_finished
                          └─ model.generation_started
                              └─ ...
                                  └─ run.completed
```

终态约束：

- `run.completed`、`run.failed`、`run.cancelled` 最多出现一个；
- 运行未成功结束时不能发送 `run.completed`；
- `run.cancelled` 仅用于 `context.Canceled` 或明确的取消语义；
- 其他错误发送 `run.failed`；
- 事件发布失败是否使 Run 失败，由所使用的 Sink 策略决定。

## 7. 发布接口

运行策略依赖最小接口：

```go
type EventSink interface {
	Publish(context.Context, Event) error
}
```

Agent 运行配置可以显式注入：

```go
type RunOptions struct {
	EventSink EventSink
}
```

当 `EventSink` 为 `nil` 时，运行不产生外部事件副作用。

`Publish` 契约：

- `ctx` 必须作为第一个参数，不能传入 `nil`；
- 调用方负责保证 `RunID`、`Sequence` 和 `Type` 合法；
- Sink 不得修改传入事件；
- 同一 Run 的事件必须按照 Sequence 发布；
- 同步 Sink 返回的错误可以被运行策略处理；
- 异步旁路 Sink 的消费者错误不应通过 `Publish` 影响主流程，而应通过错误回调或统计接口暴露。

## 8. FanoutSink 旁路消费

### 8.1 API 草案

```go
type EventHandler func(context.Context, Event) error

type FanoutOptions struct {
	QueueSize int
	Overflow  OverflowPolicy
}

type OverflowPolicy string

const (
	OverflowBlock      OverflowPolicy = "block"
	OverflowDropNewest OverflowPolicy = "drop_newest"
)

type Subscription interface {
	Close() error
}

type FanoutSink struct {
	// 订阅者目录、关闭状态和并发控制为私有字段
}

func NewFanoutSink(options FanoutOptions) *FanoutSink
func (s *FanoutSink) Subscribe(name string, handler EventHandler) (Subscription, error)
func (s *FanoutSink) Publish(ctx context.Context, event Event) error
func (s *FanoutSink) Close() error
```

### 8.2 投递模型

每个订阅者拥有独立的有界队列和消费协程：

```text
Publish(event)
      │
      ▼
  FanoutSink
   ├── subscriber A queue ──> handler A
   ├── subscriber B queue ──> handler B
   └── subscriber C queue ──> handler C
```

一个消费者的慢处理不会占用其他消费者的队列，也不会改变已经入队事件的内容。

### 8.3 背压策略

第一阶段提供两种策略：

- `OverflowBlock`：队列满时阻塞 `Publish`，适合审计和不能丢失的同步边界；
- `OverflowDropNewest`：队列满时丢弃新事件并累计丢弃计数，适合指标、调试和 UI。

默认建议使用 `OverflowDropNewest`，防止观测系统拖垮 Agent。可靠审计不应只依赖异步内存队列，应使用同步 Sink 或持久化队列。

### 8.4 消费者错误

消费者返回错误时：

1. 不影响其他消费者；
2. 不直接使 Agent Run 失败；
3. 由 FanoutSink 记录错误计数并触发可选的错误处理器；
4. 是否取消该订阅，需要单独的订阅策略，第一阶段默认保持订阅继续消费。

不能把消费者错误直接返回给 `Publish`，否则异步旁路会退化为主流程依赖。

### 8.5 生命周期

```go
fanout := agent.NewFanoutSink(agent.FanoutOptions{QueueSize: 256})
subscription, err := fanout.Subscribe("metrics", metricsHandler)
if err != nil {
	return err
}
defer subscription.Close()
defer fanout.Close()
```

`Subscription.Close` 停止该订阅接收新事件，并等待或清理自身队列。

`FanoutSink.Close` 的默认语义为：

1. 停止接收新事件；
2. 关闭所有订阅输入；
3. 等待已入队事件完成；
4. 退出所有消费协程；
5. 返回关闭过程中出现的错误。

如果未来增加“立即关闭”需求，应增加显式选项，不改变默认的有序退出语义。

## 9. 同步与异步组合

同一运行可以组合多个 Sink：

```text
Agent Run
   ├── ReliableAuditSink  （同步、错误可影响 Run）
   └── FanoutSink          （异步、旁路错误隔离）
```

建议提供一个简单的组合 Sink，但必须明确错误策略：

- 可靠 Sink 失败：返回错误，由运行策略决定是否终止；
- 异步 FanoutSink：只负责入队，消费者错误不返回；
- 不允许组合器静默吞掉同步 Sink 的错误。

第一阶段可以不提供通用组合器，由运行策略分别调用同步 Sink 和旁路 Sink，避免过早增加抽象。确认存在重复组合需求后再增加 `MultiSink`。

## 10. 线程安全与资源管理

- `FanoutSink`、订阅目录和队列必须支持多个 Run 并发调用；
- 每个消费协程必须能响应 `FanoutSink.Close` 和父级 context 取消；
- 队列创建者负责关闭队列，禁止多个协程重复关闭；
- Handler 的 context 应继承订阅生命周期，不应保存 Agent Run 的 context 到长期结构体；
- 队列必须有上限，禁止无界缓存；
- `Close` 必须等待消费协程退出，避免资源泄漏；
- 事件中不得保存网络响应体、连接、计时器或 Provider 私有对象。

## 11. 兼容与演进

### 11.1 对现有接口的影响

第一阶段只新增 Agent 事件类型和 Sink，不修改当前：

```go
type Agent interface {
	Run(context.Context, message.Message) (message.Message, error)
}
```

现有 Agent 实现无需迁移。事件能力通过未来运行策略的选项注入，而不是直接把 `Run` 改成返回事件流。

### 11.2 未来扩展

只有出现真实需求后再增加：

- `StepID`：关联一次模型调用或工具调用；
- `ParentSequence`：表达嵌套步骤；
- 事件版本号：处理持久化后的结构演进；
- 可靠持久化 Sink；
- 跨进程传输格式；
- 事件过滤和订阅范围；
- 事件重放。

新增事件类型应保持旧消费者可忽略；修改现有事件字段时，应优先增加可选字段，不删除或改变已有字段语义。

## 12. 测试策略

### 事件模型

- 事件类型和字段构造；
- RunID、Sequence、OccurredAt 保持不变；
- 消息、工具调用和工具结果完成深拷贝；
- 终态事件约束。

### FanoutSink

- 多订阅者都能收到同一事件；
- 单个订阅者错误不影响其他订阅者；
- 每个订阅者保持事件顺序；
- 队列满时的 Block 和 Drop 行为；
- 订阅关闭后不再收到事件；
- Fanout 关闭后所有协程退出；
- 多个 Run 并发发布时无数据竞争；
- Handler 不修改其他订阅者收到的事件。

### 验证命令

```bash
gofmt -w .
GOCACHE=/tmp/agent-go-cache go test ./...
GOCACHE=/tmp/agent-go-cache go vet ./...
GOCACHE=/tmp/agent-go-cache go test -race ./...
```

## 13. 实施顺序

1. 新增事件类型、事件数据和不可变复制辅助函数；
2. 新增 `EventSink` 和同步测试 Sink 所需的最小契约；
3. 实现 `FanoutSink`、订阅生命周期和有界队列；
4. 补充并发、背压和错误隔离测试；
5. 在 Agent 运行策略中注入事件 Sink；
6. 根据真实消费者需求增加可靠 Sink 或持久化适配器。

## 14. 关键决策总结

- 事件属于 `agent` 领域，不放入 `model` 或 `tool`，因为它描述完整 Agent Run；
- 事件生产和旁路消费分离，避免基础流程依赖观测基础设施；
- 默认旁路消费采用异步、有界队列和错误隔离；
- 可靠审计使用独立同步或持久化 Sink；
- 不使用全局事件总线；
- 不为尚未出现的分布式重放、版本迁移和复杂路由提前增加抽象。
