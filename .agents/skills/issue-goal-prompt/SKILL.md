---
name: issue-goal-prompt
description: 从任务系统条目生成可执行目标提示词，适用于 harness 管理的仓库。当用户要求把任务编号、任务链接、仓库内任务文件、设计文档或测试手册转换成具体实施交接提示词，或需要生成 .agents/state/GOAL-* 长提示词恢复文件时使用；尤其适用于实施前必须编码计划优先、分支门禁、独立评审、live E2E 策略和任务系统回写要求的场景。
---

# 任务目标提示词

为任务生成具体目标提示词。除非用户明确要求继续实施，否则只生成提示词，不执行任务实现。

## 必要输入

- `issue_id`、任务链接或仓库内任务文件路径。
- 可选约束：执行模式、范围限制、来源文档、live E2E 策略、分支 / 提交 / 推送边界、多仓范围。

如果没有任务标识或来源文档，先询问用户。否则从当前仓库和已配置的任务系统中自行发现细节。

## 真相来源

生成提示词前先读取或检查：

1. `AGENTS.md`
2. `docs/harness/control-plane.md`
3. `.agents/PLANS.md`
4. `.agents/plans/TEMPLATE.md`
5. `.agents/prompts/issue-standard-workflow.md`（如存在）
6. `.agents/prompts/orchestrator-thread.md`（需要多任务交接时）
7. `.agents/guides/code-review.md`（如存在）
8. `go.mod`
9. 任务正文、评论、标签 / 状态和链接的来源文档
10. 任务或用户点名的来源文档

如果真相来源冲突，协作状态以任务系统为准，执行约束以仓库文档和代码为准。未能自行消解的冲突必须写进生成的提示词。

## 生成流程

基于当前真相生成目标提示词，不只依赖记忆：

1. 提取目标、包含范围、排除范围、验收矩阵、来源文档、验证命令、停止条件、依赖 / 阻塞。
2. 要求实施前先冻结范围。
3. 要求复杂任务创建或更新 `.agents/plans/YYYY-MM-DD-<issue>-<slug>.md`，并遵循 `.agents/plans/TEMPLATE.md`。
4. 要求每个会写入的仓库先通过分支门禁：
   - 执行 `git status --short --branch`。
   - 除非用户明确允许默认分支工作，否则创建或切换到专用功能分支。
   - 分支名只允许 ASCII 字母、数字、`-`、`_` 和 `/`。
   - 保护已有脏工作区；不得丢弃或覆盖无关改动。
   - 在计划、恢复点和最终回写中记录实际分支名。
5. 在 `gate / freeze` 阶段派生 `review_policy`，并写入 Goal Prompt 与 Goal 状态文件：
   - 用户显式要求独立评审，或任务涉及多仓 / 多可写 lease / integration、安全或公开 contract、schema / 数据、并发 / 幂等 / 重试 / 状态机、release / 生产 / 不可逆副作用、required live E2E、full-auto、自动 merge 或未知风险时使用 `strict`。
   - 调用方未提供 policy 时按 `strict`；其余普通单仓低风险任务才可显式使用 `standard`。
   - `subagent_review_required` 等于 `review_policy == strict`。
   - `standard` 由主 agent 做 findings-first 对抗式自审，`review_owner=main-agent-self-review`；`strict` 必须由 subagent 独立评审，`review_owner=subagent`。
   - 两种 policy 都必须满足 `blocking_findings=none`；strict 下 subagent 不可用时停止在 `blocked: subagent_review_unavailable`。
6. 编码 live E2E 策略：
   - 默认目标是 `pre-commit ready`。
   - 除非用户明确要求，不要 commit、push、merge 或 mark Done。
   - MongoDB live E2E 默认从 issue、来源文档或 `docs/test/*` runbook 推导；没有明确安全环境、数据范围、凭据和清理方式时，在本地验证后停止在 `manual_gate_live_e2e`。
   - 如果 live E2E 不适用，设置 `live_e2e_status: not_required`。
7. 编码精简主线：默认使用 `collect + gate -> freeze + slice -> implement -> verify -> review -> closeout`；只有 fan-out 才进入 dispatch，只有 integration event 才进入 integrate 与 post-integration verify，只有获得授权才进入 PR / MR 可选交付。
8. 编码验证证据复用规则：没有 integration event 时，同 session、同快照、同命令的单仓单写入者 `deterministic-local` 证据可沿用到 closeout；发生 integration event 时必须重新验证。多仓、多 lease、strict、环境依赖、live 或不确定情况重跑，required live E2E 永不复用。
9. 加入任务系统回写要求：验证、评审、live E2E 状态、残余风险、恢复点和下一步；集成与可选交付结果只在进入对应条件分支时回写。

## 本仓项目约束

生成 `mongo-overview-tool` 的目标提示词时，必须编码以下项目事实：

- 本项目是 Go CLI 工具，模块路径固定为 `github.com/SisyphusSQ/mongo-overview-tool/v2`。
- `go.mod` 当前声明 `go 1.26`；除非维护者明确要求，不得顺手调整 Go module 版本。
- MongoDB 官方 Go SDK 固定为 `go.mongodb.org/mongo-driver v1.10.6`；除非维护者明确要求，不升级或降级该依赖。
- 新增或修改 Go import 时按三段式组织：标准库、外部依赖、项目内部包；空分组直接省略。
- 新增测试时用注释写清测试场景；需要调试输出时优先用 `t.Logf`，避免输出敏感连接信息。
- 默认验证矩阵至少包含 `make harness-verify`、`go test ./...`、`make test`、`git diff --check`；如果只生成 prompt，不执行这些命令，只把它们写入目标提示词。
- 不生成具体 `.agents/state/GOAL-*` 文件，除非完整提示词超过 4000 字符或用户明确要求状态文件。

完整提示词、状态文件和短启动提示词模板见 `references/goal-prompt-template.md`。

## 状态文件规则

组装提示词后统计字符数。

- 如果提示词 `<= 4000` 字符，直接返回完整提示词。
- 如果提示词 `> 4000` 字符，把完整提示词写入 `.agents/state/GOAL-<ISSUE-ID>.md`，返回时把路径解析成绝对路径。
- 如果状态文件已经存在，先读取旧文件，保留仍然有效的恢复字段，替换或更新 `## 目标提示词`，并更新 `updated_at`、`prompt_length`、`recovery_point` 和 `next_action`。

默认 harness 的 `.gitignore` 会把真实 `.agents/state/*` 文件当成本地辅助状态。如果用户要求提交生成的 `GOAL` 文件，必须先确认项目有意跟踪该状态文件，或带明确理由更新项目级忽略规则。

短启动提示词必须说明：先完整读取状态文件，只执行其中 `## 目标提示词`，不要只根据短启动提示词开始实现；如果状态文件、任务系统和仓库真相冲突，先基于任务系统和仓库真相重新消解。

## 输出规则

- 只输出生成的完整目标提示词或短启动提示词；如果写入了状态文件，可以额外输出一行说明。
- 在短启动提示词中使用绝对路径。
- 不包含隐藏推理。
- 只生成提示词时，不实施、不提交、不推送、不合并，也不修改任务系统状态；除非用户明确要求这些副作用。
