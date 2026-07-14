# 08 分片集群全库索引一致性审计

## 文档定位

本文细化 `index-audit` 的 `consistency` 检查，目标是在不修改服务器状态的前提下，对 MongoDB 3.4–7.x 分片集群中指定数据库的全部分片集合执行跨 shard 索引一致性检查。

该能力属于 `index-audit`，不是独立的 `metadata-check` 命令。MongoDB 7.x 会复用官方 `checkMetadataConsistency(checkIndexes=true)`，但只归一化索引相关结果；路由表、UUID、zone、collection options 等通用元数据诊断仍属于后续 `metadata-check`。

实现依赖 [01 安全、能力探测与版本门控](01-safety-capability-and-version-gating.md) 定义的 capability、collector status、finding、部分结果和脱敏 contract，以及 `index-audit` 的公共命令与 SDK 骨架。

## 目标

1. 覆盖 MongoDB 3.4–7.x，并为不同版本选择可证明覆盖范围的采集策略。
2. 发现索引缺失、名称不一致、完整定义不一致和 shard key supporting index 缺失。
3. 将官方命令和旧版本采集结果归一为同一组公开状态、finding 和 coverage contract。
4. 完全缺失某个 shard 的 observation、权限不足、构建中索引或并发 DDL 时，不把结果误判为一致。
5. 保持默认只读、有界、可取消，不提供任何自动修复动作。

## 非目标

- 不比较同一 replica set 内不同 member 的本地索引；本能力只比较分片集合在不同 shard 上的索引定义。
- 不检查普通集合是否应该分片；普通集合和 view 只作为 `skipped` 结果保留。
- 不输出或修复路由表、UUID、zone、collection options 等非索引元数据问题。
- 不执行 `createIndexes`、`dropIndexes`、`collMod`、rebuild、hide/unhide 或其它写操作。
- 不支持 MongoDB 3.4 以下或 8.x 及以上版本；扩展版本范围前必须新增 capability、fixture 和 live 验证。
- 不升级 `go.mongodb.org/mongo-driver v1.10.6`，官方命令通过稳定的 `RunCommand` 和 cursor 解码封装接入。

## CLI Contract

```text
mot index-audit [connection flags]
  (--database <db1,db2> | --all-databases)
  --collection <c1,c2>
  --checks <csv>
  --format table|json
  --max-collections 500
  --concurrency <n>
```

固定规则：

- `--database` 与 `--all-databases` 必须且只能选择一个；不允许用空过滤隐式扫描全实例。
- `--all-databases` 默认排除 `admin`、`config`、`local`。
- `--collection` 可选；指定多个 database 时，同一 collection 名过滤应用到每个 database。
- `consistency` 加入 `index-audit` 默认 checks；`--checks consistency` 表示只执行本检查。
- 未知 check 名在连接前返回 invalid options。
- `--max-collections` 统计本次选中范围内的全部集合，包括随后会被标记为 `skipped` 的普通集合和 view；超过上限时在 shard fan-out 前停止。
- collection 任务使用有界并发，同一 shard 不允许无上限 fan-out；所有命令继承调用方 context 和公共默认超时。

仅允许通过 `mongos` 执行。连接到 replica set member 或 standalone 时返回整体 `ErrUnsupportedTopology`，不能返回空成功。

## SDK Contract

公开入口沿用：

```go
func (c *Client) IndexAudit(
    ctx context.Context,
    opts IndexAuditOptions,
) (*IndexAuditResult, error)
```

`IndexAuditOptions` 至少包含：

```go
type IndexAuditCheck string

const (
    IndexAuditCheckConsistency IndexAuditCheck = "consistency"
)

type IndexAuditOptions struct {
    Databases         []string
    AllDatabases      bool
    Collections       []string
    Checks            []IndexAuditCheck
    IncludeSystemDB   bool
    MinObservation    time.Duration
    MaxCollections    int
    Concurrency       int
}
```

SDK 同样要求 `Databases` 非空与 `AllDatabases=true` 二选一。`Checks` 为空时使用 `index-audit` 默认 checks，其中必须包含 `consistency`。`IncludeSystemDB` 只供显式 SDK 调用，不改变 CLI `--all-databases` 默认排除系统库的规则。

