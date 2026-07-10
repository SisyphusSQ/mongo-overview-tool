# MongoDB 诊断与巡检能力详细设计

## 文档定位

本文定义 `mongo-overview-tool` 从“展示 MongoDB 原始状态”演进为“一次执行即可给出问题、证据和建议”的诊断工具时，需要遵守的产品、接口、安全和验证边界。

本文及 `goals/` 下的文档只描述设计，不代表对应能力已经实现。实现范围以关联 Linear 功能卡的 `Included`、`Excluded` 和 `Stop When` 为准。

## 背景

仓库当前已经提供以下能力：

- `overview`：副本集和分片集群拓扑、节点状态、版本、连接、队列、WiredTiger cache、复制延迟。
- `coll-stats`：数据库和集合规模、文档数、平均文档大小、索引数、索引大小、分片状态。
- `check-shard`：找出尚未分片的集合。
- `slowlog`：从 `system.profile` 聚合慢操作，并查看单个 query hash 的详情和索引。
- `bulk-delete` / `bulk-update`：带 dry-run、批次、暂停、重试和部分成功语义的受控写操作。
- `pkg/mot`：为上述能力提供结构化 Go SDK，并将 CLI 展示留在 adapter 层。

当前缺口不是数据完全不可获得，而是用户仍需自行解释多组指标：

1. 单次快照不能直接回答“当前是否健康、先处理什么”。
2. 缺少正在运行的操作、锁等待和长事务视图。
3. 累计计数器没有转换成短周期速率，难以发现热点 namespace。
4. 索引使用情况、存储效率和容量增长没有形成可行动的审计结果。
5. 慢日志只做聚合展示，尚未提供 scan ratio、plan summary 等诊断结论。
6. 分片均衡、路由元数据一致性和 shard key 质量需要按版本、权限和风险单独设计。

## 用户与核心场景

主要用户：

- DBA / SRE：事故现场快速定位、日常巡检、升级后核查。
- 开发者：自助定位慢查询、连接泄漏、热点集合和索引问题。
- 上层 Go 服务：通过 `pkg/mot` 获取结构化结果，接入已有运维平台或报告系统。

核心场景：

- “集群现在是否健康，风险最高的三项是什么？”
- “请求为什么变慢，是否存在锁等待、长事务或热点集合？”
- “哪些索引值得人工复核，哪些集合的空间效率异常？”
- “两次容量快照之间哪些库表增长最快？”
- “升级或分片维护后，元数据是否一致？”

## 设计目标

1. 默认只读、低负载，并把每项采集的权限和成本显式建模。
2. 输出结构化 finding，包含结论、证据、作用域和建议，而不是只有健康分数。
3. CLI 与 SDK 共享同一业务结果，表格和 JSON 只是不同 adapter。
4. 支持副本集和分片集群；单项能力不支持某个版本或拓扑时返回 `skipped`。
5. 允许一次调用返回部分结果；单节点、单库或单检查失败不抹掉已获得的证据。
6. 对 MongoDB 3.4 等旧版本保持能力降级路径，不强迫升级锁定的 `mongo-driver v1.10.6`。
7. 通过离线 fixture、默认单测和显式 live E2E 建立可复核的验证入口。

## 非目标

本轮不做：

- 不建设常驻 exporter、Prometheus、Grafana 或告警平台。
- 不自动执行 `killOp`、Profiler 配置、索引创建/删除/隐藏、`validate`、`compact`、参数修改或 Query Analyzer 配置。
- 不以尚未稳定的 `$queryStats` 输出作为公开 SDK contract。
- 不承诺替代 Atlas Performance Advisor、PMM、Ops Manager 或 MongoDB Support 诊断包。
- 不在诊断功能中升级或降级 MongoDB Go Driver。
- 不把高级分片检查混入默认低权限巡检。

## 核心原则

### 1. 结论必须可解释

每条 finding 至少包含：

- `code`：稳定机器标识，例如 `replica.majority_unavailable`。
- `severity`：`info`、`warning`、`critical`。
- `scope`：集群、replica set、node、database 或 namespace。
- `summary`：面向人的一句话结论。
- `evidence`：原始指标和阈值依据，不包含秘密或完整查询字面量。
- `recommendation`：下一步人工检查或安全处置建议。

不设计单一、不可解释的总健康分数。CLI 可以按严重级别汇总数量，但不得用该数量替代 finding。

### 2. 默认只读不等于零成本

所有 collector 都要登记：

- 支持的最低服务器能力或 wire version。
- 支持的拓扑和必须连接的节点类型。
- 需要的 privilege；默认推荐 `clusterMonitor`。
- 成本等级：`low`、`bounded`、`expensive-opt-in`。
- 默认超时、并发上限和集合数量保护。
- 是否可能返回命令、用户、client、namespace 等敏感信息。

