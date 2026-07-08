# SDK 化详细设计

## 文档定位

本文描述 `mongo-overview-tool` 从单一 CLI 工具演进为可嵌入 Go SDK 的详细设计。设计目标是让外部 Go 项目可以直接复用 MongoDB 拓扑探测、集合统计、慢日志分析和批量维护能力，同时保持现有 `mot` CLI 的用户行为稳定。

本文只定义设计和迁移边界，不包含本次代码实现。

## 背景

当前项目定位是命令行工具，README 中描述的能力都通过 `cmd/*` 触发：

- `overview`：查看副本集或分片集群概览。
- `coll-stats`：查看数据库和集合统计。
- `check-shard`：检查集合分片状态。
- `slowlog`：聚合分析慢查询日志。
- `bulk-delete` / `bulk-update`：带批次和暂停控制的批量维护操作。

代码中已经存在可复用的底层能力：

- `pkg/mongo`：MongoDB 连接、拓扑探测、`rs.status` / `serverStatus` / `collStats` / 慢日志聚合、BSON Shell 语法解析等。
- `internal/service`：各 CLI 命令的业务编排。
- `cmd`：Cobra 命令、参数解析、配置预处理和服务调用。

但当前还不能直接作为 SDK 使用，核心原因是业务编排和 CLI 展示耦合较强。

## 现状问题

### 1. 业务编排位于 `internal`

`internal/service` 包含 `OverviewSrv`、`CollStatsSrv`、`SlowlogSrv`、`BulkSrv` 等主能力，但 Go 语言的 `internal` 机制会阻止仓库外部项目 import。

这意味着外部用户只能 import `pkg/mongo` 的低层封装，无法复用完整的 overview / slowlog / bulk 工作流。

### 2. Service 返回值不是 SDK 结果

当前 service 方法主要返回 `error`：

```go
type OverviewSrv interface {
    GetOverview() error
    Close()
}
```

结果通过 `PrintSrv` 直接打印到终端，而不是返回结构化数据。SDK 调用者需要的是可编程返回值，例如 `OverviewResult`、`CollectionStatsResult`、`BulkResult`。

### 3. 业务逻辑直接依赖 stdout / color / progress

`internal/service/print_srv.go` 负责格式化表格、颜色和终端输出。`internal/service/bulk_srv.go` 还直接使用：

- `fmt.Println` / `fmt.Printf`
- `fatih/color`
- `pkg/progress`
- 文件日志
- SIGINT / SIGTERM 信号处理

这些都是 CLI adapter 的职责，不应进入 SDK 核心层。

### 4. 配置预处理带有 CLI 假设

`internal/config.BasePreCheck` 目前同时承担：

- 初始化全局 logger。
- 从 `MONGO_USER` / `MONGO_PASS` 读取认证信息。
- 拼接 URI。
- 修改传入 cfg 的派生字段。
- 校验 URI 中是否存在 `@`。

SDK 需要显式 options 和纯函数配置构造；环境变量读取、命令行默认值和打印调试日志应留在 CLI 层。

### 5. 连接生命周期不完全受调用方控制

`pkg/mongo.NewMongoConn` 内部使用固定的 `context.Background()` 和 10 秒超时；`Close()` 也使用 `context.Background()`。SDK 用户通常需要统一管理：

- 请求级超时。
- 调用链取消。
- 连接池生命周期。
- 已有 `*mongo.Client` 注入。

### 6. 测试基线不稳定

当前 `go test ./...` 会执行 `pkg/mongo/client_test.go` 中的真实 MongoDB 连接测试，固定连接 `mongod:27017`。没有本地测试 MongoDB 时会失败。

SDK 化会增加公开 API 面，必须先保证默认单测不依赖外部 MongoDB；真实连接测试应进入 integration test profile。

## 设计目标

