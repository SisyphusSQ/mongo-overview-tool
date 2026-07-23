# Mongo Overview Tool
`mongo-overview-tool` 是一个用于快速获取 MongoDB 集群概览、统计信息以及执行批量维护操作的命令行工具。它支持副本集（ReplicaSet）和分片集群（Sharded Cluster）。

## 功能特性

- **集群概览 (`overview`)**: 查看集群拓扑、节点状态、版本信息等。
- **集合统计 (`coll-stats`)**: 分析集合大小、索引大小、文档数量等。
- **分片检查 (`check-shard`)**: 检查集合是否已分片。
- **慢日志分析 (`slowlog`)**: 聚合分析慢查询日志，支持按执行次数、最大耗时等排序。
- **诊断巡检 (`doctor` / `ops` / `hotspot`)**: 以结构化 finding 和 collector status 展示健康风险、活跃操作与短周期热点。
- **索引与容量审计 (`index-audit` / `capacity`)**: 给出 MongoDB 3.4–7.x 分片集合索引一致性、通用索引复核候选、脱敏容量快照和纯离线差异，不自动执行索引或存储变更。
- **批量操作 (`bulk-delete` / `bulk-update`)**: 支持流控的批量删除和更新操作，减少对线上业务的影响。

## 安装与构建

### 源码构建

确保你已安装 Go 1.26+ 环境（与 `go.mod` 一致）。

```bash
git clone https://github.com/SisyphusSQ/mongo-overview-tool.git
cd mongo-overview-tool

# 本机构建（产物在 bin/mot）
make test

# 发布构建：生成 Linux、macOS（Darwin）和 Windows 的 amd64、arm64 归档及校验文件
make release VERSION=v2.0.0
```

发布构建会自动执行 `release-verify`，最终可上传目录 `bin/release/` 只包含以下七个正式资产：

- `bin/release/mot.linux.amd64.tar.gz`
- `bin/release/mot.linux.arm64.tar.gz`
- `bin/release/mot.darwin.amd64.tar.gz`
- `bin/release/mot.darwin.arm64.tar.gz`
- `bin/release/mot.windows.amd64.zip`
- `bin/release/mot.windows.arm64.zip`
- `bin/release/SHA256SUMS`

`bin/mot.*` 是构建中间文件，不应上传或直接分发。可用同一版本独立复核现有归档：

```bash
make release-verify VERSION=v2.0.0
```

下载全部六个归档和 `SHA256SUMS` 后，macOS 使用 `shasum -a 256 -c SHA256SUMS`，Linux 使用 `sha256sum -c SHA256SUMS` 校验完整性。

macOS 或 Linux 解压安装示例（按实际系统和架构替换归档名）：

```bash
mkdir -p "$HOME/.local/bin" /tmp/mot-install
tar -xzf mot.darwin.arm64.tar.gz -C /tmp/mot-install
install -m 0755 /tmp/mot-install/mot "$HOME/.local/bin/mot"
"$HOME/.local/bin/mot" -h
```

Windows PowerShell 解压示例：

```powershell
Expand-Archive -Path .\mot.windows.amd64.zip -DestinationPath .\mot
.\mot\mot.exe -h
```

源码构建产物位于 `bin/` 目录下，可通过 `make deploy` 将本机构建安装到 `/usr/local/bin/`。

## 连接配置

该工具支持多种方式指定 MongoDB 连接信息。

## Go SDK 用法

除 CLI 外，本仓库也提供可嵌入 Go SDK：

在已有 Go module 中，先在代码中导入下方的 `pkg/mot`，再添加依赖并整理 module：

```bash
go get github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot@v2.2.1
go mod tidy
```

如果从空目录开始，可先创建 module：

```bash
mkdir mot-sdk-demo && cd mot-sdk-demo
go mod init example.com/mot-sdk-demo
```

创建一个实际导入 SDK 的 `main.go`：

```go
package main

import "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"

func main() {
    _ = mot.Options{}
}
```

再添加依赖、整理 module 并运行：

```bash
go get github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot@v2.2.1
go mod tidy
go run .
```

`v2.2.0` 是首个采用 `/v2` module path、可供 Go module 稳定引用的 SDK 版本。v1 module 已冻结；`v2.0.0`、`v2.1.0` 仍保留为历史 CLI 二进制 Release，但不能作为 Go v2 module 依赖。升级旧的 `@main` 伪版本时，需要显式修改 import path。

