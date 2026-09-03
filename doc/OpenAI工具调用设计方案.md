# OpenAI 工具调用设计方案

## 1. 背景

`provider/openai.Executor` 目前只支持纯文本请求和响应：不向 OpenAI 发送可用工具列表，也不解析响应中的 `tool_calls`；`toOpenAIMessages` 把每条消息的 content 拼成纯文本，会静默丢弃 `message.ContentToolCall` 内容块。`tool` 包已经提供了完整的 `Spec`、`Call`、`Result` 和 `Service` 契约（见 [工具系统设计方案.md](工具系统设计方案.md)），但目前没有任何 Provider 适配层把它们接到模型请求/响应上，`tool` 包在这条链路上处于未被使用状态。

本方案解决：让 `provider/openai` 能够把工具描述发给模型，并把模型返回的工具调用意图正确还原为 `message.ContentToolCall`，为后续的 Agent ToolLoop（见 [AgentToolLoop编排设计方案.md](AgentToolLoop编排设计方案.md)）提供可用的模型适配层。

## 2. 目标与非目标

### 2.1 目标

- `model.Request` 能携带可提供给模型的工具列表，`provider/openai` 据此在请求体中发送 OpenAI 的 `tools` 参数。
- 正确解析 OpenAI 响应中的 `tool_calls`，还原为 `message.Message` 中的 `ContentToolCall` 块，`FinishReason` 映射为 `model.FinishToolCall`（已有映射逻辑，无需改动）。
- 修复 `toOpenAIMessages` 丢弃非文本内容块的问题：assistant 消息中的 `ContentToolCall` 块要能正确序列化为 OpenAI 的 `tool_calls` 字段，保证多轮工具对话回放给 OpenAI 时消息历史完整。

### 2.2 非目标

- 不实现真正的 SSE 流式请求。现状是非流式请求、包装成两个缓冲事件的假流式 `Stream`，本方案沿用这个包装方式，只让内容包含 tool_calls，不改变流式语义。
- 不支持 `tool_choice` 精确控制（强制指定某个工具、禁用工具调用等），只使用 OpenAI 默认的 `auto` 语义。
- 不涉及 Anthropic 或其他厂商的工具映射。
- 不在 `provider/openai` 内重复做参数 JSON Schema 校验——校验职责属于 `tool.ToolRuntime.Execute`（`isJSONObject`），这里只做类型转换。

## 3. 分层边界与依赖关系

依据 [工具系统设计方案.md](工具系统设计方案.md) §3 已确认的依赖方向：

```text
agent ──> tool
model ──> message
provider ──> model、tool（按适配需要）
tool 不依赖 agent、model 或 provider
```

`tool` 包零依赖（只用标准库），`model → tool` 是安全的单向叶子依赖，不产生循环。数据流：

```text
tool.Service.Specs()
      │
      ▼
model.Request.Tools           （新增字段）
      │
      ▼
provider/openai.Executor.Generate
      │  组装 OpenAI tools 参数
      ▼
OpenAI Chat Completions API
      │  响应可能包含 message.tool_calls
      ▼
解析为 message.ContentToolCall 块
      │
      ▼
model.Response.Message（assistant，可能包含 ToolCall 块）
```

## 4. 核心数据类型调整

### 4.1 `model.Request` 新增字段

```go
// model/client.go
type Request struct {
    Messages []message.Message
    Tools    []tool.Spec // 新增；nil 或空表示不提供工具
    Options  Options
}
```

`model` 包新增对 `tool` 包的导入。`Tools` 为空时行为与现状完全一致（不发送 `tools` 参数）。

### 4.2 model.Stream 契约新增 ToolCall 字段

工具调用的响应表达属于抽象执行层的契约，不应该由各 Provider 在私有实现里各自发明表达方式——这是对本方案早期草案的修正，早期草案曾把这部分当作"provider 内部实现细节"处理（见 §8）。为此需要同步调整 `model` 包本身（该调整同时更新 [model执行层设计方案.md](model执行层设计方案.md)）：

```go
// model/stream.go
type Event struct {
    Delta        string
    ToolCall     *message.ToolCall // 新增；非 nil 表示本次事件携带一个完整的工具调用
    Usage        Usage
    FinishReason FinishReason
}
```

`model.Collect` 相应调整为同时累积文本增量和工具调用，统一拼装成 assistant 消息：