## 结果 Contract

```go
type IndexConsistencyState string

const (
    IndexConsistencyConsistent   IndexConsistencyState = "consistent"
    IndexConsistencyInconsistent IndexConsistencyState = "inconsistent"
    IndexConsistencyInconclusive IndexConsistencyState = "inconclusive"
    IndexConsistencySkipped      IndexConsistencyState = "skipped"
)

type IndexConsistencyStrategy string

const (
    IndexConsistencyDirectListIndexes IndexConsistencyStrategy = "direct_list_indexes"
    IndexConsistencyIndexStats        IndexConsistencyStrategy = "index_stats"
    IndexConsistencyMetadataCheck     IndexConsistencyStrategy = "check_metadata_consistency"
)
```

每个 collection 结果至少保留：

- namespace、是否分片、最终 state 和实际 strategy。
- expected shards、observed shards、coverage 是否完整。
- 官方策略失败后的 fallback strategy 与脱敏 reason code。
- index name、保持 BSON 顺序的 key pattern、定义 fingerprint 和差异字段名。
- collection 级 collector statuses；finding 仍在 `IndexAuditResult.Findings` 统一返回。

顶层 summary 分别统计 `consistent`、`inconsistent`、`inconclusive`、`skipped`，不能把 `inconclusive` 合并进健康数量。

### 状态判定

| 条件 | collection state |
| --- | --- |
| expected shards 全覆盖、无构建中索引、无差异 | `consistent` |
| 至少存在一条二次确认后的确定性差异 | `inconsistent` |
| 没有确定性差异，但存在 shard 缺失、权限不足、采集失败、字段缺失、构建中或并发变化 | `inconclusive` |
| 普通集合、view、系统库或被明确过滤 | `skipped` |

确定性差异和覆盖缺口可以同时存在。此时 state 仍为 `inconsistent`，同时通过 collector status 声明覆盖不完整，不能用 `inconclusive` 抹掉已确认问题。

## 预期 Shard 集合

预期 shard 集合必须独立于索引 observation 建立，禁止使用“本次返回过某个索引的 shard”作为基线，否则整个 shard 的 observation 缺失时会得到假健康。

固定流程：

1. 通过 `mongos` 列举选中 database 的 collection，并识别 view。
2. 通过版本适配后的 routing metadata 获取分片状态及持有 chunk 的 shard 集合；MongoDB 不同版本的 namespace/UUID/chunk schema 差异必须封装在 `pkg/mongo` 内。
3. 使用 `collStats.shards` 交叉验证实际可见 shard，但不得把它作为唯一 expected baseline；路由表存在、而本地 collection 或 stats 缺失本身就是 coverage 异常。
4. 使用 `listShards` 校验 shard 标识并提供派生连接目标。
5. 普通集合和 view 进入 `skipped`；无法可靠获得分片状态或 expected shards 的集合进入 `inconclusive`。
6. expected shards 为空时不得根据空 observation 推断为一致。

`config.collections` 或 `config.chunks` 不作为公开 contract。确需兼容旧版本时，只允许在 `pkg/mongo` 版本适配器中使用，原始 schema 和 BSON 不得进入 public result。

## 版本策略

### MongoDB 3.4–4.2.3：直接 `listIndexes`

该版本范围虽然支持 `$indexStats`，但其输出没有稳定的 `shard`、`spec`、`building` 字段，不能依赖 host 文本反推 shard 后再比较完整定义。

流程：

1. 从 `mongos` 得到 namespace、expected shards 和 `listShards` 结果。
2. 解析每个 shard 的 replica set 名和 seed addresses，复用 SDK 派生连接能力。
3. 每个 expected shard 只需从可用 replica set 入口执行一次 `listIndexes`；不做 member 间比较。
4. 任一 expected shard 无法连接、无权限或无法列出索引时记录 coverage status；没有其它确定性差异时 collection 为 `inconclusive`。

### MongoDB 4.2.4–6.x：优先 `$indexStats`

MongoDB 4.2.4 起，`$indexStats` 增加 `shard`、完整 `spec` 和构建中的 `building` 字段。

