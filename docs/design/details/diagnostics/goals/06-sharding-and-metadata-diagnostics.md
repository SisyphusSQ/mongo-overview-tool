# 06 分片与元数据诊断

## 文档定位

本文设计 `shard-health`、`metadata-check` 和 shard key 分析，但这些能力不进入一期功能卡的实现与验收。

原因：它们受 MongoDB 版本、连接入口、权限和采样成本影响明显，不能与默认低权限 `doctor` 混为一体。本文用于锁定后续接口、安全 gate 和功能边界，避免未来直接读取 `config` 系统表并固化不稳定 schema。

## `shard-health`

### 目标

在现有 `check-shard` 仅判断集合是否分片的基础上，回答：

- collection 是否符合 balancer 当前均衡规则。
- 是否存在 draining、zone violation、chunk imbalance 或 defragmentation。
- 各 shard 的集合数据、索引和 orphan document 分布是否显著不均。
- 是否存在进行中的 migration / resharding 维护操作。

### CLI 方向

```text
mot shard-health [connection flags]
  --format table|json
  --database <db1,db2>
  --collection <c1,c2>
  --include-balancer-status
  --include-orphan-stats
  --max-collections <n>
```

该命令必须连接 `mongos`。不允许对 replica set 或 standalone 返回空成功。

### 数据来源

优先使用稳定原生命令与 aggregation：

- `listShards`
- `balancerCollectionStatus`
- `$collStats` 的 shard / storage stats
- `serverStatus.shardingStatistics`（字段存在时）
- `$currentOp` 中 migration / resharding 状态

不把直接查询 `config.chunks`、`config.collections` 的内部 schema 作为主要公开 contract。确需兼容旧版本时，访问必须封装在版本适配器，原始文档不泄漏到 SDK public type。

### Findings

- `shard.balance_noncompliant`
- `shard.zone_violation`
- `shard.draining`
- `shard.defragmenting`
- `shard.distribution_skew`
- `shard.orphan_documents_present`
- `shard.migration_in_progress`

`balancerCollectionStatus` 只返回观察到的第一个 compliance violation；finding 必须注明它不代表不存在其它原因。

### 权限

`balancerCollectionStatus` 需要 `enableSharding` action 或 `clusterManager`。因此：

- 默认 `clusterMonitor` 用户得到明确 `unauthorized` status。
- 权限不足时仍可返回低权限 `$collStats` 分布结果。
- 不建议为默认 doctor 提升到 `clusterManager`。

## `metadata-check`

### 目标

MongoDB 7+ 在重大升级、降级、分片维护或迁移之后，通过官方 `checkMetadataConsistency` 发现：

- collection UUID / options 不一致。
- routing table gap / overlap / missing。
- zone range 问题。
- collection 位于错误 shard 或本地集合缺失。
- shard key supporting index 缺失。
- `checkIndexes=true` 时跨 shard index 不一致。

### CLI 方向

```text
mot metadata-check [connection flags]
  --format table|json
  --database <db>
  --collection <collection>
  --check-indexes
  --batch-size <n>
  --max-time <duration>
```

约束：

- 仅 MongoDB 7+。
- 仅通过 `mongos` 执行。
- 默认 cluster scope 需要显式确认或过滤；常规调用优先 database / collection scope。
- `checkIndexes` 默认 false，因为会扩大检查范围。
- 正确消费 command cursor 和 getMore，context 取消后关闭 cursor。

结果将官方 inconsistency type 原样保存为稳定 source code，同时映射为项目 finding code；未知新类型保留 raw type 并按 warning 展示，不能 decode failure。

## Shard Key 分析

### 定位

`analyzeShardKey` 能返回 cardinality、frequency、monotonicity 和采样后的 read/write routing，但不适合默认巡检：

- 默认 sample size 可能达到 1000 万文档。
- key characteristics 需要 supporting index。
- read/write distribution 依赖事先配置 Query Analyzer。
- Query Analyzer 配置会改变服务器状态，不属于本工具默认只读边界。
- 分布质量取决于采样窗口是否代表真实工作负载。

### 后续安全接口

```text
mot shard-key analyze
  --database <db>
  --collection <collection>
  --key <document>
  --sample-size <explicit-positive-int>
  --characteristics-only
```

固定规则：

- 没有显式 `--sample-size` 或 `--sample-rate` 时拒绝执行，不采用服务器 1000 万默认值。
- 默认 `characteristics-only`，即 `readWriteDistribution=false`。
- 工具不提供自动启停 Query Analyzer；若外部已配置采样，可在未来增加只读读取结果选项。
- 推荐 secondary / secondaryPreferred，不在 PRIMARY 强制全量扫描。
- 不生成或执行 `shardCollection`、`refineCollectionShardKey`、`reshardCollection`。
- most common values 默认只输出频次和脱敏摘要，不输出业务值。

## 与一期功能的关系

- `doctor` 只报告当前 shard replica set 健康，不调用高权限 balancer 命令。
- `index-audit` 可以做低权限 spec 对比，但不替代 `metadata-check(checkIndexes=true)`。
- `capacity` 保留 per-shard storage stats，为未来 distribution skew 提供结构化基础。
- `ops` 可以展示正在进行的 migration / resharding，但不控制它们。

## 后续验收要求

进入实现前必须单独建立 execution issue，并至少验证：

- MongoDB 7+ 分片集群的 metadata inconsistency fixture 与 live E2E。
- `clusterMonitor` 和 `clusterManager` 两种权限行为。
- mongos / mongod 错误入口。
- 大 collection sample gate 不会落入服务器默认 sample size。
- unknown inconsistency type 向前兼容。
- 所有输出经过 shard key value、host 和 namespace 脱敏策略评审。

## 参考资料

- [MongoDB `balancerCollectionStatus`](https://www.mongodb.com/docs/manual/reference/command/balancercollectionstatus/)
- [MongoDB `checkMetadataConsistency`](https://www.mongodb.com/docs/manual/reference/command/checkmetadataconsistency/)
- [MongoDB Inconsistency Types](https://www.mongodb.com/docs/manual/reference/inconsistency-type/)
- [MongoDB `analyzeShardKey`](https://www.mongodb.com/docs/manual/reference/command/analyzeshardkey/)