```go
import "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"

ctx := context.Background()
client, err := mot.NewClient(ctx, mot.Options{
    URI: "mongodb://root:password@127.0.0.1:27017/admin",
})
if err != nil {
    return err
}
defer func() {
    closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = client.Close(closeCtx)
}()

overview, err := client.Overview(ctx, mot.OverviewOptions{IncludeHosts: true})
```

一次只调用一个只读能力时，直接使用 `Client` 即可。一次上层请求需要调用多个只读能力时，应创建一个请求级 `CollectorSession`，让这些能力共享拓扑发现、目录清单和派生成员连接：

```go
session, err := client.NewCollectorSession(mot.CollectorSessionOptions{
    MaxConcurrency: 4,
})
if err != nil {
    return err
}
defer func() {
    closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    _ = session.Close(closeCtx)
}()

overview, overviewErr := session.Overview(ctx, mot.OverviewOptions{NodeConcurrency: 2})
doctor, doctorErr := session.Doctor(ctx, mot.DoctorOptions{NodeConcurrency: 2})
stats := session.Stats()
```

一个 session 只服务一次上层请求，可以被该请求内的多个 capability 并发使用，但调用方必须等待它们全部结束后再 `Close`。session 不跨请求、定时任务轮次或租户复用；关闭 session 后再关闭基础 `Client`。`BulkDelete` 和 `BulkUpdate` 继续直接使用 `Client`。CLI 每次只执行一个命令并退出，因此不需要自行管理 session，现有 `Client` 方法会使用兼容包装保持原行为。

SDK 入口返回结构化 result，不返回 CLI 表格字符串；SDK 核心不读取环境变量，CLI 或示例应用层负责读取后显式组装 `mot.Options`。更多示例见：

- `examples/sdk/overview`
- `examples/sdk/coll_stats`
- `examples/sdk/bulk_delete_dry_run`
- `examples/sdk/diagnostics`
- `examples/sdk/index_consistency`

其中 `diagnostics` 展示在一次 `CollectorSession` 中依次调用全部只读能力，并输出不含连接信息的 session 统计摘要；`index_consistency` 展示 MongoDB 3.4–7.x 分片集合索引一致性检查。完整运行方式和环境变量见 `examples/sdk/README.md`。

真实 MongoDB 集成测试需显式启用：

```bash
MOT_TEST_MONGO_URI='mongodb://user:pass@127.0.0.1:27017/admin' \
  go test -tags=integration ./pkg/mongo ./pkg/mot
```

SDK 只读 live E2E 使用独立的 host/port 环境变量，并从现有 `MONGO_USER` / `MONGO_PASS` 读取认证信息；测试覆盖全部九个只读 capability，不执行 bulk-delete / bulk-update：

```bash
MOT_TEST_MONGO_HOST='<host>' \
MOT_TEST_MONGO_PORT='<port>' \
MOT_TEST_EXPECT_CLUSTER='repl|sharding' \
go test -tags=integration -count=1 -v \
  -run '^TestLiveSDKReadOnlyE2E(Legacy|Session)$' ./pkg/mot
```

`Legacy` 和 `Session` 使用相同 scope、权限和 capability 参数；做性能验收时应按 Legacy → Session → Session → Legacy → Legacy → Session 的顺序分别运行并比较三次中位数。该 live E2E 还覆盖 `Doctor`、`CurrentOperations`、`Hotspot`、通用 `IndexAudit`、`Capacity` 和增强后的 slowlog insight。分片集合索引一致性使用独立的 `TestLiveIndexConsistencyReadOnlyE2E` 和预置 namespace 环境变量。测试会读取真实 routing metadata、shard 索引定义和诊断统计，但不会创建数据、修改 Profiler、变更索引或执行维护命令。

### CollectorSession Live E2E 结果（2026-07-17）

本次使用相同账号、scope 和 capability 参数，在 MongoDB 3.4、4.2、7.0 的复制集和 6 replica set / 18 节点分片集群中完成交替实测。六个目标共 36 次 Legacy / Session 矩阵全部通过，另有一次 Session smoke 通过：

| MongoDB | 拓扑 | Legacy 中位耗时 | Session 中位耗时 | 降幅 |
| --- | --- | ---: | ---: | ---: |
| 3.4 | 复制集 | 9.892 秒 | 3.164 秒 | 68.0% |
| 3.4 | 6×18 分片集群 | 55.981 秒 | 10.694 秒 | 80.9% |
| 4.2 | 复制集 | 7.983 秒 | 2.712 秒 | 66.0% |
| 4.2 | 6×18 分片集群 | 60.560 秒 | 16.641 秒 | 72.5% |
| 7.0 | 复制集 | 10.367 秒 | 3.811 秒 | 63.2% |
| 7.0 | 6×18 分片集群 | 73.306 秒 | 15.306 秒 | 79.1% |

