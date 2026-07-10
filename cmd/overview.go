package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/clioutput"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var overCfg config.BaseCfg

var overviewCmd = &cobra.Command{
	Use:     "overview",
	Short:   "Get MongoDB replica or sharding cluster overview",
	Long:    `Get MongoDB replica or sharding cluster overview`,
	Example: fmt.Sprintf("%s overview --uri <mongodbUri>\n", vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		if err := config.BasePreCheck(&overCfg); err != nil {
			return err
		}

		ctx := context.Background()
		client, err := mot.NewClient(ctx, sdkOptionsFromBase(&overCfg))
		if err != nil {
			l.Logger.Errorf("mot.NewClient failed, err: %v", err)
			return err
		}
		defer closeSDKClient(client)

		result, err := client.Overview(ctx, mot.OverviewOptions{IncludeHosts: true})
		if err != nil {
			l.Logger.Errorf("Overview failed, err: %v", err)
			return err
		}
		if err = clioutput.PrintOverview(os.Stdout, result, clioutput.OverviewPrintOptions{URI: overCfg.BuildUri}); err != nil {
			l.Logger.Errorf("PrintOverview failed, err: %v", err)
			return err
		}
		utils.PrintCost(start)

		return nil
	},
}

func initOverview() {
	registerBaseFlags(overviewCmd, &overCfg)
	rootCmd.AddCommand(overviewCmd)
}
