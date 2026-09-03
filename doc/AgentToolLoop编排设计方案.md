# Agent ToolLoop 编排设计方案

> 本方案已被 [AgentRuntime与运行策略架构重构设计方案.md](AgentRuntime与运行策略架构重构设计方案.md) 取代；ToolLoop 已迁移为 `strategy/toolloop.Strategy`。以下内容仅保留为历史记录。

## 1. 背景

`agent-go` 目前已经具备 `model`（模型执行）、`tool`（工具执行）、`prompt`（system prompt 渲染）、`session`（会话历史存储）、`agent`（运行领域事件）等独立组件，但没有任何实现把它们组合成一个可运行的 Agent。`agent.Agent` 接口目前只是 `Run(ctx, message.Message) (message.Message, error)`，没有具体实现。

本方案定义第一个具体 Agent 实现 —— `ToolLoopAgent`：把 `model.Executor`、`tool.Service`、`prompt.Renderer`、`session.Store`、`agent.EventSink` 组合成一个支持"模型 → 工具调用 → 回填结果 → 再次调模型"循环的编排器，复用 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) 中已定义的事件类型和生命周期，并依赖 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) 提供的模型适配能力。

## 2. 目标与非目标

### 2.1 目标

- 提供一个把现有组件组合起来的可运行 Agent 实现，支持单轮对话和多轮工具调用循环。
- 严格按照 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) §5、§6 定义的事件类型和生命周期发布事件。
- 遵循 [工具系统设计方案.md](工具系统设计方案.md) §9 的 ToolLoop 协作边界：串行执行工具调用，Agent 层负责轮数/次数限制、失败策略和 Session 消息格式转换。
- 遵循 [prompt与session组件化设计方案.md](prompt与session组件化设计方案.md) §5 的 Session 提交边界：一次 Run 只提交一次。

### 2.2 非目标

- 不做上下文裁剪 / Token 预算控制（[prompt与session组件化设计方案.md](prompt与session组件化设计方案.md) §11 阶段三已列为独立后续工作）。
- 不支持并行执行多个工具调用（[工具系统设计方案.md](工具系统设计方案.md) §9 明确第一阶段串行）。
- 不做多 Agent 编排、路由或跨 Agent 协作。
- 不修改现有 `agent.Agent` 最小接口定义。

### 2.3 约束

- 不修改现有 `agent.Agent` 接口（`Run(ctx, message.Message) (message.Message, error)`），按 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) §11.1 的既定结论，`ToolLoopAgent` 是独立的新类型，不要求满足该接口。
- `ToolLoopAgent` 必须可被多个 goroutine 并发调用 `Run`。

### 2.4 成功标准

- 单轮无工具对话和多轮工具循环都能跑通，产生正确的 `run.*`/`model.*`/`tool.*` 事件序列。
- Session 冲突、超时、超限都有确定性的错误契约，不静默吞错误。
- 现有 `agent`、`model`、`tool`、`prompt`、`session` 包的公开契约均不需要破坏性调整（`model.Request` 按 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) 新增 `Tools` 字段除外）。

## 3. 分层边界与依赖关系

依据 [prompt与session组件化设计方案.md](prompt与session组件化设计方案.md) §6 已确认的依赖方向：

```text
agent ──> model
agent ──> prompt
agent ──> session
agent ──> tool
agent ──> message
```

组件放置：新增 `agent/toolloop.go`，与现有 `agent.go`（接口）、`event.go`/`eventsink.go`（事件）同包。这与仓库内其它包的既有惯例一致：`tool` 包里 `Service` 接口和 `ToolRuntime` 实现同包，`session` 包里 `Store` 接口和 `MemoryStore` 实现同包，`prompt` 包里 `Renderer` 接口和 `Static`/`Template` 实现同包。

## 4. 数据流

### 4.1 单轮（无工具调用）

```text
RunRequest{SessionKey, PromptValues, Input}
  → prompt.Render            (system prompt；Renderer 为 nil 时得到空字符串)
  → session.Load              (历史快照；Store 为 nil 时得到零值快照)
  → 组装 model.Request         (system + 历史 + 本轮输入 [+ Tools])
  → model.Executor.Generate    (发布 model.* 事件)
  → 无 ToolCall → session.Append（一次性提交）→ RunCompleted
```

