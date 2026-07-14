# 07 结果 Contract、CLI 与测试

## 目标

锁定诊断能力的公开结果、CLI 适配、兼容策略和验证矩阵，确保一期实现不会将 MongoDB 原始 BSON、终端字符串或单一服务器版本结构固化为外部 API。

## 公共结果约定

所有一期 result 都应包含：

- `CollectedAt` 或明确的 `StartedAt` / `FinishedAt`。
- 业务主体结果，例如 operations、nodes、namespaces、collections。
- `Findings []DiagnosticFinding`。
- `CollectorStatuses []CollectorStatus`。

共同规则：

- 时间使用 `time.Time` / `time.Duration`，JSON 使用 Go 标准时间与 duration 的既有项目约定；CLI 再格式化。
- bytes、counts、rates 保留数值，不在 SDK 保存 humanized 字符串。
- 不使用 map 作为需要稳定输出顺序的顶层集合。
- slice 在返回前按稳定业务键排序。
- 新字段使用 `omitempty` 不能代替“缺失与真实零”的语义；需要时使用 pointer / presence flag。
- public type 不直接暴露 `bson.M`，只有明确标记的 raw detail 保持现有兼容。

## Finding Contract

```go
type FindingScope struct {
    Type       string `json:"type"`
    Cluster    string `json:"cluster,omitempty"`
    ReplicaSet string `json:"replicaSet,omitempty"`
    Shard      string `json:"shard,omitempty"`
    Node       string `json:"node,omitempty"`
    Database   string `json:"database,omitempty"`
    Namespace  string `json:"namespace,omitempty"`
}
```

规则：

- `code` 是稳定 contract；summary 和 recommendation 可以改善措辞。
- evidence key 使用稳定英文标识，value 只能是已脱敏的 scalar、短数组或小型结构。
- severity 只能是 `info`、`warning`、`critical`。
- finding code 按 `<domain>.<snake_case>` 命名。
- 未知或不足证据不生成 warning；用 collector status 或 info 表达。

## Collector Status Contract

collector status 用于解释覆盖范围，不与 finding 重复：

```go
type CollectorStatus struct {
    Name       string          `json:"name"`
    State      CapabilityState `json:"state"`
    Scope      FindingScope    `json:"scope,omitempty"`
    ReasonCode string          `json:"reasonCode,omitempty"`
    Message    string          `json:"message,omitempty"`
}
```

- 每个计划执行的 collector 至少有一条最终 status。
- status 顺序按 collector name + scope 稳定排序。
- `Message` 不直接透传可能含连接串或查询值的 server error；原始 error 只进入安全日志。

## CLI 输出

### Table

- 首屏显示采集范围、时间、最严重级别和 finding 数量。
- finding 表至少包含 severity、code、scope、summary。
- evidence 和 recommendation 采用紧随 finding 的详情块或显式 verbose 模式，不能被列宽截断到失去关键含义。
- unsupported / skipped / unauthorized 汇总在 Coverage 段，不混入 critical finding。
- 无颜色终端、重定向和测试 writer 下仍输出完整文本。

### JSON

- `--format json` 直接编码 SDK result。
- JSON 不包含 ANSI color、humanized bytes 或终端 header。
- 默认 compact JSON；如提供 pretty 选项，只改变缩进。
- 任何 `NaN`、`Infinity` 或非 JSON duration 禁止进入结果。
- schema 新增字段保持向后兼容；capacity snapshot 额外维护 `schemaVersion`。

### 命令兼容

- 现有 `overview`、`coll-stats`、`check-shard`、`slowlog`、`bulk-delete`、`bulk-update` 命令名和主要 flag 不变。
- `slowlog` 只追加列或 JSON 字段；默认 table 如需扩展，先通过 golden 明确新输出。
- 新命令复用 root connection flags，不复制另一套认证环境变量规则。
- SDK 不读取 `MONGO_USER` / `MONGO_PASS`；CLI 继续负责转换。

## Error Contract

沿用现有错误类型并补充诊断语义：

- invalid options：调用前返回 `ErrInvalidOptions`，不连接或不开始第二快照。
- unsupported topology：整条命令要求特定入口时返回 `ErrUnsupportedTopology`；单 collector 不支持时用 status。
- partial failure：返回非空 result + `*PartialError`。
- context cancel / deadline：映射为现有取消语义，保留已完成结果。
- unauthorized：单 collector status；只有命令的所有必需数据都不可见时才返回整体 error。

`index-audit consistency` 的 CLI 将索引差异和 collection 级 incomplete visibility 视为审计结果：只要范围发现成功且存在可渲染 result，CLI 输出 result 后返回 0；SDK 仍可返回 result + `*PartialError`。参数非法、基础连接失败、非 `mongos`、范围发现失败、collection gate 超限、context 取消或 formatter 失败仍返回非零。该命令不提供 `--fail-on`。

