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
	shardCmd.Flags().BoolVar(&shCfg.Debug, "debug", false, "If debug_mode is true, print debug logs")
	shardCmd.Flags().BoolVar(&shCfg.ShowAll, "show-all", false, "If show-all is true, print all collections whether is sharded or not")

	shardCmd.Flags().StringVar(&shCfg.Database, "database", "", "ShardDatabase to check(Example: db1 or db1,db2)")
	shardCmd.Flags().StringVar(&shCfg.Collection, "coll", "", "Collection to check(Example: col1 or col1,col2)")

	shardCmd.Flags().StringVarP(&shCfg.Host, "host", "t", "127.0.0.1", "Server to connect to")
	shardCmd.Flags().IntVarP(&shCfg.Port, "port", "P", 27017, "Port to connect to")
	shardCmd.Flags().StringVarP(&shCfg.Username, "username", "u", "", "Username for authentication")
	shardCmd.Flags().StringVarP(&shCfg.Password, "password", "p", "", "Password for authentication")
	shardCmd.Flags().StringVar(&shCfg.AuthSource, "authSource", "admin", "User source")

	shardCmd.Flags().StringVar(&shCfg.MongoUri, "uri", "", "Connection string URI(Example:mongodb://192.168.0.5:9999/foo)")

	rootCmd.AddCommand(shardCmd)
}
