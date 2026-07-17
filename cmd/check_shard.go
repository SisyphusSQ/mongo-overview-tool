package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/internal/clioutput"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/internal/config"
	l "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/vars"
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

		ctx := context.Background()
		client, err := mot.NewClient(ctx, sdkOptionsFromBase(&shCfg.BaseCfg))
		if err != nil {
			l.Logger.Errorf("mot.NewClient failed, err: %v", err)
			return err
		}
		defer closeSDKClient(client)

		statsOpts, showAll := checkShardStatsOptions(&shCfg)
		result, err := client.CollectionStats(ctx, statsOpts)
		if err != nil {
			l.Logger.Errorf("CollectionStats failed, err: %v", err)
			return err
		}
		if err := clioutput.PrintCollectionStats(os.Stdout, result, clioutput.CollectionStatsPrintOptions{
			URI:       shCfg.BuildUri,
			ShardView: true,
			ShowAll:   showAll,
		}); err != nil {
			l.Logger.Errorf("PrintCollectionStats failed, err: %v", err)
			return err
		}
		utils.PrintCost(start)

		return nil
	},
}

func checkShardStatsOptions(cfg *config.StatsConfig) (mot.CollectionStatsOptions, bool) {
	showAll := cfg.ShowAll || cfg.Collection != ""
	return mot.CollectionStatsOptions{
		Databases:             splitCSV(cfg.Database),
		Collections:           splitCSV(cfg.Collection),
		RequireShardedCluster: true,
	}, showAll
}

func initCheckShard() {
	registerBaseFlags(shardCmd, &shCfg.BaseCfg)

	shardCmd.Flags().BoolVar(&shCfg.ShowAll, "show-all", false, "If show-all is true, print all collections whether is sharded or not")
	shardCmd.Flags().StringVar(&shCfg.Database, "database", "", "ShardDatabase to check(Example: db1 or db1,db2)")
	shardCmd.Flags().StringVar(&shCfg.Collection, "coll", "", "Collection to check(Example: col1 or col1,col2)")

	rootCmd.AddCommand(shardCmd)
}