## 离线测试矩阵

### Result 与规则单测

- severity、scope、finding 排序和抑制。
- capability state / reason code 映射。
- 脱敏和 evidence allowlist。
- partial result、全失败、context cancel。
- duration、rate、ratio、bytes 和 optional 字段编码。

### MongoDB 版本 fixture

至少准备以下语义 fixture，不要求启动对应服务器：

| 版本族 | 重点 |
| --- | --- |
| MongoDB 3.4 | 无 queryHash、旧 currentOp / serverStatus 字段、索引一致性 direct collector |
| MongoDB 4.2.3 | `$indexStats` 尚无稳定 shard/spec/building，使用 direct collector |
| MongoDB 4.2.4 | `$indexStats` shard/spec/building 版本断点 |
| MongoDB 4.4 | 常见自建副本集基线、hidden index 属性 |
| MongoDB 5.x | `$indexStats` 完整 spec 与字段缺失 fallback |
| MongoDB 6.x | 新 profiler / connection 字段、currentOp command 弃用边界、索引一致性 legacy 主路径 |
| MongoDB 7.x | metadata consistency cursor、索引 finding 归一化与 fallback |
| MongoDB 8.x | 仅用于其它诊断字段的未知字段向前兼容；不属于 Goal 08 支持范围 |

fixture 必须脱敏，不提交真实 host、用户名、URI 或业务查询。

### CLI 测试

- 每个新命令的 flag 默认值、必填与互斥校验。
- table golden：健康、warning/critical、partial coverage、无结果。
- JSON golden / struct assertion：字段、数值、稳定排序、无 ANSI。
- `slowlog` 现有输出回归和新增 insight 输出。
- cancellation 不留下持续采样 goroutine。

### 集成测试

继续使用 `//go:build integration` 与显式环境变量：

- 副本集：doctor、ops、hotspot、index-audit、capacity、slowlog insight。
- 分片集群：逐 shard 派生连接、namespace 聚合和部分节点失败。
- 索引一致性：MongoDB 3.4 与 7.x 分片集群为 required 只读 live gate；中间版本使用离线 fixture。
- 权限：`clusterMonitor` 正常覆盖；受限用户返回 unauthorized / incomplete visibility。
- 不在 live E2E 执行任何写操作或配置变更。

真实环境变量沿用 `MOT_TEST_MONGO_URI` 或现有只读 live E2E 的 host/port 入口，不在文档或日志输出秘密。

## 验证命令

默认验证：

```bash
make harness-verify
go test ./...
make test
```

显式 integration：

```bash
MOT_TEST_MONGO_URI='<redacted-uri>' \
  go test -tags=integration -count=1 ./pkg/mongo ./pkg/mot
```

只读 live E2E 应继续支持按现有 `MOT_TEST_MONGO_HOST`、`MOT_TEST_MONGO_PORT` 和 `MOT_TEST_EXPECT_CLUSTER` 入口执行。

## Review Gate

实现交付前重点审查：

1. 是否将字段缺失误判为零值或健康。
2. 是否有默认 collector 会进行无界集合扫描或高权限操作。
3. table 已脱敏但 JSON / logger 仍泄漏 raw BSON。
4. 分片结果是否丢失 shard / host 来源，或重复汇总。
5. counter reset、节点重启和不同采集时间是否产生错误 rate。
6. 索引一致性的 expected shards 是否独立于 observation，整个 shard 缺失时是否仍可能误报健康。
7. 7.x 官方 cursor 是否完整消费，fallback 是否保留原始失败与最终 coverage。
8. 构建中索引或并发 DDL 是否产生未经二次确认的 legacy warning。

## Done Gate

一期功能卡只有在以下条件同时满足时才能 Done：

- Top 5 与 slowlog insight 全部实现，公共 API 和 CLI 文档同步。
- 默认构建、单测、CLI golden 通过。
- 必需副本集和分片 live E2E 已执行并形成脱敏摘要；缺环境时保持 manual gate。
- 权限降级、版本 fixture、partial result 和脱敏通过 review。
- 高级分片功能没有被顺手纳入一期实现。

Goal 08 使用独立 execution issue 和验收出口，只依赖 `TOO-230` 的公共 capability/finding/status 与 `index-audit` 骨架，不要求等待其它 Top 5 全部完成；也不得借此实现完整 `metadata-check`。

## 参考资料

- [SDK 化详细设计](../../sdkization/README.md)
- [测试基线目标](../../sdkization/goals/07-default-test-baseline.md)
- [CLI 兼容目标](../../sdkization/goals/02-cli-compatibility.md)
- [分片集群全库索引一致性审计](08-sharded-index-consistency-audit.md)
