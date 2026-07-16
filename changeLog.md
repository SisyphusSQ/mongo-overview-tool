## Unreleased
<!-- 普通 issue 新增条目只写在本 Unreleased 段；不要写入下面已归档版本段。 -->

### v2.1.0(20260716)
#### feature:
1. 新增 `doctor`、`ops`、`hotspot`、`index-audit` 和 `capacity` 只读诊断命令及对应 Go SDK，支持结构化 finding、collector status、部分结果、脱敏 table/JSON、容量快照与纯离线 diff。
2. 完善慢日志洞察，追加 plan summary、扫描返回比、planning/CPU、匿名 appName、error 和 COLLSCAN 证据，同时保持既有 sort、query hash 与 legacy 标识兼容。
3. 为 `index-audit` 增加 MongoDB 3.4–7.x 分片集群全库索引一致性检查，支持 direct `listIndexes`、`$indexStats`、7.x 官方 metadata cursor、fallback、coverage、二次确认及稳定的 SDK/table/JSON 结果。
4. 新增 diagnostics 与 index consistency Go SDK 示例，支持从独立环境变量组装 mot.Options、处理部分结果并输出脱敏结构化摘要。
#### optimization:
1. 优化 MongoDB 诊断采集的版本、拓扑、权限、数量、并发、超时和 context gate，支持分片数据节点派生连接、双快照 counter reset 处理和 free storage 显式 opt-in。
#### bugFix:
1. GitHub Release 改用 tar.gz/zip 归档资产，保留 Unix 执行权限并增加 `SHA256SUMS` 完整性校验。
2. 修复 MongoDB 3.4 currentOp 聚合路径的 namespace 兼容问题，按 wire version 直接使用 command fallback，并兼容错误码 17138。
3. 修复 top.totals 包含 note 等标量元数据时的 BSON 解码失败，避免生成伪 namespace。
#### note:
1. `index-audit consistency` 仅执行只读检查，不自动创建、删除、隐藏或修改索引。
2. 已完成 MongoDB 3.4、4.2、7.0 副本集/分片集群综合诊断及三段索引一致性策略的真实只读正向验证；环境未预置索引不一致 namespace，因此未通过修改索引构造负向场景。
### v2.0.0(20260710)
#### feature:
1. 新增可嵌入的 Go SDK pkg/mot，支持连接管理、集群概览、集合统计、慢日志分析和批量维护，并返回结构化结果。
2. CLI 改为调用 SDK，保留主要命令和 flag；新增 SDK 示例与默认离线单测。
3. 支持 MongoDB 3.4 等缺少 queryHash 的慢日志场景，以 legacy: 兼容标识衔接概览与详情查询。
#### optimization:
1. 连接、派生节点连接和 cursor 使用调用方 context 与有界清理，并补充 URI 脱敏和可识别的取消或部分结果错误。
#### note:
1. GitHub Release 提供 Linux、Darwin（macOS）和 Windows 的 amd64、arm64 原始二进制文件。

## v1.1.0(20260214)
#### feature:
1. 支持 Mongo Shell 查询语法：`--filter` 与 `--update` 参数支持使用 `ISODate()`, `ObjectId()`, 无引号键名, 单引号字符串等 Shell 风格语法，自动转换为 Extended JSON，提升 CLI 易用性

#### optimization:
1. 完善 `pkg/mongo/bson` 测试覆盖：新增 `bson_test.go`，覆盖 Shell 语法转换、混合写法及边界场景
2. 更新 `bulk` 命令文档：增加 Shell 语法示例，优化 flag 说明


## v1.0.0(20260212)
#### feature:
1. 新增 `bulk-delete`、`bulk-update` 子命令：支持按批次大小与暂停间隔的流控批量删除/更新，支持 `--dry-run` 试运行与 `-o` 日志输出。
2. 新增 `pkg/progress` 进度条：批量操作时展示百分比、处理速度（docs/sec）与 ETA。
3. 新增 `utils/signal`：注册 SIGINT/SIGTERM，支持批量操作等场景的优雅退出（当前批次完成后退出）。
4. 新增 `pkg/mongo/bson`：`ParseBsonDoc` 解析 filter/update 的 JSON 与 ExtJSON（如 `$date`、`$oid`）。
5. 新增 `pkg/mongo/cluster`：集群类型检测（副本集/分片）及 `ClusterInfo`，供 overview 等命令区分拓扑。
6. 新增 `pkg/mongo/errors`：`IsRetryableCursorError` 判断游标错误是否可重试（游标超时、主从切换、网络类错误等）。
7. 配置层新增 `BulkConfig`（Database、Collection、Filter、Update、BatchSize、PauseMS、DryRun、Output），并接入 `cmd/bulk`。

#### optimization:
1. `utils/retry`：新增 `DoWithContext` 支持 context 取消；`Do`/`DoCondition` 增加指数退避上限 `maxBackoff`(30s)、最后一次失败不再等待。
2. `utils/timeutil`：提供 CST 时区常量与 `FormatLayoutString`，供日志等统一时间格式。
3. README 更新：补充 bulk 命令参数与示例、密码脱敏、分片集群行为差异、并发控制说明及项目结构。

#### bugFix:
1. 修正 service 文件名拼写：`overveiw_srv.go` 重命名为 `overview_srv.go`。
