# Project Mechanical Constraints

## 文档定位

本文件登记当前项目的项目级机械约束：哪些工程边界已经变成可执行检查，哪些还只是文档约束，哪些计划后续接入。

它不定义通用 lint 规则，也不预设某个业务项目的架构边界。初始化后，项目维护者需要基于真实代码、架构文档、运行入口和协作规则补齐本文件。

固定原则：

- 没有可执行命令或 gate 时，不得假装 `enforced`
- `enforced` 必须能对应到本地命令、CI、linter、script、test、contract diff、E2E 或 review gate
- `documented` 只表示已有文档规则，不表示机器会拦截
- `partial` 必须说明哪些部分已机械化，哪些仍需人工 review
- 项目专属规则不要写进 base harness 模板本身，先登记到本文件，再按项目选择 linter / script / test / E2E 载体

## 状态枚举

| Status | 含义 |
| --- | --- |
| `enforced` | 已有可执行命令或 gate 会在违反时失败 |
| `partial` | 部分已机械化，仍有人工 review 或后续补齐项 |
| `documented` | 只有文档约束，尚无可执行检查 |
| `planned` | 已决定后续接入，但当前没有规则或命令 |
| `not_applicable` | 当前项目明确不适用 |

## 分类枚举

| Category | 典型内容 |
| --- | --- |
| `architecture` | 分层、依赖方向、目录职责、模块边界 |
| `contract` | API / schema / DTO / OpenAPI / provider-consumer contract |
| `runtime` | 配置、环境变量、日志、指标、trace、启动方式 |
| `verification` | 测试矩阵、E2E、live self-test、构建和验证入口 |
| `docs` | 设计文档、runbook、计划、结果摘要和链接同步 |
| `security` | 权限、副作用、危险命令 |
| `cross-repo` | provider / consumer / shared truth 分层与同步 |

## 维护循环关联

Maintenance loop 默认扫描本文件，用来判断项目规则是否仍停留在文档层、是否需要建 issue，或是否已具备升级为机械检查的条件。

| Maintenance Tag | 含义 |
| --- | --- |
| `maintenance_candidate` | 维护循环应定期扫描该规则是否漂移，但当前不一定适合机械化 |
| `rule_promotion_candidate` | 重复 review finding 或已有稳定命令，适合评估升级为机械检查 |
| `human_decision_required` | 涉及产品、API、安全、数据或跨团队取舍，需要人类确认后才能修改 |

固定规则：

- maintenance loop 发现 `documented` 规则长期未机械化时，只能报告或建议建 issue，不得自动把它改成 `enforced`。
- repeated review finding 可以升级为 `project-check`、linter、contract diff、E2E 或 harness check，但必须先写清 evidence、目标 `Rule ID`、执行命令、回归验证和回滚方式。
- `rule_promotion_candidate` 只是候选标签，不代表已经允许自动新增检查脚本或 CI。

## 约束登记表

| Rule ID | Category | Rule | Source | Enforcement | Command | Status | Maintenance Tag | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `RUNTIME-001` | `runtime` | Go module 版本保持 `go 1.26`，初始化或文档调整不得顺手改动 `go.mod` 语言版本 | `go.mod` / `AGENTS.md` | review | N/A | `documented` | `maintenance_candidate` | 当前没有独立 project-check；`go test ./...` 会按当前 toolchain 编译 |
| `CONTRACT-001` | `contract` | MongoDB 官方 Go SDK 固定为 `go.mongodb.org/mongo-driver v1.10.6` | `go.mod` / `AGENTS.md` | module graph / test / review | `go test ./...` | `partial` | `human_decision_required` | 依赖版本由 `go.mod` 记录；测试能暴露编译兼容性，但不能阻止人工改版本 |
| `CONTRACT-002` | `contract` | Go module、项目内 import 与构建 ldflags 统一使用 `github.com/SisyphusSQ/mongo-overview-tool/v2` | `go.mod` / `Makefile` / `AGENTS.md` | project check | `make project-check` | `enforced` | `maintenance_candidate` | 检查 module path、`VARS_PKG` 和 Go 源码中的旧本仓 import；`make harness-verify` 同时执行该检查 |
| `STYLE-001` | `architecture` | Go import 按标准库、外部依赖、项目内部包三段式组织，空分组省略 | `AGENTS.md` | review / formatter | `gofmt -w <files>` | `partial` | `rule_promotion_candidate` | `gofmt` 只能规范格式，不能完整表达三段式语义 |
| `VERIFY-001` | `verification` | 稳定回归入口是 harness check、Go 单测和当前 Makefile 构建目标 | `README.md` / `Makefile` | test / build | `make harness-verify`; `go test ./...`; `make test` | `enforced` | `maintenance_candidate` | 三个命令均应在初始化或功能变更后执行 |
| `DOCS-001` | `docs` | `.agents/plans/` 只跟踪计划协议和主模板；真实计划、示例计划和运行态计划文件默认不提交 | `.gitignore` / `AGENTS.md` | git ignore / review | `git check-ignore .agents/plans/EXAMPLE-implementation.md` | `partial` | `maintenance_candidate` | `.agents/plans/TEMPLATE.md` 是唯一默认提交的 plan 目录 markdown |
| `DOCS-002` | `docs` | `docs/test/` 只跟踪通用模板；具体 runbook 和脱敏结果默认本地忽略，原始结果进入 `.agents/runs/` | `.gitignore` / `AGENTS.md` | harness / git ignore | `make harness-verify` | `enforced` | `maintenance_candidate` | `docs/test/RUNBOOK_TEMPLATE.md` 必须保持可跟踪 |
| `TEST-001` | `verification` | 新增测试应注释说明测试场景，调试输出优先用 `t.Logf` 且避免敏感信息 | `AGENTS.md` | review | N/A | `documented` | `rule_promotion_candidate` | 暂无机械检查 |

## `project-check` 挂载协议

base harness 不默认生成 `project-check`，也不生成永远 pass 的占位脚本。

当项目已有稳定的项目级机械约束后，可以按需补充：

```text
scripts/project-checks/
  check.sh
  check-architecture.sh
  check-contracts.sh
  check-runtime.sh
  check-docs.sh
```

推荐 Makefile 入口：

```makefile
project-check:
	bash scripts/project-checks/check.sh
```

固定要求：

- 一旦某条规则标记为 `enforced`，`Command` 必须指向真实可执行入口
- `project-check` 可以汇总项目专属检查，但不替代 `make harness-check`
- `make harness-check` 只校验本文件作为登记入口存在且结构完整，不替项目臆造项目规则
- 违反规则时，失败信息应说明违反了哪条 `Rule ID`、参考哪个 `Source`、应运行或修复哪个 `Command`

## 初始化后补齐步骤

1. 从 `AGENTS.md`、目录级 `AGENTS.md`、README、架构文档和现有 Makefile 里提取项目不可违反的规则。
2. 先把规则登记到上方表格，并诚实标注 `Status`。
3. 已有命令或 gate 的规则，补齐 `Enforcement` 和 `Command`。
4. 只有文档约束的规则，保持 `documented`，不要写成 `enforced`。
5. 后续把稳定规则逐步接入 linter、script、test、contract diff、E2E 或 CI。
6. 为每条规则补齐 `Maintenance Tag`，让 maintenance loop 能区分扫描、升级和人工决策边界。
