package clioutput

import (
	"fmt"
	"io"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

type OverviewPrintOptions struct {
	URI string
}

func PrintOverview(w io.Writer, result *mot.OverviewResult, opts OverviewPrintOptions) error {
	if result == nil {
		return nil
	}
	PrintAhead(w, opts.URI)
	PrintHosts(w, result.Hosts)
	for _, repl := range result.ReplicaSets {
		printOverviewReplicaSet(w, repl)
	}
	return nil
}

func printOverviewReplicaSet(w io.Writer, repl mot.ReplicaSetOverview) {
	fmt.Fprintln(w)
	if len(repl.Nodes) == 0 {
		return
	}
	replWidth := len(repl.Name) + 2
	if replWidth < 6 {
		replWidth = 6
	}
	stateWidth := 7
	for _, node := range repl.Nodes {
		if len(node.State)+2 > stateWidth {
			stateWidth = len(node.State) + 2
		}
	}
	fmt.Fprint(w, color.CyanString("%-*s%-23s%-*s%-6s%-6s%-6s%-4s%-4s%-10s%-10s%-10s%-7s%-15s%-10s\n",
		replWidth, "repl", "host", stateWidth, "state", "conn", "qr", "qw", "ar", "aw", "size", "memUsed", "memRes", "delay", "uptime", "version"))
	fmt.Fprintf(w, "%-*s%-23s%-*s%-6s%-6s%-6s%-4s%-4s%-10s%-10s%-10s%-7s%-15s%-10s\n",
		replWidth, "----", "----", stateWidth, "-----", "----", "--", "--", "--", "--", "----", "-------", "-------", "-----", "------", "-------")
	for _, node := range repl.Nodes {
		cacheUsedPercent := "n/a"
		if node.CacheSizeBytes > 0 {
			cacheUsedPercent = fmt.Sprintf("%.1f%%", float64(node.CacheUsedBytes)*100.0/float64(node.CacheSizeBytes))
		}
		state := node.State
		if state == "PRIMARY" || state == "SECONDARY" || state == "ARBITER" {
			state = color.HiGreenString(state)
		} else if state != "" {
			state = color.RedString(state)
		}
		fmt.Fprintf(w, "%-*s%-23s%-*s%-6d%-6d%-6d%-4d%-4d%-10s%-10s%-10s%-7s%-15s%-10s\n",
			replWidth,
			node.ReplicaSet,
			node.Address,
			stateWidth,
			state,
			node.ConnectionsCurrent,
			node.QueueReaders,
			node.QueueWriters,
			node.ActiveReaders,
			node.ActiveWriters,
			humanizeBytes(node.CacheSizeBytes),
			cacheUsedPercent,
			humanizeBytes(node.CacheUsedBytes),
			durationText(node.ReplicationLag),
			durationText(node.Uptime),
			node.Version,
		)
	}
}