三个分片环境的 Session 中位耗时均低于 45 秒，且相对 Legacy 的降幅均超过 35%。每次分片 Session 都只加载一次 topology 和一次 shard inventory，派生连接数为 24，远程采集峰值并发为 4。MongoDB 3.4、4.2、7.0 的显式 Session 索引一致性检查也全部通过，分别命中 `direct_list_indexes`、`index_stats`、`check_metadata_consistency` 三条版本策略，结果均为 complete、consistent 且未触发 fallback。

本轮同时验证了 `-t/--target host:port` 在三个分片版本上的连接行为，以及 `--host` 配合 `-P/--port` 的兼容入口。完整测试、race、vet、构建、integration build-only 和 benchmark 均通过；测试过程没有执行 Bulk、数据或索引写入，也没有记录集群地址、业务 namespace 或认证信息。

结论：请求级 `CollectorSession` 优化已达到当前 SDK 上线门槛。接入方需要在一次上层请求内创建并复用同一个 session，才能获得上述拓扑、连接和并发调度收益；单能力 `Client` 调用和 CLI 行为继续保持兼容。DBBridge 及其他接入方的适配不包含在当前 SDK 分支中。

### 1. 命令行参数（推荐）

```bash
# 使用完整 target（推荐）
mot overview -t 127.0.0.1:27017 -u root -p password --authSource admin

# 分别指定 host/port
mot overview --host 127.0.0.1 -P 27017 -u root -p password --authSource admin

# 使用 URI
mot overview --uri "mongodb://root:password@127.0.0.1:27017/admin"
```

### 2. 环境变量

如果不想在命令行中暴露密码，可以使用环境变量：

```bash
export MONGO_USER=root
export MONGO_PASS=password
mot overview -t 127.0.0.1:27017
```

### 通用参数

所有命令都支持以下基础参数：

| 参数 | 简写 | 描述 | 默认值 |
| :--- | :--- | :--- | :--- |
| `--uri` | | MongoDB 连接 URI (覆盖其他连接参数) | "" |
| `--target` | `-t` | 完整目标地址（host:port） | 127.0.0.1:27017 |
| `--host` | | 目标主机 IP | 127.0.0.1 |
| `--port` | `-P` | 目标端口 | 27017 |
| `--username` | `-u` | 认证用户名 | "" |
| `--password` | `-p` | 认证密码 | "" |
| `--authSource` | | 认证数据库 | admin |
| `--debug` | | 开启调试日志 | false |

---

## 命令详解

### 1. 集群概览 (`overview`)

获取集群的整体状态，包括成员角色、健康状态、复制延迟等。

**使用示例:**
```bash
mot overview --uri "mongodb://127.0.0.1:27017"
```

**预期输出:**
展示集群成员列表、状态（PRIMARY/SECONDARY）、版本、Uptime 等信息的表格。

### 2. 集合统计 (`coll-stats`)

查看数据库中集合的统计信息（大小、文档数）。

**参数:**
- `--database`: 指定数据库名（可选，支持逗号分隔多个）。
- `--coll`: 指定集合名（可选，支持逗号分隔多个）。

**使用示例:**
```bash
# 查看所有库的集合统计
mot coll-stats

# 查看指定库的统计
mot coll-stats --database mydb
```

### 3. 分片检查 (`check-shard`)

检查集合是否配置了分片。

**参数:**
- `--show-all`: 是否显示所有集合（默认只显示尚未分片的集合）。
- `--database`: 指定数据库。
- `--coll`: 指定集合。

**使用示例:**
```bash
# 检查 mydb 库中哪些集合尚未分片
mot check-shard --database mydb

# 显示所有集合的分片状态
mot check-shard --show-all
```

### 4. 慢日志分析 (`slowlog`)

从数据库中拉取并分析慢查询日志（基于 `system.profile` 或日志聚合，具体取决于实现）。

**参数:**
- `--sort`: 排序字段，可选值: `cnt` (次数), `maxMills` (最大耗时), `maxDocs` (扫描文档数)。默认 `cnt`。
- `--hash`: 指定 Query Hash 查看特定慢查询详情。
- `--db`: 指定数据库。

