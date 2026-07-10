---
name: project-version-release
description: 当维护项目版本、changeLog、release archive 或发布策略说明，或需要为 Go 项目在 GitHub Release 上传交叉编译二进制资产时使用。
---

# Project Version Release

用于项目内版本、changeLog 和 release policy 的通用维护。

核心规则：issue 是执行粒度，release 是发布粒度。不要因为一个 issue 完成就自动提升 release version。

## 第一步

1. 先读取根级 `AGENTS.md`、`docs/harness/control-plane.md` 和项目 release 文档。
2. 写文件前先判断本次变更属于哪一类。
3. 先用 dry-run 跑检查：

```bash
python3 .agents/skills/project-version-release/scripts/project_version_release.py check \
  --repo "$PWD" --json
```

## 先分类再变更

| 分类 | 含义 |
| --- | --- |
| `issue-only` | 只更新 issue / run summary，不提升 release version。 |
| `changelog-only` | 只追加到 `changeLog.md -> Unreleased`，不归档 release。 |
| `version-bump` | 项目版本文件发生变化，需要显式版本号和验证入口。 |
| `release-archive` | 正在发布 artifact，需要把 `Unreleased` 归档为真实 release 段。 |
| `policy-plan` | release policy 需要操作计划；本 skill 只输出 operator intent。 |

```bash
python3 .agents/skills/project-version-release/scripts/project_version_release.py classify \
  --repo "$PWD" \
  --changed-files <文件路径...> --json
```

## 写入规则

- 脚本默认 dry-run。
- 只有目标动作明确后才添加 `--write`。
- 脚本不得执行 `git push`、修改 Issue Tracker、连接数据库、上传对象存储或发布 artifact。
- `changeLog.md` 使用 `Unreleased + release archive` 模式；issue 结果先写入 `Unreleased`，只有真实 release 时才归档。
- 用户未明确指定版本号时，不得根据日期、issue 编号、提交内容或历史版本自行创建版本号。
- stable / beta / dev 版本都必须使用合法 SemVer 字符串，建议带 `v` 前缀。

## changeLog 目标段强制规则

- 普通 issue 收口、bug fix、文档变更、API 示例或 schema 可见面变更，都不是 release archive；对应条目仍然写入 `## Unreleased`。
- 手工编辑 `changeLog.md` 前，必须先在 plan 或回复中写明目标段：`changeLog.md -> Unreleased -> #### <category>:`。
- 已存在的 `### vX.Y.Z(YYYYMMDD)` 都视为已归档 release 段；除非用户明确要求执行真实 release/archive 并给出版本号与日期，不得把新的 issue 条目追加到历史版本段。
- 如果文件结构是 `## Unreleased` 后紧跟最新版本段，不要把第一个版本段当成本次变更目标。

## GitHub Go 多平台二进制 Release

本仓唯一 changelog 路径是 `changeLog.md`。当用户明确要求 GitHub Release 附带 Go 交叉编译二进制时，必须完整读取 [references/github-go-binary-release.md](references/github-go-binary-release.md)。

- 先完成 changeLog、代码和 Makefile 的 PR 合并；二进制仅在合并后的 `main` 构建，不进入 Git。
- 版本必须显式传给 Makefile；先验证全部资产，再创建 annotated tag，最后用 `gh release create --verify-tag` 一次性上传。
- 缺少资产、tag 指向错误、Release 已存在或任何校验失败时立即停止；不得用 `--clobber`，不得自动覆盖或删除 tag / Release。
- 普通 issue、仅 changeLog 收口或没有用户明确发布意图时，不得触发 GitHub 发布。

## 详细策略

当任务涉及 release 边界、channel policy、兼容性或外部发布流程时，读取 [references/project-version-policy.md](references/project-version-policy.md)。
