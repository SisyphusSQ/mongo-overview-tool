# 03 活跃操作与热点采样

## 目标

补齐两类事故现场问题：

1. `ops` 回答“现在有哪些操作正在运行、等待或持有事务资源”。
2. `hotspot` 回答“最近一个短窗口内负载如何变化、哪些 namespace 最热”。

两者均为只读能力，不包含终止操作。`ops` 是单快照，`hotspot` 是有界双快照。

## `ops` CLI

```text
mot ops [connection flags]
  --format table|json
  --min-duration 2s
  --all-users
  --include-idle-transactions
  --include-idle-cursors
  --database <db1,db2>
  --namespace <db.collection>
  --limit 100
```

默认：

- `min-duration=2s`
- 请求 all users；无 `inprog` 权限时降级为当前用户范围并标记结果不完整。
- 包含持锁或仍打开的 inactive transaction，不包含普通 idle connection / idle cursor。
- 等待锁、等待 flow control、维护操作和带 progress 的操作不受最小时长过滤。
- 最多返回 100 条，优先保留 waiting、transaction 和运行时间最长的操作。

## `ops` SDK

```go
type CurrentOperationsOptions struct {
    MinDuration             time.Duration
    AllUsers                bool
    IncludeIdleTransactions bool
    IncludeIdleCursors      bool
    Databases               []string
    Namespaces              []string
    Limit                   int
}

type CurrentOperationsResult struct {
    ClusterType       ClusterType          `json:"clusterType"`
    CollectedAt       time.Time            `json:"collectedAt"`
    Visibility        string               `json:"visibility"`
    Operations        []CurrentOperation   `json:"operations"`
    Findings          []DiagnosticFinding  `json:"findings"`
    CollectorStatuses []CollectorStatus    `json:"collectorStatuses"`
}
```

`CurrentOperation` 使用显式字段，不直接公开 `$currentOp` 原始 document：

- host、shard、namespace、operation type、appName。
- query hash / plan summary（存在时）。
- running duration、waiting for lock、waiting for flow control。
- transaction start / expiry、participant count。
- maintenance message、progress done / total。
- kill pending 状态。

默认不包含完整 command、filter、pipeline、client、user、lsid 或 transaction ID。

## `$currentOp` 采集

- 现代版本优先使用 admin database 的 `$currentOp` aggregation。
- pipeline 先在服务器端 `$match` 最小时长、waiting、transaction 和维护操作，再 `$project` 必要字段，避免传输全量 command。
- 分片从 `mongos` 执行时，默认获取 shard 侧操作；版本支持且用户显式要求时才使用 `targetAllNodes`。
- 旧版本不支持 `$currentOp` 时，才调用兼容 command，并经过相同字段映射和脱敏。
- fallback 不得掩盖服务器错误；结果中记录实际 source。

## `ops` Findings

| Code | 条件 | 默认 Severity |
| --- | --- | --- |
| `operation.waiting_for_lock` | `waitingForLock=true` | warning |
| `operation.long_running` | 超过 30 秒且非维护类 | warning |
| `transaction.long_running` | transaction 已运行超过 60 秒或接近 expiry | warning |
| `operation.waiting_for_flow_control` | 正在等待 flow control | warning |
| `operation.kill_pending` | 已标记终止但尚未退出 | info |
| `maintenance.in_progress` | index build、resharding、validate、compact 等带 progress 操作 | info |

阈值只影响 finding，不影响用户通过 `--min-duration` 查看原始 compact operation 条目。

## `hotspot` CLI

```text
mot hotspot [connection flags]
  --format table|json
  --duration 10s
  --top 10
  --node-concurrency <n>
  --database <db1,db2>
  --include-system-db
```

约束：

- duration 必须大于 0，默认 10 秒。
- duration 的等待尊重 context；取消后返回已完成的第一快照和取消错误，但不伪造 delta。
- `top` 只能在 `mongod` 执行；分片和副本集复用派生数据节点连接。
- 默认排除 `admin`、`config`、`local`，除非显式启用。

## `hotspot` SDK

```go
type HotspotOptions struct {
    Duration        time.Duration
    TopN            int
    NodeConcurrency int
    Databases       []string
    IncludeSystemDB bool
}

type HotspotResult struct {
    ClusterType       ClusterType          `json:"clusterType"`
    StartedAt         time.Time            `json:"startedAt"`
    FinishedAt        time.Time            `json:"finishedAt"`
    EffectiveDuration time.Duration        `json:"effectiveDuration"`
    Nodes             []NodeHotspot         `json:"nodes"`
    Namespaces        []NamespaceHotspot    `json:"namespaces"`
    Findings          []DiagnosticFinding  `json:"findings"`
    CollectorStatuses []CollectorStatus    `json:"collectorStatuses"`
}
```

## 双快照算法

第一、第二快照对每个 node 采集：

- `serverStatus.uptime`
- `opcounters`、`metrics.document`
- `opLatencies`（字段存在时）
- connections、network、queue / execution ticket
- WiredTiger cache / eviction 计数
- `top` namespace count / time

差值规则：

1. 以同一个 node address / process identity 对齐。
2. 使用实际采集时间差，而不是用户请求 duration，计算 rate。
3. 任一累计计数器下降，或 uptime 下降，标记 `counter_reset`；该 node 不生成错误 delta。
4. 第二快照缺少 node 时保留 status `node_unreachable`，其它节点继续计算。
5. namespace 在第一快照不存在时以 0 为基线；第二快照消失时不生成负值。
6. 分片结果同时保留 shard 和 host，不能把同名 namespace 的 node 计数简单覆盖。

## 输出指标

Node 层：

- insert / query / update / delete / getMore / command 每秒速率。
- documents returned / inserted / updated / deleted 每秒速率。
- read / write / command 平均延迟；没有操作时为 unavailable，不用 0 代表。
- connections created / rejected rate。
- network bytes rate。
- eviction、cache read/write 和 ticket wait delta。

Namespace 层：

- read count、write count、read time、write time delta。
- 平均 read/write time。
- 按 total time、operation count 分别提供排序值。

Top N 默认按窗口内 total time 排序；相同值按 namespace、shard、host 稳定排序。

## Findings

`hotspot` 只对窗口内变化下结论：

- `hotspot.namespace_read` / `hotspot.namespace_write`：Top namespace 的事实性 info。
- `connection.rejected_during_sample`：rejected 增长，critical。
- `operation.queue_sustained`：两个快照均有排队，warning。
- `storage.eviction_pressure`：eviction / application thread wait 与延迟同时增加，warning。
- `node.counter_reset`：采样期间节点重启或计数器重置，warning，结果不可比较。

不根据单个 namespace 的高 QPS 自动判断异常；热点是排序事实，需要结合用户基线。

## 验收标准

- `$currentOp` pipeline 在服务端过滤并投影，默认结果不含 command 字面量。
- 受限用户降级后明确展示 visibility，不误称为全局结果。
- fixture 覆盖锁等待、idle transaction、index build progress 和分片 operation。
- 双快照测试覆盖正常 delta、counter reset、节点消失、新 namespace、零操作和 context cancel。
- `hotspot` 不在 `mongos` 上直接执行 `top`。
- table 与 JSON 的 rate 使用同一 effective duration。

## 参考资料

- [MongoDB `$currentOp`](https://www.mongodb.com/docs/manual/reference/operator/aggregation/currentop/)
- [MongoDB `top`](https://www.mongodb.com/docs/manual/reference/command/top/)
- [MongoDB `serverStatus`](https://www.mongodb.com/docs/manual/reference/command/serverstatus/)
