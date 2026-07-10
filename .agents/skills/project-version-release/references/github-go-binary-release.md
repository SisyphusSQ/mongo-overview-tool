# GitHub Go 多平台二进制发布

适用于用户明确要求 GitHub Release 上传 Go CLI 二进制资产的场景。本仓发布 profile 是 Linux、Darwin（macOS）和 Windows 的 amd64、arm64 共六个文件。

## 前置条件

1. 用户明确给出合法 SemVer，例如 `v2.0.0`。
2. `changeLog.md` 已通过 `Unreleased -> release archive` 写入本次说明。
3. 代码、Makefile、README 和 skill 已在 PR 中验证并合并到 `main`。
4. `gh auth status` 成功，远端 tag 与 Release 均不存在。

## 固定流程

1. 在分支完成代码、文档和 Makefile；`bin/` 始终忽略，不随 PR 提交。
2. 合并后切到 `main`，执行 `git pull --ff-only origin main`，记录目标 commit。
3. 执行 `make release VERSION=<version>`。Makefile 必须把 `VERSION` 注入二进制版本，并使用 `CGO_ENABLED=0`、`-trimpath` 生成：

   - `bin/mot.linux.amd64`
   - `bin/mot.linux.arm64`
   - `bin/mot.darwin.amd64`
   - `bin/mot.darwin.arm64`
   - `bin/mot.windows.amd64.exe`
   - `bin/mot.windows.arm64.exe`

4. 检查六个文件都非空；用 `file` 确认 Linux 为 ELF、Darwin 为 Mach-O、Windows 为 PE；在本机架构上运行 `mot version`，确认显示目标版本。
5. 创建并推送指向该 `main` commit 的 annotated tag：`git tag -a <version> -m <version>`、`git push origin <version>`。
6. 使用 `gh release create <version> --verify-tag --fail-on-no-commits --title <title> --notes-file <notes>`，在同一命令附带六个资产。Release Note 必须包含功能、兼容性和发布矩阵。
7. 回读 Release：tag、目标提交、标题、正文、资产名称和非零大小均要匹配；本地最终切回干净的 `main`。

## 停止条件

| 场景 | 必须动作 |
| --- | --- |
| 普通 issue 或仅 changeLog 收口 | 不创建 tag、Release 或资产。 |
| 已存在 tag / Release，或 tag 不指向当前 main | 停止；不得覆盖或删除。 |
| 任一资产缺失、为空或格式错误 | 停止；不得打 tag 或创建 Release。 |
| 上传失败 | 停止并保留现状；不得用 `--clobber`、不得自动删除 tag 或 Release。 |

## 压力场景

| 输入 | 正确行为 |
| --- | --- |
| “给 issue 补 changeLog” | 仅更新 `Unreleased`。 |
| “发 GitHub Go 六平台二进制” | 执行本流程并读取全部 gate。 |
| “tag 已存在，直接重发” | 先核对目标；不匹配即停止。 |
| “先发 Release，少一个 Windows arm64” | 停止，补齐并验证全部六个文件后再发布。 |