### 4.2 多轮（含工具调用）

依据 [工具系统设计方案.md](工具系统设计方案.md) §9：

```text
model.Generate
  └─ 有 ToolCall（串行逐个执行）
       ├─ ToolCallRequested / ToolExecutionStarted 事件
       ├─ tool.Service.Execute
       ├─ ToolExecutionFinished 事件
       ├─ 追加 assistant ToolCall 消息 + tool Result 消息到本地 history
       └─ 回到 model.Generate，直到无 ToolCall 或达到轮数/次数上限
  └─ 无 ToolCall → 结束
```

### 4.3 工具调用识别

判断 `response.Message` 是否包含工具调用，统一调用 `message` 包新增的共享辅助函数，不在 `agent` 包内重复实现扫描 `ContentToolCall` 块的逻辑：

```go
// message/message.go
func ToolCalls(value Message) []ToolCall {
    var calls []ToolCall
    for _, block := range value.Content {
        if block.Kind == ContentToolCall && block.ToolCall != nil {
            calls = append(calls, *block.ToolCall)
        }
    }
    return calls
}
```

`response.Message` 本身的组装方式见 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) §4.2：`model.Event` 新增 `ToolCall` 字段，`model.Collect`（或 §6 描述的手动 `Recv` 累积逻辑）据此拼出带 `ContentToolCall` 块的消息。`ToolLoopAgent` 只消费 `message.ToolCalls` 的结果，不关心消息是被 `Collect` 还是手动循环组装出来的。

## 5. 接口契约

```go
// agent/toolloop.go

// RunRequest is the input for one ToolLoopAgent run.
type RunRequest struct {
    SessionKey   session.Key   // 必填，标识本次运行归属的会话
    PromptValues prompt.Values // 传给 Renderer 的变量，可为空
    Input        message.Message
}

// RunResult is the output of one completed run.
type RunResult struct {
    Message  message.Message  // 最终 assistant 回复
    Revision session.Revision // Session 提交后的新版本；未配置 Store 时为零值
}

// ToolLoopOptions configures a ToolLoopAgent.
type ToolLoopOptions struct {
    Executor            model.Executor  // 必填
    Tools                tool.Service    // 可选；nil 表示不提供工具能力
    Renderer             prompt.Renderer // 可选；nil 表示不注入 system prompt
    Store                 session.Store   // 可选；nil 表示无状态单轮（不加载/不提交历史）
    EventSink             EventSink       // 可选；nil 表示不产生事件
    MaxTurns              int             // ≤0 时取默认值 8
    MaxToolCallsPerTurn   int             // ≤0 时取默认值 8
    StopOnToolError       bool            // 默认 false：工具失败转为错误消息，继续下一轮
}

// NewToolLoopAgent validates options and returns an immutable orchestrator.
func NewToolLoopAgent(options ToolLoopOptions) (*ToolLoopAgent, error)

// Run executes one ToolLoop run. Safe for concurrent use across goroutines.
func (a *ToolLoopAgent) Run(ctx context.Context, request RunRequest) (RunResult, error)
```

### 5.1 契约细节

- `Executor` 为 nil 时 `NewToolLoopAgent` 直接返回错误，构造期失败，不留到运行期。
- `MaxTurns`/`MaxToolCallsPerTurn` ≤0 时取默认值，不视为非法输入，与 `agent.FanoutOptions` 对 `QueueSize` 的"零值即默认"处理方式保持仓库内一致。
- 内部错误统一走 Go error；`RunFailed` 事件的 `ErrorKind` 只放稳定分类字符串（如 `"max_turns_exceeded"`、`"too_many_tool_calls"`、`"tool_execution_failed"`、`"session_conflict"`），不放原始错误文本，遵循 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) §5.2 对 `RunFailed.ErrorKind` 的约束。
- `ctx` 取消：每个阶段（渲染 / 加载 / 生成 / 工具执行 / 提交）开始前检查 `ctx.Err()`；因 `context.Canceled` 中止时发布 `RunCancelled`，其他情况发布 `RunFailed`。
- `RunRequest.SessionKey` 在 `Store` 非 nil 时必填（复用 `session.ErrInvalidKey` 语义）；`Store` 为 nil 时忽略 `SessionKey`。

### 5.2 与 `agent.Agent` 接口的关系

