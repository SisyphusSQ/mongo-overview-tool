# 01 安全、能力探测与版本门控

## 目标

为所有诊断命令提供统一的前置 gate，使调用方能区分“可执行”“不支持”“权限不足”“因成本跳过”和“执行失败”，并保证默认操作只读、有界、可取消、可脱敏。

该层是 `doctor`、`ops`、`hotspot`、`index-audit`、`capacity` 和后续高级分片能力的共同依赖。任何 collector 不得自行发明版本判断、错误分类或脱敏规则。

## Capability Contract

```go
type CapabilityState string

const (
    CapabilitySupported    CapabilityState = "supported"
    CapabilityUnsupported  CapabilityState = "unsupported"
    CapabilityUnauthorized CapabilityState = "unauthorized"
    CapabilitySkipped      CapabilityState = "skipped"
    CapabilityFailed       CapabilityState = "failed"
)

type Capability struct {
    Name          string          `json:"name"`
    State         CapabilityState `json:"state"`
    ReasonCode    string          `json:"reasonCode,omitempty"`
    ServerVersion string          `json:"serverVersion,omitempty"`
    Topology      ClusterType     `json:"topology,omitempty"`
    RequiredRole  string          `json:"requiredRole,omitempty"`
    Cost          string          `json:"cost"`
}
```

稳定的 `ReasonCode` 至少包括：

- `unsupported_server_version`
- `unsupported_wire_version`
- `unsupported_topology`
- `requires_mongod`
- `requires_mongos`
- `unauthorized`
- `disabled_by_default`
- `collection_limit_exceeded`
- `timeout`
- `node_unreachable`
- `counter_reset`
- `decode_failed`

面向人的 MongoDB 错误文本不得作为调用方判断条件。

## 探测顺序

1. 使用现有连接完成 ping 和拓扑探测。
2. 从 `hello` / 兼容 handshake 结果与 `serverStatus.version` 获取 wire version 和服务器版本。
3. 记录连接入口是 `mongos`、replica set member 还是其它形态。
4. 根据静态能力表筛除明确不支持的 collector。
5. 执行 collector 时，根据 MongoDB error code 将授权失败映射为 `unauthorized`。
6. 对只在单个 `mongod` 可执行的命令，复用当前派生节点连接；派生失败只影响该节点。

不通过查询用户角色列表推断权限。自定义 role 可能具有等价 privilege，真实命令结果才是最终权限真相。

## 推荐权限

默认文档推荐 `clusterMonitor`，因为它覆盖常见监控动作，包括 `serverStatus`、`top`、`replSetGetStatus`、`dbStats`、`collStats`、`indexStats`、`inprog` 和读取 `system.profile`。

规则：

- 缺少 `clusterMonitor` 不直接拒绝整条命令；逐 collector 尝试并降级。
- `ops --all-users` 在没有 `inprog` 时，允许降级为当前用户可见操作，并明确标记结果不完整。
- `balancerCollectionStatus`、`analyzeShardKey` 等需要更高权限的能力不得进入默认 `doctor`。
- 不为方便诊断建议用户授予 `root`、`dbOwner` 或其它过宽角色。

## 成本等级与默认策略

| 等级 | 定义 | 默认策略 |
| --- | --- | --- |
| `low` | 常数级管理命令或已存在的单节点快照 | 默认启用 |
| `bounded` | 按节点、数据库或集合遍历，但存在明确上限 | 默认启用并应用超时、并发和数量 gate |
| `expensive-opt-in` | 可能扫描大量文档、触发额外采样或需要高权限 | 默认禁用，只能显式启用 |

共同默认值：

- 所有 public method 接受调用方 `context.Context`。
- 每个 MongoDB command 继承调用方 deadline；CLI 在没有 deadline 时提供命令级默认超时。
- 节点级采集默认串行或低并发；集合级遍历沿用可配置的有界并发。
- 涉及 free storage、全量索引统计或高级分片分析时，必须提供数据库/集合过滤和数量保护。
- `maxTimeMS` 在原生命令支持时下推；不支持时仍由 context 取消保护客户端等待。

## 版本与拓扑门控

能力表不把版本字符串散落在业务代码中。统一登记到 capability registry，并允许 fixture 覆盖字段缺失：

