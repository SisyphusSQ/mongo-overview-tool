# Go SDK 示例

所有示例都直接调用公开的 `pkg/mot`，不会经过 CLI。SDK 本身不读取环境变量；示例程序负责读取环境变量并显式组装 `mot.Options`。

## 连接与范围

推荐使用独立 host、port 和凭据变量，避免把密码拼进命令行 URI：

```bash
export MOT_MONGO_HOST='<host>'
export MOT_MONGO_PORT='<port>'
export MOT_MONGO_AUTH_SOURCE='admin'
export MONGO_USER='<user>'
export MONGO_PASS='<password>'
export MOT_EXAMPLE_DATABASE='<database>'
export MOT_EXAMPLE_COLLECTION='<collection>'
```

也可以设置 `MOT_MONGO_URI` 替代 `MOT_MONGO_HOST`、`MOT_MONGO_PORT`、`MONGO_USER` 和 `MONGO_PASS`。示例不会输出连接 URI 或凭据。

## 综合只读诊断

```bash
go run ./examples/sdk/diagnostics
```

该示例依次调用：

- `Overview`
- `CollectionStats`
- `Doctor`
- `CurrentOperations`
- `Hotspot`
- 通用 `IndexAudit`，显式排除分片一致性检查
- `Capacity`
- `SlowlogSummary`
- 当 Summary 存在候选项时调用 `SlowlogDetail`

示例只创建一个请求级 `CollectorSession`，所有能力共享拓扑、目录和成员连接，最后输出从 SDK 结构化结果提取的脱敏 JSON 摘要及 session 统计。统计不包含 URI、用户名、密码或原始诊断文档。遇到 `mot.ErrPartialResult` 时保留已获得的数据，并在 `partialCollectors` 中标出对应 collector。

## 分片索引一致性

```bash
go run ./examples/sdk/index_consistency
```

该示例只适用于通过 mongos 连接的分片集群，并只执行 `IndexCheckConsistency`。输出包含 state、strategy、coverage、expected/observed shard 数量、difference 数量和 fallback/partial 状态，不输出原始 BSON 或索引定义。

## 安全边界

- 两个示例都只执行读取和诊断，不创建或修改数据、索引、Profiler、服务器参数或 routing metadata。
- 示例不会调用 `BulkDelete`、`BulkUpdate` 或任何维护动作。
- `MOT_EXAMPLE_DATABASE` 和 `MOT_EXAMPLE_COLLECTION` 必须显式设置，防止无边界扫描。
- `diagnostics` 的 session 全局远程并发上限为 4；`IndexAudit` 和 `Capacity` 均限制为一个 collection、能力并发度 1；`Hotspot` 使用 100ms 采样窗口。
