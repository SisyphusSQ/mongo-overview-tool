# 目标 7：默认测试不依赖外部 MongoDB

## 目标定义

SDK 化后，默认回归入口必须稳定。`go test ./...` 应该在没有 MongoDB、没有固定 hostname、没有真实凭据的环境中通过。

真实 MongoDB 测试仍然需要保留，但必须移入 integration profile，由显式 build tag 和环境变量触发。

## 当前问题

当前 `pkg/mongo/client_test.go` 中的测试会调用：

```go
NewMongoConn("mongodb://user:pwd@mongod:27017/admin")
```

如果本地没有名为 `mongod` 的主机，默认 `go test ./...` 会失败。这会带来几个问题：

- 无法判断 SDK 化改动是否破坏了纯代码逻辑。
- CI 或新机器上测试不可复现。
- 每次测试都要等待连接超时。
- 外部贡献者无法直接运行默认单测。

## 测试分层

推荐分成三层：

| 层级 | 命令 | 是否依赖 MongoDB | 用途 |
| --- | --- | --- | --- |
| unit | `go test ./...` | 否 | 默认回归、纯逻辑、formatter、options、错误语义 |
| integration | `go test -tags=integration ./...` | 是 | 真实 MongoDB 行为 |
| build | `make test` | 否 | CLI 编译验证 |

## Integration Test 约定

真实连接测试文件使用 build tag：

```go
//go:build integration
```

环境变量：

```text
MOT_TEST_MONGO_URI
```

测试 helper：

```go
func integrationMongoURI(t *testing.T) string {
    t.Helper()
    uri := os.Getenv("MOT_TEST_MONGO_URI")
    if uri == "" {
        t.Skip("MOT_TEST_MONGO_URI is required for integration tests")
    }
    return uri
}
```

执行命令：

```bash
MOT_TEST_MONGO_URI='mongodb://user:pass@127.0.0.1:27017/admin' \
  go test -tags=integration ./pkg/mongo ./pkg/mot
```

## 默认单测覆盖范围

默认 `go test ./...` 应覆盖：

- `BuildURI` 默认值、URI 优先级、认证拼接、无认证 URI。
- `RedactURI` 密码脱敏。
- `ParseDocument` 支持 string、`bson.D`、`bson.M`、nil。
- `ShellToExtJSON` 现有场景。
- options 参数校验。
- SDK result mapping。
- error wrapping 和 `errors.Is` / `errors.As`。
- CLI formatter golden output。
- bulk dry-run 逻辑。
- bulk 参数校验。
- context cancel 的纯逻辑路径。

这些测试可以通过 fake repository、mock connection 或 fixture 数据完成，不需要真实 MongoDB。

## Mock / Fake 边界

为了让 SDK 可测，`pkg/mot` 内部可以定义小接口隔离 MongoDB 执行：

```go
type mongoOps interface {
    IsSharding(ctx context.Context) (bool, error)
    RsStatus(ctx context.Context) (mongo.RsStatus, error)
    ListShards(ctx context.Context) (mongo.ShStatus, error)
    ServerStatus(ctx context.Context) (bson.M, error)
    DBStatus(ctx context.Context, db string) (mongo.DBStats, error)
}
```

规则：

- 接口只放 SDK 实际需要的方法。
- 不为了测试抽象出过大的通用 repository。
- fake 数据使用真实 `pkg/mongo` model，避免 contract 漂移。

## Golden Test

CLI 输出兼容用 golden test，不连接 MongoDB。

流程：

1. 构造 `mot.OverviewResult` fixture。
2. 调用 `internal/clioutput.PrintOverview`。
3. 比对 `testdata/overview_repl.golden`。

优点：

- 快。
- 可重复。
- 能保护 CLI 输出格式。
- 不依赖数据库状态。

## 当前测试迁移建议

把 `pkg/mongo/client_test.go` 拆分：

```text
pkg/mongo/client_integration_test.go
```

并加上：

```go
//go:build integration
```

原有测试继续保留真实连接价值，但不进入默认单测。

如果需要保留纯单测文件，可以新增：

```text
pkg/mongo/client_test.go
```

覆盖：

- URI direct 判断。
- retryable cursor error。
- error redaction。

## CI / 本地验证命令

默认验证：

```bash
make harness-verify
go test ./...
make test
```

集成验证：

```bash
MOT_TEST_MONGO_URI='mongodb://user:pass@127.0.0.1:27017/admin' \
  go test -tags=integration ./pkg/mongo ./pkg/mot
```

如果没有 MongoDB，只跑默认验证即可。

## 落地步骤

1. 给真实连接测试加 `integration` build tag。
2. 引入 `MOT_TEST_MONGO_URI`。
3. 保证没有 env 时 integration test skip。
4. 新增 options / document / error / formatter 单测。
5. 在 SDK 每个阶段保持 `go test ./...` 可通过。
6. 在 runbook 或 README 中记录 integration test 命令。

## 验收标准

- 无 MongoDB 环境下 `go test ./...` 通过。
- `go test ./...` 不访问 `mongod:27017`。
- integration test 只有显式 `-tags=integration` 才执行。
- 没有 `MOT_TEST_MONGO_URI` 时 integration test skip，而不是失败。
- SDK 新增公开 API 都有默认单测覆盖。

## 风险

如果默认测试继续依赖真实 MongoDB，SDK 化后每个阶段的回归反馈都会变慢且不稳定。更严重的是，测试失败无法区分是代码问题、网络问题还是本地环境问题。

测试分层是 SDK 化的第一前置，不应推迟到实现后期。
