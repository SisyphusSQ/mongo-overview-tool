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

var shCfg config.StatsConfig

var shardCmd = &cobra.Command{
	Use:     "check-shard",
	Short:   "Check whether collection is sharded or not",
	Long:    `Check whether collection is sharded or not`,
	Example: fmt.Sprintf("%s check-shard --uri <mongodbUri>\n", vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		if err := config.BasePreCheck(&shCfg.BaseCfg); err != nil {
			return err
		}

		conn, err := mongo.NewMongoConn(shCfg.BuildUri)
		if err != nil {
			l.Logger.Errorf("NewMongoConn failed, err: %v", err)
			return err
		}

		shSrv, err := service.NewCollStatsSrv(context.Background(), &shCfg, conn, true)
		if err != nil {
			l.Logger.Errorf("NewCheckShardSrv failed, err: %v", err)
			return err
		}
		defer shSrv.Close()

		if err := shSrv.Stats(true); err != nil {
			l.Logger.Errorf("ShColl failed, err: %v", err)
			return err
		}
		utils.PrintCost(start)

		return nil
	},
}

func initCheckShard() {
	registerBaseFlags(shardCmd, &shCfg.BaseCfg)

	shardCmd.Flags().BoolVar(&shCfg.ShowAll, "show-all", false, "If show-all is true, print all collections whether is sharded or not")
	shardCmd.Flags().StringVar(&shCfg.Database, "database", "", "ShardDatabase to check(Example: db1 or db1,db2)")
	shardCmd.Flags().StringVar(&shCfg.Collection, "coll", "", "Collection to check(Example: col1 or col1,col2)")

	rootCmd.AddCommand(shardCmd)
}
