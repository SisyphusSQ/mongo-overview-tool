package cmd

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/service"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var slowlogCfg config.SlowlogConfig

var slowlogCmd = &cobra.Command{
	Use:     "slowlog",
	Short:   "Get MongoDB slow log",
	Long:    `Get MongoDB slow log`,
	Example: fmt.Sprintf("%s slowlog --uri <mongodbUri>\n", vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		if err := config.BasePreCheck(&slowlogCfg.BaseCfg); err != nil {
			return err
		}

		if !slices.Contains([]string{"cnt", "maxMills", "maxDocs"}, slowlogCfg.Sort) {
			return fmt.Errorf("invalid sort field: %s, expect: cnt, maxMills, maxDocs", slowlogCfg.Sort)
		}

		if slowlogCfg.QueryHash == "" {
			slowlogCfg.Overview = true
		} else {
			slowlogCfg.Detail = true
		}
		conn, err := mongo.NewMongoConn(slowlogCfg.BuildUri)
		if err != nil {
			l.Logger.Errorf("NewMongoConn failed, err: %v", err)
			return err
		}

		slowSrv, err := service.NewSlowlogSrv(context.Background(), &slowlogCfg, conn)
		if err != nil {
			l.Logger.Errorf("NewSlowlogSrv failed, err: %v", err)
			return err
		}
		defer slowSrv.Close()

		if slowlogCfg.Overview {
			if err = slowSrv.GetOverview(); err != nil {
				l.Logger.Errorf("GetOverview failed, err: %v", err)
				return err
			}
		} else {
			if err = slowSrv.GetSlowDetail(); err != nil {
				l.Logger.Errorf("GetSlowDetail failed, err: %v", err)
				return err
			}
		}
		utils.PrintCost(start)
		return nil
	},
}

func initSlowlogCmd() {
	slowlogCmd.Flags().BoolVar(&slowlogCfg.Debug, "debug", false, "If debug_mode is true, print debug logs")

	slowlogCmd.Flags().StringVarP(&slowlogCfg.Host, "host", "t", "127.0.0.1", "Server to connect to")
	slowlogCmd.Flags().IntVarP(&slowlogCfg.Port, "port", "P", 27017, "Port to connect to")
	slowlogCmd.Flags().StringVarP(&slowlogCfg.Username, "username", "u", "", "Username for authentication")
	slowlogCmd.Flags().StringVarP(&slowlogCfg.Password, "password", "p", "", "Password for authentication")
	slowlogCmd.Flags().StringVar(&slowlogCfg.AuthSource, "authSource", "admin", "User source")

	slowlogCmd.Flags().StringVar(&slowlogCfg.MongoUri, "uri", "", "Connection string URI(Example:mongodb://192.168.0.5:9999/foo)")
	slowlogCmd.Flags().StringVar(&slowlogCfg.QueryHash, "hash", "", "Query hash to filter slow log")
	slowlogCmd.Flags().StringVar(&slowlogCfg.Sort, "sort", "cnt", "Sort field, default by cnt desc, list: cnt, maxMills, maxDocs")
	slowlogCmd.Flags().StringVar(&slowlogCfg.DB, "db", "", "Database where slowlog in")

	rootCmd.AddCommand(slowlogCmd)
}
