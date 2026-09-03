# agent-go

agent-go 是一个轻量级的 AI Agent 构建框架，以“可导出的 Go 库”形式发布：
你可以在自己的 Go 工程里通过模块路径直接 import 它的组成包，按需组合出不同类型的 Agent 应用。

项目以“组件化、可组合”为核心设计方向，提供清晰、独立、可复用的 Agent 构建组件。

## 项目概况

- 技术栈：Go 1.26.6
- 目标平台：Linux/amd64
- 模块路径：`github.com/JIAOZAI1/agent-go`
- 设计方向：以组件化、可组合为核心，同时保持模块职责清晰、依赖关系可控，并优先保证可靠性、可测试性和可维护性。

## 安装与使用（作为 Go 库）

将 agent-go 加入你的 Go 模块依赖：

```bash
# 拉取指定发布版本（推荐，使用带 vX.Y.Z 的语义化 tag）
go get github.com/JIAOZAI1/agent-go@v0.1.0
# 或拉取 main 分支的最新未打 Tag 版本（伪版本）
go get github.com/JIAOZAI1/agent-go@latest
```

在代码里按包名引入所需组件，例如接入一个带工具调用能力的 Agent：

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/JIAOZAI1/agent-go/agent"
    "github.com/JIAOZAI1/agent-go/message"
    "github.com/JIAOZAI1/agent-go/model"
    "github.com/JIAOZAI1/agent-go/provider/openai"
    "github.com/JIAOZAI1/agent-go/session"
    "github.com/JIAOZAI1/agent-go/strategy/toolloop"
    "github.com/JIAOZAI1/agent-go/tool"
)

func main() {
    ctx := context.Background()

    // 1) 模型执行器：绑定你选用的 provider + 具体 model。此处用 openai，key 从环境取。
    executor, err := openai.NewExecutor(
        model.Ref{ProviderID: "openai", ModelID: "gpt-4o"},
        openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")},
    )
    if err != nil {
        panic(err)
    }

    // 2) 工具运行时：按需 AddTool / AddMiddleware。这里先不注册任何工具。
    tools, err := tool.NewBuilder().Build()
    if err != nil {
        panic(err)
    }

    // 3) 持有会话历史（可选；Store 为 nil 即无状态单轮）
    store := session.NewMemoryStore()
    key := session.Key{Scope: "demo", ID: "session-1"}

    // 4) 注册可复用策略实例，并构建冻结后的 Runtime。
    loop, err := toolloop.New(toolloop.Options{})
    if err != nil {
        panic(err)
    }
    factory := agent.NewDefaultStrategyFactory()
    if err := factory.Register("tool-loop", loop); err != nil {
        panic(err)
    }
    if err := factory.Default("tool-loop"); err != nil {
        panic(err)
    }
    runtime, err := agent.NewRuntimeBuilder().Executor(executor).Tools(tools).
        Session(store).StrategyFactory(factory).Build()
    if err != nil {
        panic(err)
    }

    // 5) 执行一轮，得到最终回复与运行统计。
    result, err := runtime.Run(ctx, agent.Request{
        Strategy:   "tool-loop",
        SessionKey: key,
        Input:      message.Text(message.RoleUser, "你好，请帮我查一下今天的天气。"),
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Stats) // 内含 turn/step/tool 与 token 统计
}
```

说明：

- `agent` 下层细分为可单独引入的原子包（`model` / `message` / `tool` / `prompt` / `session` 等），可按需选择，避免整包耦合。
- `agent` 提供不可变 `AgentRuntime`、Request/Result 和策略工厂；策略通过独立 `scope` 包读取运行能力与单次状态。
- 版本间的发布通过语义化 tag 自动完成：可 `git tag vX.Y.Z && git push origin vX.Y.Z` 触发 GitHub Actions `release` workflow 创建 GitHub Release（见仓库 `.github/workflows/release.yml`）。

## 包目录

| 包 | 说明 |
| --- | --- |
| `agent` | AgentRuntime、Request/Result、Strategy 与策略工厂 |
| `scope` | Env、单次运行 Scope、统计状态和公开 Builder |
| `event` | 运行领域事件、EventSink 与 FanoutSink |
| `strategy/toolloop` | 支持多轮工具调用的模型循环策略 |
| `strategy/singleturn` | 只执行一次模型生成、不暴露工具的单轮策略 |
| `model` | 模型描述、请求响应与 Executor 契约 |
| `provider` | Provider 描述与协议类型 |
| `provider/openai` | OpenAI Chat Completions 执行器（含工具调用）| 
| `message` | 结构化消息类型
| `tool` | Tool Spec/Call/Result 与注册执行运行时（Runtime/Builder）| 
| `prompt` | 静态与模板化 system prompt 渲染 |
| `session` | 带 revision 乐观锁的会话历史存储 |
| `session/sqlite` | `session.Store` 的 SQLite 持久化实现（纯 Go，见下方依赖提示）|
| `trim` | 在送入模型前对加载的会话历史做受限上下文裁剪 |
| `builtin` | 预写好的文件/命令工具对象（NewRead/NewEdit/NewBash），需手动 `AddTool` 后才生效；默认不对任何 Agent 自动挂载 |
| `examples` | 可直接 `go run` 的组件拼接演示（见 [examples/README.md](examples/README.md)）|

> **依赖提示**：本库大多数子包零第三方依赖。`session/sqlite` 为 `session.Store` 提供 SQLite 持久化，实现基于纯 Go 的 `modernc.org/sqlite`；只有当你 import 该子包时，对应依赖才会进入你的 module 依赖图。

## 当前状态与路线

项目正处于基础组件陆续落地阶段：已完成结构化消息、Prompt、Session（含 `session/sqlite` SQLite 持久化参考实现）、工具系统、OpenAI 工具调用执行器，以及运行策略(含 ToolLoop 与 RunScope 状态管理)与一个用于有界上下文的 `trim` 流水线；CI 与 Release 已自动化，`examples/` 下提供了可离线的 `go run` 演示。后续将围绕更多的运行策略以及更多 provider 适配逐步补充。
