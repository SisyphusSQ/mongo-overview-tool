package clioutput

import (
	"fmt"
	"io"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
)

type CollectionStatsPrintOptions struct {
	URI       string
	ShardView bool
	ShowAll   bool
}

func PrintCollectionStats(w io.Writer, result *mot.CollectionStatsResult, opts CollectionStatsPrintOptions) error {
	if result == nil {
		return nil
	}
	PrintAhead(w, opts.URI)
	for _, db := range result.Databases {
		width := collectionNameWidth(db.Collections)
		fmt.Fprintf(w, "Database: %s\n", color.GreenString(db.Name))
		fmt.Fprintf(w, "TotalSize: %s\n", color.GreenString(humanizeBytes(db.StorageSizeBytes)))
		if opts.ShardView {
			fmt.Fprint(w, color.CyanString("%-*s%-12s%-15s%-15s%-15s\n", width, "ns", "isSharded", "documents", "avgObjSize", "storageSize"))
			fmt.Fprintf(w, "%-*s%-12s%-15s%-15s%-15s\n", width, "--", "---------", "---------", "----------", "-----------")
		} else {
			fmt.Fprint(w, color.CyanString("%-*s%-15s%-15s%-15s\n", width, "ns", "documents", "avgObjSize", "storageSize"))
			fmt.Fprintf(w, "%-*s%-15s%-15s%-15s\n", width, "--", "---------", "----------", "-----------")
		}
		for _, coll := range db.Collections {
			if opts.ShardView && !opts.ShowAll && coll.IsSharded {
				continue
			}
			if opts.ShardView {
				isSharded := color.RedString("false")
				if coll.IsSharded {
					isSharded = color.GreenString("true")
				}
				fmt.Fprintf(w, "%-*s%-12s%-15d%-15s%-15s\n",
					width, coll.Namespace, isSharded, coll.Count, humanizeBytes(int64(coll.AvgObjectBytes)), humanizeBytes(coll.StorageSizeBytes))
			} else {
				fmt.Fprintf(w, "%-*s%-15d%-15s%-15s\n",
					width, coll.Namespace, coll.Count, humanizeBytes(int64(coll.AvgObjectBytes)), humanizeBytes(coll.StorageSizeBytes))
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

func collectionNameWidth(collections []mot.CollectionStats) int {
	width := 4
	for _, coll := range collections {
		if len(coll.Namespace)+2 > width {
			width = len(coll.Namespace) + 2
		}
	}
	return width
}
