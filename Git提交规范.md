# Git 提交规范（AI 执行版）

当用户要求创建 Git 提交时，必须遵循以下规范。

## 1. 提交格式

提交信息遵循 Conventional Commits：

```text
<type>(<scope>): <subject>

[可选正文]

[可选脚注]
```

## 2. Type 类型

根据变更的主要目的选择一个类型：

| Type | 使用场景 |
| --- | --- |
| `feat` | 新增功能 |
| `fix` | 修复缺陷 |
| `refactor` | 重构代码，既不新增功能，也不修复缺陷 |
| `perf` | 性能优化 |
| `test` | 新增或修改测试 |
| `docs` | 仅修改文档 |
| `style` | 仅调整格式，不改变代码逻辑 |
| `build` | 修改构建系统、依赖或打包配置 |
| `ci` | 修改 CI/CD 配置或脚本 |
| `chore` | 其他维护性变更 |
| `revert` | 回滚已有提交 |

不得为了方便而统一使用 `chore`。应根据变更的实际目的选择最准确的类型。

## 3. Scope 范围

`scope` 表示本次变更主要影响的业务模块、包或组件，例如：

```text
feat(order): add order cancellation API
fix(auth): reject expired refresh tokens
perf(database): reduce order query round trips
test(payment): add refund service tests
```

要求：

- 优先使用仓库中已有且稳定的模块名称。
- 使用小写、简短的名称。
- 不确定或变更横跨多个模块时可以省略。
- 不得编造仓库中不存在的模块名称。

## 4. Subject 摘要

`subject` 必须准确概括本次提交的主要变化：

- 使用简洁的祈使句。
- 默认使用英文；如果仓库历史统一使用中文，则遵循现有风格。
- 英文首字母小写，末尾不加句号。
- 建议不超过 72 个字符。
- 描述代码发生了什么变化，不要只写任务编号。
- 禁止使用 `update`、`changes`、`fix bug`、`WIP` 等含义模糊的描述。

正确示例：

```text
feat(payment): add idempotency check for callbacks
fix(order): prevent duplicate inventory deduction
refactor(config): centralize environment loading
docs(api): document pagination parameters
```

错误示例：

```text
update code
fix bug
WIP
修改代码
feat: #123
```

## 5. 正文与脚注

简单变更只需要标题。复杂变更应添加正文，说明：

- 为什么需要修改。
- 关键实现或设计取舍。
- 与旧行为的差异。
- 兼容性、配置或迁移影响。

正文重点解释“为什么”，不要逐行复述代码。每行建议不超过 72 个字符。

关联任务时使用脚注：

```text
Closes #123
Refs #456
```

只有在确实知道任务编号及其关系时才能添加，不得猜测或编造编号。

## 6. 破坏性变更

如果变更破坏已有 API、配置、数据库结构或外部行为，必须在类型或范围后添加 `!`，并使用 `BREAKING CHANGE:` 说明影响和迁移方式：

```text
feat(api)!: change pagination response structure

BREAKING CHANGE: replace total_page with total and page_size.
Clients must update response parsing before deployment.
```

不得遗漏已识别出的破坏性影响。

## 7. 原子提交

每个提交只包含一个逻辑目标，并满足以下要求：

- 代码、测试和对应文档可以放在同一个提交中。
- 无关修改必须拆分到其他提交或保留在工作区。
- 不得把用户原有的无关改动纳入提交。
- 不得提交调试代码、临时文件、日志或构建产物。
- 不得提交密钥、密码、Token、私钥、生产配置或敏感数据。
- 不得擅自修改、覆盖或丢弃用户尚未提交的改动。

如果当前改动包含多个独立目标，应拆分为多个提交，并分别使用准确的提交信息。

## 8. AI 提交前检查

创建提交前，AI 必须：

1. 查看仓库状态和实际差异，确认提交范围。
2. 区分本次任务改动与用户原有改动。
3. 检查暂存区内容，确保没有意外文件或敏感信息。
4. 根据实际差异生成提交信息，不得仅根据用户描述推测。
5. 执行与改动相关的格式化、测试或静态检查；无法执行时说明原因。
6. 确认提交中不存在未解决的冲突标记。
7. 只有用户明确要求提交时才执行 `git commit`。

推荐检查命令：

```bash
git status --short
git diff
git diff --cached
```

Go 项目根据改动范围执行：

```bash
gofmt -w <changed-go-files>
go vet ./...
go test ./...
```

涉及并发逻辑时，执行：

```bash
go test -race ./...
```

除非用户明确要求，不得为了格式化本次修改而改动无关文件。

## 9. AI 提交行为边界

- 用户只要求修改代码时，不得默认创建提交。
- 用户要求“提交”时，可以暂存本次任务相关文件并创建本地提交。
- 未经明确授权，不得执行 `git push`。
- 未经明确授权，不得执行变基、提交修订或历史重写。
- 不得对公共分支执行强制推送。
- 不得使用 `--no-verify` 绕过提交钩子，除非用户明确授权。
- 提交失败时应保留现场并报告原因，不得通过危险操作强行完成。
- 提交完成后，应报告提交哈希、提交标题、包含的文件和检查结果。

## 10. 完整示例

简单功能：

```text
feat(user): add account deactivation endpoint
```

缺陷修复：

```text
fix(auth): prevent expired tokens from passing validation
```

带正文和任务关联：

```text
fix(order): prevent duplicate payment processing

Check the callback idempotency key before creating a payment record.
This avoids duplicate charges when the provider retries a callback.

Closes #123
```

破坏性变更：

```text
feat(config)!: replace YAML configuration with environment variables

BREAKING CHANGE: YAML configuration files are no longer loaded.
Set the documented environment variables before upgrading.
```

## 11. 最终决策原则

生成提交信息时，依次判断：

1. 本次提交的主要目的是什么，以此确定 `type`。
2. 主要影响哪个模块，以此确定 `scope`；无法准确确定时省略。
3. 用一句话准确描述可观察到的变化，形成 `subject`。
4. 是否需要解释原因、设计取舍或迁移方式，决定是否添加正文。
5. 是否关联已知任务或存在破坏性变更，决定是否添加脚注。

最终提交信息必须以实际差异为依据，做到准确、简洁、可追溯。