1. 提供稳定、结构化、可嵌入的 Go API。
2. CLI 行为兼容现有用户预期，命令名、参数、默认输出尽量不变。
3. SDK 核心层不直接打印、不注册系统信号、不写本地文件、不使用终端颜色。
4. 所有 SDK 方法接受 `context.Context`，尊重调用方取消和超时。
5. 公开类型表达真实业务结果，而不是 CLI 表格字符串。
6. 写操作必须有清晰的安全开关、进度回调和部分成功语义。
7. 默认 `go test ./...` 不依赖外部 MongoDB。

### 设计目标细化文档

| 序号 | 目标 | 细化文档 |
| --- | --- | --- |
| 1 | 稳定、结构化、可嵌入的 Go API | [01-structured-embeddable-api.md](goals/01-structured-embeddable-api.md) |
| 2 | CLI 行为兼容 | [02-cli-compatibility.md](goals/02-cli-compatibility.md) |
| 3 | SDK 核心层无 CLI 副作用 | [03-core-side-effect-boundary.md](goals/03-core-side-effect-boundary.md) |
| 4 | `context.Context` 与生命周期控制 | [04-context-and-lifecycle.md](goals/04-context-and-lifecycle.md) |
| 5 | 公开类型表达真实业务结果 | [05-result-type-contracts.md](goals/05-result-type-contracts.md) |
| 6 | 写操作安全、进度和部分成功语义 | [06-write-operation-safety.md](goals/06-write-operation-safety.md) |
| 7 | 默认测试不依赖外部 MongoDB | [07-default-test-baseline.md](goals/07-default-test-baseline.md) |

## 非目标

1. 不在 SDK 化过程中升级 `go.mod` 的 Go 版本。
2. 不升级或降级 `go.mongodb.org/mongo-driver v1.10.6`，除非维护者另行确认。
3. 不重写 MongoDB 官方 driver 的能力；SDK 是业务工作流封装，不是通用 MongoDB driver 替代品。
4. 不改变现有 CLI 的默认命令语义。
5. 不在第一轮引入额外大型框架或新的日志体系。

## 目标架构

推荐分层：

```text
cmd/
  root.go
  overview.go
  coll_stats.go
  check_shard.go
  slowlog.go
  bulk.go
      |
      v
internal/clioutput/
  overview_table.go
  coll_stats_table.go
  slowlog_table.go
  bulk_progress.go
      |
      v
pkg/mot/
  client.go
  options.go
  overview.go
  coll_stats.go
  slowlog.go
  bulk.go
  errors.go
      |
      v
pkg/mongo/
  client.go
  cluster.go
  model.go
  bson.go
  errors.go
```

职责划分：

| 层 | 职责 | 不应承担 |
| --- | --- | --- |
| `cmd` | Cobra 参数解析、读取环境变量、调用 SDK、选择输出格式 | MongoDB 业务编排 |
| `internal/clioutput` | 表格、颜色、进度条、文件日志、信号处理适配 | SDK contract |
| `pkg/mot` | 面向外部使用者的 SDK facade、options、结构化结果、错误语义 | 终端输出和 OS 信号 |
| `pkg/mongo` | MongoDB 低层封装、命令执行、BSON 解析、retry 判断 | CLI 默认值和业务展示 |

`pkg/mot` 是推荐的公开 SDK 包名。它比 `pkg/sdk` 更贴近模块语义，也避免导入路径出现宽泛的 `sdk`：

```go
import "github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
```

## 公开 Client 设计

### Options

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

    Logger Logger
}
```

规则：

- `URI` 非空时优先使用 `URI`。
- `URI` 为空时使用 `Host` / `Port` / `Username` / `Password` / `AuthSource` 生成 URI。
- SDK 不读取 `MONGO_USER` / `MONGO_PASS`；CLI 可在调用 SDK 前读取环境变量并填入 options。
- `AuthSource` 默认值由 `DefaultOptions()` 给出，推荐仍为 `admin`。
- `Logger` 可选；为空时使用 no-op logger。
- `Direct` 为空时保持当前自动探测 host 数量的逻辑；显式设置时尊重调用者选择。

### Client 构造

```go
func NewClient(ctx context.Context, opts Options) (*Client, error)

