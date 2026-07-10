# 02 Doctor 综合健康检查

## 目标

新增 `doctor`，将现有拓扑与节点指标转换为确定性检查和可解释 finding，使用户一次执行即可获得按严重级别排序的风险列表。

`doctor` 是一次性巡检，不是持续监控。单次快照无法证明趋势的检查必须采用保守表述，或内部进行短时二次采样。

## CLI

```text
mot doctor [connection flags]
  --format table|json
  --minimum-severity info|warning|critical
  --node-concurrency <n>
  --timeout <duration>
  --replication-lag-warning <duration>
  --replication-lag-critical <duration>
  --include-system-db
```

默认值：

- `--format table`
- `--minimum-severity info`，即默认输出全部 severity；按 `critical -> warning -> info` 排序。
- 复制延迟 warning 为 60 秒、critical 为 300 秒，可由调用方覆盖。
- 不默认执行 free storage、索引遍历和高级分片检查。

## SDK

```go
type DoctorOptions struct {
    MinimumSeverity         Severity
    NodeConcurrency         int
    ReplicationLagWarning   time.Duration
    ReplicationLagCritical  time.Duration
    IncludeSystemDB         bool
}

type DoctorResult struct {
    ClusterType      ClusterType       `json:"clusterType"`
    CollectedAt      time.Time         `json:"collectedAt"`
    Findings         []DiagnosticFinding `json:"findings"`
    CollectorStatuses []CollectorStatus `json:"collectorStatuses"`
    Summary          FindingSummary    `json:"summary"`
}
```

`Summary` 只包含 severity 数量和最严重级别，不生成不可解释的总分。

## 检查目录

### 1. 拓扑与 majority

| Code | 条件 | Severity | Evidence |
| --- | --- | --- | --- |
| `replica.primary_missing` | replica set 没有 PRIMARY | critical | replica set、成员状态 |
| `replica.member_unhealthy` | member `health != 1` 或状态为 DOWN / UNKNOWN / ROLLBACK | critical | node、state、heartbeat message |
| `replica.majority_unavailable` | 健康可写 voting member 少于 `writeMajorityCount` | critical | available、required |
| `replica.member_recovering` | RECOVERING / STARTUP2 / initial sync | warning | state、initial sync progress |
| `replica.arbiter_present` | 存在 arbiter | info | node、votes；只陈述拓扑，不断言错误 |

分片场景逐 shard replica set 检查，某个 shard critical finding 不能被其它健康 shard 抵消。

### 2. 复制与 heartbeat

| Code | 条件 | Severity |
| --- | --- | --- |
| `replica.lag_high` | lag 超过 warning 阈值 | warning |
| `replica.lag_critical` | lag 超过 critical 阈值 | critical |
| `replica.heartbeat_error` | `lastHeartbeatMessage` 非空 | warning；成员不健康时由更严重 finding 覆盖 |
| `replica.heartbeat_stale` | heartbeat 时间显著早于 `replSetGetStatus.date` | warning |
| `replica.sync_source_missing` | SECONDARY 没有有效 sync source 且不是稳定无写入场景 | warning |
| `replica.too_stale` | 输出包含 `tooStale=true` | critical |

复制延迟优先使用 wall clock 字段；旧版本缺少 wall time 时才回退到 optime timestamp。没有写入时，不能仅因最后 optime 时间较旧就判定 lag。

### 3. 连接与队列

| Code | 条件 | Severity |
| --- | --- | --- |
| `connection.headroom_low` | available / (current + available) 低于 10% | warning |
| `connection.headroom_critical` | 可用比例低于 5% | critical |
| `connection.rejected` | 短时二次采样发现 rejected 增长 | critical |
| `operation.queue_sustained` | 连续两个快照都有排队且未下降 | warning |
| `operation.ticket_exhausted` | 执行 ticket 可用为 0 且存在排队 | critical |

连接字段在旧版本缺失时标记 collector 能力不足，不将缺失值当作 0。

### 4. WiredTiger

cache fill ratio 不能单独判断内存压力。WiredTiger 通常会主动使用大部分 cache，因此只在以下组合证据下生成 warning：

- cache 接近配置上限；并且
- application thread eviction、aggressive eviction、read into cache 或 queued operation 在短采样窗口中明显增长。

确定性异常包括：

- 读写 ticket 耗尽且存在等待。
- cache configured size 为 0 或字段结构异常，标记 collector failed，不生成“cache 使用率 100%”假结论。

### 5. 磁盘与容量

基础 `doctor` 只读取已有 `dbStats` 文件系统字段，不启用 `freeStorage`：

| Code | 条件 | Severity |
| --- | --- | --- |
| `storage.fs_headroom_low` | `fsUsedSize / fsTotalSize >= 85%` | warning |
| `storage.fs_headroom_critical` | 使用率 `>= 95%` | critical |
| `storage.stats_unavailable` | 所有业务库都无法获得 `dbStats` | warning |

多个数据库可能报告同一文件系统，按 node + filesystem 语义去重；无法识别 filesystem 时至少按 node 去重，不能把数据库数量乘入磁盘占用。

### 6. Uptime、选举和 initial sync

- 节点 uptime 低于一小时：`node.recent_restart`，info；如果同时有缺 primary、复制延迟或 initial sync，则由相关 finding 提升严重级别。
- 当前 primary 的 election date 距采集时间很近：`replica.recent_election`，warning；默认窗口 15 分钟。
- initial sync 输出剩余时间时，作为 evidence 展示；不根据进度百分比推断完成时间之外的结论。

### 7. Oplog 窗口

oplog window 需要读取 `local.oplog.rs` 的最早和最新时间，权限与拓扑不同于基础 `serverStatus`：

- 作为独立 collector；权限不足时 `skipped`。
- finding 比较 secondary lag 与 oplog window，lag 接近或超过窗口时分别 warning / critical。
- 不扫描 oplog 全表，只按 `$natural` 首尾各读取一条并读取集合统计。
- 分片场景逐 shard replica set 计算，不使用 mongos 聚合值。

## 去重与排序

- 同一根因的 critical finding 抑制同 scope 的低级别派生 finding，例如 `majority_unavailable` 可抑制单纯 `primary_missing` 的重复建议文本，但 evidence 仍保留。
- finding 按 severity、scope type、scope name、code 稳定排序。
- 同一个 node 的多项异常不合并为一段不可解析文本。

## 建议文案规则

- 建议人工检查下一步，不自动执行变更。
- 不建议直接 step down、resize、drop index 或 compact；只说明需要结合业务流量和维护窗口评估。
- 建议中引用具体 evidence，例如“确认 node3 heartbeat 网络路径”，而不是通用“检查数据库”。

## 验收标准

- fixture 覆盖健康副本集、缺 PRIMARY、majority 不可用、initial sync、旧版本字段缺失和分片中单 shard 异常。
- 无写入的静止副本集不会被误判为复制延迟。
- cache 高使用率但无 eviction/queue 压力时不生成 warning。
- 权限不足的 oplog collector 不影响其它检查。
- table 与 JSON 的 finding code、severity、scope 和 evidence 一致。
- 输出不包含 URI 密码、完整 query 或用户/session 信息。

## 参考资料

- [MongoDB `replSetGetStatus`](https://www.mongodb.com/docs/manual/reference/command/replsetgetstatus/)
- [MongoDB `serverStatus`](https://www.mongodb.com/docs/manual/reference/command/serverstatus/)
- [MongoDB `dbStats`](https://www.mongodb.com/docs/manual/reference/command/dbstats/)