流程：

1. 在 `mongos` 对目标 collection 执行首阶段 `$indexStats`。
2. 使用独立 expected shards 校验返回覆盖，不从 `$indexStats` 结果反推预期范围。
3. `shard`、`spec` 缺失、expected shard 无 observation 或 decode 不完整时，对该 namespace 降级为直接 `listIndexes`。
4. fallback 完整成功后可据其结果判定一致；fallback 仍不完整时为 `inconclusive`。

### MongoDB 7.x：官方检查优先

流程：

1. 对 database scope 执行 `checkMetadataConsistency: 1, checkIndexes: true`；存在 collection 过滤时按 collection scope 执行。
2. 正确消费 `firstBatch`、cursor id 和后续 `getMore`，context 取消后停止并关闭 cursor。
3. 官方命令完整成功后，以官方结果为本次主结果，不再双跑 legacy collector。
4. 官方命令 unauthorized、unsupported 或执行失败，且 context 仍有效时，降级到 `$indexStats`，必要时再降级到直接 `listIndexes`。
5. fallback 结果必须记录 `fallbackFrom=check_metadata_consistency`、官方 reason code 和最终 coverage。只有 fallback 完整时才允许判定 `consistent`。
6. context canceled 或 deadline exceeded 后不启动新 fallback。

官方命令会同时返回非索引元数据问题。本能力只映射 `InconsistentIndex`、`MissingShardKeyIndex` 和未来可确定为索引域的类型；已知非索引类型不转换成 `index.*` finding。未知类型保留脱敏后的 source type，并以 `index.consistency_inconclusive` 表达无法归一化，不能当成 decode failure 或静默健康。

## 索引定义归一化

内部为每份索引定义生成两个 canonical fingerprint：

- semantic fingerprint：排除 `name`、`ns`、host、shard、accesses、building 等身份或运行态字段。
- full fingerprint：在 semantic 内容上包含 `name`，用于完整定义比较和稳定输出。

归一化规则：

- key pattern 保持 BSON 字段顺序；不能先转为无序 map。
- 比较 `v`、`unique`、`sparse`、`expireAfterSeconds`、`partialFilterExpression`、`collation`、`hidden`、`wildcardProjection`、text/geo/hashed 选项、`storageEngine` 等持久化属性。
- 未知持久化字段进入 canonical Extended JSON 后参与 SHA-256，避免新版本字段被静默忽略。
- 缺失字段与显式零值/false 分开；只有 MongoDB 语义明确等价时才允许标准化。
- public result 不输出 raw spec、partial filter 内容或 canonical bytes，只输出 fingerprint、key pattern 和顶层 differing field names。

## 差异分类与二次确认

比较顺序：

1. 按 index name 对齐 expected shards。
2. 同名索引缺失时，检查缺失 shard 上是否存在 semantic fingerprint 相同但名称不同的索引。
3. semantic 相同但名称不同，生成 `index.name_mismatch`。
4. 同名但 semantic fingerprint 不同，生成 `index.spec_mismatch`。
5. 某索引只存在于部分 expected shards，且不存在等价改名定义，生成 `index.missing_on_shard`。
6. 官方 `MissingShardKeyIndex` 归一为 `index.shard_key_support_missing`。

legacy 策略第一次发现差异后，只重新读取相关 namespace 和 shard：

- 两次 observation 稳定一致时才生成 warning finding。
- 第二次结果发生变化，或任一次观察到 `building=true` / collection index build 时，受影响索引只生成 `index.build_in_progress` 或 `index.consistency_inconclusive` info。
- 构建中的单个索引不抑制其它已稳定确认的索引差异。

稳定 finding code：

- `index.missing_on_shard`
- `index.name_mismatch`
- `index.spec_mismatch`
- `index.shard_key_support_missing`
- `index.build_in_progress`
- `index.consistency_inconclusive`

## 错误与退出语义

SDK 继续遵守公共错误 contract：

- unsupported、unauthorized、skipped 返回 result 和 nil error。
- 实际执行失败且仍有业务结果时返回 result 和 `*PartialError`。
- 参数非法、基础连接失败、非 `mongos`、无法发现任何扫描范围或 context 取消返回整体 error。

