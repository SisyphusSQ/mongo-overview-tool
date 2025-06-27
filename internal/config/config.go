package config

import (
	"fmt"
	"strings"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
)

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

		if cfg.Username != "" && cfg.Password != "" {
			cfg.Auth = fmt.Sprintf(authFmt, cfg.Username, cfg.Password)
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
		cfg.Auth = split[0]
	}

	cli, err := mongo.NewMongoConn(cfg.BuildUri)
	if err != nil {
		return err
	}
	defer cli.Close()

	if !cli.IsGood() {
		return fmt.Errorf("connect to %s failed: %v", utils.BlockPassword(cfg.BuildUri, "***"), err)
	}

	return nil
}

func (c *BaseCfg) ConcatUri(addr string) string {
	return fmt.Sprintf("mongodb://%s@%s/%s", c.Auth, addr, c.AuthSource)
}
