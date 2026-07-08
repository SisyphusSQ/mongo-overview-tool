# 目标 6：写操作安全、进度和部分成功语义

## 目标定义

`bulk-delete` 和 `bulk-update` 是会修改 MongoDB 数据的能力。SDK 化后，它们可能被服务端程序、自动化任务或巡检平台直接调用，因此必须比 CLI 更明确地表达安全边界、进度事件和部分成功状态。

核心要求：

- 危险操作显式确认。
- dry-run 语义清晰。
- 批次进度可观察。
- context 取消可停止。
- 部分成功可返回。
- 错误不吞掉已完成事实。

## 当前行为

当前 bulk service 支持：

- `Database` / `Collection` 必填。
- `Filter` 字符串解析。
- `BatchSize` 和 `PauseMS`。
- `DryRun`。
- 批次执行。
- 游标错误重试。
- Ctrl+C 优雅退出。
- 可选文件日志。

但当前实现把安全逻辑、进度条、文件日志、信号处理和业务执行混在一起，不适合 SDK 嵌入。

## 安全选项

```go
type BulkOptions struct {
    Database   string
    Collection string
    Filter     any
    BatchSize  int
    Pause      time.Duration
    DryRun     bool

    AllowEmptyFilter bool
    MaxRetries       int
    Observer         BulkObserver
}
```

`BulkUpdateOptions`：

```go
type BulkUpdateOptions struct {
    BulkOptions
    Update any
}
```

## 参数校验

必需规则：

- `Database` 非空。
- `Collection` 非空。
- `BatchSize > 0`。
- `BatchSize <= 50000`。
- `Pause >= 0`。
- `BulkUpdateOptions.Update` 非空。
- `Filter` 能解析为合法 BSON 文档。
- `Update` 能解析为合法 MongoDB update 文档。

危险操作规则：

- `Filter` 为空且 `DryRun=false` 时，必须设置 `AllowEmptyFilter=true`。
- 如果 `AllowEmptyFilter=false`，返回 `ErrDangerousOperation`。
- `DryRun=true` 时允许空 filter，但结果必须清楚标记 `DryRun=true`。

## 空 Filter 语义

空 filter 在 MongoDB 中表示匹配全部文档。CLI 当前默认 filter 是 `{}`，这对命令行人工操作可以接受，但 SDK 自动化调用风险更高。

SDK 推荐行为：

```go
if isEmptyFilter(filter) && !opts.DryRun && !opts.AllowEmptyFilter {
    return result, fmt.Errorf("%w: empty filter requires AllowEmptyFilter", ErrDangerousOperation)
}
```

CLI 为保持兼容，可以在命令层决定是否传入 `AllowEmptyFilter=true`。如果未来要收紧 CLI 行为，应单独设计，不在 SDK 化中顺手改变。

## 进度 Observer

```go
type BulkObserver interface {
    OnBulkStart(ctx context.Context, total int64)
    OnBulkBatch(ctx context.Context, batch BulkBatchResult)
    OnBulkRetry(ctx context.Context, err error, attempt int)
    OnBulkDone(ctx context.Context, result BulkResult)
}
```

事件语义：

- `OnBulkStart`：count 完成后触发，传入匹配总量。
- `OnBulkBatch`：每个批次执行成功后触发。
- `OnBulkRetry`：遇到可重试游标错误并准备重试时触发。
- `OnBulkDone`：正常完成或 dry-run 完成时触发。

CLI 可用 observer 实现：

- 终端 summary。
- 进度条。
- 文件日志。
- 完成提示。

SDK 不直接输出这些内容。

## 部分成功结果

写操作失败时必须返回已完成事实：

```go
type BulkResult struct {
    Database     string
    Collection   string
    DryRun       bool
    MatchedTotal int64
    Processed    int64
    Deleted      int64
    Matched      int64
    Modified     int64
    BatchCount   int
    StartedAt    time.Time
    FinishedAt   time.Time
}
```

错误类型：

```go
type PartialError struct {
    Op     string
    Result BulkResult
    Err    error
}
```

调用方可以：

```go
result, err := client.BulkDelete(ctx, opts)
if err != nil {
    var partial *mot.PartialError
    if errors.As(err, &partial) {
        result = &partial.Result
    }
}
```

## Retry 语义

当前游标重试逻辑可以保留，但 SDK contract 必须写清楚：

- 默认只重试游标打开或遍历中的瞬态错误。
- 已完成批次不回滚。
- delete 场景中已删除文档不会再次匹配。
- update 场景默认要求 update 操作幂等，尤其是 `$set` 这类操作。
- 非幂等 update 应由调用方谨慎使用。

`MaxRetries`：

- `0` 表示使用默认值，推荐 `3`。
- 负数非法。

## Cancel 语义

context 取消时：

- 停止继续读取游标。
- 不再执行新批次。
- 当前正在执行的 MongoDB 调用由 driver 响应 ctx。
- 返回当前 `BulkResult`。
- 错误可通过 `errors.Is(err, ErrCancelled)` 判断。

Pause 必须可取消，不能直接 `time.Sleep`。

## Dry-run 语义

dry-run 只执行 count：

- 不打开 `_id` 游标。
- 不执行 delete / update。
- 返回 `BulkResult{DryRun: true, MatchedTotal: total}`。
- 触发 `OnBulkStart` 和 `OnBulkDone`，不触发 batch。

## 落地步骤

1. 在 `pkg/mot` 定义 bulk options、result、observer、partial error。
2. 抽出 filter / update 解析，支持 string、`bson.D`、`bson.M`。
3. 实现参数校验和危险操作拦截。
4. 实现 dry-run result。
5. 实现批次执行和 observer。
6. 实现 context cancel 和 partial result。
7. CLI bulk 命令改为 observer adapter。

## 验收标准

- 空 filter 写操作默认返回 `ErrDangerousOperation`。
- dry-run 不执行写操作。
- context cancel 返回部分结果。
- 批次失败返回部分结果。
- CLI 进度条和文件日志不在 SDK 核心层。
- bulk 单测覆盖 delete、update、dry-run、危险 filter、取消、retry、partial error。

## 风险

写操作的风险比只读能力高。若 SDK 只复制当前 CLI 行为，外部自动化调用可能因为空 filter 或不可见的部分成功造成数据事故。写操作应最后迁移，并把安全 contract 先固定下来。