func NewClientFromMongoClient(ctx context.Context, client *mongo.Client, opts ClientOptions) (*Client, error)

func (c *Client) Close(ctx context.Context) error
```

`NewClient` 负责创建连接并 ping；`NewClientFromMongoClient` 允许上层系统复用已有连接池。

`Close(ctx)` 必须接受调用方 context，不再使用 `context.Background()`。

### 纯函数 URI 构造

```go
func BuildURI(opts Options) (string, error)

func RedactURI(uri string) string
```

`BuildURI` 不初始化日志、不读取环境变量、不修改调用方传入的 options。`RedactURI` 只负责脱敏展示。

## API Contract

### Overview

```go
type OverviewOptions struct {
    IncludeHosts     bool
    NodeConcurrency  int
}

type OverviewResult struct {
    ClusterType ClusterType
    Hosts       []string
    ReplicaSets []ReplicaSetOverview
}

type ReplicaSetOverview struct {
    Name  string
    Nodes []NodeOverview
}

type NodeOverview struct {
    ReplicaSet string
    Address    string
    State      string
    Version    string
    Uptime     time.Duration

    ConnectionsCurrent int64
    QueueReaders       int64
    QueueWriters       int64
    ActiveReaders      int64
    ActiveWriters      int64

    CacheSizeBytes int64
    CacheUsedBytes int64
    ReplicationLag time.Duration
}

func (c *Client) Overview(ctx context.Context, opts OverviewOptions) (*OverviewResult, error)
```

设计要点：

- SDK 返回原始数值，CLI 再格式化为 human readable 字符串。
- `ReplicationLag` 使用 `time.Duration`，避免 SDK 返回 `"0s"` 这类展示字符串。
- 分片集群下返回多个 `ReplicaSetOverview`。
- 仲裁节点允许部分字段为空或为零值，但需要明确 `State`。

### Collection Stats / Check Shard

```go
type CollectionStatsOptions struct {
    Databases       []string
    Collections     []string
    IncludeSystemDB bool
    ShardedOnly     bool
    Concurrency     int
}

type CollectionStatsResult struct {
    Databases []DatabaseStats
}

type DatabaseStats struct {
    Name             string
    StorageSizeBytes int64
    Collections      []CollectionStats
}

type CollectionStats struct {
    Namespace        string
    Count            int64
    AvgObjectBytes   float64
    StorageSizeBytes int64
    IsSharded        bool
    IndexCount       int
    TotalIndexBytes  int64
}

func (c *Client) CollectionStats(ctx context.Context, opts CollectionStatsOptions) (*CollectionStatsResult, error)
```

`check-shard` 不需要独立的 SDK 主流程，可以作为 `CollectionStatsOptions{ShardedOnly: true}` 的一个 CLI 展示模式。

默认规则：

- 默认跳过 `admin`、`config`、`local`。
- `IncludeSystemDB=true` 时才包含系统库。
- `Collections` 非空时只匹配指定集合。
- `Concurrency <= 0` 使用 SDK 默认值，推荐默认 `20`，不沿用硬编码 `50`。

### Slowlog

```go
type SlowlogSort string

const (
    SlowlogSortCount    SlowlogSort = "cnt"
    SlowlogSortMaxMillis SlowlogSort = "maxMills"
    SlowlogSortMaxDocs   SlowlogSort = "maxDocs"
)

type SlowlogOptions struct {
    Databases   []string
    Sort        SlowlogSort
    QueryHash   string
    Concurrency int
}

type SlowlogSummaryResult struct {
    ClusterType ClusterType
    ReplicaSets []ReplicaSetSlowlogSummary
}

