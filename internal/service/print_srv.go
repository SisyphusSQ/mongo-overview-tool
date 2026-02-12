package service

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cast"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/model"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/utils/timeutil"
)

var _ PrintSrv = (*PrintSrvImpl)(nil)

type PrintSrv interface {
	Ahead(uri string)
	SingleHost(host string, state mongo.ReplicaState)
	Hosts(hosts []string)
	Repl(set string)
	OverviewRepl(stats []*model.OverviewStats)

	Database(db mongo.DBStats, width int)
	ShardDatabase(db mongo.DBStats, width int)
	CollStats(stats mongo.CollStats)
	ShardCollStats(stats mongo.CollStats, showAll bool)

	SlowOverView(logs []*mongo.SlowlogView)
	SlowDetail(ns, indexes, slow string)

	PrintBlankLine()
	PrintDashedLine()
	PrintHorizontalLine()
}

type PrintSrvImpl struct {
	width int
}

func NewPrintSrv() PrintSrv {
	return &PrintSrvImpl{}
}

func (p *PrintSrvImpl) Ahead(uri string) {
	fmt.Printf("URI: %s\n", color.GreenString(utils.BlockPassword(uri, "***")))
}

func (p *PrintSrvImpl) Hosts(hosts []string) {
	fmt.Println("Hosts: ")
	for _, host := range hosts {
		fmt.Printf("%s\n", color.GreenString(host))
	}
}

func (p *PrintSrvImpl) SingleHost(host string, state mongo.ReplicaState) {
	fmt.Printf("Host: %s, State: %s\n", color.GreenString(host), color.GreenString(state.String()))
}

func (p *PrintSrvImpl) Repl(set string) {
	fmt.Println("\nReplSet: " + color.GreenString(set))
}

func (p *PrintSrvImpl) OverviewRepl(stats []*model.OverviewStats) {
	p.PrintBlankLine()

	var (
		stateWidth int
		replWidth  = len(stats[0].Repl) + 2

		tmpWidth int
	)

	for _, stat := range stats {
		tmpWidth = len(stat.State) + 2
		if tmpWidth > stateWidth {
			stateWidth = tmpWidth
		}

		if slices.Contains([]string{"PRIMARY", "SECONDARY", "ARBITER"}, stat.State) {
			stat.ColoredState = color.HiGreenString(stat.State)
		} else {
			stat.ColoredState = color.RedString(stat.State)
		}
	}

	color.Cyan("%-*s%-23s%-*s%-6s%-6s%-6s%-4s%-4s%-7s%-10s%-10s%-7s%-15s%-10s\n", replWidth, "repl", "host", stateWidth, "state", "conn", "qr", "qw", "ar", "aw", "size", "memUsed", "memRes", "delay", "uptime", "version")
	fmt.Printf("%-*s%-23s%-*s%-6s%-6s%-6s%-4s%-4s%-7s%-10s%-10s%-7s%-15s%-10s\n", replWidth, "----", "----", stateWidth, "-----", "----", "--", "--", "--", "--", "----", "-------", "-------", "-----", "------", "-------")

	for _, stat := range stats {
		// width 6
		qrWidth := 6
		if stat.QR != "n/a" {
			qr := cast.ToInt64(stat.QR)
			if qr > 1000 {
				stat.QR = color.RedString(stat.QR)
				qrWidth = len(stat.QR) + 2
			} else if qr > 100 {
				stat.QR = color.YellowString(stat.QR)
				qrWidth = len(stat.QR) + 3
			}
		}

		// width 6
		qwWidth := 6
		if stat.QW != "n/a" {
			qw := cast.ToInt64(stat.QW)
			if qw > 1000 {
				stat.QW = color.RedString(stat.QW)
				qwWidth = len(stat.QW) + 2
			} else if qw > 100 {
				stat.QW = color.YellowString(stat.QW)
				qwWidth = len(stat.QW) + 3
			}
		}

		// width 10
		usedWidth := 10
		if stat.MemUsed != "n/a" {
			userPer := cast.ToFloat64(strings.ReplaceAll(stat.MemUsed, "%", ""))
			if userPer >= 90 {
				stat.MemUsed = color.RedString(stat.MemUsed)
				usedWidth = len(stat.MemUsed) + 5
			} else if userPer > 80 {
				stat.MemUsed = color.YellowString(stat.MemUsed)
				usedWidth = len(stat.MemUsed) + 5
			}
		}

		valStaWidth := len(stat.ColoredState) + (stateWidth - len(stat.State))

		fmt.Printf("%-*s%-23s%-*s%-6s%-*s%-*s%-4s%-4s%-7s%-*s%-10s%-7s%-15s%-10s\n",
			replWidth,
			stat.Repl,
			stat.Addr,
			valStaWidth,
			stat.ColoredState,
			stat.Conn,
			qrWidth,
			stat.QR,
			qwWidth,
			stat.QW,
			stat.AR,
			stat.AW,
			stat.Size,
			usedWidth,
			stat.MemUsed,
			stat.MemRes,
			stat.Delay,
			stat.UpTime,
			stat.Version,
		)
	}
}

