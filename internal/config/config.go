package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
)

const (
	MongoUser = "MONGO_USER"
	MongoPass = "MONGO_PASS"
)

// FilterDBs is the list of system databases to skip
var FilterDBs = []string{"admin", "config", "local"}

type BaseCfg struct {
	Debug bool

	Host     string
	Port     int
	MongoUri string

	Username   string
	Password   string
	AuthSource string

	Auth     string
	BuildUri string
}

type StatsConfig struct {
	BaseCfg

	ShowAll    bool
	Database   string
	Collection string
}

type SlowlogConfig struct {
	BaseCfg

	Overview  bool
	Detail    bool
	DB        string
	Sort      string
	QueryHash string
}

type BulkConfig struct {
	BaseCfg

	Database   string // 目标数据库（必填）
	Collection string // 目标集合（必填）
	Filter     string // JSON 格式的查询过滤条件，默认 "{}"
	Update     string // JSON 格式的更新操作（仅 update 模式需要）
	BatchSize  int    // 每批处理文档数，默认 1000
	PauseMS    int    // 批次间暂停毫秒数，默认 100
	DryRun     bool   // 试运行模式，仅统计匹配数量不实际执行
	Output     string // 可选日志输出文件路径
}

var (
	authFmt = "%s:%s@"
	uriFmt  = "mongodb://%s%s:%d/%s"
)

func BasePreCheck(cfg *BaseCfg) error {
	log.New(cfg.Debug)

	if cfg.MongoUri == "" {
		if cfg.Host == "" || cfg.Port == 0 {
			return fmt.Errorf("host and port must be set")
		}

		user := os.Getenv(MongoUser)
		pass := os.Getenv(MongoPass)

		if cfg.Username != "" && cfg.Password != "" {
			cfg.Auth = fmt.Sprintf(authFmt, cfg.Username, cfg.Password)
		} else if user != "" && pass != "" {
			cfg.Auth = fmt.Sprintf(authFmt, user, pass)
		} else {
			cfg.Auth = ""
		}

		cfg.BuildUri = fmt.Sprintf(uriFmt, cfg.Auth, cfg.Host, cfg.Port, cfg.AuthSource)
	} else {
		cfg.BuildUri = cfg.MongoUri

		uri := strings.ReplaceAll(cfg.BuildUri, "mongodb://", "")
		split := strings.Split(uri, "@")
		if len(split) != 2 {
			return fmt.Errorf("invalid MongoDB URI: %s", cfg.MongoUri)
		}
		cfg.Auth = split[0] + "@"
	}

	return nil
}

func (c *BaseCfg) ConcatUri(addr string) string {
	return fmt.Sprintf("mongodb://%s%s/%s", c.Auth, addr, c.AuthSource)
}

func (c *BaseCfg) ConcatUriWithAuthDB(addr, db string) string {
	return fmt.Sprintf("mongodb://%s%s/%s?authSource=%s", c.Auth, addr, db, c.AuthSource)
}
