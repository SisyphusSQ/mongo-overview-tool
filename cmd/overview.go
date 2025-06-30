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
	overviewCmd.Flags().BoolVar(&overCfg.Debug, "debug", false, "If debug_mode is true, print debug logs")

	overviewCmd.Flags().StringVarP(&overCfg.Host, "host", "t", "127.0.0.1", "Server to connect to")
	overviewCmd.Flags().IntVarP(&overCfg.Port, "port", "P", 27017, "Port to connect to")
	overviewCmd.Flags().StringVarP(&overCfg.Username, "username", "u", "", "Username for authentication")
	overviewCmd.Flags().StringVarP(&overCfg.Password, "password", "p", "", "Password for authentication")
	overviewCmd.Flags().StringVar(&overCfg.AuthSource, "authSource", "admin", "User source")

	overviewCmd.Flags().StringVar(&overCfg.MongoUri, "uri", "", "Connection string URI(Example:mongodb://192.168.0.5:9999/foo)")

	rootCmd.AddCommand(overviewCmd)
}
