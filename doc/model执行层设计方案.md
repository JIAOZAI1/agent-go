# 模型执行层设计方案

## 1. 目标

为 agent-go 提供统一的模型执行抽象，使 Agent 上层可以用同一套接口调用不同模型厂商、不同 API 协议和不同部署形态的模型。

核心目标：

- 屏蔽厂商 API 请求和响应格式差异；
- 支持 OpenAI、Anthropic、本地模型等多种实现；
- 支持按模型标识进行显式组合和替换；
- 保持基础接口简单，只将统一的流式执行纳入核心契约，不把路由和工具编排塞入执行器；
- 使具体厂商实现可以独立测试和演进。

## 2. 非目标

本方案暂不负责：

- 配置文件加载和密钥管理；
- HTTP 客户端的具体实现；
- 重试、熔断、限流和计费策略；
- Agent 多轮循环和状态管理；
- Tool 的实际执行；
- 多模型路由策略的具体实现。

## 3. 分层边界

```text
Agent 编排层
    │
    │ 依赖通用 Executor 接口
    ▼
模型执行层
    ├── Request / Response
    ├── Executor
    ├── Stream
    └── Event
    └── 错误和使用量定义
    │
    ▼
厂商适配层
    ├── OpenAI Adapter
    ├── Anthropic Adapter
    └── Local Model Adapter
```

模型执行层只定义稳定的领域契约。厂商适配层负责把通用请求转换为厂商请求，再把厂商响应转换为通用响应。

## 4. 模型引用

模型 ID 不能单独作为全局唯一标识，必须与 provider 一起使用。`Ref` 作为稳定的模型身份值保留，后续可用于日志、指标、tracing、缓存和模型路由：

```go
// Ref identifies a model exposed by a provider.
type Ref struct {
	ProviderID string
	ModelID    string
}
```

这样可以区分不同厂商的同名模型，也为后续主备切换和模型路由保留空间。

约束：

- `ProviderID` 和 `ModelID` 不能为空；
- 两个字段共同构成一次执行的目标模型；
- `Ref` 只表示目标，不包含密钥、URL 或 HTTP 客户端。

## 5. 执行器创建与通用请求

执行器由 Agent 运行层通过工厂创建，并在创建时绑定一个具体的 `Ref`：

```go
type Factory interface {
	New(ctx context.Context, ref Ref) (Executor, error)
}
```

执行器创建后可以被 Agent 长期复用，不建议在每次 `Generate` 调用时重复创建，以便复用 HTTP Client、连接池和其他运行时资源。

```go
// Request contains the input for one model generation.
type Request struct {
	Messages []message.Message
	Tools    []tool.Spec // 可提供给模型的工具列表；为空表示不提供工具
	Options  Options
}

// Options contains optional generation parameters.
type Options struct {
	Temperature *float64
	MaxTokens   *int
	Reasoning   bool
}
```

使用指针表示可选数值参数，以区分“未设置”和“显式设置为零”。厂商不支持某个参数时，由适配器决定忽略、转换或返回不支持错误，但不能让厂商专属字段泄漏到公共请求结构中。

`Tools` 直接复用 [工具系统设计方案.md](工具系统设计方案.md) 定义的 `tool.Spec`，不新增镜像类型：`tool` 包零依赖，`model → tool` 是安全的单向叶子依赖（详见 §13 依赖方向）。厂商不支持工具调用时，由适配器决定忽略该字段或返回 `ErrorUnsupported`。

## 6. 通用响应

```go
// Response contains the result of one model generation.
type Response struct {
	Message      message.Message
	Usage        Usage
	FinishReason FinishReason
}

// Usage records token consumption when provided by the provider.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// FinishReason describes why generation ended.
type FinishReason string

const (
	FinishStop     FinishReason = "stop"
	FinishLength   FinishReason = "length"
	FinishToolCall FinishReason = "tool_call"
)
```

`Usage` 允许 provider 不提供完整数据，此时使用零值。公共响应只表达 Agent 需要的结果，不暴露厂商原始响应对象。

## 7. 核心执行接口

```go
// Executor generates a response for a model request.
type Executor interface {
	Model() Ref
	Generate(ctx context.Context, request Request) (Stream, error)
}
```

