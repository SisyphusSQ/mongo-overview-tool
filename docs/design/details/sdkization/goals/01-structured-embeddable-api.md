# 目标 1：稳定、结构化、可嵌入的 Go API

## 目标定义

SDK 化的第一目标是让外部 Go 项目能直接 import 并调用 `mongo-overview-tool` 的核心能力，而不是只能通过 shell 执行 `mot` 命令并解析终端输出。

目标 API 必须满足：

- 可 import：包路径稳定，外部项目不依赖 `internal/*`。
- 可组合：调用方可以把结果接入自己的日志、告警、Web API、任务系统或巡检平台。
- 可测试：调用方可以 mock 或用 fixture 验证自己的业务逻辑。
- 可演进：公开类型和错误语义有明确兼容边界。

## 现状差距

当前完整业务编排集中在 `internal/service`，外部项目不能 import。`pkg/mongo` 虽然可 import，但它是偏底层的 MongoDB 命令封装，不提供完整的 overview / collection stats / slowlog / bulk 工作流。

当前 service 方法多以 `error` 作为唯一返回值，业务结果通过 `PrintSrv` 打印。这使得调用方无法拿到结构化结果，也无法在自己的程序里复用。

## 公开包选择

推荐新增公开包：

```text
pkg/mot
```

导入路径：

```go
import "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
```

选择 `mot` 的理由：

- 与 CLI 二进制名 `mot` 对齐。
- 比 `sdk` 更具业务语义。
- 后续可以承载统一 facade，而不是让外部项目直接依赖多个底层包。

## API 入口形态

推荐以 `Client` 为主入口：

```go
client, err := mot.NewClient(ctx, mot.Options{
    URI: "mongodb://root:password@127.0.0.1:27017/admin",
})
if err != nil {
    return err
}
defer client.Close(ctx)

overview, err := client.Overview(ctx, mot.OverviewOptions{})
```

核心公开方法：

```go
func NewClient(ctx context.Context, opts Options) (*Client, error)
func NewClientFromMongoClient(ctx context.Context, client *mongo.Client, opts ClientOptions) (*Client, error)
func (c *Client) Close(ctx context.Context) error

func (c *Client) Overview(ctx context.Context, opts OverviewOptions) (*OverviewResult, error)
func (c *Client) CollectionStats(ctx context.Context, opts CollectionStatsOptions) (*CollectionStatsResult, error)
func (c *Client) SlowlogSummary(ctx context.Context, opts SlowlogOptions) (*SlowlogSummaryResult, error)
func (c *Client) SlowlogDetail(ctx context.Context, db, queryHash string) (*SlowlogDetailResult, error)
func (c *Client) BulkDelete(ctx context.Context, opts BulkOptions) (*BulkResult, error)
func (c *Client) BulkUpdate(ctx context.Context, opts BulkUpdateOptions) (*BulkResult, error)
```

## Options 设计原则

SDK options 使用显式字段，不直接读取 CLI flag 或环境变量：

```go
type Options struct {
    URI        string
    Host       string
    Port       int
    Username   string
    Password   string
    AuthSource string

    ConnectTimeout time.Duration
    Direct          *bool
    Logger          Logger
}
```

规则：

- `URI` 优先级最高。
- `URI` 为空时由 `Host` / `Port` / `Username` / `Password` / `AuthSource` 构造连接串。
- `DefaultOptions()` 提供默认值，如 `Host=127.0.0.1`、`Port=27017`、`AuthSource=admin`。
- `BuildURI(opts)` 是纯函数，不修改 `opts`，不初始化日志，不读取环境变量。
- CLI 层负责把 `MONGO_USER` / `MONGO_PASS` 转换成 SDK options。

## 可嵌入要求

SDK 代码必须避免以下行为：

- 直接调用 `os.Exit`。
- 直接读取命令行参数。
- 直接从环境变量读取连接信息。
- 直接向 stdout / stderr 输出业务信息。
- 直接注册进程级信号处理。
- 强制创建或关闭调用方传入的 `*mongo.Client`。

## 错误语义

公开 API 需要稳定错误类型：

```go
var (
    ErrInvalidOptions      = errors.New("invalid options")
    ErrUnsupportedTopology = errors.New("unsupported topology")
    ErrDangerousOperation  = errors.New("dangerous operation")
    ErrCancelled           = errors.New("operation cancelled")
)
```

约束：

- 参数错误必须能通过 `errors.Is(err, ErrInvalidOptions)` 判断。
- MongoDB driver 原始错误必须保留，调用方可通过 `errors.As` 取回。
- 部分成功场景使用结构化错误，错误中携带 result。

## 落地步骤

1. 新增 `pkg/mot` 包和空的 `Client` facade。
2. 新增 `Options`、`DefaultOptions`、`BuildURI`、`RedactURI`。
3. 支持 `NewClient` 和 `NewClientFromMongoClient`。
4. 先实现只读 API：`Overview`、`CollectionStats`、`SlowlogSummary`、`SlowlogDetail`。
5. 再实现写操作 API：`BulkDelete`、`BulkUpdate`。
6. CLI 改为调用 `pkg/mot`，不再直接使用 `internal/service` 作为业务真相。

## 验收标准

- 外部包可以成功 import `github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot`。
- `pkg/mot` 不 import `cmd`、`internal/config`、`internal/service`。
- 公开方法返回结构化 result，不以终端输出作为结果载体。
- 所有公开方法都有 options、result、错误语义单测。
- `go test ./...` 通过。

## 反模式

以下做法不满足目标：

- 只把 `internal/service` 复制或移动到 `pkg/service`。
- SDK 方法仍只返回 `error`，结果继续打印到终端。
- 公开 API 使用 CLI cfg 类型作为入参。
- SDK 自动读取 `MONGO_USER` / `MONGO_PASS`。
- SDK 为了兼容 CLI 而调用 `os.Exit` 或 `fmt.Println`。
