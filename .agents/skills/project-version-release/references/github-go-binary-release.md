# GitHub Go 多平台归档发布

适用于用户明确要求 GitHub Release 上传 Go CLI 资产的场景。本仓发布 profile 是 Linux、Darwin（macOS）和 Windows 的 amd64、arm64 共六个平台归档，以及一份 checksum 清单。GitHub Release 不上传裸二进制。

## 正式资产契约

`bin/release/` 只能包含以下七个正式资产，文件名不包含版本号：

- `mot.linux.amd64.tar.gz`
- `mot.linux.arm64.tar.gz`
- `mot.darwin.amd64.tar.gz`
- `mot.darwin.arm64.tar.gz`
- `mot.windows.amd64.zip`
- `mot.windows.arm64.zip`
- `SHA256SUMS`

四个 `tar.gz` 内只能有根级 `mot`，模式必须是 `0755`；两个 `zip` 内只能有根级 `mot.exe`。版本由二进制内容、Git tag 和 GitHub Release 标识，不写入资产文件名。

## 前置条件

1. 用户明确给出合法 SemVer，例如 `v2.0.0`。
2. `changeLog.md` 已通过 `Unreleased -> release archive` 写入本次说明。
3. 代码、Makefile、README 和 skill 已在 PR 中验证并合并到 `main`。
4. 本机具备 `tar`、`zip`、`unzip`，以及 `sha256sum` 或 `shasum`。
5. `gh auth status` 成功，远端 tag 与 Release 均不存在。

## 固定流程

1. 在分支完成代码、文档和 Makefile；`bin/` 始终忽略，不随 PR 提交。
2. 合并后切到 `main`，执行 `git pull --ff-only origin main`，记录目标 commit。
3. 执行 `make release VERSION=<version>`。Makefile 必须把 `VERSION` 注入六个平台裸二进制，以 `CGO_ENABLED=0` 和 `-trimpath` 构建；随后生成七个正式资产、写入 `SHA256SUMS` 并自动执行 `release-verify`。`bin/mot.*` 仅是中间产物。
4. 如需对现有产物重新验收，执行 `make release-verify VERSION=<version>`。验证必须覆盖正式资产集合、归档内唯一文件名、tar 的 `0755` 模式、六个平台架构、六项 SHA256，以及本机 Darwin arm64 的 `mot version` 和 `mot -h`。
5. 创建并推送指向该 `main` commit 的 annotated tag：`git tag -a <version> -m <version>`、`git push origin <version>`。
6. 使用一条显式命令上传全部七个资产，不使用 glob：

   ```bash
   gh release create <version> \
     --verify-tag \
     --fail-on-no-commits \
     --title <title> \
     --notes-file <notes> \
     bin/release/mot.linux.amd64.tar.gz \
     bin/release/mot.linux.arm64.tar.gz \
     bin/release/mot.darwin.amd64.tar.gz \
     bin/release/mot.darwin.arm64.tar.gz \
     bin/release/mot.windows.amd64.zip \
     bin/release/mot.windows.arm64.zip \
     bin/release/SHA256SUMS
   ```

   Release Note 必须包含功能、兼容性、发布矩阵、解压方式和 SHA256 校验方式。
7. 回读 Release，逐项确认 tag、目标提交、标题、正文、七个资产名称和非零大小；下载 `SHA256SUMS` 与六个归档，重新执行 checksum 校验。本地最终切回干净的 `main`。

## 停止条件

| 场景 | 必须动作 |
| --- | --- |
| 普通 issue 或仅 changeLog 收口 | 不创建 tag、Release 或资产。 |
| 已存在 tag / Release，或 tag 不指向当前 main | 停止；不得覆盖或删除。 |
| `make release` 或 `make release-verify` 失败 | 停止；不得打 tag 或创建 Release。 |
| `bin/release/` 多于或少于七个正式资产 | 停止；不得上传裸二进制或半成品。 |
| 任一归档缺失、为空、内容不唯一、架构错误、权限错误或 checksum 失败 | 停止；不得打 tag 或创建 Release。 |
| 上传失败 | 停止并保留现状；不得用 `--clobber`、不得自动删除 tag 或 Release。 |
| 上传后回读不一致 | 停止后续发布动作，保留现场并报告差异；不得自动覆盖资产。 |

## 压力场景

| 输入 | 正确行为 |
| --- | --- |
| “给 issue 补 changeLog” | 仅更新 `Unreleased`。 |
| “发 GitHub Go 六平台二进制” | 构建六个平台，但只上传六个归档和 `SHA256SUMS`。 |
| “tag 已存在，直接重发” | 先核对目标；不匹配即停止。 |
| “先发 Release，少一个 Windows arm64” | 停止，补齐并验证全部七个正式资产后再发布。 |
| “把 bin 下的裸文件一起传上去” | 拒绝；裸二进制只作为本地中间产物。 |
