package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/service"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var collCfg config.StatsConfig

var collStatsCmd = &cobra.Command{
	Use:     "coll-stats",
	Short:   "Check collection stats",
	Long:    `Check collection stats`,
	Example: fmt.Sprintf("%s coll-stats --uri <mongodbUri>\n", vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		if err := config.BasePreCheck(&collCfg.BaseCfg); err != nil {
			return err
		}

		conn, err := mongo.NewMongoConn(collCfg.BuildUri)
		if err != nil {
			l.Logger.Errorf("NewMongoConn failed, err: %v", err)
			return err
		}

		collSrv, err := service.NewCollStatsSrv(context.Background(), &collCfg, conn, false)
		if err != nil {
			l.Logger.Errorf("NewCheckShardSrv failed, err: %v", err)
			return err
		}
		defer collSrv.Close()

		if err := collSrv.Stats(false); err != nil {
			l.Logger.Errorf("collStatsCmd failed, err: %v", err)
			return err
		}
		utils.PrintCost(start)

		return nil
	},
}

func initCollStats() {
	registerBaseFlags(collStatsCmd, &collCfg.BaseCfg)

	collStatsCmd.Flags().StringVar(&collCfg.Database, "database", "", "ShardDatabase to check(Example: db1 or db1,db2)")
	collStatsCmd.Flags().StringVar(&collCfg.Collection, "coll", "", "Collection to check(Example: col1 or col1,col2)")

	rootCmd.AddCommand(collStatsCmd)
}