### 3. 不支持与失败分开表达

- `unsupported`：服务器版本或拓扑不提供该能力。
- `unauthorized`：当前用户没有所需权限。
- `skipped`：因默认安全策略、用户过滤或成本保护未执行。
- `failed`：已经尝试采集，但 MongoDB、网络或解码失败。

前三类不得让整次诊断返回失败；`failed` 是否形成 `PartialError` 取决于调用方是否仍获得了有效结果。

## 能力地图与阶段边界

| 能力 | 主要数据源 | 默认成本 | 一期范围 | 默认行为 |
| --- | --- | --- | --- | --- |
| `doctor` | `replSetGetStatus`、`serverStatus`、`dbStats` | low / bounded | 是 | 执行确定性健康检查和保守阈值检查 |
| `ops` | `$currentOp`，旧版本受控 fallback | low | 是 | 只展示长操作、等待、长事务和维护进度 |
| `hotspot` | 两次 `serverStatus` + `top` | bounded | 是 | 默认采样 10 秒，返回速率和 Top N |
| `index-audit` | `listIndexes`、`$indexStats`、集合统计 | bounded | 是 | 只给人工复核候选，不建议自动删除 |
| `capacity` | `dbStats`、`collStats` / `$collStats` | bounded | 是 | free storage 仅显式启用，支持快照差异 |
| `slowlog-insight` | `system.profile` | bounded | 是 | 追加诊断字段，不自动开启 Profiler |
| `shard-health` | `balancerCollectionStatus`、分片统计 | 高权限 | 后续 | 显式命令，不进入默认 `doctor` |
| `metadata-check` | `checkMetadataConsistency` | 版本受限 | 后续 | MongoDB 7+、仅 `mongos`、显式执行 |
| shard key 分析 | `analyzeShardKey` | expensive-opt-in | 后续 | 必须显式 sample，绝不自动开启 Query Analyzer |
| diagnostic bundle | 上述结构化结果 | 本地 I/O | 后续 | 只输出脱敏后的可共享材料 |

一期范围固定为 Top 5 加 `slowlog-insight`。高级分片能力在本文档中完成接口与安全设计，但不进入一期功能卡的验收出口。

## 目标架构

继续沿用 SDK 化后的分层：

```text
cmd/
  doctor.go
  ops.go
  hotspot.go
  index_audit.go
  capacity.go
      |
      v
internal/clioutput/
  diagnostic_table.go
  diagnostic_json.go
      |
      v
pkg/mot/
  capabilities.go
  findings.go
  doctor.go
  operations.go
  hotspot.go
  index_audit.go
  capacity.go
  slowlog.go
      |
      v
pkg/mongo/
  原生命令、aggregation 和节点连接封装
```

固定职责：

| 层 | 职责 | 禁止事项 |
| --- | --- | --- |
| `cmd` | flag、环境变量、context、调用 SDK、选择 formatter | 诊断阈值和 MongoDB 业务编排 |
| `internal/clioutput` | 表格、JSON、颜色、脱敏后的展示 | 重新判定 severity 或吞掉 collector 状态 |
| `pkg/mot` | options、collector 编排、finding、部分结果、版本和权限降级 | 终端打印、文件写入、OS signal |
| `pkg/mongo` | 执行原生命令、解码原始 BSON、管理派生连接 | 产品阈值、CLI 默认值、建议文案 |

## CLI 设计

一期新增：

```text
mot doctor
mot ops
mot hotspot
mot index-audit
mot capacity
```

共同约定：

- 继续复用现有连接参数。
- 新命令提供 `--format table|json`，默认 `table`。
- JSON 直接序列化 SDK result，不解析终端表格。
- 涉及遍历的命令提供 database / collection 过滤、并发和超时参数。
- 输出顺序稳定：severity、scope、code 或业务主键排序，便于 golden test 和 diff。
- 默认不把完整 URI、query filter、update document、用户信息、session ID 或 client 地址写入输出。

高级命令名称预留为 `shard-health` 和 `metadata-check`，但一期不注册到 root command。

## SDK 接口方向

一期公开方法：

```go
func (c *Client) Doctor(ctx context.Context, opts DoctorOptions) (*DoctorResult, error)
func (c *Client) CurrentOperations(ctx context.Context, opts CurrentOperationsOptions) (*CurrentOperationsResult, error)
func (c *Client) Hotspot(ctx context.Context, opts HotspotOptions) (*HotspotResult, error)
func (c *Client) IndexAudit(ctx context.Context, opts IndexAuditOptions) (*IndexAuditResult, error)
func (c *Client) Capacity(ctx context.Context, opts CapacityOptions) (*CapacityResult, error)
```