type ReplicaSetSlowlogSummary struct {
    Name  string
    Hosts []HostSlowlogSummary
}

type HostSlowlogSummary struct {
    Address   string
    State     ReplicaState
    Databases []DatabaseSlowlogSummary
}

type DatabaseSlowlogSummary struct {
    Database  string
    Total     int64
    FirstTime time.Time
    LastTime  time.Time
    Items     []SlowlogSummaryItem
}

type SlowlogSummaryItem struct {
    Namespace string
    Operation string
    QueryHash string
    Count     int64
    MaxMillis int64
    MinMillis int64
    MaxDocs   int64
    FirstTime time.Time
    LastTime  time.Time
}

type SlowlogDetailResult struct {
    Namespace string
    Slowlog   bson.M
    Indexes   []bson.M
}

func (c *Client) SlowlogSummary(ctx context.Context, opts SlowlogOptions) (*SlowlogSummaryResult, error)

func (c *Client) SlowlogDetail(ctx context.Context, db, queryHash string) (*SlowlogDetailResult, error)
```

设计要点：

- Summary 和 Detail 分成两个方法，避免通过 `QueryHash` 隐式切换行为。
- `Sort` 只能取枚举值，非法值返回 `ErrInvalidOptions`。
- Detail 中字段删减属于 CLI 展示策略；SDK 默认返回完整 slowlog 文档，并可后续增加 `RedactSlowlogDetail` / `CompactSlowlogDetail` helper。

### Bulk Delete / Bulk Update

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

type BulkUpdateOptions struct {
    BulkOptions
    Update any
}

type BulkResult struct {
    Database   string
    Collection string
    DryRun     bool

    MatchedTotal int64
    Processed    int64
    Deleted      int64
    Matched      int64
    Modified     int64

    BatchCount int
    StartedAt  time.Time
    FinishedAt time.Time
}

type BulkBatchResult struct {
    BatchNumber int
    Processed   int64
    Deleted     int64
    Matched     int64
    Modified    int64
}

type BulkObserver interface {
    OnBulkStart(ctx context.Context, total int64)
    OnBulkBatch(ctx context.Context, batch BulkBatchResult)
    OnBulkRetry(ctx context.Context, err error, attempt int)
    OnBulkDone(ctx context.Context, result BulkResult)
}

func (c *Client) BulkDelete(ctx context.Context, opts BulkOptions) (*BulkResult, error)

func (c *Client) BulkUpdate(ctx context.Context, opts BulkUpdateOptions) (*BulkResult, error)
```

写操作安全规则：

- `Database` 和 `Collection` 必填。
- `BatchSize` 必须大于 0，且不超过当前上限 `50000`。
- `Pause` 不允许为负数。
- `Filter` 为空且 `DryRun=false` 时，必须显式设置 `AllowEmptyFilter=true`，否则返回 `ErrDangerousOperation`。
- `BulkUpdateOptions.Update` 必填，且必须是合法 MongoDB update 操作。
- 遇到 context 取消时返回当前 `BulkResult` 和 `ErrCancelled`，调用方可以通过 `errors.Is` 判断。
- 发生批次失败时返回部分结果和错误，不吞掉已完成批次。

CLI 层可实现 `BulkObserver`，用于进度条、文件日志和终端提示。SDK 核心只调用 observer，不直接打印。

## BSON 输入策略

当前 `pkg/mongo.ParseBsonDoc` 支持标准 JSON、Extended JSON 和 MongoDB Shell 风格语法，包括：

- `ISODate(...)`
- `new Date(...)`
- `ObjectId(...)`
- `NumberLong(...)`
- `NumberInt(...)`
- `NumberDecimal(...)`
- `Timestamp(...)`
- 无引号键名
- 尾部逗号清理

SDK 可以继续复用该能力，但公开 API 不应只接受字符串。推荐策略：

```go
type DocumentInput interface {
    ToBSONDocument() (bson.D, error)
}

func ParseDocument(input any) (bson.D, error)
```

