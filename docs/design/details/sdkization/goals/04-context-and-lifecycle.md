# 目标 4：`context.Context` 与生命周期控制

## 目标定义

所有 SDK 方法必须接受 `context.Context`，并把取消、超时和连接生命周期控制权交给调用方。SDK 不应在核心路径中自行使用不可取消的 `context.Background()`。

目标是支持以下场景：

- Web 请求超时后自动取消 MongoDB 查询。
- 定时任务被调度器取消后停止批量操作。
- 调用方统一管理 MongoDB 连接池。
- 测试中可以用短超时快速验证取消语义。

## 现状差距

当前 `pkg/mongo.NewMongoConn` 内部固定：

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
```

`Close()` 也使用：

```go
return c.Client.Disconnect(context.Background())
```

这导致：

- 调用方无法控制连接超时。
- 调用方无法取消正在进行的连接或断开操作。
- 批量操作中如果 service 自行处理信号，外部系统无法统一管理取消。

## API 设计

所有公开方法都接受 ctx：

```go
func NewClient(ctx context.Context, opts Options) (*Client, error)
func (c *Client) Close(ctx context.Context) error

func (c *Client) Overview(ctx context.Context, opts OverviewOptions) (*OverviewResult, error)
func (c *Client) CollectionStats(ctx context.Context, opts CollectionStatsOptions) (*CollectionStatsResult, error)
func (c *Client) SlowlogSummary(ctx context.Context, opts SlowlogOptions) (*SlowlogSummaryResult, error)
func (c *Client) SlowlogDetail(ctx context.Context, db, queryHash string) (*SlowlogDetailResult, error)
func (c *Client) BulkDelete(ctx context.Context, opts BulkOptions) (*BulkResult, error)
func (c *Client) BulkUpdate(ctx context.Context, opts BulkUpdateOptions) (*BulkResult, error)
```

规则：

- SDK 不保存长期业务 ctx 到 `Client` 结构体。
- `Client` 可以保存连接和 immutable options。
- 每次操作用调用方传入的 ctx。
- 内部 goroutine 必须从同一个 ctx 派生。

## 连接构造

推荐 `NewClient` 行为：

1. 调用 `BuildURI(opts)`。
2. 构造 MongoDB driver options。
3. 如果 `opts.ConnectTimeout > 0`，从传入 ctx 派生连接超时 ctx。
4. `mongo.Connect(ctx, clientOptions)`。
5. `client.Ping(ctx, readPreference)`。
6. 返回 `*Client`。

如果 `mongo.Connect` 成功但 `Ping` 失败，构造函数必须使用有界 cleanup context 断开已创建的 client，避免失败重试积累连接资源。

Overview / Slowlog 访问副本集成员时，派生连接 URI 必须保留原 URI 的认证、TLS、证书、压缩和 timeout 参数，并重新计算 `directConnection`、`replicaSet`、`readPreference` 等拓扑参数。注入 client 若未提供可用于派生的 `ClientOptions.URI`，需要成员连接的能力应返回明确的配置错误。

示例：

```go
func NewClient(ctx context.Context, opts Options) (*Client, error) {
    if opts.ConnectTimeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, opts.ConnectTimeout)
        defer cancel()
    }

    // connect and ping with ctx
}
```

## 已有连接注入

外部服务通常已有统一连接池，应支持：

```go
func NewClientFromMongoClient(ctx context.Context, client *mongo.Client, opts ClientOptions) (*Client, error)
```

规则：

- SDK 不拥有注入的 `*mongo.Client`，默认不在 `Close` 中断开。
- 可通过 `ClientOptions.OwnsMongoClient` 明确声明是否由 SDK 负责关闭。
- 注入连接时仍可执行可选 ping 或 cluster detect。

示例：

```go
type ClientOptions struct {
    URI             string
    OwnsMongoClient bool
    Logger          Logger
}
```

## Close 语义

```go
func (c *Client) Close(ctx context.Context) error
```

规则：

- `Close` 幂等。
- 如果 client 不拥有底层连接，`Close` 返回 nil。
- 如果 ctx 已取消，直接返回 ctx 错误或 driver disconnect 错误。
- 非空 ctx 原样传给 driver；nil ctx 仅作为防御性输入归一化为 `context.Background()`。
- 业务查询不得脱离调用方 ctx；只有失败连接、派生成员连接和 cursor 的资源释放可以使用 `context.WithoutCancel(ctx)` 派生的有界 cleanup context，避免业务取消后跳过清理。

## 批量操作取消

Bulk 操作需要在以下点检查 ctx：

- 统计总量前。
- 打开游标前。
- 每次 `cur.Next(ctx)`。
- 每个批次执行前。
- `Pause` 等待期间。
- retry sleep 期间。

`Pause` 不应使用不可取消的 `time.Sleep`：

```go
select {
case <-ctx.Done():
    return result, ctx.Err()
case <-time.After(opts.Pause):
}
```

## 错误映射

context 错误应保留：

```go
if errors.Is(err, context.Canceled) {
    return result, fmt.Errorf("%w: %w", ErrCancelled, err)
}
if errors.Is(err, context.DeadlineExceeded) {
    return result, fmt.Errorf("%w: %w", ErrCancelled, err)
}
```

调用方可以：

```go
if errors.Is(err, mot.ErrCancelled) {
    // handle cancellation
}
```

## 落地步骤

1. 在 `pkg/mongo` 增加 context-aware 构造函数，例如 `NewMongoConnWithContext(ctx, uri, opts)`。
2. 保留旧 `NewMongoConn(uri)` 作为兼容包装，内部调用新函数。
3. `Close` 增加 context-aware 版本。
4. `pkg/mot.Client` 全面使用 context-aware 方法。
5. Bulk pause 和 retry sleep 改为可取消等待。
6. CLI 信号处理改为 cancel ctx。

## 验收标准

- `pkg/mot` 所有公开方法首参为 `context.Context`。
- SDK 业务查询路径不使用不可取消的 `context.Background()`；nil ctx 防御性归一化和有界资源清理除外。
- context canceled 单测能快速通过，不等待真实长超时。
- `Close(ctx)` 支持调用方控制断开超时。
- Bulk 取消时返回部分结果和可识别错误。

## 风险

如果 context 没有贯穿，SDK 在上层服务中可能造成请求泄漏、长时间占用连接、调度器取消无效。写操作尤其危险，因为无法及时停止会扩大线上影响范围。