**使用示例:**
```bash
# 获取慢日志概览，按出现次数排序
mot slowlog --sort cnt

# 查看特定 Query Hash（或低版本 legacy 标识）的慢日志详情
mot slowlog --db mydb --hash xxxxxxxx
```

MongoDB 3.4 等旧版本的 `system.profile` 不提供 `queryHash`。此时概览会根据 namespace、operation 和 plan summary 生成 `legacy:` 前缀的稳定标识；该标识可直接传给 `--hash` 查看这一聚合组中最新的详情记录。它是兼容标识，不等同于新版 MongoDB 的查询形状哈希。

### 5. 健康巡检 (`doctor`)

执行只读健康检查，输出 finding 和各 collector 的执行状态。所有诊断命令都支持 `--format table|json` 与 `--timeout`；`unsupported`、`unauthorized`、`skipped`、`failed` 不会被表格输出吞掉。

**常用参数：**
- `--minimum-severity`: 最低 finding 严重级别，取值为 `info`、`warning` 或 `critical`，默认 `info`。
- `--concurrency`: 节点 collector 最大并发数，默认 `10`。
- `--include-system-db`: 是否纳入系统库。
- `--oplog-window`: 显式采集 oplog window 指标。

```bash
# 健康巡检；oplog window 需要显式启用
mot doctor --uri '<mongodb-uri>' --oplog-window --format json

```

### 6. 活跃操作 (`ops`)

查看活跃操作，过滤条件在服务端生效。输出会脱敏，不展示 command、filter、user 或 session 内容。

**常用参数：**
- `--min-duration`: 最短运行时长，默认 `2s`。
- `--database`、`--namespace`: 以逗号分隔的数据库或 namespace 过滤条件。
- `--limit`: 最大结果数，默认 `100`。
- `--all-users`: 请求所有用户的操作；无权限时退回当前用户，默认 `true`。
- `--include-idle-transactions`、`--include-idle-cursors`: 显式纳入空闲事务或 cursor。

```bash
# 服务端过滤并脱敏活跃操作；不输出 command/filter/user/session
mot ops --uri '<mongodb-uri>' --min-duration 2s --limit 100

```

### 7. 热点分析 (`hotspot`)

连续采集两次快照，按实际采样间隔计算节点和 namespace 的访问速率，用于定位短周期热点。

**常用参数：**
- `--duration`: 两次快照之间的间隔，默认 `10s`。
- `--top`: 最多输出的 namespace 热点数，默认 `10`。
- `--database`: 以逗号分隔的数据库过滤条件。
- `--concurrency`: 节点 collector 最大并发数，默认 `10`。
- `--include-system-db`: 是否纳入系统库。

```bash
# 默认使用 10 秒双快照，按实际间隔计算 namespace rate
mot hotspot --uri '<mongodb-uri>' --database app --top 10

```

### 8. 索引审计 (`index-audit`)

审计索引使用情况、冗余定义、空间占用、构建状态和分片集合索引一致性。必须且只能指定 `--database` 或 `--all-databases` 之一。

**常用参数：**
- `--database`、`--all-databases`: 审计范围，二者互斥。
- `--collection`: 以逗号分隔的集合过滤条件。
- `--checks`: 指定检查项：`unused`、`redundant`、`space`、`building`、`consistency`。
- `--min-observation`: 零使用索引的最小观测窗口，默认 `7d`。
- `--max-collections`、`--concurrency`: 集合数上限及 collection collector 最大并发数。

```bash
# database 与 all-databases 二选一；默认 checks 包含 consistency
mot index-audit --uri '<mongodb-uri>' --database app --format table

# 只运行跨 shard 索引一致性，不运行 usage/capacity collector
mot index-audit --uri '<mongodb-uri>' --database app \
  --checks consistency --collection orders --format json

```

collection 结果分别给出 `consistent`、`inconsistent`、`inconclusive` 或 `skipped`，同时保留 expected/observed shards、coverage、最终 strategy、fallback reason 和脱敏 fingerprint。索引差异或可渲染 partial coverage 的 CLI 退出码为 0；参数、连接、拓扑、范围发现、collection gate、取消或输出失败仍返回非零。

expected shards 来自独立 routing metadata，并使用 `listShards` 与 `collStats.shards` 校验；工具不会从本次索引 observation 反推预期范围，也不会把整 shard 缺失误报为健康。

### 9. 容量快照与离线差异 (`capacity`)