共享结构方向：

```go
type Severity string

const (
    SeverityInfo     Severity = "info"
    SeverityWarning  Severity = "warning"
    SeverityCritical Severity = "critical"
)

type DiagnosticFinding struct {
    Code           string         `json:"code"`
    Severity       Severity       `json:"severity"`
    Scope          FindingScope   `json:"scope"`
    Summary        string         `json:"summary"`
    Evidence       map[string]any `json:"evidence,omitempty"`
    Recommendation string         `json:"recommendation,omitempty"`
}

type CollectorStatus struct {
    Name       string `json:"name"`
    State      string `json:"state"`
    ReasonCode string `json:"reasonCode,omitempty"`
    Message    string `json:"message,omitempty"`
}
```

`Evidence` 只承载可安全展示且语义稳定的字段。需要保留完整原始 BSON 时，应放入独立 raw result，并通过显式 option 控制；默认 CLI 不展示 raw。

## 能力探测与调用流程

```text
连接和拓扑探测
    -> 获取 server version / wire version / topology
    -> 构建 capability matrix
    -> 按命令选择 collector
    -> 应用版本、权限、成本和过滤 gate
    -> 有界并发采集原始数据
    -> 生成 findings 和 collector statuses
    -> 返回完整或部分结果
    -> CLI 渲染 table / JSON
```

权限不做预先枚举角色名的强依赖。优先通过能力调用的 MongoDB error code 识别 unauthorized，并将该 collector 标记为 `unauthorized`；角色说明只用于文档和建议。

## 实施顺序

1. 共享 capability、collector status、finding、脱敏与部分结果 contract。
2. `doctor`：优先复用现有 overview 数据，再增加确定性检查。
3. `ops` 和 `slowlog-insight`：补齐当前事故定位链路。
4. `hotspot`：建立通用双快照和 counter delta 组件。
5. `index-audit` 与 `capacity`：复用遍历、过滤、并发和快照能力。
6. 完成 CLI table/JSON、fixture、golden 和 live E2E。
7. 一期结束后再评估高级分片命令和 diagnostic bundle。

## 验收入口

设计落地后的实现至少需要满足：

- 默认 `go test ./...` 不连接外部 MongoDB。
- 新公开类型、部分结果和版本降级有离线测试。
- CLI table 和 JSON 有稳定 fixture / golden。
- `make harness-verify`、`go test ./...`、`make test` 通过。
- 真实副本集和分片集群通过显式 integration profile 验证只读能力。
- 受限权限用户可以获得可解释的 `skipped` / `unauthorized`，而不是整次失败。
- live E2E 未执行时，功能卡不得进入 Done，应停在 manual gate 并写明缺失环境。

## 细化文档索引

| 主题 | 文档 |
| --- | --- |
| 安全、能力与版本门控 | [01-safety-capability-and-version-gating.md](goals/01-safety-capability-and-version-gating.md) |
| Doctor 健康检查 | [02-doctor-health-checks.md](goals/02-doctor-health-checks.md) |
| 活跃操作与热点采样 | [03-active-operations-and-hotspot-sampling.md](goals/03-active-operations-and-hotspot-sampling.md) |
| 索引与容量审计 | [04-index-and-capacity-audit.md](goals/04-index-and-capacity-audit.md) |
| 慢日志洞察 | [05-slowlog-insights.md](goals/05-slowlog-insights.md) |
| 分片与元数据诊断 | [06-sharding-and-metadata-diagnostics.md](goals/06-sharding-and-metadata-diagnostics.md) |
| 结果、CLI 与测试 contract | [07-result-contracts-cli-and-testing.md](goals/07-result-contracts-cli-and-testing.md) |

## 参考资料

- [MongoDB `$currentOp`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/currentop/)
- [MongoDB `serverStatus`](https://www.mongodb.com/docs/manual/reference/command/serverstatus/)
- [MongoDB `top`](https://www.mongodb.com/docs/manual/reference/command/top/)
- [MongoDB `$indexStats`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/indexstats/)
- [MongoDB `dbStats`](https://www.mongodb.com/docs/manual/reference/command/dbstats/)
- [MongoDB `$collStats`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/collstats/)
- [MongoDB Built-In Roles](https://www.mongodb.com/docs/manual/reference/built-in-roles/)
- [MongoDB `checkMetadataConsistency`](https://www.mongodb.com/docs/manual/reference/command/checkmetadataconsistency/)
- [MongoDB `analyzeShardKey`](https://www.mongodb.com/docs/manual/reference/command/analyzeshardkey/)