`Model()` 返回该执行器绑定的模型身份，供 Agent 层记录日志或指标使用。`Request` 不重复携带 `Ref`，避免请求中的模型标识与执行器实际绑定的模型不一致。

### 接口契约

- `ctx` 必须作为第一个参数；调用方负责设置超时和取消策略；
- `Model` 返回固定的执行器身份，不应随请求变化；
- `Generate` 一次只处理一个请求，不保存对话状态，并返回一个流；
- `Recv` 返回 `io.EOF` 表示正常结束；
- 调用方负责关闭流，context 取消后流必须尽快结束并释放资源；
- 实现应支持多个 goroutine 并发调用，若无法支持，必须在实现文档中明确说明；
- 请求非法、模型不支持或 provider 返回失败时返回错误；
- 不使用 `panic` 表示正常业务失败；
- 默认不在执行器内部自动重试，避免非幂等生成被重复执行；
- API Key、Authorization header 和其他凭证不得出现在日志或错误信息中；
- 返回值应是独立值，调用方修改返回结果不应改变执行器内部状态。

## 8. 厂商多态实现

具体厂商实现只需要满足 `model.Executor`，并在创建时绑定 `Ref`：

```go
type OpenAIExecutor struct {
	ref Ref
	// 私有 HTTP 客户端和厂商配置
}

func (e *OpenAIExecutor) Model() Ref {
	return e.ref
}

func (e *OpenAIExecutor) Generate(
	ctx context.Context,
	request model.Request,
) (model.Stream, error) {
	// 通用 Request -> OpenAI 请求
	// 调用 OpenAI API
	// OpenAI 响应 -> 通用 Response
}
```

```go
type AnthropicExecutor struct {
	// 私有 HTTP 客户端和厂商配置
}
```

Agent 只依赖：

```go
type Agent struct {
	executor model.Executor
}
```

因此 Agent 不需要判断当前使用的是哪一家厂商。

厂商适配器的职责边界：

- 构造厂商请求；
- 转换消息和生成参数；
- 处理厂商认证和 HTTP 状态；
- 将厂商错误转换为通用错误；
- 将厂商响应转换为通用响应。

## 9. 流式执行

流式执行是所有模型执行器的基础契约，不再定义单独的可选 `StreamExecutor`：

```go
type Stream interface {
	Recv(ctx context.Context) (Event, error)
	Close() error
}

type Event struct {
	Delta        string
	ToolCall     *message.ToolCall
	FinishReason FinishReason
}
```

对于原生支持流式的 provider，适配器应逐步返回增量事件。对于只支持完整响应的 provider，适配器应在内部等待完整响应，然后包装为至少一个内容事件和一个结束事件，以满足统一契约。

`ToolCall` 字段是工具调用意图在流式契约中的正式表达：非 nil 时表示本次事件携带一个完整的工具调用。每个 `Event` 最多携带一个工具调用，不做增量 JSON 片段拼接；provider 有多个工具调用时依次发送多个只携带 `ToolCall` 的事件。这是抽象执行层的契约，不是某个 provider 的私有实现细节——具体 provider 只需要按此契约吐出事件，不需要自己发明表达工具调用的方式（详见 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) §4.2）。

上层如需普通的完整响应，可以提供独立的收集函数：

```go
func Collect(ctx context.Context, stream Stream) (Response, error)
```

`Collect` 同时累积 `Delta` 文本和 `ToolCall`，把二者组装成一条可能同时包含 `message.ContentText` 和 `message.ContentToolCall` 块的 assistant 消息。该便利函数不属于 `Executor` 核心接口，避免同时维护两套执行语义，但必须和手动 `Recv` 循环遵循同一套累积规则，不能只有 `Collect` 能正确保留工具调用信息。

流式接口必须明确：

- `Recv` 返回 `io.EOF` 表示正常结束；
- 调用方负责调用 `Close`；
- `ctx` 取消后，流必须尽快结束并释放网络资源；
- 不支持原生流式的 provider 仍然必须由适配器转换为统一的单次事件流。

## 10. 工具调用边界