```go
func Collect(ctx context.Context, stream Stream) (response Response, err error) {
    ...
    var text []byte
    var calls []message.ToolCall
    for {
        event, recvErr := stream.Recv(ctx)
        if errors.Is(recvErr, io.EOF) {
            response.Message = buildAssistantMessage(text, calls)
            return response, nil
        }
        if recvErr != nil {
            return Response{}, recvErr
        }
        text = append(text, event.Delta...)
        if event.ToolCall != nil {
            calls = append(calls, *event.ToolCall)
        }
        if event.Usage.TotalTokens != 0 {
            response.Usage = event.Usage
        }
        if event.FinishReason != "" {
            response.FinishReason = event.FinishReason
        }
    }
}

func buildAssistantMessage(text []byte, calls []message.ToolCall) message.Message {
    blocks := make([]message.ContentBlock, 0, 1+len(calls))
    if len(text) > 0 {
        blocks = append(blocks, message.ContentBlock{Kind: message.ContentText, Text: string(text)})
    }
    for _, call := range calls {
        value := call
        blocks = append(blocks, message.ContentBlock{Kind: message.ContentToolCall, ToolCall: &value})
    }
    return message.Message{Role: message.RoleAssistant, Content: blocks}
}
```

约束：每个 `Event` 最多携带一个完整的工具调用，不做增量 JSON 片段拼接（真正的增量流式工具调用参数拼接是非目标，见 §2.2）。多个工具调用通过多个事件依次发出。这样一来，`provider/openai`（以及未来任何 Provider）都只需要按这个契约吐出事件，不需要在私有的 `stream` 类型里自己发明一套表达工具调用的方式；`message` 包新增的 `ToolCalls` 辅助函数（见 [AgentToolLoop编排设计方案.md](AgentToolLoop编排设计方案.md) §4.3）负责从组装好的消息里反向提取工具调用，消费方和生产方共用同一套数据结构，不需要各自解释。

### 4.3 OpenAI 请求侧 wire 类型（新增）

```go
// provider/openai/executor.go
type openAIMessage struct {
    Role       string           `json:"role"`
    Content    *string          `json:"content,omitempty"`
    ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
    ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
    ID       string             `json:"id"`
    Type     string             `json:"type"` // 固定 "function"
    Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
    Name      string `json:"name"`
    Arguments string `json:"arguments"` // OpenAI 用 JSON 字符串，不是原始 object
}

type openAITool struct {
    Type     string         `json:"type"` // 固定 "function"
    Function openAIFunction `json:"function"`
}

type openAIFunction struct {
    Name        string          `json:"name"`
    Description string          `json:"description,omitempty"`
    Parameters  json.RawMessage `json:"parameters"`
}
```

`chatCompletionRequest` 新增：

```go
type chatCompletionRequest struct {
    Model       string          `json:"model"`
    Messages    []openAIMessage `json:"messages"`
    Tools       []openAITool    `json:"tools,omitempty"`
    Temperature *float64        `json:"temperature,omitempty"`
    MaxTokens   *int            `json:"max_tokens,omitempty"`
}
```

### 4.4 OpenAI 响应侧 wire 类型（调整）

```go
type chatCompletionResponse struct {
    Choices []struct {
        Message struct {
            Content   string            `json:"content"`
            ToolCalls []openAIToolCall  `json:"tool_calls"`
        } `json:"message"`
        FinishReason string `json:"finish_reason"`
    } `json:"choices"`
    Usage openAIUsage `json:"usage"`
}
```

`toFinishReason` 已经把 `"tool_calls"`/`"function_call"` 映射到 `model.FinishToolCall`（executor.go:220-231），无需改动。

## 5. 请求侧行为：消息与工具序列化

`toOpenAIMessages` 调整为：文本块拼接进 `Content`，`ContentToolCall` 块转换为 `ToolCalls` 条目；两者互不影响，一条 assistant 消息可以同时有文本和工具调用。

```go
func toOpenAIMessages(values []message.Message) []openAIMessage {
    result := make([]openAIMessage, len(values))
    for index, item := range values {
        var text strings.Builder
        var calls []openAIToolCall
        for _, block := range item.Content {
            switch block.Kind {
            case message.ContentText:
                text.WriteString(block.Text)
            case message.ContentToolCall:
                calls = append(calls, toOpenAIToolCall(*block.ToolCall))
            }
        }
        result[index] = openAIMessage{
            Role:       string(item.Role),
            Content:    contentPointer(text.String(), len(calls) > 0),
            ToolCalls:  calls,
            ToolCallID: item.ToolCallID,
        }
    }
    return result
}

func toOpenAIToolCall(value message.ToolCall) openAIToolCall {
    return openAIToolCall{
        ID:   value.ID,
        Type: "function",
        Function: openAIFunctionCall{
            Name:      value.Name,
            Arguments: string(value.Arguments),
        },
    }
}

// contentPointer 在存在 tool_calls 且文本为空时返回 nil，
// 兼容 OpenAI 对 assistant 消息 content 可省略的要求。
func contentPointer(text string, hasToolCalls bool) *string {
    if text == "" && hasToolCalls {
        return nil
    }
    return &text
}
```