采集脱敏、稳定的容量快照；默认不采集 free storage，避免引入高成本操作。`capacity diff` 仅比较两个本地快照，不连接 MongoDB。

**常用参数：**
- `--database`、`--collection`: 以逗号分隔的范围过滤；未指定数据库时选择所有非系统库。
- `--free-storage`: 显式启用高成本的 free storage 采集。
- `--snapshot`: 将脱敏 JSON 快照写入本地路径。
- `--max-collections`、`--concurrency`: 集合数上限及 collection collector 最大并发数。

```bash
# free storage 是显式高成本 opt-in；snapshot 只包含脱敏结构化结果
mot capacity --uri '<mongodb-uri>' --database app \
  --free-storage --snapshot ./capacity-after.json

# 纯离线比较，不连接 MongoDB
mot capacity diff ./capacity-before.json ./capacity-after.json
```

#### 容量与 SDK 补充说明

- `capacity` 中 `dataSize` 是逻辑未压缩数据量，`storageSize` 是集合已分配存储且不含索引；free storage 表示存储引擎可复用空间，不代表操作系统会立即回收。
- SDK 诊断方法可能返回 result 与 `*mot.DiagnosticPartialError`；既有 bulk `*mot.PartialError` 保持源码兼容，诊断已有有效证据也不会因单个节点或 collector 失败而丢失。

### 10. 批量删除 (`bulk-delete`)

分批次删除数据，支持流控（暂停时间），避免一次性删除大量数据导致数据库负载过高。

**参数:**
- `-d, --database`: **(必填)** 目标数据库。
- `-c, --collection`: **(必填)** 目标集合。
- `-f, --filter`: **(必填)** JSON 格式的过滤条件，默认为 `{}` (删除所有，慎用)。
- `-b, --batch-size`: 每批次删除的数量，默认 `1000`，必须大于 `0`。
- `--pause-ms`: 每批次删除后的暂停时间（毫秒），默认 `100`，必须大于等于 `0`。
- `--dry-run`: 试运行模式，仅统计匹配文档数，不执行删除。
- `-o, --output`: 日志输出文件路径。

说明:
- `--filter` 支持 **MongoDB Shell 语法**（如 `ISODate(...)`、`ObjectId(...)`、无引号键名）及标准 ExtJSON。

**使用示例:**

```bash
# 使用 Shell 语法：删除 createTime 早于 2024-01-01 的文档
mot bulk-delete -d mydb -c users \
  -f '{createTime: {$lt: ISODate("2024-01-01T00:00:00Z")}}' \
  --dry-run

# 试运行：查看将要删除多少条 status 为 inactive 的数据
mot bulk-delete -d mydb -c users -f '{"status":"inactive"}' --dry-run

# 执行删除：删除 mydb.users 中 status=inactive 的文档，每批 500 条，每批间隔 200ms
mot bulk-delete -t 10.0.0.1:27017 \
  -d mydb -c users \
  -f '{"status":"inactive"}' \
  -b 500 --pause-ms 200
```

### 11. 批量更新 (`bulk-update`)

分批次更新数据，同样支持流控。

**参数:**
- 包含 `bulk-delete` 的所有参数。
- `--update`: **(必填)** JSON 格式的更新操作（如 `{"$set": ...}`）。

说明:
- `--filter` 与 `--update` 均支持 **MongoDB Shell 语法**（如 `ISODate(...)`、无引号键名）及标准 ExtJSON。
- 运行结果会区分三个指标：`processed`（已处理文档数）、`matched`（命中文档数）、`modified`（实际修改文档数）。

**使用示例:**

```bash
# 使用 Shell 语法：将 status: "pending" 更新为 "archived"
mot bulk-update -d mydb -c orders \
    -f '{status: "pending"}' \
    --update '{$set: {status: "archived"}}' \
    --dry-run

# 简单更新：将 status 为 pending 的订单更新为 archived
mot bulk-update -d mydb -c orders \
    -f '{"status":"pending"}' \
    --update '{"$set":{"status":"archived"}}' \
    -b 1000 --pause-ms 100

# 复杂更新：将 mydb.orders 中 2024 年前的订单标记为 archived，并将执行日志输出到文件
mot bulk-update --uri "mongodb://user:pass@host:27017" \
  -d mydb -c orders \
  -f '{"createdAt":{"$lt":{"$date":"2024-01-01T00:00:00Z"}}}' \
  --update '{"$set":{"status":"archived"}}' \
  -b 1000 --pause-ms 100 \
  -o bulk_update.log
```

