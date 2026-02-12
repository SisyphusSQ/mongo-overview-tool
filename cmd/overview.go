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

		conn, err := mongo.NewMongoConn(overCfg.BuildUri)
		if err != nil {
			l.Logger.Errorf("NewMongoConn failed, err: %v", err)
			return err
		}

		ovSrv, err := service.NewOverviewSrv(context.Background(), &overCfg, conn)
		if err != nil {
			l.Logger.Errorf("NewOverviewSrv failed, err: %v", err)
			return err
		}
		defer ovSrv.Close()

		if err = ovSrv.GetOverview(); err != nil {
			l.Logger.Errorf("GetOverview failed, err: %v", err)
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