工具描述转换：

```go
func toOpenAITools(specs []tool.Spec) []openAITool {
    if len(specs) == 0 {
        return nil
    }
    result := make([]openAITool, len(specs))
    for index, spec := range specs {
        result[index] = openAITool{
            Type: "function",
            Function: openAIFunction{
                Name:        spec.Name,
                Description: spec.Description,
                Parameters:  spec.Parameters,
            },
        }
    }
    return result
}
```

`Generate` 组装请求体时新增 `Tools: toOpenAITools(request.Tools)`。

## 6. 响应侧行为：tool_calls 解析

`Generate` 按 §4.2 新增的 `model.Event.ToolCall` 契约组装 `stream.events`：先发一个纯文本 `Delta` 事件（如果 `choice.Message.Content` 非空），再为每个 OpenAI `tool_calls` 条目发一个只携带 `ToolCall` 的事件，最后发一个只携带 `Usage`/`FinishReason` 的收尾事件：

```go
func toStreamEvents(choice chatCompletionChoice, usage openAIUsage) []model.Event {
    events := make([]model.Event, 0, 2+len(choice.Message.ToolCalls))
    if choice.Message.Content != "" {
        events = append(events, model.Event{Delta: choice.Message.Content})
    }
    for _, call := range choice.Message.ToolCalls {
        toolCall := message.ToolCall{
            ID:        call.ID,
            Name:      call.Function.Name,
            Arguments: []byte(call.Function.Arguments),
        }
        events = append(events, model.Event{ToolCall: &toolCall})
    }
    events = append(events, model.Event{
        Usage:        toUsage(usage),
        FinishReason: toFinishReason(choice.FinishReason),
    })
    return events
}
```

`stream.events` 直接用这个函数的返回值构造，`Recv` 逻辑不需要改动（依次吐出切片里的事件）。这样调用方无论是用 `model.Collect`，还是像 [AgentToolLoop编排设计方案.md](AgentToolLoop编排设计方案.md) §6 那样手动 `Recv` 循环，拿到的都是同一套按 `model.Event` 契约表达的工具调用信息——`provider/openai` 不需要、也不应该自己拼装最终的 `message.Message`，组装逻辑只在 `model.Collect`（或手动循环里与之等价的累积逻辑）里出现一次。

## 7. 校验规则调整

`validateRequest` 目前逐条拒绝非 `ContentText` 的内容块（executor.go:195-199），需要放开：

- assistant 消息允许包含 `ContentToolCall` 块；
- `tool` 角色消息继续要求 `ToolCallID` 非空（已有校验不变）；
- 其他角色（`system`、`user`）仍然只允许 `ContentText`，防止误用。

## 8. 关键设计取舍与风险

- **Tools 字段类型**：直接复用 `tool.Spec`，不新增镜像类型。理由：`tool` 包零依赖，`model → tool` 是安全的叶子依赖，避免维护两份结构相同的类型定义（已与用户确认，见方案确认记录）。
- **工具调用的响应表达放在抽象层**：`model.Event` 新增 `ToolCall` 字段、`model.Collect` 同步调整（§4.2），而不是让 `provider/openai` 在私有的 `stream` 类型里自己拼出带工具调用的 `message.Message`。这是对本方案的一次修正：最初草案把这部分归为"provider 内部实现细节，不改变 model 公开契约"，讨论后确认工具调用的表达方式本身就应该是抽象执行层契约的一部分，否则每个 Provider 都要各自摸索一套等价但不统一的实现，且 `model.Collect` 这样的通用便利函数会丢失工具调用信息。此修正需要同步更新 [model执行层设计方案.md](model执行层设计方案.md) 中 §9 流式执行 的 `Event` 定义（该文档已提交，属于本次修正范围外的追加改动，见 §9）。
- **Arguments 转换**：只做 `string ↔ []byte` 的类型转换，不做二次 JSON 合法性校验，校验职责保留在 `tool.ToolRuntime.Execute`，避免校验逻辑分散在两处。
- **不支持 `tool_choice`**：非目标，YAGNI，后续有真实需求再扩展 `model.Options`。
- **风险**：OpenAI 对 assistant 消息 `content` 字段在存在 `tool_calls` 时是否允许省略，不同模型版本可能有细微差异；用 `*string` 并在纯工具调用场景下置 `nil` 是目前已知最兼容的做法，需要在真实联调中验证。
- **风险**：`chatCompletionResponse` 目前是匿名内嵌 struct，改动后建议顺手拆成命名类型，便于后续复用于真正的流式响应解析（不在本次范围内实现流式，只做类型整理，降低后续改动成本）。