## 行为说明

### 自动过滤系统库

`coll-stats`、`check-shard`、`slowlog` 等命令在遍历数据库时，会自动跳过以下 MongoDB 系统库：

- `admin`
- `config`
- `local`

无需手动排除，即使不指定 `--database` 参数也不会扫描这三个库。

### 密码脱敏

工具在所有输出（包括终端打印和日志文件）中会自动对连接 URI 中的密码进行脱敏处理，替换为 `***`，避免敏感信息泄漏。例如：

```
URI: mongodb://root:***@10.0.0.1:27017/admin
```

### 分片集群下的行为差异

工具会自动检测集群拓扑类型（副本集 / 分片集群），不同命令在两种拓扑下的表现有所不同：

| 命令 | 副本集 (ReplicaSet) | 分片集群 (Sharded Cluster) |
| :--- | :--- | :--- |
| `overview` | 展示当前副本集所有节点状态 | 遍历每个 shard，分别展示各 shard 副本集的节点状态 |
| `coll-stats` | 展示集合的 `documents`、`avgObjSize`、`storageSize` | 额外展示 `isSharded` 列，标识集合是否已分片 |
| `slowlog` | 从当前副本集的 PRIMARY/SECONDARY 节点聚合 `system.profile` | 逐 shard 遍历，分别聚合各 shard 的慢日志 |
| `index-audit` | 显式不含 `consistency` 时可运行通用检查；默认 consistency 会拒绝该拓扑 | 支持 3.4–7.x 跨 shard 一致性与通用索引检查 |

### 并发控制

为兼顾查询效率和对目标 MongoDB 的压力，各命令内部设置了不同的并发限制：

| 命令 | 并发数 | 说明 |
| :--- | :--- | :--- |
| `coll-stats` / `check-shard` | 20 | 每个数据库内，集合级别的统计查询并发上限 |
| `slowlog` | 5 | 每个节点内，数据库级别的 `system.profile` 聚合并发上限 |
| `index-audit` | 10（可配置） | collection 级有界并发；单 collection 的 shard fallback/二次确认串行并共享 context budget |

并发通过 `errgroup.SetLimit()` 控制，超出上限的任务会自动排队等待。

---

## 项目结构

```
mongo-overview-tool/
├── main.go                          # 程序入口
├── cmd/                             # CLI 命令定义（基于 Cobra）
│   ├── root.go                      # 根命令 & 通用参数
│   ├── version.go                   # version 子命令
│   ├── overview.go                  # overview 子命令
│   ├── coll_stats.go                # coll-stats 子命令
│   ├── check_shard.go               # check-shard 子命令
│   ├── slowlog.go                   # slowlog 子命令
│   └── bulk.go                      # bulk-delete / bulk-update 子命令
├── internal/
│   ├── config/                      # 配置定义 & 预检逻辑
│   └── clioutput/                   # CLI 输出适配（表格、颜色、进度、文件日志）
├── pkg/
│   ├── log/                         # 日志（基于 Zap）
│   ├── mot/                         # 可嵌入 Go SDK facade
│   ├── mongo/                       # MongoDB 连接、查询封装
│   └── progress/                    # 进度条
├── utils/
│   ├── retry/                       # 重试（指数退避 + 随机抖动）
│   ├── timeutil/                    # 时间格式化（CST）
│   ├── signal.go                    # 信号处理（优雅退出）
│   └── utils.go                     # 通用工具（密码脱敏、字节格式化等）
└── vars/                            # 版本号等构建变量
```

## 技术栈

| 依赖 | 版本 | 用途 |
| :--- | :--- | :--- |
| [spf13/cobra](https://github.com/spf13/cobra) | v1.9.1 | CLI 框架 |
| [go.mongodb.org/mongo-driver](https://github.com/mongodb/mongo-go-driver) | v1.10.6 | MongoDB 官方 Go 驱动 |
| [uber-go/zap](https://github.com/uber-go/zap) | v1.27.0 | 高性能结构化日志 |
| [fatih/color](https://github.com/fatih/color) | v1.18.0 | 终端彩色输出 |
| [dustin/go-humanize](https://github.com/dustin/go-humanize) | v1.0.1 | 字节大小人性化格式 |
| [golang.org/x/sync](https://pkg.go.dev/golang.org/x/sync) | v0.8.0 | errgroup 并发控制 |

---

## 开发规范

本项目遵循以下规范：
- **MongoDB Driver**: 锁定版本 `v1.10.6`。