`ParseDocument` 支持：

- `string`：按现有 `ParseBsonDoc` 解析。
- `bson.D` / `bson.M`：直接使用或规范化。
- `map[string]any`：转换为 BSON。
- `nil`：表示空文档。

这样 CLI 可以继续传入字符串，SDK 用户可以直接传入 driver 原生类型。

## 错误语义

公开错误需要支持 `errors.Is` / `errors.As`。

建议定义：

```go
var (
    ErrInvalidOptions     = errors.New("invalid options")
    ErrUnsupportedTopology = errors.New("unsupported topology")
    ErrNotSharded         = errors.New("not sharded")
    ErrDangerousOperation = errors.New("dangerous operation")
    ErrCancelled          = errors.New("operation cancelled")
    ErrPartialResult      = errors.New("partial result")
)
```

复杂错误使用结构体：

```go
type PartialError struct {
    Op     string
    Result any
    Err    error
}

func (e *PartialError) Error() string
func (e *PartialError) Unwrap() error
```

规则：

- 参数错误包装 `ErrInvalidOptions`。
- 空 filter 写操作包装 `ErrDangerousOperation`。
- context 取消包装或映射为 `ErrCancelled`。
- 已产生部分结果时返回 `PartialError`。
- MongoDB driver 原始错误不丢失，必须可通过 `errors.As` 取回。

## 日志与输出

SDK 日志接口保持小而稳定：

```go
type Logger interface {
    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)
}
```

默认使用 no-op logger。CLI 可以把现有 `pkg/log.Logger` 适配为该接口。

SDK 禁止：

- `fmt.Print*` 写 stdout / stderr。
- 使用 `fatih/color`。
- 创建或追加本地日志文件。
- 注册系统信号。
- 直接渲染进度条。

CLI 输出迁移到 `internal/clioutput`，从 SDK result 生成现有表格。

## 配置迁移

当前 `internal/config` 可以保留给 CLI，但 SDK 不直接依赖它。

推荐拆分：

```text
pkg/mot/options.go
  Options
  DefaultOptions
  BuildURI
  RedactURI

internal/config/config.go
  CLI flag cfg
  Env fallback
  ConvertToSDKOptions
```

CLI 流程调整为：

```go
cfg := parseFlags()
cfg.ApplyEnv()
opts := cfg.ToSDKOptions()

client, err := mot.NewClient(ctx, opts)
result, err := client.Overview(ctx, overviewOpts)
clioutput.PrintOverview(os.Stdout, result)
```

`BasePreCheck` 后续可以逐步收敛为 CLI 专用方法，不再作为业务层前置。

## CLI 兼容策略

SDK 化后，现有 CLI 命令仍保留：

- `mot overview`
- `mot coll-stats`
- `mot check-shard`
- `mot slowlog`
- `mot bulk-delete`
- `mot bulk-update`

兼容原则：

- flag 名称和默认值保持不变，除非发现安全问题并经过确认。
- 输出字段保持不变，内部数据来源改为 SDK result。
- 密码脱敏保持。
- `MONGO_USER` / `MONGO_PASS` 仍由 CLI 支持。
- `--output` 文件日志仍由 CLI bulk observer 支持。
- Ctrl+C 优雅退出仍由 CLI 信号处理触发 context cancel，而不是 SDK 内部注册信号。

## 测试设计

### 默认单测

默认 `go test ./...` 应满足：

- 不依赖外部 MongoDB。
- 不访问固定 hostname。
- 不读取真实用户凭据。
- 覆盖 options 校验、URI 构造、BSON 解析、结果转换、错误包装、CLI formatter。

当前 `pkg/mongo/client_test.go` 中的真实连接测试需要迁移。

### Integration 测试

真实 MongoDB 测试使用 build tag 和环境变量：

