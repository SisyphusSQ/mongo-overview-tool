# 04 索引与容量审计

## 目标

新增 `index-audit` 和 `capacity`：

- `index-audit` 找出值得人工复核的索引使用与定义问题，但绝不自动删除或创建索引。
- `capacity` 解释数据、索引、已分配空间和可复用空间，并支持两份脱敏快照的离线差异比较。

两条命令共享数据库/集合过滤、数量 gate、有界并发和部分结果 contract。

## `index-audit` CLI

```text
mot index-audit [connection flags]
  --format table|json
  --database <db1,db2>
  --collection <c1,c2>
  --min-observation 168h
  --max-collections 500
  --concurrency <n>
```

默认观察窗口为 7 天。该窗口不是等待时间，而是 `$indexStats.accesses.since` 到采集时间的最短跨度。

## `index-audit` SDK

```go
type IndexAuditOptions struct {
    Databases         []string
    Collections       []string
    IncludeSystemDB   bool
    MinObservation    time.Duration
    MaxCollections    int
    Concurrency       int
}

type IndexAuditResult struct {
    CollectedAt       time.Time            `json:"collectedAt"`
    Collections       []CollectionIndexAudit `json:"collections"`
    Findings          []DiagnosticFinding  `json:"findings"`
    CollectorStatuses []CollectorStatus    `json:"collectorStatuses"`
}
```

每个 index observation 保留：namespace、index name、key pattern、必要的 index properties、host、shard、ops、since、building、size（可获得时）。

## 索引审计规则

### 1. 长期零使用候选

仅在以下条件全部成立时生成 `index.unused_candidate`：

- 非 `_id_` 索引。
- 每个可达数据节点的 `$indexStats` 都成功。
- 每个节点的 `ops == 0`。
- 每个节点的 observation duration 均达到 `MinObservation`。
- 采集期间没有 index build / rebuild 迹象。

TTL、unique、sparse、partial、hidden、wildcard、text、geo、hashed 或 shard key supporting index 需要在 evidence 中标注；即使零使用，也只给“人工复核”建议，不给删除建议。

如果部分节点不可达、统计起点过新或权限不足，使用 `index.usage_inconclusive` info finding，不把索引判为 unused。

### 2. 疑似前缀冗余

`index.redundant_prefix_candidate` 只比较 key pattern 前缀，并要求以下 properties 兼容：

- collation
- unique
- sparse
- partial filter expression
- hidden
- wildcard projection
- expireAfterSeconds

任何属性不同都不得自动视为冗余。多键状态和真实 query sort / projection 无法只从定义完整推断，因此 recommendation 必须要求结合慢日志和 query pattern 人工确认。

### 3. 索引空间占比

- 输出每个 namespace 的 `totalIndexSize / dataSize`。
- 比例高只作为 info evidence，不使用统一比例直接判 critical。
- 如果索引总大小超过数据大小且存在长期零使用候选，可生成 warning 组合 finding。
- 对空集合、clustered collection 或缺失 data size 的场景不计算无穷比例。

### 4. 构建中与跨 shard 差异

- `building=true` 或 collection stats 中存在 index build 时输出 `index.build_in_progress` info。
- 一期只比较从各 shard / node 读取到的 index spec；发现同名 spec 不一致或缺失时输出 warning。
- MongoDB 7+ 的官方 `checkMetadataConsistency(checkIndexes=true)` 留到高级 `metadata-check`，不在一期自动执行。

## `capacity` CLI

```text
mot capacity [connection flags]
  --format table|json
  --database <db1,db2>
  --collection <c1,c2>
  --free-storage
  --snapshot <path>
  --max-collections 500
  --concurrency <n>

mot capacity diff <before.json> <after.json>
  --format table|json
```

规则：

- 默认不请求 free storage；只有 `--free-storage` 显式启用。
- `--snapshot` 的本地文件写入属于 CLI adapter；`pkg/mot` 只返回结构化 result。
- `capacity diff` 为纯离线能力，不连接 MongoDB。
- 快照不包含 URI、用户、原始查询或业务文档。

## `capacity` SDK