| 能力 | 拓扑约束 | 版本策略 |
| --- | --- | --- |
| `serverStatus` | `mongod`，部分字段可来自 `mongos` | 字段按 presence 解码，不假定所有版本都有同一结构 |
| `$currentOp` | admin aggregate；分片可从 `mongos` fan-out | 支持时优先；旧版本才使用受控 fallback |
| `top` | 仅 `mongod` | 对分片和副本集逐数据节点执行 |
| `$indexStats` 基础统计 | collection aggregate | 3.2+；结果按 node/shard 保留，不错误合并统计起点 |
| 索引一致性 direct collector | 分片集群且入口为 `mongos` | MongoDB 3.4–4.2.3 通过派生 shard 连接执行 `listIndexes` |
| 索引一致性 `$indexStats` collector | 分片集群且入口为 `mongos` | MongoDB 4.2.4–6.x 优先使用 `shard`、`spec`、`building`，字段或覆盖不完整时 direct fallback |
| 索引一致性官方 collector | MongoDB 7.x 且仅 `mongos` | `index-audit consistency` 优先执行 `checkMetadataConsistency(checkIndexes=true)`，失败时按 coverage 规则 fallback |
| free storage | `dbStats` / collection stats | 字段不存在则标记 unsupported，不伪造零值 |
| 通用 `checkMetadataConsistency` | MongoDB 7+ 且仅 `mongos` | 索引域由 `index-audit consistency` 复用；完整元数据检查仍是后续显式命令 |
| `analyzeShardKey` | 版本、拓扑、索引和权限均有限制 | `expensive-opt-in`，一期不实现 |

MongoDB 3.4 等旧版本继续允许基础 `doctor`、慢日志兼容标识和现有集合统计工作；新字段缺失不得引发 decode failure。

`index-audit consistency` 的支持范围明确冻结为 MongoDB 3.4–7.x。expected shards 必须独立于索引 observation 获取；版本无法可靠识别、低于 3.4 或高于 7.x 时使用 `unsupported_server_version`，不能猜测执行策略。详细策略见 [08 分片集群全库索引一致性审计](08-sharded-index-consistency-audit.md)。

## 脱敏规则

默认结果不得包含：

- URI 密码、认证机制秘密或完整连接串。
- query filter、update document、aggregation pipeline 的字段值。
- 完整 client IP、用户名、session ID、transaction ID。
- 可能含业务数据的 shard key 常见值或 profiler 原始 command。

允许默认展示：

- namespace、operation type、query hash、plan summary。
- 脱敏后的 appName。
- 节点地址；如果用于可共享 diagnostic bundle，需额外支持主机名匿名化。
- 只包含数值与布尔值的 evidence。

JSON 输出与 table 输出必须调用同一脱敏后的 result。不能只在 formatter 隐藏字段、却让默认 JSON 泄漏 raw BSON。

## 部分结果与错误语义

每个命令 result 都包含 `CollectorStatuses`。规则：

- collector 为 `unsupported`、`unauthorized` 或 `skipped` 时，方法返回 result 和 `nil` error。
- 至少一个 collector 成功、另一个 collector 运行失败时，返回 result 和 `*PartialError`。
- 所有必需 collector 都失败且没有可用业务结果时，返回普通 error。
- context 取消或 deadline exceeded 继续映射为现有 `ErrCancelled`，并保留已完成 collector 的部分结果。
- 单节点失败不得取消其它独立节点；但调用方 context 取消必须停止新任务。

## 日志规则

- SDK 仍使用可选 `Logger`，默认 no-op。
- 正常的 unsupported / unauthorized / skipped 不记为 error。
- 日志中的 URI 必须使用现有 `RedactURI`。
- 不记录完整 currentOp command、profiler document 或索引 partial filter。

## 验收标准

- capability registry 能用离线 fixture 覆盖旧版本、现代版本、`mongod`、`mongos` 和字段缺失。
- unauthorized collector 不使整次诊断失败。
- bounded collector 的超时、并发和数量 gate 可配置且有默认值测试。
- table 与 JSON 均使用脱敏后的同一结构化结果。
- partial result、context cancel 和全失败三种错误路径可区分。
- `pkg/mot` 不读取环境变量、不打印、不写文件、不注册 signal。

## 参考资料

- [MongoDB Built-In Roles](https://www.mongodb.com/docs/manual/reference/built-in-roles/)
- [MongoDB `$currentOp`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/currentop/)
- [MongoDB `serverStatus`](https://www.mongodb.com/docs/manual/reference/command/serverstatus/)