```bash
MOT_TEST_MONGO_URI='mongodb://user:pass@127.0.0.1:27017/admin' \
  go test -tags=integration ./pkg/mongo ./pkg/mot
```

建议拆分：

```text
pkg/mongo/client_integration_test.go
pkg/mot/overview_integration_test.go
pkg/mot/coll_stats_integration_test.go
pkg/mot/slowlog_integration_test.go
pkg/mot/bulk_integration_test.go
```

集成测试文件头：

```go
//go:build integration
```

没有 `MOT_TEST_MONGO_URI` 时跳过测试，不失败。

### CLI 回归测试

CLI formatter 使用 golden test：

```text
internal/clioutput/testdata/
  overview_repl.golden
  coll_stats.golden
  slowlog_summary.golden
```

测试方式：

- 构造 SDK result fixture。
- 调用 formatter。
- 对比 golden 输出。

这样可以在不连接 MongoDB 的情况下保护 CLI 输出兼容。

### 验证命令

SDK 化每个阶段至少运行：

```bash
make harness-verify
go test ./...
make test
```

若涉及真实 MongoDB 行为，再运行 integration profile。

## 迁移计划

### 阶段 1：测试基线前置

目标：

- 让默认 `go test ./...` 不依赖外部 MongoDB。
- 把真实连接测试迁移到 `integration` build tag。
- 补充 `BuildURI` / `ParseDocument` / options 校验单测。

验收：

- `go test ./...` 在无 MongoDB 环境通过。
- `go test -tags=integration ./pkg/mongo` 在提供 `MOT_TEST_MONGO_URI` 时可执行真实连接测试。

### 阶段 2：公开 options 和连接 facade

目标：

- 新增 `pkg/mot`。
- 提供 `Options`、`DefaultOptions`、`BuildURI`、`RedactURI`。
- 提供 `NewClient`、`NewClientFromMongoClient`、`Close(ctx)`。
- 不改变 CLI 行为。

验收：

- 外部包可以 import `github.com/SisyphusSQ/mongo-overview-tool/pkg/mot`。
- `pkg/mot` 不 import `cmd`、`internal/config`、`internal/service`。
- `pkg/mot` 不直接打印 stdout。

### 阶段 3：只读能力 SDK 化

目标：

- 实现 `Overview`。
- 实现 `CollectionStats`。
- 实现 `SlowlogSummary` 和 `SlowlogDetail`。
- CLI 改为调用 SDK result，再由 `internal/clioutput` 渲染。

验收：

- CLI 输出与迁移前保持一致。
- SDK 单测覆盖结果转换、分片 / 副本集分支、系统库过滤、非法 sort。
- `go test ./...` 通过。

### 阶段 4：批量写操作 SDK 化

目标：

- 实现 `BulkDelete` / `BulkUpdate`。
- 引入 `BulkObserver`。
- 将进度条、文件日志、信号处理迁移到 CLI adapter。
- 明确空 filter 写操作保护。

验收：

- SDK 不直接使用 `fmt.Println`、`progress.NewProgressBar`、`SetupSignalHandler`。
- dry-run、空 filter、batch size、pause、update 校验有单测。
- 部分成功结果可被调用方读取。
- CLI 仍支持 `--output` 和 Ctrl+C 优雅退出。

### 阶段 5：文档和示例

目标：

- README 增加 SDK 使用入口。
- 新增 SDK 示例。
- 明确 CLI 和 SDK 的兼容承诺。

建议示例：

```text
examples/sdk/overview/main.go
examples/sdk/coll_stats/main.go
examples/sdk/bulk_delete_dry_run/main.go
```

验收：

- 示例可编译。
- README 链接到 `docs/design/details/sdkization/README.md`。

## 文件调整建议

新增：

```text
pkg/mot/client.go
pkg/mot/options.go
pkg/mot/overview.go
pkg/mot/coll_stats.go
pkg/mot/slowlog.go
pkg/mot/bulk.go
pkg/mot/errors.go
pkg/mot/document.go

internal/clioutput/overview.go
internal/clioutput/coll_stats.go
internal/clioutput/slowlog.go
internal/clioutput/bulk.go
```

