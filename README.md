# Mongo Overview Tool

`mongo-overview-tool` 是一个用于快速获取 MongoDB 集群概览、统计信息以及执行批量维护操作的命令行工具。它支持副本集（ReplicaSet）和分片集群（Sharded Cluster）。

## 功能特性

- **集群概览 (`overview`)**: 查看集群拓扑、节点状态、版本信息等。
- **集合统计 (`coll-stats`)**: 分析集合大小、索引大小、文档数量等。
- **分片检查 (`check-shard`)**: 检查集合是否已分片。
- **慢日志分析 (`slowlog`)**: 聚合分析慢查询日志，支持按执行次数、最大耗时等排序。
- **批量操作 (`bulk-delete` / `bulk-update`)**: 支持流控的批量删除和更新操作，减少对线上业务的影响。

## 安装与构建

### 源码构建

确保你已安装 Go 环境（建议 Go 1.20+）。

```bash
git clone https://github.com/SisyphusSQ/mongo-overview-tool.git
cd mongo-overview-tool

# 本机构建（产物在 bin/mot）
make test

# 交叉编译 Linux amd64（产物在 bin/mot.linux.amd64）+ 本机 Darwin arm64（产物在 bin/mot.darwin.arm64）
make release
```

构建产物位于 `bin/` 目录下，可通过 `make deploy` 将其安装到 `/usr/local/bin/`。

## 连接配置

该工具支持多种方式指定 MongoDB 连接信息。

### 1. 命令行参数（推荐）

```bash
# 使用 host/port
mot overview -t 127.0.0.1 -P 27017 -u root -p password --authSource admin

# 使用 URI
mot overview --uri "mongodb://root:password@127.0.0.1:27017/admin"
```

### 2. 环境变量

如果不想在命令行中暴露密码，可以使用环境变量：

```bash
export MONGO_USER=root
export MONGO_PASS=password
mot overview -t 127.0.0.1 -P 27017
```

### 通用参数

所有命令都支持以下基础参数：

| 参数 | 简写 | 描述 | 默认值 |
| :--- | :--- | :--- | :--- |
| `--uri` | | MongoDB 连接 URI (覆盖其他连接参数) | "" |
| `--host` | `-t` | 目标主机 IP | 127.0.0.1 |
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
- `--show-all`: 是否显示所有集合（默认只显示已分片的集合）。
- `--database`: 指定数据库。
- `--coll`: 指定集合。

**使用示例:**
```bash
# 检查 mydb 库中哪些集合已分片
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

# 查看特定 Query Hash 的慢日志详情
mot slowlog --hash xxxxxxxx
```

### 5. 批量删除 (`bulk-delete`)

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
mot bulk-delete -t 10.0.0.1 -P 27017 \
  -d mydb -c users \
  -f '{"status":"inactive"}' \
  -b 500 --pause-ms 200
```

### 6. 批量更新 (`bulk-update`)

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

### 并发控制

为兼顾查询效率和对目标 MongoDB 的压力，各命令内部设置了不同的并发限制：

| 命令 | 并发数 | 说明 |
| :--- | :--- | :--- |
| `coll-stats` / `check-shard` | 50 | 每个数据库内，集合级别的统计查询并发上限 |
| `slowlog` | 5 | 每个节点内，数据库级别的 `system.profile` 聚合并发上限 |

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
│   ├── model/                       # 数据模型（overview 统计等）
│   └── service/                     # 业务逻辑层
│       ├── overview_srv.go          # 集群概览
│       ├── coll_stats_srv.go        # 集合统计
│       ├── slowlog_srv.go           # 慢日志分析
│       ├── bulk_srv.go              # 批量删除/更新
│       └── print_srv.go             # 格式化输出（表格、颜色）
├── pkg/
│   ├── log/                         # 日志（基于 Zap）
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
