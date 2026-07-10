package clioutput

import (
	"fmt"
	"io"
	"time"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
)

func PrintAhead(w io.Writer, uri string) {
	if uri == "" {
		return
	}
	fmt.Fprintf(w, "URI: %s\n", color.GreenString(mot.RedactURI(uri)))
}

func PrintHosts(w io.Writer, hosts []string) {
	if len(hosts) == 0 {
		return
	}
	fmt.Fprintln(w, "Hosts: ")
	for _, host := range hosts {
		fmt.Fprintf(w, "%s\n", color.GreenString(host))
	}
}

func humanizeBytes(size int64) string {
	if size <= 0 {
		return "n/a"
	}
	return utils.HumanizeBytes(uint64(size))
}

func durationText(duration time.Duration) string {
	if duration == 0 {
		return "0s"
	}
	return duration.Round(time.Second).String()
}