func (p *PrintSrvImpl) ShardDatabase(db mongo.DBStats, width int) {
	p.width = width
	fmt.Printf("Database: %s\n", color.GreenString(db.DB))
	fmt.Printf("TotalSize: %s\n", color.GreenString(utils.HumanizeBytes(uint64(db.StorageSize))))

	color.Cyan("%-*s%-12s%-15s%-15s%-15s\n", width, "ns", "isSharded", "documents", "avgObjSize", "storageSize")
	fmt.Printf("%-*s%-12s%-15s%-15s%-15s\n", width, "--", "---------", "---------", "----------", "-----------")
}

func (p *PrintSrvImpl) Database(db mongo.DBStats, width int) {
	p.width = width
	fmt.Printf("Database: %s\n", color.GreenString(db.DB))
	fmt.Printf("TotalSize: %s\n", color.GreenString(utils.HumanizeBytes(uint64(db.StorageSize))))

	color.Cyan("%-*s%-15s%-15s%-15s\n", width, "ns", "documents", "avgObjSize", "storageSize")
	fmt.Printf("%-*s%-15s%-15s%-15s\n", width, "--", "---------", "----------", "-----------")
}

func (p *PrintSrvImpl) ShardCollStats(stats mongo.CollStats, showAll bool) {
	if !showAll && stats.Sharded {
		l.Logger.Debugf("coll[%s] is sharded and no need to print, skip...", stats.Ns)
		return
	}

	var (
		isSh    string
		shWidth int
	)

	if stats.Sharded {
		isSh = color.GreenString("true")
		shWidth = len(isSh) + 8
	} else {
		isSh = color.RedString("false")
		shWidth = len(isSh) + 7
	}

	fmt.Printf("%-*s%-*s%-15s%-15s%-15s\n",
		p.width,
		stats.Ns,
		shWidth,
		isSh,
		cast.ToString(stats.Count),
		utils.HumanizeBytes(uint64(stats.AvgObjSize)),
		utils.HumanizeBytes(uint64(stats.StorageSize)),
	)
}

func (p *PrintSrvImpl) CollStats(stats mongo.CollStats) {
	fmt.Printf("%-*s%-15s%-15s%-15s\n",
		p.width,
		stats.Ns,
		cast.ToString(stats.Count),
		utils.HumanizeBytes(uint64(stats.AvgObjSize)),
		utils.HumanizeBytes(uint64(stats.StorageSize)),
	)
}

func (p *PrintSrvImpl) PrintBlankLine() {
	fmt.Println()
}

func (p *PrintSrvImpl) PrintDashedLine() {
	fmt.Println("====================================")
}

func (p *PrintSrvImpl) PrintHorizontalLine() {
	fmt.Println("--------------")
}

func (p *PrintSrvImpl) SlowOverView(logs []*mongo.SlowlogView) {
	var (
		width      int
		total      int64
		start, end time.Time
	)

	for i, log := range logs {
		if i == 0 {
			start = log.MaxTs
			end = log.MinTs
		}

		total += log.Cnt

		if len(log.Ns)+2 > width {
			width = len(log.Ns) + 2
		}

		if log.MaxTs.After(start) {
			start = log.MaxTs
		}
		if log.MinTs.Before(end) {
			end = log.MinTs
		}
	}

	startTs := timeutil.FormatLayoutString(start)
	endTs := timeutil.FormatLayoutString(end)

	p.width = width
	fmt.Printf("Database: %s\n", color.GreenString(logs[0].DB))
	fmt.Printf("Total: %s TimeFrame: [%s]~[%s]\n\n", color.HiRedString("%d", total), color.GreenString(endTs), color.GreenString(startTs))

	color.Cyan("%-*s%-12s%-10s%-6s%-10s%-10s%-10s%-22s%-22s\n", width, "ns", "queryHash", "op", "count", "maxMills", "minMills", "maxDocs", "firstTs", "lastTs")
	fmt.Printf("%-*s%-12s%-10s%-6s%-10s%-10s%-10s%-22s%-22s\n", width, "--", "---------", "--", "-----", "--------", "--------", "-------", "-------", "------")

	for _, log := range logs {
		fmt.Printf("%-*s%-12s%-10s%-6s%-10d%-10d%-10d%-22s%-22s\n",
			width,
			log.Ns,
			log.QueryHash,
			log.Op,
			cast.ToString(log.Cnt),
			log.MaxMills,
			log.MinMills,
			log.MaxDocs,
			timeutil.FormatLayoutString(log.MinTs),
			timeutil.FormatLayoutString(log.MaxTs),
		)
	}
	p.PrintBlankLine()
}

func (p *PrintSrvImpl) SlowDetail(ns, indexes, slow string) {
	fmt.Printf("ns: %s, indexes detail info:\n", color.GreenString(ns))
	fmt.Println("--------")
	fmt.Println(indexes)
	p.PrintBlankLine()
	color.Green("slow detail info:")
	fmt.Println("--------")
	fmt.Println(slow)
}
