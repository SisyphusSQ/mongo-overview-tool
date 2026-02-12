package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var rootCmd = &cobra.Command{
	Use:  vars.AppName,
	Long: fmt.Sprintf("%s easily get overviews from MongoDB cluster", vars.AppName),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Option missed! Use %s -h or --help for details.\n", vars.AppName)
	},
}

func registerBaseFlags(cmd *cobra.Command, cfg *config.BaseCfg) {
	cmd.Flags().BoolVar(&cfg.Debug, "debug", false, "If debug_mode is true, print debug logs")

	cmd.Flags().StringVarP(&cfg.Host, "host", "t", "127.0.0.1", "Server to connect to")
	cmd.Flags().IntVarP(&cfg.Port, "port", "P", 27017, "Port to connect to")
	cmd.Flags().StringVarP(&cfg.Username, "username", "u", "", "Username for authentication")
	cmd.Flags().StringVarP(&cfg.Password, "password", "p", "", "Password for authentication")
	cmd.Flags().StringVar(&cfg.AuthSource, "authSource", "admin", "User source")

	cmd.Flags().StringVar(&cfg.MongoUri, "uri", "", "Connection string URI(Example:mongodb://192.168.0.5:9999/foo)")
}

func initAll() {
	initVersion()
	initOverview()
	initCheckShard()
	initCollStats()
	initSlowlogCmd()
	initBulkDelete()
	initBulkUpdate()
}

func Execute() {
	initAll()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
