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
