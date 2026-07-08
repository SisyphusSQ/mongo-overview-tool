# 目标 3：SDK 核心层无 CLI 副作用

## 目标定义

SDK 核心层必须是可嵌入库，而不是带命令行行为的工具函数集合。调用方在自己的服务、任务系统或测试环境中使用 SDK 时，不应被 SDK 的终端输出、文件写入、信号处理或进程退出影响。

核心要求：

- 不直接打印。
- 不直接注册系统信号。
- 不直接写本地文件。
- 不使用终端颜色。
- 不创建进度条。
- 不调用 `os.Exit`。

## 当前副作用来源

当前副作用集中在 service 和工具层：

- `PrintSrv` 直接调用 `fmt.Printf`、`fmt.Println` 和 `color.*`。
- `BulkSrv` 直接打印 summary、dry-run 提示、完成提示。
- `BulkSrv` 直接创建文件日志 writer。
- `BulkSrv` 直接调用 `utils.SetupSignalHandler()`。
- `BulkSrv` 直接使用 `progress.NewProgressBar`。
- `utils.PrintCost` 直接打印执行耗时。

这些行为对 CLI 合理，但对 SDK 不合理。

## 分层边界

推荐边界：

| 能力 | SDK 核心层 | CLI adapter |
| --- | --- | --- |
| MongoDB 查询 | 是 | 否 |
| 业务结果聚合 | 是 | 否 |
| 参数校验 | 是 | CLI 也可做早期校验 |
| stdout / stderr 输出 | 否 | 是 |
| 表格渲染 | 否 | 是 |
| 颜色 | 否 | 是 |
| 进度条 | 否 | 是 |
| 文件日志 | 否 | 是 |
| SIGINT / SIGTERM | 否 | 是 |
| `os.Exit` | 否 | 只允许 `main` / Cobra 顶层处理 |

## SDK 观察者机制

需要向调用方报告进度时，使用回调或 observer，而不是直接输出。

批量操作示例：

```go
type BulkObserver interface {
    OnBulkStart(ctx context.Context, total int64)
    OnBulkBatch(ctx context.Context, batch BulkBatchResult)
    OnBulkRetry(ctx context.Context, err error, attempt int)
    OnBulkDone(ctx context.Context, result BulkResult)
}
```

SDK 调用 observer 的规则：

- observer 为空时不做任何事。
- observer panic 不应被吞掉，除非明确设计 `RecoverObserverPanic`。
- observer 不参与业务决策，只接收事件。
- observer 不应持有 SDK 内部可变状态指针。

## 日志接口

SDK 可以支持可选 logger，但必须默认 no-op：

```go
type Logger interface {
    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)
}
```

规则：

- SDK 不初始化全局 logger。
- SDK 不强依赖 `zap` 具体类型。
- CLI 可以把现有 logger 适配为该接口。
- 日志内容必须脱敏 URI 密码。

## 输出迁移方案

现有 `PrintSrv` 的能力迁移到：

```text
internal/clioutput
```

`internal/clioutput` 可以保留：

- 颜色。
- 表格列宽。
- humanize。
- stdout writer。
- golden test。

SDK 结果结构中只保留原始数值，不保留颜色字符串。例如：

```go
type NodeOverview struct {
    CacheSizeBytes int64
    CacheUsedBytes int64
}
```

CLI formatter 再把它渲染为：

```text
size=4.0GB memUsed=76.3%
```

## 文件日志迁移

`--output` 是 CLI 功能，不是 SDK 功能。

迁移后：

- CLI 根据 flag 打开文件。
- CLI 实现 observer，把批次事件写入文件。
- SDK 不知道文件路径。
- SDK 不管理文件 flush / close。

这样 SDK 调用方可以把进度接入自己的日志系统，而不被迫使用本地文件。

## 信号处理迁移

当前 `utils.SetupSignalHandler()` 适合 CLI，但 SDK 不应注册进程级信号。

迁移后：

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

stop := cliSignalCancel(cancel)
defer stop()

result, err := client.BulkDelete(ctx, opts)
```

SDK 只检查 `ctx.Done()` 或把 ctx 传给 MongoDB driver。

## 静态检查建议

后续可以增加项目检查，限制 `pkg/mot` 中出现以下 import：

```text
fmt
os
os/signal
github.com/fatih/color
github.com/SisyphusSQ/mongo-overview-tool/pkg/progress
```

注意：`fmt` 可能用于 `fmt.Errorf`。如果要机械检查，需要区分 `fmt.Errorf` 和 `fmt.Print*`，第一轮可以先用 review gate，不急着脚本化。

## 落地步骤

1. 新增 `internal/clioutput`，复制并适配 `PrintSrv` 的输出能力。
2. 在 `pkg/mot` 中返回结构化 result。
3. CLI command 调用 SDK 后交给 formatter 输出。
4. Bulk 迁移为 observer 模式。
5. 删除或收敛 service 中的直接输出逻辑。

## 验收标准

- `pkg/mot` 中没有 `fmt.Print*`、`color.*`、`progress.NewProgressBar`、`signal.Notify`、`os.Exit`。
- SDK 单测不需要捕获 stdout。
- CLI 输出测试只覆盖 `internal/clioutput`。
- Bulk 进度和文件日志由 CLI observer 实现。

## 风险

如果副作用边界不清，SDK 用户在 Web 服务中调用 `BulkDelete` 时可能突然看到 stdout 输出、进程信号被覆盖、文件被创建。这会让 SDK 不可嵌入。

因此本目标是 SDK 化的硬边界，不应为了少改代码而放松。
