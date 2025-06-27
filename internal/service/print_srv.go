package service

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cast"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/model"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
)

var _ PrintSrv = (*PrintSrvImpl)(nil)

type PrintSrv interface {
	Ahead(uri string)
	OverviewRepl(stats []*model.OverviewStats)
}

type PrintSrvImpl struct {
	format string
}

func NewPrintSrv() PrintSrv {
	return &PrintSrvImpl{}
}

func (p *PrintSrvImpl) Ahead(uri string) {
	fmt.Print("URI: ")
	color.Green(utils.BlockPassword(uri, "***"))
}

func (p *PrintSrvImpl) OverviewRepl(stats []*model.OverviewStats) {
	fmt.Println()

	width := len(stats[0].Repl) + 3

	color.Cyan("%-*s%-23s%-10s%-6s%-6s%-6s%-4s%-4s%-7s%-10s%-10s%-7s%-15s%-10s\n", width, "repl", "host", "state", "conn", "qr", "qw", "ar", "aw", "size", "memUsed", "menRes", "delay", "uptime", "version")
	fmt.Printf("%-*s%-23s%-10s%-6s%-6s%-6s%-4s%-4s%-7s%-10s%-10s%-7s%-15s%-10s\n", width, "----", "----", "-----", "----", "--", "--", "--", "--", "----", "-------", "-------", "-----", "------", "-------")

	for _, stat := range stats {
		// width 4
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

		// width 4
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

		fmt.Printf("%-*s%-23s%-10s%-6s%-*s%-*s%-4s%-4s%-7s%-*s%-10s%-7s%-15s%-10s\n",
			width,
			stat.Repl,
			stat.Addr,
			stat.State,
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
			stat.MenRes,
			stat.Delay,
			stat.UpTime,
			stat.Version,
		)
	}
}