`ToolLoopAgent.Run` 的签名是 `Run(ctx, RunRequest) (RunResult, error)`，不满足现有 `agent.Agent` 接口。原因：一个 `ToolLoopAgent` 实例需要并发服务多个会话/租户，`SessionKey` 必须按调用传入，不能绑死在构造参数里，因此不能直接实现只接收单个 `message.Message` 的最小接口。

如需要满足最小接口的场景（单会话、无需按调用切换 Prompt 变量），可以在使用方一侧包一层适配器：

```go
type boundAgent struct {
    loop  *ToolLoopAgent
    key   session.Key
    values prompt.Values
}

func (a boundAgent) Run(ctx context.Context, input message.Message) (message.Message, error) {
    result, err := a.loop.Run(ctx, RunRequest{SessionKey: a.key, PromptValues: a.values, Input: input})
    return result.Message, err
}
```

本方案不在 v1 中提供该适配器，作为可扩展点记录（见 §8）。

## 6. 事件发布规则

- `RunID` 每次 `Run` 调用生成一个新值（建议用 `crypto/rand` 生成的十六进制字符串，避免引入新依赖）。
- `Sequence` 从 1 开始，在单次 `Run` 内部单调递增，由 `ToolLoopAgent` 内部的一个仅限本次调用使用的计数器分配（不是包级共享状态）。
- 不使用 `model.Collect`，而是手动循环 `stream.Recv`：每收到一个 `model.Event` 就发布一次 `EventModelDelta`（`Delta` 为空、只携带 `ToolCall` 的事件不发布 `EventModelDelta`，避免空增量事件），循环结束后按 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) §4.2 中 `model.Collect` 同样的累积规则（文本拼接 + `ToolCall` 收集，组装成带 `ContentText`/`ContentToolCall` 块的 `message.Message`）拼出最终消息，并发布 `EventModelGenerationFinished`。理由：当前 `provider/openai` 是假流式（一次性发完事件），用 `Collect` 和手动循环效果一样；但手动循环让编排器在 provider 未来支持真流式后，无需改动就能自然获得逐 token 的 `model.delta` 事件。累积逻辑与 `model.Collect` 内部完全一致，不重新发明，只是多了一步逐事件发布。
- 事件序列必须符合 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) §6 定义的终态约束：`run.completed`/`run.failed`/`run.cancelled` 最多出现一个。

## 7. 关键设计取舍与风险

### 7.1 Session 只在 Run 结束时提交一次

按 [prompt与session组件化设计方案.md](prompt与session组件化设计方案.md) §5 建议，中间轮次不提交，失败/取消时也不提交部分历史，保证 Session 里不会出现"用户消息有了，但助手回复因为后续工具失败而缺失"的半截历史。提交批次是 `history[len(snapshot.Messages):]`（即 Load 之后新增的全部消息），使用 Load 时的 `snapshot.Revision` 作为 `expected`，冲突时不自动重试模型生成，直接把 `session.ErrConflict` 返回给调用方。

### 7.2 工具执行失败策略

默认 `StopOnToolError=false`：把错误内容写入一条 `IsError=true` 的 tool 消息，交给模型自行决定重试、更换策略或放弃，循环继续。`StopOnToolError=true` 时任何工具执行失败立即终止整个 Run，返回错误并发布 `run.failed`。默认选择继续，因为多数工具失败（网络抖动、参数错误）模型有能力自我修正；对失败后果严重的场景（如支付类工具），应用层可以显式开启 `StopOnToolError`。

### 7.3 并发安全

`ToolLoopAgent.Run` 的局部状态（`history`、`RunID`、`Sequence` 计数器）都是每次调用的局部变量，不跨调用共享。唯一共享的是 `Executor`/`Tools`/`Renderer`/`Store`/`EventSink`，它们各自的设计文档都已声明为可并发使用，因此 `ToolLoopAgent` 本身不需要额外加锁即可支持并发 `Run`。

### 7.4 风险

- 无上下文裁剪：长对话 + 多轮工具循环可能超出模型 context window，目前只能靠 `MaxTurns` 兜底，不能根治。已在非目标中明确，等待独立的 Context Reducer 组件。
- `MaxTurns`/`MaxToolCallsPerTurn` 的默认值（均为 8）是经验值，缺少真实工作负载验证，实现后应允许通过 `ToolLoopOptions` 覆盖，不作为硬编码常量暴露给调用方之外的地方。

