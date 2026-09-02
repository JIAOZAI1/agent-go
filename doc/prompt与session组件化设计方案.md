# Prompt 与 Session 组件化设计方案

## 1. 背景

`agent-go` 当前已经具备 `agent`、`model`、`message`、`tool` 等基础包，但还缺少可复用的 Prompt 和会话历史组件。参考 `workspace/acore` 的 `prompt`、`session` 设计，本方案将这两类能力从 Agent 编排逻辑中拆出，使它们可以独立替换、测试和组合。

本方案是设计方案，不包含具体实现。实现前需要先确认本方案中的公开接口和消息模型调整。

## 2. 目标与非目标

### 2.1 目标

- 提供 provider 无关的 system prompt 渲染能力。
- 提供按会话键加载历史、按 revision 原子追加消息的 Session 存储契约。
- 保证组件实例可并发使用，调用方和组件之间不共享可变消息数据。
- 让 Agent 运行策略显式组合 Prompt、Session、Model 和 Tool，不依赖全局状态。
- 保留静态 Prompt、模板 Prompt、内存 Session 等简单默认实现。
- 为后续持久化 Session、上下文裁剪和多轮 ToolLoop 保留清晰扩展点。

### 2.2 非目标

- 本阶段不实现数据库、Redis 或外部 KV 存储。
- 不在 Prompt 组件中承担模板文件加载、配置管理或权限控制。
- 不在 Session 组件中实现模型调用、Tool 执行、重试或消息摘要。
- 不在 Session 组件中自动解决并发冲突；冲突处理属于运行策略或应用层。
- 不立即引入复杂的事件溯源、事务抽象或全局 Session 注册表。

## 3. 核心设计

### 3.1 Prompt 是一次运行的输入组件

Prompt 只负责根据一次运行的输入生成完整 system prompt，不保存会话状态，也不修改输入。

```go
package prompt

type Values map[string]string

type Input struct {
	Values Values
}

type Renderer interface {
	Render(context.Context, Input) (string, error)
}
```

`Renderer` 的实现必须满足：

- `context.Context` 为第一个参数，支持取消；
- 输入中的 map 视为只读，组件不得保留或修改；
- 实例构造完成后不可变，可被多个运行并发调用；
- 失败时返回空字符串和可用 `errors.Is` 判断的错误；
- 空 Prompt 是合法结果，由运行层决定是否添加 system message。

### 3.2 Session 是历史存储组件

Session 只保存可回放的对话消息。system prompt 属于本次运行上下文，不默认写入 Session，避免 Prompt 配置变化后历史中出现重复或过时的 system message。

```go
package session

type Key struct {
	Scope string
	ID    string
}

type Revision uint64

type Snapshot struct {
	Revision Revision
	Messages []message.Message
}

type Store interface {
	Load(context.Context, Key) (Snapshot, error)
	Append(context.Context, Key, Revision, []message.Message) (Revision, error)
}
```

Session 契约：

- `Scope` 和 `ID` 必须非空；
- 不存在的会话返回 revision `0` 和空消息；
- `Append` 只接受非空消息批次；
- 只有 `expectedRevision == currentRevision` 时追加成功；
- 追加成功后 revision 加一并原子返回；
- 返回的 Snapshot、传入的消息以及内部存储互相隔离；
- Store 实现必须支持并发调用，或在实现文档中明确限制；
- context 取消应尽快终止操作；
- revision 溢出、非法 key、空消息和冲突都返回稳定的 sentinel error。

首个实现为 `MemoryStore`，零值可用，使用 `sync.RWMutex` 保护 `map[Key]Snapshot`。数据随进程退出而丢失，适合作为测试替身和单进程默认实现。

## 4. 当前项目需要的前置调整

### 4.1 扩展 message 模型，但保持模型层独立

当前 `message.Message` 只有字符串 Content，无法安全表达 ToolCall、图片、签名等可回放内容。Session 不应自行定义另一套消息结构，因此需要将结构化内容能力放在 `message` 包：

```go
type ContentKind string

const (
	ContentText     ContentKind = "text"
	ContentImage    ContentKind = "image"
	ContentToolCall ContentKind = "tool_call"
)

type ContentBlock struct {
	Kind      ContentKind
	Text      string
	MIMEType  string
	URL       string
	Data      string
	ToolCall  *ToolCall
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments []byte
}

type Message struct {
	Role       Role
	Content   []ContentBlock
	ToolCallID string
	IsError    bool
}
```

