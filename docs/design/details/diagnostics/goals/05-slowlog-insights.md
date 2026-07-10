# 05 慢日志洞察

## 目标

在现有 `SlowlogSummary` / `SlowlogDetail` 兼容语义上追加可解释的查询效率信息，让用户能从“哪些 query hash 慢”继续判断“慢在哪里、是否值得检查索引”。

本能力仍以已经存在的 `system.profile` 为输入。工具不自动开启、关闭或调整 Profiler，也不改变 `slowms`、sample rate 或 filter。

## 兼容原则

- 保留现有 `slowlog --sort cnt|maxMills|maxDocs`、`--hash` 和 `--db` 行为。
- 保留 MongoDB 3.4 等缺少 `queryHash` 时的 `legacy:` 兼容标识。
- 对公开 result 只追加字段，不重命名或删除现有 JSON 字段。
- Detail 继续能够返回完整 slowlog 和 index BSON 给 SDK 调用方，但默认 CLI table / JSON 使用脱敏的 insight 结构。
- 如果不存在 `system.profile`，保持“没有可用 profiler 数据”的正常结果，不自动创建 collection 或修改 profiling level。

## Summary 字段扩展

```go
type SlowlogSummaryItem struct {
    // 现有字段保持不变。
    Namespace    string
    Operation    string
    QueryHash    string
    Count        int64
    MaxMillis    int64
    MinMillis    int64
    MaxDocs      int64
    FirstTime    time.Time
    LastTime     time.Time

    PlanSummary           string  `json:"planSummary,omitempty"`
    MaxKeysExamined       int64   `json:"maxKeysExamined,omitempty"`
    MaxDocsExamined       int64   `json:"maxDocsExamined,omitempty"`
    MaxDocsReturned       int64   `json:"maxDocsReturned,omitempty"`
    WorstDocsToReturned   float64 `json:"worstDocsToReturned,omitempty"`
    WorstKeysToReturned   float64 `json:"worstKeysToReturned,omitempty"`
    MaxPlanningMicros     int64   `json:"maxPlanningMicros,omitempty"`
    MaxCPUNanos           int64   `json:"maxCpuNanos,omitempty"`
    AppNames              []string `json:"appNames,omitempty"`
    ErrorCount            int64   `json:"errorCount,omitempty"`
    CollectionScanCount   int64   `json:"collectionScanCount,omitempty"`
}
```

字段缺失与数值零必须可区分。实际实现可使用 presence bool、pointer 或内部 optional 类型，不能把旧版本缺失字段展示为真实 0。

## 聚合规则

继续以 namespace、query hash、operation、plan summary 为 query shape 主键；legacy 版本沿用现有稳定标识。

追加聚合：

- `keysExamined`、`docsExamined`、`nreturned` 的 max。
- 仅在 `nreturned > 0` 时计算 examined / returned ratio。
- `nreturned == 0` 且 examined > 0 时记录事实 finding，不使用除零或无限大 JSON。
- `planningTimeMicros`、`cpuNanos` 字段存在时取 max。
- planSummary 含 `COLLSCAN` 时累计 collection scan count。
- profiler document 含 error code / error name 时累计 error count，但默认不展示 error message 中的业务值。
- appName 去重、稳定排序并限制数量；超出部分只给 count。

不能把不同 plan summary 强行合并成一个 item，否则会丢失同一 query shape 的 plan 变化信息。

## Findings

| Code | 条件 | Severity | 说明 |
| --- | --- | --- | --- |
| `query.collection_scan` | plan summary 为 COLLSCAN | warning | 对高频或高 examined 查询优先展示 |
| `query.docs_examined_high` | examined / returned 明显偏高 | warning | evidence 给出 ratio 和样本范围 |
| `query.zero_return_scan` | 返回 0 但扫描大量文档 | warning | 可能是不命中查询或索引选择性问题 |
| `query.planning_time_high` | planning time 占 max latency 比例高 | info / warning | 只在字段存在时判断 |
| `query.error_observed` | profiler 中存在失败操作 | warning | 展示 error code/name，不展示原始 message |
| `query.plan_changed` | 同一 query hash 出现多个 plan summary | info | 只陈述变化，不判断 plan cache 异常 |

阈值基于 item 自身证据并允许 CLI / SDK 覆盖。设计不承诺自动生成可直接执行的 `createIndex` 语句。

## Detail 与索引上下文

`SlowlogDetailResult` 追加脱敏视图：

```go
type SlowlogInsight struct {
    Namespace       string              `json:"namespace"`
    QueryHash       string              `json:"queryHash"`
    Operation       string              `json:"operation"`
    PlanSummary     string              `json:"planSummary,omitempty"`
    Metrics         SlowlogMetrics      `json:"metrics"`
    Indexes         []IndexSummary      `json:"indexes"`
    Findings        []DiagnosticFinding `json:"findings"`
}
```

`IndexSummary` 仅包含 index name、key pattern 和影响语义的 properties。partial filter、collation 或 wildcard projection 可能包含字段结构，应保留结构但默认隐藏具体常量值。

完整 raw slowlog：

- SDK 继续通过现有 detail 字段获得，保持兼容。
- 新增 helper 生成 compact / redacted insight，不把 raw 放入默认 CLI JSON。
- CLI 如未来提供 raw 输出，必须使用显式危险提示和独立 flag，不与 `--format json` 等价。

## Profiler 状态

每个数据库记录：

- `system.profile` 是否存在。
- 可见记录的 first / last time。
- 本次聚合是否因权限、超时或字段不兼容而跳过。

工具不得仅凭 collection 不存在就建议用户在生产开启 level 2。建议文案应说明 Profiler 可能带来性能与磁盘开销，并由用户按维护规范决定。

## 分片和节点语义

- 保留当前逐 shard、逐 PRIMARY/SECONDARY 的来源维度。
- 同一 query hash 在不同 host 的 item 不默认合并；上层可根据 namespace + query hash 展示 aggregate view，但必须保留 source hosts。
- profiler 数据窗口不同的 host 不直接比较 count rate。
- 无法连接某个 member 时返回部分结果和 collector status。

## 验收标准

- 旧版缺字段 fixture 不产生伪造零值或 decode error。
- `nreturned=0` 不出现除零、Infinity 或 NaN JSON。
- 同一 query hash 不同 plan summary 能分别展示并产生 plan changed info。
- 默认 CLI JSON 不包含 raw command、filter、pipeline、client、user 或 session。
- `system.profile` 不存在时不报整条命令失败，也不执行配置写入。
- 聚合并发、context cancel 和分片部分失败继续符合现有 SDK contract。

## 参考资料

- [MongoDB Database Profiler Output](https://www.mongodb.com/docs/manual/reference/database-profiler/)
- [MongoDB Database Profiler](https://www.mongodb.com/docs/manual/tutorial/manage-the-database-profiler/)
- [MongoDB Analyze Query Performance](https://www.mongodb.com/docs/manual/tutorial/evaluate-operation-performance/)
