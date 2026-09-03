# SingleTurn 运行策略设计方案

状态：**已确认并已实现**。相关代码位于 `strategy/singleturn/`。

## 1. 目标

SingleTurn 为 `AgentRuntime` 提供一次 Run 只调用一次模型的运行策略。策略可以使用 Prompt、Session、EventSink 和 Trimmer，但不会向模型暴露工具，也不会执行工具调用。

## 2. 运行流程

```text
读取 Scope
  → 渲染可选 System Prompt
  → 加载可选 Session 历史
  → 裁剪发送给模型的历史
  → 组装 system + history + 当前输入
  → 调用一次模型并收集流
  → 拒绝意外工具调用
  → 提交当前输入和模型回复
  → 返回 Result
```

SingleTurn 中的“单轮”表示一次模型生成，不表示无会话。配置 Session 时仍加载历史并保存本轮消息。

## 3. 公共契约

```go
type Options struct {
    ModelOptions model.Options
    TrimBudget   int
}

func New(options Options) (*Strategy, error)
func (s *Strategy) Run(context.Context, *scope.Scope) (agent.Result, error)
```

策略构造时复制 `ModelOptions` 的指针字段。策略实例构建后不可变，可被多个 Run 并发复用；单次运行状态只保存在 Scope 和方法局部变量中。

## 4. 能力与行为

- Executor：必需，由 Scope Builder 保证。
- Prompt：可选，输出作为第一条 system 消息。
- Session：可选，加载历史并以加载到的 Revision 乐观提交。
- Trimmer：可选，只裁剪模型输入，不修改或重复写入历史。
- EventSink：可选，以 best-effort 方式发布模型开始、增量和完成事件。
- Tools：忽略，模型请求中的工具列表始终为空。

Session 提交批次固定为当前用户输入和模型回复。Prompt、Session Load、模型生成或流收集失败时不提交。

## 5. 工具调用与统计

模型返回工具调用时返回 `ErrUnexpectedToolCall`，不执行工具且不提交 Session。

一次成功运行的统计口径：

- `TurnCount = 1`；
- `StepCount = 1`；
- `ToolCallCount = 0`；
- 模型 Usage 累加到 Scope；
- `RetryCount = 0`。

通用运行起止事件由 AgentRuntime 发布；SingleTurn 只发布模型调用事件。

## 6. 错误和并发

策略检查 nil Context、已取消 Context 和 nil Scope。底层 Prompt、Session、模型及提交错误使用 `%w` 包装。策略不关闭 Env 中由调用方拥有的共享资源。

策略本身无可变运行状态；并发安全仍要求 Env 中的 Executor、Session、Prompt、Trimmer 和 EventSink 实现满足各自并发契约。

## 7. 验证

测试覆盖：

- Prompt、历史和当前输入的组装顺序；
- ModelOptions 快照隔离；
- 单次生成及统计；
- Session 只提交当前输入和回复；
- 不向模型提供工具；
- 意外工具调用不提交 Session。