保留：

```text
pkg/mongo/client.go
pkg/mongo/cluster.go
pkg/mongo/model.go
pkg/mongo/bson.go
pkg/mongo/errors.go
```

逐步收敛：

```text
internal/service/*
```

`internal/service` 可以先作为迁移中间层存在，但最终应避免承载公开 contract。完成迁移后，它可以被删除，或保留为 CLI 专用 orchestration adapter。

## 兼容和版本策略

当前模块路径固定为：

```text
github.com/SisyphusSQ/mongo-overview-tool
```

SDK 初期建议不承诺 Go module v1 级别的长期 API 稳定。可以在 README 中声明：

- CLI 行为优先兼容。
- `pkg/mot` 在 SDK 化完成前可能调整。
- 达到稳定 contract 后再开始按 SemVer 维护公开 API。

若后续要发布 v1 SDK，需要补充：

- API freeze 清单。
- 公开类型兼容策略。
- Deprecated 流程。
- 示例编译检查。

## 风险与对抗式审查

### 风险 1：只是把 `internal/service` 搬到 `pkg`

如果只移动目录，不改返回值和输出耦合，外部虽然能 import，但仍然拿不到结构化结果，SDK 价值很低。

应对：

- 所有公开方法必须返回 result struct。
- `pkg/mot` 禁止直接使用 `PrintSrv`。

### 风险 2：CLI 输出回归

SDK 化会把数据生成和表格渲染拆开，容易改变列宽、颜色和输出顺序。

应对：

- 给 `internal/clioutput` 加 golden test。
- 每个命令迁移后用 fixture 对比输出。

### 风险 3：写操作部分成功语义不清

批量删除 / 更新一旦执行中断，调用方必须知道已经处理了多少、删除 / 命中 / 修改了多少。

应对：

- `BulkResult` 在错误时也返回。
- 批次失败包装 `PartialError`。
- CLI 终端和文件日志都从 `BulkResult` 渲染。

### 风险 4：context 和连接生命周期泄漏

如果 SDK 内部继续使用 `context.Background()`，外部调用方无法取消长操作，也无法统一超时。

应对：

- 公开方法全部接受 `ctx`。
- `NewClient`、`Close`、MongoDB 查询都传递调用方 ctx。
- 单测覆盖 context canceled 场景。

### 风险 5：依赖版本被顺手升级

项目约束要求 Go module 版本保持 `go 1.26`，MongoDB 官方 Go SDK 固定为 `go.mongodb.org/mongo-driver v1.10.6`。

应对：

- SDK 化不引入新大依赖。
- 每次 diff 检查 `go.mod` / `go.sum`。
- 依赖升级必须单独决策。

## Open Questions

1. 公开包名是否采用 `pkg/mot`。当前推荐 `mot`，但也可以讨论 `pkg/sdk` 或根包导出。
2. SDK 初期是否需要单独打 tag 或 release note。
3. 写操作是否要求更强的显式确认，例如 `ConfirmWrite: true`。
4. Slowlog detail 默认是否返回完整原始文档，还是沿用 CLI 当前删减字段。
5. 是否需要支持 JSON 输出 CLI 模式，让 CLI 直接复用 SDK result 输出机器可读结果。

## 第一轮建议切片

建议第一轮只做前置和只读能力，避免一开始触碰批量写操作：

1. 迁移真实 MongoDB 测试到 integration profile。
2. 新增 `pkg/mot` options、client、error、document helper。
3. 实现 `Overview(ctx, opts)` 并保留现有 CLI 输出。
4. 给 overview CLI formatter 补 golden test。

这轮完成后，项目会拥有一个真实可 import 的 SDK 入口，并且不会改变高风险写操作。