这是对当前 `message.Message{Role, Content string}` 的兼容性调整。建议在实现阶段一次性完成迁移：将纯文本消息转换为一个 `ContentText` block，不保留两套并行字段，避免序列化和复制语义分裂。

实现中必须提供内部深拷贝逻辑，至少复制 content 切片、ToolCall 指针和 Arguments 字节切片。

### 4.2 model.Request 继续由模型层承载最终上下文

不建议让 `prompt` 或 `session` 依赖 `model`，以保持依赖方向稳定。运行策略负责将二者组合成 `model.Request`：

```go
promptText, err := renderer.Render(ctx, prompt.Input{Values: values})
snapshot, err := store.Load(ctx, key)

request := model.Request{
	Messages: prependSystemMessage(promptText, append(snapshot.Messages, input)),
}
```

如果后续模型请求需要 Tools、模型选择或生成选项，应扩展 `model.Request` 或新增 `model.Context`，而不是把这些字段塞入 Prompt 或 Session。system message 是否为空由组合层统一处理。

## 5. 运行时数据流

```text
调用方
  │  RunRequest{SessionKey, PromptValues, Input}
  ▼
运行策略
  ├─ Prompt.Render(ctx, values)
  ├─ Session.Load(ctx, key)
  ├─ 组装 system prompt + 历史 + 本轮输入
  ├─ Model.Generate(ctx, request)
  └─ Session.Append(ctx, key, snapshot.Revision, 本轮输入+最终回复)
       │
       ├─ 成功：返回结果和新 revision
       └─ 冲突：返回 ErrConflict，由调用方决定重新加载或放弃
```

一次运行的建议语义：

1. 先渲染 Prompt 和加载 Session Snapshot。
2. 在内存中构造本次模型请求，不修改 Snapshot。
3. 模型成功完成后，将本轮 user message 与最终 assistant/tool 消息作为一个批次追加。
4. 追加使用加载时的 revision，避免覆盖并发运行产生的消息。
5. 追加冲突不自动重试模型生成，因为生成请求可能已经产生外部成本，且重试会改变非幂等运行语义。

该策略会允许两个并发运行基于同一历史生成结果，但只允许一个结果提交。若业务要求“先占用会话再生成”，应在未来新增明确的 reservation/turn 协议，不将其隐含在普通 `Append` 中。

## 6. 包结构与依赖方向

```text
agent/
  agent.go                 # Agent、RunStrategy、RunRequest
message/
  message.go               # Role、Message、ContentBlock、ToolCall
model/
  request.go               # 模型请求及生成选项
prompt/
  prompt.go                # Renderer、Input、Values、错误
  static.go                # Static
  template.go              # Template
session/
  session.go               # Key、Revision、Snapshot、Store、错误
  memory.go                # MemoryStore
  clone.go                 # 消息深拷贝
```

依赖关系：

```text
agent ──> model
agent ──> prompt
agent ──> session
agent ──> message
session ──> message
model ──> message
prompt ──> 标准库
message ──> 标准库
```

`prompt` 不依赖 `session`，`session` 不依赖 `agent`，存储实现不反向依赖运行策略。具体的 Prompt、Session 组合只出现在 Agent 策略或应用组装层。

## 7. Prompt 实现组件

### 7.1 Static

```go
func NewStatic(text string) *Static
```

保存不可变字符串，Render 原样返回。适用于固定 system prompt 和测试。

### 7.2 Template

```go
type TemplateConfig struct {
	Name     string
	Text     string
	Defaults Values
}

func NewTemplate(config TemplateConfig) (*Template, error)
```

使用标准库 `text/template`，在构造阶段解析，Render 阶段只执行。应启用 `missingkey=error`，并对带连字符的 key 提供严格的 `index` 访问方式。Defaults 在构造阶段复制，调用方后续修改不会影响模板实例。

模板失败应区分构造错误和执行错误，例如 `ErrInvalidTemplate`、`ErrRender`。模板文本不做 HTML 转义，也不自动拼接用户输入；注入防护和变量来源校验由应用层负责。

### 7.3 组合 Renderer 的范围

第一阶段不新增复杂的 Chain/Composite Renderer。需要组合时，应用可以用一个小的 `RendererFunc` 包装多个 Renderer。只有当多处真实复用且组合规则稳定后，再增加顺序组合、条件分支或分段 Prompt 组件。

## 8. Session 实现组件

### 8.1 MemoryStore

```go
type MemoryStore struct {
	// 私有锁和按 Key 存储的 Snapshot
}

func NewMemoryStore() *MemoryStore
```

