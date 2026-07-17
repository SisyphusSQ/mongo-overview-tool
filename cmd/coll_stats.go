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

		ctx := context.Background()
		client, err := mot.NewClient(ctx, sdkOptionsFromBase(&collCfg.BaseCfg))
		if err != nil {
			l.Logger.Errorf("mot.NewClient failed, err: %v", err)
			return err
		}
		defer closeSDKClient(client)

		result, err := client.CollectionStats(ctx, mot.CollectionStatsOptions{
			Databases:   splitCSV(collCfg.Database),
			Collections: splitCSV(collCfg.Collection),
		})
		if err != nil {
			l.Logger.Errorf("CollectionStats failed, err: %v", err)
			return err
		}
		if err := clioutput.PrintCollectionStats(os.Stdout, result, clioutput.CollectionStatsPrintOptions{URI: collCfg.BuildUri}); err != nil {
			l.Logger.Errorf("PrintCollectionStats failed, err: %v", err)
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
