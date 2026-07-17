package clioutput

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/utils/timeutil"
)

type SlowlogPrintOptions struct {
	URI string
}

func PrintSlowlogSummary(w io.Writer, result *mot.SlowlogSummaryResult, opts SlowlogPrintOptions) error {
	if result == nil {
		return nil
	}
	PrintAhead(w, opts.URI)
	for _, repl := range result.ReplicaSets {
		fmt.Fprintf(w, "\nReplSet: %s\n", color.GreenString(repl.Name))
		fmt.Fprintln(w, "====================================")
		for _, host := range repl.Hosts {
			fmt.Fprintf(w, "Host: %s, State: %s\n", color.GreenString(host.Address), color.GreenString(host.State))
			for _, db := range host.Databases {
				printSlowlogDatabase(w, db)
			}
		}
	}
	printFindings(w, result.Findings)
	printStatuses(w, result.CollectorStatuses)
	return nil
}

func PrintSlowlogDetail(w io.Writer, result *mot.SlowlogDetailResult, opts SlowlogPrintOptions) error {
	if result == nil {
		return nil
	}
	PrintAhead(w, opts.URI)
	fmt.Fprintf(w, "ns: %s, indexes detail info:\n", color.GreenString(result.Namespace))
	fmt.Fprintln(w, "--------")
	for _, index := range result.Indexes {
		payload, err := bson.MarshalExtJSONIndent(index, false, false, " ", "    ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(payload))
	}
	fmt.Fprintln(w)
	color.New(color.FgGreen).Fprintln(w, "slow detail info:")
	fmt.Fprintln(w, "--------")
	payload, err := bson.MarshalExtJSONIndent(result.Slowlog, false, false, " ", "    ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(payload))
	return nil
}

func printSlowlogDatabase(w io.Writer, db mot.DatabaseSlowlogSummary) {
	width := 4
	hashWidth := 12
	for _, item := range db.Items {
		if len(item.Namespace)+2 > width {
			width = len(item.Namespace) + 2
		}
		if len(item.QueryHash)+2 > hashWidth {
			hashWidth = len(item.QueryHash) + 2
		}
	}
	fmt.Fprintf(w, "Database: %s\n", color.GreenString(db.Database))
	fmt.Fprintf(w, "Total: %s TimeFrame: [%s]~[%s]\n\n",
		color.HiRedString("%d", db.Total),
		color.GreenString(timeutil.FormatLayoutString(db.FirstTime)),
		color.GreenString(timeutil.FormatLayoutString(db.LastTime)))
	fmt.Fprint(w, color.CyanString("%-*s%-*s%-10s%-6s%-10s%-10s%-10s%-16s%-12s%-12s%-6s%-9s%-18s%-22s%-22s\n",
		width, "ns", hashWidth, "queryHash", "op", "count", "maxMills", "minMills", "maxDocs", "plan", "docs/ret", "keys/ret", "err", "collscan", "apps", "firstTs", "lastTs"))
	fmt.Fprintf(w, "%-*s%-*s%-10s%-6s%-10s%-10s%-10s%-16s%-12s%-12s%-6s%-9s%-18s%-22s%-22s\n",
		width, "--", hashWidth, "---------", "--", "-----", "--------", "--------", "-------", "----", "--------", "--------", "---", "--------", "----", "-------", "------")
	for _, item := range db.Items {
		fmt.Fprintf(w, "%-*s%-*s%-10s%-6d%-10d%-10d%-10d%-16s%-12s%-12s%-6d%-9d%-18s%-22s%-22s\n",
			width,
			item.Namespace,
			hashWidth,
			item.QueryHash,
			item.Operation,
			item.Count,
			item.MaxMillis,
			item.MinMillis,
			item.MaxDocs,
			item.PlanSummary,
			optionalRatioText(item.WorstDocsToReturned),
			optionalRatioText(item.WorstKeysToReturned),
			item.ErrorCount,
			item.CollectionScanCount,
			strings.Join(item.AppNames, ","),
			timeutil.FormatLayoutString(item.FirstTime),
			timeutil.FormatLayoutString(item.LastTime),
		)
	}
	fmt.Fprintln(w)
}

func optionalRatioText(value *float64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.2f", *value)
}
