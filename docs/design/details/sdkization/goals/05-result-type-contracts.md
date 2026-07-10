# 目标 5：公开类型表达真实业务结果

## 目标定义

SDK 返回值必须表达真实业务结果，而不是 CLI 表格字符串。外部调用方应能直接读取数值字段、时间字段、拓扑字段和统计字段，再按自己的业务场景做判断、展示或告警。

核心要求：

- 返回结构体，不返回已渲染表格。
- 数值保持原始单位。
- 时间使用 `time.Time` 或 `time.Duration`。
- 字段含义稳定并可文档化。
- CLI 格式化由 adapter 完成。

## 现状差距

当前 `internal/model.OverviewStats` 混合了业务字段和展示字段：

- `Size`、`MemUsed`、`MemRes` 是展示字符串。
- `Delay` 是字符串。
- `ColoredState` 是终端颜色输出专用字段。
- 同时又有 `CacheUsed`、`CacheSize` 这类原始数值。

这种结构不适合 SDK，因为调用方无法可靠做数值比较。

## 类型设计原则

### 原始值优先

SDK result 使用原始值：

```go
CacheSizeBytes int64
CacheUsedBytes int64
ReplicationLag time.Duration
Uptime          time.Duration
```

CLI formatter 再决定展示为：

```text
4.0GB
76.3%
0s
```

### 拓扑层级明确

分片集群和副本集使用统一结果模型：

```go
type OverviewResult struct {
    ClusterType ClusterType
    Hosts       []string
    ReplicaSets []ReplicaSetOverview
}
```

副本集场景下 `ReplicaSets` 只有一个元素；分片场景下每个 shard replica set 一个元素。

### 可选字段明确

部分节点可能没有某些指标，例如 arbiter 没有 WiredTiger cache。可选字段策略：

- 基础字段用零值。
- 需要区分未知和真实零值时使用 pointer 或 `Valid` 字段。

示例：

```go
type OptionalInt64 struct {
    Value int64
    Valid bool
}
```

第一轮可以先使用零值加文档说明，后续如出现歧义再引入 optional 类型。

## Overview Result

```go
type OverviewResult struct {
    ClusterType ClusterType
    Hosts       []string
    ReplicaSets []ReplicaSetOverview
}

type ReplicaSetOverview struct {
    Name  string
    Nodes []NodeOverview
}

type NodeOverview struct {
    ReplicaSet string
    Address    string
    State      string
    Version    string
    Uptime     time.Duration

    ConnectionsCurrent int64
    QueueReaders       int64
    QueueWriters       int64
    ActiveReaders      int64
    ActiveWriters      int64

    CacheSizeBytes int64
    CacheUsedBytes int64
    ReplicationLag time.Duration
}
```

CLI 派生字段：

- `Size` = `HumanizeBytes(CacheSizeBytes)`
- `MemRes` = `HumanizeBytes(CacheUsedBytes)`
- `MemUsed` = `CacheUsedBytes / CacheSizeBytes`
- `Delay` = `ReplicationLag.String()`

这些派生字段不进入 SDK result。

## Collection Stats Result

```go
type CollectionStatsResult struct {
    Databases []DatabaseStats
}

type DatabaseStats struct {
    Name             string
    StorageSizeBytes int64
    Collections      []CollectionStats
}

type CollectionStats struct {
    Namespace        string
    Count            int64
    AvgObjectBytes   float64
    StorageSizeBytes int64
    IsSharded        bool
    IndexCount       int
    TotalIndexBytes  int64
}
```

排序规则应稳定：

- 默认按 `Count` 降序。
- Count 相同时按 `Namespace` 升序。

## Slowlog Result

Slowlog summary 保持聚合语义：

```go
type SlowlogSummaryItem struct {
    Namespace string
    Operation string
    QueryHash string
    Count     int64
    MaxMillis int64
    MinMillis int64
    MaxDocs   int64
    FirstTime time.Time
    LastTime  time.Time
}
```

兼容约定：MongoDB 3.4 等 profiler 不提供 `queryHash` 时，`QueryHash` 使用 `legacy:` 前缀的稳定兼容标识。该标识按 namespace、operation 和 plan summary 聚合，用于连接 Summary 与 Detail；它不等同于新版 MongoDB 的查询形状哈希，Detail 返回这一聚合组最新的匹配文档。

Detail 返回原始文档和索引：

```go
type SlowlogDetailResult struct {
    Namespace string
    Slowlog   bson.M
    Indexes   []bson.M
}
```

CLI 当前删减字段是展示策略，不应默认污染 SDK 原始结果。

## Bulk Result

```go
type BulkResult struct {
    Database   string
    Collection string
    DryRun     bool

    MatchedTotal int64
    Processed    int64
    Deleted      int64
    Matched      int64
    Modified     int64

    BatchCount int
    StartedAt  time.Time
    FinishedAt time.Time
}
```

语义：

- `MatchedTotal`：操作开始前 count 的匹配总量。
- `Processed`：游标中已处理的 `_id` 数。
- `Deleted`：delete 实际删除数。
- `Matched`：update 命中数。
- `Modified`：update 实际修改数。
- dry-run 时只填 `MatchedTotal` 和基本信息，不执行写操作。

## JSON 序列化

公开 result 类型建议带 JSON tag，方便调用方直接输出 API：

```go
type NodeOverview struct {
    Address string `json:"address"`
}
```

规则：

- JSON tag 使用 lower camel case。
- 不暴露颜色字段。
- 不暴露密码和完整敏感 URI。

## 落地步骤

1. 在 `pkg/mot` 定义公开 result 类型。
2. 从现有 `internal/model` 和 `pkg/mongo` 类型映射到 result。
3. CLI formatter 从 result 派生展示字段。
4. 单测覆盖 result mapping。
5. 删除或隔离 `ColoredState` 这类展示字段。

## 验收标准

- SDK result 中没有 ANSI color 字符串。
- SDK result 中大小字段保留 bytes 原始值。
- SDK result 中时长字段使用 `time.Duration`。
- CLI 输出需要的人类可读字符串全部由 formatter 派生。
- result mapping 有单测覆盖。

## 风险

如果公开类型保留 CLI 字符串，后续 API 一旦被外部项目依赖，就很难再修正。第一版公开类型必须先把业务语义定清楚。