模型执行器只负责识别并返回工具调用意图，不直接执行工具。工具调用意图通过 §9 中 `Event.ToolCall`（流式）和 `Response.Message` 里的 `message.ContentToolCall` 块（`Collect` 组装后）表达，是模型执行层的公开契约，不是各 provider 适配层各自约定的私有格式。

```text
Agent
  ├─ 调用 Executor
  ├─ 得到 ToolCall
  ├─ 调用 Tool
  ├─ 追加 ToolResult
  └─ 再次调用 Executor
```

工具执行、调用次数限制、循环检测和工具错误处理属于 Agent 编排层。这样可以避免模型适配器同时承担模型转换和 Agent 状态管理。

## 11. 错误契约

公共错误应支持 `errors.Is` 或 `errors.As` 判断。建议后续定义以下错误类别：

```go
type ErrorKind string

const (
	ErrorInvalidRequest ErrorKind = "invalid_request"
	ErrorUnsupported    ErrorKind = "unsupported"
	ErrorRateLimited    ErrorKind = "rate_limited"
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorAuthentication ErrorKind = "authentication"
)
```

厂商原始错误只作为内部原因保留，通过 `%w` 包装；对外错误上下文不得包含密钥、完整请求头或敏感请求内容。

重试建议由上层策略控制：

- `rate_limited` 和临时 `unavailable` 可以重试；
- `invalid_request` 和 `authentication` 不应盲目重试；
- 生成请求默认视为非幂等，重试必须由调用方明确授权。

## 12. 多模型组合

第一阶段采用显式组合，不引入全局注册表：

```go
executors := map[model.Ref]model.Executor{
	{ProviderID: "openai", ModelID: "gpt-5"}:     openAIExecutor,
	{ProviderID: "anthropic", ModelID: "claude"}: anthropicExecutor,
}
```

后续确实需要路由时，再增加独立抽象：

```go
type Router interface {
	Resolve(ctx context.Context, ref Ref) (Executor, error)
}
```

路由器可以实现主备、按能力选择、成本选择和故障切换，但不应放入 `Executor`。

## 13. 包结构

```text
model/
├── model.go       # Model、Ref
├── request.go     # Request、Options
├── response.go    # Response、Usage、FinishReason
├── executor.go    # Executor
├── stream.go      # Stream、Event
└── errors.go      # 公共错误类型

provider/
├── provider.go
├── openai/
│   └── executor.go
└── anthropic/
    └── executor.go
```

依赖方向：

```text
agent ──> model
agent ──> message
model ──> message
model ──> tool
provider/openai ──> model
provider/openai ──> message
provider/anthropic ──> model
```

`model ──> tool` 是为了让 `Request.Tools` 使用 `tool.Spec` 而新增的依赖；`Event.ToolCall` 使用的是 `message.ToolCall`，不额外引入依赖（详见 [OpenAI工具调用设计方案.md](OpenAI工具调用设计方案.md) §4）。`tool` 包本身零依赖，是安全的单向叶子依赖，不产生循环。

`model` 包不得反向依赖具体 provider。

## 14. 测试策略

### model 包

- 请求、响应和模型引用的字段构造；
- 可选参数的“未设置”和显式零值；
- `Executor`、`Stream` 的编译期契约；
- 公共错误类型可通过 `errors.Is` 或 `errors.As` 判断。

### 厂商适配器

- 使用 `httptest.Server` 验证请求转换；
- 验证响应转换和 token 使用量；
- 验证非成功 HTTP 状态到公共错误的映射；
- 验证 context 取消后请求及时退出；
- 验证 API Key 不出现在日志和错误文本中。

### 并发

- 并发调用同一执行器；
- 并发请求之间不存在消息、请求和响应数据污染；
- 执行 `go test -race ./...`。

## 15. 演进顺序

1. 先稳定 `Ref`、`Request`、`Response` 和 `Executor`；
2. 再实现一个厂商适配器验证抽象是否足够；
3. 根据第二个厂商的差异修正通用协议；
4. 在真实需求出现后增加更完整的工具调用事件和路由能力；
5. 最后补充重试、限流和可观测性等基础设施。

核心原则是：公共接口表达跨厂商真正共性的流式能力，厂商特有能力通过适配层扩展，不污染基础执行契约。