## 9. 兼容与演进

- `model.Request.Tools` 为新增字段，默认零值（nil）与现状行为完全一致，不影响现有纯文本调用方。
- `model.Event.ToolCall` 同样为新增字段，默认零值（nil）与现状行为完全一致；现有只消费 `Delta`/`Usage`/`FinishReason` 的调用方不需要改动。
- `provider/openai` 之外的 Provider 实现（如未来的 Anthropic 适配器）不受影响，各自决定是否支持 `Tools` 字段和发出 `ToolCall` 事件。
- 后续如需 `tool_choice`、并行工具调用限制等能力，在 `model.Options` 或 `model.Request` 上新增可选字段，不改变本方案确定的结构。
- **遗留事项**：`model.Event`/`model.Collect` 的契约定义原本记录在 [model执行层设计方案.md](model执行层设计方案.md) §9（该文档已提交到 Git，非本次新增文档）。本方案在 §4.2 描述的调整需要同步反映到那份文档，保持"接口变化时同步更新文档"的要求；这是本方案确认范围之外的追加改动，需要单独与用户确认后再编辑那份已提交的文档。

## 10. 测试策略

- `httptest.Server` 用例：请求体正确包含 `tools`，assistant 历史消息（含 `ContentToolCall` 块）正确序列化为 `tool_calls`。
- 响应解析用例：`finish_reason=tool_calls` 时，`Response.Message.Content` 含正确数量和内容的 `ContentToolCall` 块，`FinishReason == model.FinishToolCall`。
- 混合场景：assistant 消息同时包含文本和工具调用时，请求和响应两个方向都能正确往返。
- 回归用例：纯文本请求/响应路径行为不变，现有 `executor_test.go` 全部保持通过。
- `tool` 角色消息、非法角色内容块的校验错误路径。
- 验证命令：

```bash
gofmt -w .
go test ./...
go vet ./...
go test -race ./...
```

## 11. 实施顺序

1. `model` 包契约调整：`Request` 新增 `Tools []tool.Spec` 字段，`Event` 新增 `ToolCall *message.ToolCall` 字段，`Collect` 按 §4.2 调整累积逻辑（`model` 包新增对 `tool` 的依赖）。
2. `provider/openai` 新增请求侧 wire 类型（`openAIMessage`、`openAIToolCall`、`openAITool` 等）及 `toOpenAITools`、调整后的 `toOpenAIMessages`。
3. 调整响应侧 `chatCompletionResponse` 结构和 `toStreamEvents` 组装逻辑（§6），让 `provider/openai` 只按 `model.Event` 契约吐出事件，不自己拼装 `message.Message`。
4. 放开 `validateRequest` 对 `ContentToolCall` 的限制。
5. 补充 httptest 用例覆盖请求/响应两个方向的工具调用往返，以及 `model.Collect` 对 `ToolCall` 事件的累积测试。
6. 执行 `gofmt`、`go test`、`go vet`、`go test -race`。
7. 同步更新 [model执行层设计方案.md](model执行层设计方案.md) §9（遗留事项，见 §9，需单独确认）。

## 12. 关键决策总结

- `model.Request.Tools` 直接复用 `tool.Spec`，不新增镜像类型，`model → tool` 是有意引入的安全叶子依赖。
- 工具调用的响应表达是 `model` 包的抽象契约（`Event.ToolCall` + `Collect`），不是 `provider/openai` 的私有实现细节；这是对早期草案的修正，避免每个 Provider 各自发明一套等价但不统一的实现。
- 工具参数的 JSON 合法性校验职责保留在 `tool` 包，`provider/openai` 只做类型转换。
- 本方案不做真正的 SSE 流式、不做 `tool_choice` 精确控制，均作为后续独立工作。
- `toOpenAIMessages`/`toStreamEvents` 是本次修复的核心：保证多轮工具对话消息历史在请求/响应两个方向都不丢字段，且响应侧的组装逻辑完全落在 `model` 包的公开契约里。