要求与 `acore.MemoryService` 一致：读写锁保护状态，Load 返回深拷贝，Append 在锁内完成 revision 检查、溢出检查和消息合并。

### 8.2 持久化实现扩展点

未来可实现：

- `SQLStore`：以 `(scope, id, revision)` 或独立会话版本表保证乐观并发；
- `KVStore`：使用 CAS/事务完成 revision 检查和消息追加；
- `ReadOnlyStore`：用于回放、审计或测试；
- `DecoratedStore`：在不改变 Store 契约的前提下增加指标、日志或加密。

这些实现只需要满足 `session.Store`，不需要被 Agent 感知。持久化实现必须明确序列化格式、迁移策略、消息大小限制和失败重试语义。

## 9. 错误与并发契约

建议在 `prompt` 中定义：

```go
ErrInvalidContext
ErrNilRenderer
ErrInvalidTemplate
ErrRender
```

建议在 `session` 中定义：

```go
ErrInvalidContext
ErrInvalidKey
ErrInvalidMessages
ErrInvalidSnapshot
ErrConflict
ErrRevisionExhausted
```

错误需要通过 `%w` 保留原因链，调用方通过 `errors.Is` 判断类别。错误文本不包含 API Key、Authorization、完整 Prompt 变量或敏感消息内容。

组件本身不启动 goroutine，不使用全局状态，不依赖 init。所有阻塞操作由持久化 Store 或上层模型调用通过 context 控制。

## 10. 测试策略

### Prompt

- Static 原样返回，包括空字符串和空白字符。
- nil context、已取消 context、nil renderer。
- Template 构造阶段拒绝空名称和非法模板。
- 默认值与本次值的覆盖规则。
- 缺少变量返回 `ErrRender`，不返回部分结果。
- 输入 map 不被修改，模板实例可并发 Render。

### Session

- 不存在会话的 revision 和消息结果。
- Append 成功后 revision 单调递增。
- 错误 revision 返回 `ErrConflict` 且不写入数据。
- 非法 key、空批次、revision 溢出。
- 修改 Append 入参或 Load 返回值不会污染内部数据。
- 不同 Key 相互隔离。
- 并发 Append 同一 expected revision 时只有一个成功。
- `go test -race ./...` 验证共享状态和深拷贝实现。

### 组合层

- system prompt 只出现在模型请求，不被 Session 重复保存。
- 初始历史、本轮输入和模型回复按正确顺序组装。
- Append 冲突不会触发隐式模型重试。
- context 取消能中止 Render、Load、Generate 和 Append。
- 模型失败时不会追加不存在的 assistant 回复。

## 11. 分阶段实施与兼容策略

### 阶段一：消息和独立组件

1. 先确定结构化 `message.Message` 的公开字段和深拷贝规则。
2. 新增 `prompt` 包及 Static、Template、RendererFunc。
3. 新增 `session` 包及 Store、MemoryStore。
4. 为两个包补充表驱动、并发和错误路径测试。

### 阶段二：运行策略组合

1. 扩展 Agent 的运行请求，注入 Prompt、Session 和 Session Key。
2. 在策略层完成 Load、Prompt.Render、模型请求组装和 Append。
3. 明确单轮策略与 ToolLoop 对 Session 的提交边界。
4. 补充集成测试和使用示例。

### 阶段三：上下文治理

当真实需求出现后，再独立增加 Context Window Reducer/Estimator，用于在模型请求前裁剪历史。Reducer 只处理本次请求快照，不修改 Session；摘要、压缩和持久化策略也不放入基础 Session 接口。

当前 `agent-go` 尚未发布稳定的消息和 Agent 运行接口，因此建议不增加兼容层，直接在阶段一完成一次清晰迁移。若后续已有外部调用方，再通过新主版本或显式适配函数迁移，不在 `session` 中同时支持 string Content 和结构化 Content。

## 12. 验收标准

- `prompt`、`session` 可独立导入，不依赖具体 provider。
- MemoryStore 在并发读写下无数据竞争，冲突行为确定。
- Prompt 和 Session 均不修改调用方数据，返回值可安全独立修改。
- Agent 策略可以替换 Static/Template Prompt 和 Memory/持久化 Store，而不修改模型适配器。
- system prompt、会话历史、模型请求和最终持久化消息的边界有测试证明。
- 实现完成后执行 `gofmt`、`go test ./...`、`go vet ./...`、`go test -race ./...`。