CLI 必须先渲染非空 result，再区分退出语义：

- `consistent`、`inconsistent`、partial coverage、unauthorized、fallback 和 `inconclusive` 均返回 0。
- 不提供 `--fail-on`。
- 参数非法、基础连接失败、非 `mongos`、范围发现失败、collection gate 超限、context 取消或输出失败返回非零。

索引差异是审计结果，不等同于 CLI 运行失败。

## 成本与安全

- `MaxCollections` 默认 500，按全部选中范围在 fan-out 前统一检查。
- collection 级有界并发；fallback 和二次确认复用同一 semaphore 和 context budget。
- 禁止将 URI、用户名、原始 server error、host-to-shard 重建细节、raw BSON 或 partial filter 值写入 table、JSON 或普通日志。
- shard 名和 namespace 可作为默认作用域；用于可共享 diagnostic bundle 时继续走主机名与 namespace 匿名化策略。
- 只读不代表零成本；table Coverage 段和 JSON statuses 必须说明 strategy、fallback 与 incomplete visibility。

## 测试矩阵

### 离线 fixture

| 版本 | 必测策略 |
| --- | --- |
| 3.4、4.2.3 | expected shards + 直接 `listIndexes` |
| 4.2.4、4.4、5.x、6.x | `$indexStats` 的 shard/spec/building 与 direct fallback |
| 7.x | 官方 command、cursor/getMore、官方失败后的 fallback |

场景至少覆盖：

- 全部 shard 一致。
- 整个 shard observation 缺失。
- routing metadata 声明 shard 持有 chunk，但 `collStats` 或本地 collection 不可见。
- 单 shard 缺失索引。
- semantic 相同但名称不同。
- 同名索引的 key 顺序或任一持久化 option 不同。
- partial/TTL/unique/hidden/wildcard/text/geo/hashed 定义。
- 构建中索引与两次读取发生变化。
- shard 不可达、无权限、字段缺失、decode 失败和 timeout。
- 普通集合、view、空分片集合、系统库和 collection gate。
- `InconsistentIndex`、`MissingShardKeyIndex`、已知非索引类型和未知 source type。
- table/JSON 稳定排序、脱敏、partial result 和 CLI 退出码。

### 只读 live E2E

- MongoDB 3.4 分片集群：验证 expected shards、派生 shard 连接和直接 `listIndexes`。
- MongoDB 7.x 分片集群：验证官方 command、cursor 消费和只读行为。
- 中间版本以 fixture 覆盖，不强制准备真实集群。
- 工具不得为测试创建或删除索引。若没有预置不一致 namespace，负向 live case 保持 `manual-gate`，不得以健康环境替代。

## 验收标准

- MongoDB 3.4–7.x 的策略选择与 4.2.4、7.0 断点有 fixture 证明。
- 完全缺失 shard observation 时不能输出 `consistent`。
- 同 key 改名、同名 spec 差异和缺失索引能稳定分类。
- 构建中或并发 DDL 不产生未经二次确认的 legacy warning。
- 7.x 官方命令成功时不双跑；失败 fallback 后仍可解释原始失败和最终 coverage。
- SDK、table、JSON 均不暴露 raw BSON、partial filter 业务值或内部连接信息。
- 默认测试、race、CLI golden、3.4/7.x 只读 live gate 和文档回写均有结果摘要；live 环境缺失时功能卡保持 manual gate。

## 参考资料

- [MongoDB 4.4 `$indexStats`（含 4.2.4 字段说明）](https://www.mongodb.com/docs/v4.4/reference/operator/aggregation/indexstats/)
- [MongoDB `checkMetadataConsistency`](https://www.mongodb.com/docs/manual/reference/command/checkmetadataconsistency/)
- [MongoDB `InconsistentIndex`](https://www.mongodb.com/docs/manual/reference/inconsistency-type/inconsistentindex/)
- [MongoDB `MissingShardKeyIndex`](https://www.mongodb.com/docs/manual/reference/inconsistency-type/missingshardkeyindex/)
