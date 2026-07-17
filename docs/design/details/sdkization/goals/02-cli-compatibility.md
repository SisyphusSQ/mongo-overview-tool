# 目标 2：CLI 行为兼容

## 目标定义

SDK 化不能让现有 `mot` CLI 用户感知到不必要的行为变化。SDK 是内部能力重组和外部复用入口，不是重新设计 CLI 产品。

兼容范围包括：

- 命令名称。
- flag 名称和默认值。
- 认证信息来源。
- 终端输出中的主要字段。
- 密码脱敏行为。
- 批量操作的 dry-run、进度、日志输出和中断行为。

## 当前 CLI 契约

现有命令：

```text
mot overview
mot coll-stats
mot check-shard
mot slowlog
mot bulk-delete
mot bulk-update
mot version
```

通用连接参数：

```text
--uri
--target / -t（完整 host:port）
--host
--port / -P
--username / -u
--password / -p
--authSource
--debug
```

环境变量：

```text
MONGO_USER
MONGO_PASS
```

这些入口在 SDK 化过程中默认保持。

连接地址兼容规则：`-t/--target` 接收完整 `host:port`，默认值为 `127.0.0.1:27017`；`--host` 与 `-P/--port` 保留用于拆分传参。显式 `--target` 覆盖拆分的 host/port，`--uri` 继续拥有最高优先级。

## 兼容策略

CLI 迁移后的职责：

```text
cmd flags -> internal/config -> mot.Options -> pkg/mot SDK -> internal/clioutput -> stdout
```

关键原则：

- `cmd` 继续持有 Cobra command 和 flag 默认值。
- `internal/config` 继续服务 CLI，可读取环境变量。
- `pkg/mot` 不读取 CLI flag 和环境变量。
- `internal/clioutput` 负责把 SDK result 渲染成当前表格。

## 输出兼容边界

必须保持的输出信息：

- `overview`：URI 脱敏、Hosts、repl、host、state、conn、qr、qw、ar、aw、size、memUsed、memRes、delay、uptime、version。
- `coll-stats`：Database、TotalSize、ns、documents、avgObjSize、storageSize。
- `check-shard`：在 `coll-stats` 基础上展示 `isSharded`；默认展示尚未分片集合，`--show-all` 展示全部集合，指定 `--coll` 时展示目标集合。
- `slowlog`：Database、Total、TimeFrame、ns、queryHash、op、count、maxMills、minMills、maxDocs、firstTs、lastTs。
- `bulk-delete` / `bulk-update`：summary、dry-run 提示、进度、完成统计、可选文件日志。

允许改进但必须谨慎的内容：

- 内部排序稳定性。
- 列宽计算。
- 错误消息措辞。
- 调试日志格式。

不应在 SDK 化顺手改变的内容：

- 命令名。
- flag 名称。
- 默认 batch size。
- 默认 pause。
- 默认系统库过滤行为。
- 密码脱敏策略。

## Formatter 设计

新增 CLI formatter 包：

```text
internal/clioutput
```

示例接口：

```go
func PrintOverview(w io.Writer, result *mot.OverviewResult, opts OverviewPrintOptions) error
func PrintCollectionStats(w io.Writer, result *mot.CollectionStatsResult, opts CollectionStatsPrintOptions) error
func PrintSlowlogSummary(w io.Writer, result *mot.SlowlogSummaryResult, opts SlowlogPrintOptions) error
```

规则：

- formatter 可以使用 `fatih/color`。
- formatter 可以写 stdout。
- formatter 可以持有 CLI 专属列宽和 humanize 逻辑。
- formatter 不执行 MongoDB 查询。
- formatter 不修改 SDK result。

## Bulk CLI 适配

批量操作的 CLI 兼容依赖 observer：

```go
type cliBulkObserver struct {
    writer io.Writer
    file   *bufio.Writer
    bar    *progress.ProgressBar
}
```

职责：

- 打印 summary。
- 更新进度条。
- 写 `--output` 文件日志。
- 处理完成提示。

信号处理也留在 CLI：

```go
ctx, cancel := context.WithCancel(context.Background())
stop := setupSignalCancel(cancel)
defer stop()

result, err := client.BulkDelete(ctx, opts)
```

SDK 只感知 context 被取消，不直接注册 SIGINT / SIGTERM。

## Golden Test

为了保护 CLI 输出，应为 `internal/clioutput` 增加 golden test。

建议结构：

```text
internal/clioutput/testdata/
  overview_repl.golden
  overview_sharding.golden
  coll_stats.golden
  check_shard.golden
  slowlog_summary.golden
  bulk_summary.golden
```

测试方式：

1. 构造固定 SDK result fixture。
2. 调用 formatter 写入 buffer。
3. 与 golden 文件对比。

如果后续确实需要修改输出格式，应显式更新 golden 并在变更说明中解释原因。

## 落地步骤

1. 先抽 `internal/clioutput`，保留现有输出格式。
2. 为现有输出补 fixture 和 golden test。
3. 将 `overview` CLI 改为调用 SDK result，再通过 formatter 输出。
4. 逐个迁移 `coll-stats`、`check-shard`、`slowlog`。
5. 最后迁移 `bulk-delete` / `bulk-update`。

## 验收标准

- SDK 化后现有 CLI 命令仍可执行。
- `mot <cmd> -h` 中主要 flag 未丢失。
- formatter golden test 通过。
- 迁移前后的典型输出字段一致。
- `MONGO_USER` / `MONGO_PASS` 仍只在 CLI 层生效。

## 风险

最大风险是把 CLI 兼容压到 SDK 层，导致 SDK 被迫保留终端输出、颜色和进程信号等副作用。

处理原则是：CLI 兼容由 `cmd` 和 `internal/clioutput` 承担；SDK 只提供结构化数据和错误语义。
