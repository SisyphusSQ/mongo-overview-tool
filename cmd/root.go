package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var rootCmd = &cobra.Command{
	Use:  vars.AppName,
	Long: fmt.Sprintf("%s easily get overviews from MongoDB cluster", vars.AppName),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Option missed! Use %s -h or --help for details.\n", vars.AppName)
	},
}

func initAll() {
	initVersion()
	initOverview()
	initCheckShard()
	initCollStats()
}

func Execute() {
	initAll()
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
