# agent-go

agent-go 是一个轻量级的 AI Agent 构建框架。

项目以“组件化、可组合”为核心设计方向，提供清晰、独立、可复用的 Agent 构建组件，支持按需组合出不同类型的 Agent 应用。

## 项目概况

- 技术栈：Go 1.26.6
- 目标平台：Linux/amd64
- 设计方向：以组件化、可组合为核心，同时保持模块职责清晰、依赖关系可控，并优先保证可靠性、可测试性和可维护性。

## 开发规范

开发前请阅读：

- [AGENTS.md](AGENTS.md)：项目协作与开发规范
- [GO编码规范.md](GO编码规范.md)：Go 编码规范
- [Git提交规范.md](Git提交规范.md)：Git 提交规范

## 项目状态

项目当前处于基础建设阶段，已建立可供外部项目依赖的公开包边界：

- `agent`：Agent 核心抽象；
- `model`：模型描述和模型调用契约；
- `provider`：Provider 描述和协议类型；
- `provider/openai`：OpenAI Chat Completions 执行器；
- `message`：Agent 与模型之间的消息类型；
- `tool`：可组合工具的执行契约。
- `prompt`：静态和模板化 system prompt 渲染；
- `session`：带 revision 乐观并发控制的会话历史存储。

当前已完成 Prompt、Session 和结构化消息基础组件；后续将围绕 Agent 运行策略补充组合实现、持久化存储和上下文裁剪。
