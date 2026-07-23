# 目标提示词模板

生成任务目标提示词时使用本参考模板。占位符内容来自任务系统、仓库文档、来源文档和用户约束。最终提示词必须足够具体，让另一个 agent 不依赖原始聊天也能执行。

## 完整目标提示词

```text
执行 <ISSUE-ID>：<ISSUE-TITLE>。

工作仓库：
- 主仓库：<ABSOLUTE-REPO-PATH>
- 相关仓库：<ABSOLUTE-REPO-PATHS-OR-NONE>

当前模式：
- issue_provider: <linear|github|gitlab|repo|other>
- mode: <propose-only|plan-only|create-issues|implement-no-merge|full-auto>
- review_policy: <standard|strict>
- subagent_review_required: <true|false>
- default_boundary: pre-commit ready，除非用户明确要求 commit / push / merge / Done

分支要求：
- 实施前必须在每个会写入的仓库创建或切换到专用功能分支，除非用户明确要求默认分支工作。
- 分支名只允许英文、数字、`-`、`_`、`/`；建议使用 `<issue-id-lower>-<english-slug>`。
- 跨仓任务默认各仓使用同一个基于任务编号的英文短标识。
- 切分支前必须执行 `git status --short --branch`，确认当前分支和工作区状态。
- 如果工作区已有未提交改动，必须先保护这些改动；安全时保留工作区切分支，否则使用 stash/reapply，或在存在丢失风险时停止确认。
- 不得丢弃、覆盖或回滚无关用户改动。
- 计划、`recovery_point` 和最终 `writeback` 必须记录实际使用的分支名。

目标：
<来自任务和用户约束的目标>

成功标准：
- <验收标准 1>
- <验收标准 2>
- <验收标准 3>

必须先读取：
- <ABSOLUTE-REPO-PATH>/AGENTS.md
- <ABSOLUTE-REPO-PATH>/docs/harness/control-plane.md
- <ABSOLUTE-REPO-PATH>/.agents/PLANS.md
- <ABSOLUTE-REPO-PATH>/.agents/plans/TEMPLATE.md
- <ABSOLUTE-REPO-PATH>/.agents/prompts/issue-standard-workflow.md（如存在）
- <ABSOLUTE-REPO-PATH>/.agents/prompts/orchestrator-thread.md（多 thread / worktree / subagent 编排时，如存在）
- <ABSOLUTE-REPO-PATH>/.agents/guides/code-review.md（如存在）
- <ABSOLUTE-REPO-PATH>/go.mod
- 任务 <ISSUE-ID> 正文、评论、标签 / 状态和链接文档
- <任务或用户点名的来源文档>

范围：
包含：
- <包含项>

排除：
- <排除项>

Harness 要求：
1. 先基于任务系统、来源文档和仓库现状冻结范围。
2. 进入实现前必须完成分支门禁：每个可写仓库都在专用功能分支上，且已保护既有未提交改动。
3. 复杂任务必须创建或更新 `.agents/plans/YYYY-MM-DD-<issue>-<slug>.md`。
4. 计划必须遵循 `.agents/plans/TEMPLATE.md`，至少包含：
   - 目标（对应 `Goal`）
   - 范围和非目标（对应 `Scope and Non-Goals`）
   - 范围冻结（对应 `Scope Freeze`）
   - 上下文和定位（对应 `Context and Orientation`）
   - 架构 / 数据流（对应 `Architecture / Data Flow`）或等价实现设计章节，并补齐：
     - 真实入口与触发
     - 输入装配与边界校验
     - 组件职责与代码落点
     - 关键执行时序
     - 停止 / 错误 / 恢复
   - 关键对象片段（对应 `Reference Snippets`）
   - 具体步骤（对应 `Concrete Steps`）
   - 验证和验收（对应 `Validation and Acceptance`）
   - 评审摘要（对应 `Review Summary`）
   - 结果与复盘（对应 `Outcomes & Retrospective`）
5. 如果任务信息不足以冻结范围，先停在 plan-only，不开始实现。
6. 如果发现范围过大、依赖缺失或写入范围失控，先回写阻塞项，不硬做。

实现要求：
- 严格按冻结范围实施。
- 修改某个目录下代码前，读取就近 `AGENTS.md`。
- 新增或变更外部可见接口、schema、contract、runbook 或示例时，同步相关仓库真相。
- 不保存真实凭据、token、Cookie、完整 URL、真实数据库主机、真实 SQL 或其它敏感输出到提交版文档。
- 本仓是 Go CLI 工具，模块路径固定为 `github.com/SisyphusSQ/mongo-overview-tool/v2`。
- 不得顺手调整 `go.mod` 中的 `go 1.26`。
- 不得顺手升级或降级 `go.mongodb.org/mongo-driver v1.10.6`。
- Go import 按标准库、外部依赖、项目内部包三段式组织，空分组直接省略。
- 新增测试时用注释写清测试场景；调试输出优先用 `t.Logf`，避免输出敏感连接信息。

评审要求：
- gate / freeze 阶段根据冻结范围派生 review_policy；用户显式要求独立评审时无条件使用 strict。
- 多仓 / 多可写 lease / integration、安全 / contract、schema / 数据、并发 / 幂等 / 重试 / 状态机、release / 生产 / 不可逆副作用、required live E2E、full-auto、自动 merge 或未知风险时必须 strict。
- 未提供 policy 的旧调用按 strict；只有不命中 strict 条件的普通单仓低风险任务才可显式使用 standard。
- standard 由主 agent 做 findings-first 对抗式自审，`review_owner: main-agent-self-review`。
- strict 由 subagent 独立评审，`review_owner: subagent`；不可用时停止在 `blocked: subagent_review_unavailable`。
- 两种 policy 都必须满足 `blocking_findings=none`；有阻塞发现时修复并重新执行验证 -> 评审。

Live E2E 要求：
- live_e2e_required: <true|false|derive-from-issue>
- MongoDB live E2E 默认从 issue、来源文档或 `docs/test/*` runbook 推导。
- 如果 live E2E 为必需项且当前环境具备明确安全目标、数据范围、凭据和清理方式，必须在目标内完成 live E2E。
- 如果 live E2E 为必需项但缺少环境、凭据、安全测试目标、数据范围、清理方式或人工确认，完成本地实现和本地验证后停止在 `manual_gate_live_e2e`，明确列出缺什么；此时只能判定为本地就绪，不能判定为完全完成。
- 如果 live E2E 不适用，记录 `live_e2e_status: not_required`。

验证矩阵：
- `make harness-verify`
- `go test ./...`
- `make test`
- `git diff --check`
- <任务或 runbook 要求的额外命令>

验证证据与复用：
- `verification_summary` 记录 `evidence_id`、有序命令和结果、`execution_session_id`、`verification_type`、`verified_at` 与 `repository_path`。
- `verification_type` 只能是 `deterministic-local`、`environment-dependent` 或 `live`。
- 没有 integration event 时，同一 session、同一快照、同一命令顺序的单仓单写入者 deterministic-local 证据可沿用到 closeout。
- 多仓、多 lease、strict、环境依赖、live 或不确定情况必须重跑；required live E2E 永不复用。
- 发生 integration event 时必须执行 post-integration verify；不得用集成前证据替代。

任务系统回写：
完成或阻塞时，给任务 <ISSUE-ID> 追加评论，或写入仓库任务回写日志，至少包含：
- verification_summary
- review_summary，其中明确 `review_policy`、`subagent_review_required`、`review_owner` 与 `blocking_findings`
- writeback_summary
- live_e2e_status
- residual_risks
- recovery_point
- next_action

条件回写：
- 发生 integration event 时补 integration_summary、post_integration_verify_summary。
- 进入可选交付阶段时补 pr_prep_summary、merge_summary。

停止条件：
- 任务系统 / 仓库真相冲突且无法自行判断
- 写入范围需要扩大
- verify 失败
- 必需评审存在阻塞发现
- 必需 live E2E 不可用
- 需要真实凭据、生产环境或人工确认

最终回复：
- 汇总改动文件
- 汇总验证命令和结果
- 汇总评审结果
- 汇总 live_e2e_status
- 汇总未执行项、残余风险和下一步
```

## 状态文件模板

完整提示词超过 4000 字符时使用本模板。

```markdown
# GOAL-<ISSUE-ID>

- `state_id`: GOAL-<ISSUE-ID>
- `updated_at`: <ISO8601 timestamp>
- `execution_issue`: <ISSUE-ID>
- `issue_provider`: <linear|github|gitlab|repo|other>
- `mode`: implement-no-merge
- `full_auto_allowed`: false，除非用户明确要求
- `goal_state`: ready_to_execute
- `prompt_length`: <字符数>
- `plan_required`: true
- `review_policy`: <standard|strict>
- `subagent_review_required`: <true|false>
- `live_e2e_policy`: required_if_available_else_manual_gate
- `pre_commit_boundary`: true
- `branch_required`: true
- `branch_policy`: create_or_switch_dedicated_feature_branch_before_implementation
- `recovery_point`: 读取本文件、任务条目、来源文档、当前计划和仓库状态
- `next_action`: 执行下方目标提示词

## 目标提示词

<完整目标提示词>
```

## 短启动提示词

```text
执行 <ISSUE-ID>：<ISSUE-TITLE>。

完整目标提示词已写入：
<ABSOLUTE-REPO-PATH>/.agents/state/GOAL-<ISSUE-ID>.md

请先完整读取该文件，再按其中 `## 目标提示词` 执行。
不要只根据本短启动提示词开始实现。
默认边界是 pre-commit ready；不要 commit、push、merge 或 mark Done，除非用户另行明确要求。
如果状态文件、任务系统和仓库文档之间冲突，以任务系统 + 仓库真相为准，并先更新状态文件的 recovery_point / next_action。
```
