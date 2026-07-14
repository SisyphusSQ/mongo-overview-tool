package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/clioutput"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var slowlogCfg config.SlowlogConfig
var slowlogFormat string

var slowlogCmd = &cobra.Command{
	Use:     "slowlog",
	Short:   "Get MongoDB slow log",
	Long:    `Get MongoDB slow log`,
	Example: fmt.Sprintf("%s slowlog --uri <mongodbUri>\n", vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		if err := config.BasePreCheck(&slowlogCfg.BaseCfg); err != nil {
			return err
		}

		if !slices.Contains([]string{"cnt", "maxMills", "maxDocs"}, slowlogCfg.Sort) {
			return fmt.Errorf("invalid sort field: %s, expect: cnt, maxMills, maxDocs", slowlogCfg.Sort)
		}
		if err := clioutput.ValidateFormat(slowlogFormat); err != nil {
			return err
		}

		if slowlogCfg.QueryHash == "" {
			slowlogCfg.Overview = true
		} else {
			if slowlogFormat == clioutput.FormatJSON {
				return fmt.Errorf("slowlog detail raw output does not support JSON format")
			}
			slowlogCfg.Detail = true
		}

		ctx := context.Background()
		client, err := mot.NewClient(ctx, sdkOptionsFromBase(&slowlogCfg.BaseCfg))
		if err != nil {
			l.Logger.Errorf("mot.NewClient failed; detail suppressed")
			return safeDiagnosticCommandError(err)
		}
		defer closeSDKClient(client)

		if slowlogCfg.Overview {
			result, operationErr := client.SlowlogSummary(ctx, mot.SlowlogOptions{
				Databases: splitCSV(slowlogCfg.DB),
				Sort:      mot.SlowlogSort(slowlogCfg.Sort),
			})
			var printErr error
			if result != nil && slowlogFormat == clioutput.FormatJSON {
				printErr = clioutput.PrintDiagnosticResult(os.Stdout, result, slowlogFormat)
			} else if result != nil {
				printErr = clioutput.PrintSlowlogSummary(os.Stdout, result, clioutput.SlowlogPrintOptions{URI: slowlogCfg.BuildUri})
			}
			if printErr != nil {
				return printErr
			}
			if operationErr != nil {
				l.Logger.Errorf("SlowlogSummary failed; detail suppressed")
				return safeDiagnosticCommandError(operationErr)
			}
		} else {
			result, err := client.SlowlogDetail(ctx, slowlogCfg.DB, slowlogCfg.QueryHash)
			if err != nil {
				l.Logger.Errorf("SlowlogDetail failed; detail suppressed")
				return safeDiagnosticCommandError(err)
			}
			if err = clioutput.PrintSlowlogDetail(os.Stdout, result, clioutput.SlowlogPrintOptions{URI: slowlogCfg.BuildUri}); err != nil {
				l.Logger.Errorf("PrintSlowlogDetail failed; detail suppressed")
				return safeDiagnosticCommandError(err)
			}
		}
		utils.PrintCost(start)
		return nil
	},
}

func initSlowlogCmd() {
	registerBaseFlags(slowlogCmd, &slowlogCfg.BaseCfg)

	slowlogCmd.Flags().StringVar(&slowlogCfg.QueryHash, "hash", "", "Query hash to filter slow log")
	slowlogCmd.Flags().StringVar(&slowlogCfg.Sort, "sort", "cnt", "Sort field, default by cnt desc, list: cnt, maxMills, maxDocs")
	slowlogCmd.Flags().StringVar(&slowlogCfg.DB, "db", "", "Database where slowlog in")
	slowlogCmd.Flags().StringVar(&slowlogFormat, "format", "table", "Output format for slowlog summary: table|json")

	rootCmd.AddCommand(slowlogCmd)
}