```go
type CapacityOptions struct {
    Databases         []string
    Collections       []string
    IncludeSystemDB   bool
    IncludeFreeStorage bool
    MaxCollections    int
    Concurrency       int
}

type CapacityResult struct {
    SchemaVersion     int                  `json:"schemaVersion"`
    ClusterIdentity   CapacityIdentity     `json:"clusterIdentity"`
    CollectedAt       time.Time            `json:"collectedAt"`
    Databases         []DatabaseCapacity   `json:"databases"`
    Findings          []DiagnosticFinding  `json:"findings"`
    CollectorStatuses []CollectorStatus    `json:"collectorStatuses"`
}

func DiffCapacity(before, after CapacityResult) (*CapacityDiffResult, error)
```

`ClusterIdentity` 只序列化拓扑类型和匿名化稳定标识，不写入 URI、密码或原始 member 地址。稳定标识以拓扑类型、排序后的 replica set / shard 名和规范化 member 地址为输入计算 SHA-256；输入只参与计算，不进入快照，结果保存完整 hex digest。拓扑成员变化会产生新标识，此时 diff 默认判定为不同集群并拒绝比较；调用方显式允许后，结果必须标记 incomparable，且不能生成增长率 finding。

## 容量指标语义

数据库层：

- objects、dataSize、storageSize、indexSize、totalSize。
- fsUsedSize、fsTotalSize；按 node/filesystem 语义去重后汇总或展示。
- freeStorageSize、indexFreeStorageSize、totalFreeStorageSize（仅显式启用且服务器返回时）。

集合层：

- count、avg object size、logical data size、allocated storage size。
- total index size、per-index size。
- free storage、compression ratio、index/data ratio。
- sharded collection 按 shard 保留明细，同时给出安全的总计。

固定解释：

- `dataSize` 是逻辑未压缩数据规模，不能等同磁盘占用。
- `storageSize` 包含为集合分配、但可能未被数据占用的空间，不包含索引。
- free storage 表示存储引擎可复用空间，不承诺操作系统可立即回收。
- 删除文档后 `storageSize` 不一定下降，不自动建议 compact。
- 非正常关闭后统计可能存在偏差，finding 只能提示复核，不能自动执行 validate。

## 快照与 Diff

快照 JSON：

- 使用稳定 `schemaVersion`。
- 数值统一保存原始 bytes / counts，不保存 humanized 字符串。
- slice 按 database、namespace、shard、host 稳定排序。
- 新增字段保持向后兼容；未知字段可忽略。

diff 输出：

- database / namespace 的 count、data、storage、index 增量。
- 窗口长度与平均每日增长仅在时间顺序有效时计算。
- collection 新增、删除分别标记，不显示为巨大的正负异常。
- 一侧字段 unsupported 时结果为 unavailable，不以 0 计算。
- 不在只有两个样本时推导容量耗尽日期；仅展示线性日增量事实。

## 成本保护

- `MaxCollections` 默认 500；超过时要求 database / collection 过滤或显式提高上限。
- free storage collector 标记 `expensive-opt-in`，单独超时和 status。
- collection 级任务使用有界并发；同一 node 不进行无上限 fan-out。
- 对 view 识别并跳过 storage stats，不能因单个 view 使数据库失败。

## 验收标准

- index audit 覆盖多节点 stats、重启后统计起点、TTL/partial/unique 属性和部分节点失败。
- 未达到最短观察窗口时不生成 unused candidate。
- 前缀相同但属性不同的索引不误判为冗余。
- free storage 默认不执行，显式启用后仍受数量和超时保护。
- capacity fixture 覆盖普通、分片、空集合、view、字段缺失和异常关闭提示。
- snapshot diff 覆盖新增/删除集合、schema version、不同集群和不可比较字段。
- SDK 不读写本地快照文件；CLI adapter 的写入错误不会丢失已采集 result。

## 参考资料

- [MongoDB `$indexStats`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/indexstats/)
- [MongoDB `dbStats`](https://www.mongodb.com/docs/manual/reference/command/dbstats/)
- [MongoDB `$collStats`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/collstats/)