## 8. 可扩展点与兼容策略

- §5.2 描述的 `agent.Agent` 适配器：后续如有单会话简单场景的真实需求，再实现，不在 v1 中提供。
- `MaxTurns`/`MaxToolCallsPerTurn`/`StopOnToolError` 都是 `ToolLoopOptions` 字段，后续新增配置项走可选字段扩展，不破坏已有调用方。
- 并行执行 ToolCall、上下文裁剪、`tool_choice` 精确控制都是已知的后续扩展点，明确不在本方案实现范围内；一旦需要，应作为独立方案讨论，不通过隐式行为变更引入。

## 9. 测试策略

- **单轮无工具**：验证事件序列 `run.started → input.received → model.generation_started → model.delta* → model.generation_finished → run.completed`，Session 恰好 `Append` 一次。
- **多轮工具循环**：用测试替身 `tool.Service` 模拟多次 ToolCall 后再返回纯文本，验证事件序列匹配 [agent运行领域事件设计方案.md](agent运行领域事件设计方案.md) §6 的典型 ToolLoop 顺序，历史消息顺序正确，`tool_call_id` 关联正确。
- **超限场景**：`MaxTurns`/`MaxToolCallsPerTurn` 超限时返回稳定错误，发布 `run.failed`，Session 不提交半截历史。
- **工具失败两种策略**：`StopOnToolError` 为 `true`/`false` 时分别验证行为符合 §7.2 描述。
- **取消场景**：在渲染 / 加载 / 生成 / 工具执行任一阶段取消 `ctx`，验证及时返回 `ctx.Err()` 并发布正确的终态事件。
- **并发场景**：多个 goroutine 并发调用同一个 `ToolLoopAgent.Run`（不同 `SessionKey`），`go test -race` 验证无数据竞争。
- **Session 冲突**：模拟并发写入触发 `session.ErrConflict`，验证不重试模型生成，直接把冲突返回调用方。
- 验证命令：

```bash
gofmt -w .
go test ./...
go vet ./...
go test -race ./...
```

## 10. 实施顺序

1. 确认 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) 已实现（`model.Request.Tools`、`model.Event.ToolCall`、`model.Collect` 调整、`provider/openai` 的 tool_calls 收发），`ToolLoopAgent` 依赖其提供的 `model.Executor` 行为和 `model.Event` 契约。
2. `message` 包新增 `ToolCalls(Message) []ToolCall` 辅助函数（§4.3）。
3. 新增 `agent/toolloop.go`：`RunRequest`、`RunResult`、`ToolLoopOptions`、`NewToolLoopAgent`。
4. 实现单轮无工具路径（渲染 → 加载 → 生成 → 提交），跑通事件序列和 Session 提交测试。
5. 实现多轮工具循环路径（串行执行、轮数/次数限制、失败策略），工具调用识别统一走 `message.ToolCalls`。
6. 补充取消、并发、Session 冲突的测试。
7. 执行 `gofmt`、`go test`、`go vet`、`go test -race`。

## 11. 关键决策总结

- `ToolLoopAgent` 新增 `RunRequest`/`RunResult`，不实现现有最小 `agent.Agent` 接口，因为 `SessionKey` 需要按调用传入以支持多会话并发（已与用户确认）。
- Session 提交边界是"一次 Run 提交一次"，不在中间轮次提交，避免出现半截历史。
- 工具执行失败默认转为错误消息并继续，通过 `StopOnToolError` 支持更保守的立即终止策略（已与用户确认）。
- 事件发布走手动 `Recv` 循环而不是 `model.Collect`，为未来真流式 provider 预留逐 delta 事件能力，不需要改动编排器；累积逻辑与 `model.Collect` 保持一致。
- 判断消息是否包含工具调用统一调用 `message.ToolCalls`，不在 `agent` 包内重复实现扫描逻辑——这是对早期草案的修正：工具调用的识别和表达都属于 `message`/`model` 抽象层的契约，`agent` 包只消费，不重新定义。
- 串行执行工具调用、不做上下文裁剪，均遵循已有设计文档的既定结论，不在本方案中重新展开讨论。
