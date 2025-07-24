package model

type OverviewStats struct {
	Repl    string `json:"repl"`
	Addr    string `json:"addr"`
	State   string `json:"state"`
	Conn    string `json:"conn"`
	QR      string `json:"qr"`
	QW      string `json:"qw"`
	AR      string `json:"ar"`
	AW      string `json:"aw"`
	Size    string `json:"size"`
	MemUsed string `json:"memUsed"`
	MenRes  string `json:"menRes"`
	Delay   string `json:"delay"`
	UpTime  string `json:"upTime"`
	Engine  string `json:"engine"`
	Version string `json:"version"`

	CacheUsed    int64  `json:"cacheUsed"`
	CacheSize    int64  `json:"cacheSize"`
	ColoredState string `json:"coloredState"`
}

func NewOverviewStats(repl, addr, stateStr string) *OverviewStats {
	return &OverviewStats{
		Repl:    repl,
		Addr:    addr,
		State:   stateStr,
		Conn:    "n/a",
		QR:      "n/a",
		QW:      "n/a",
		AR:      "n/a",
		AW:      "n/a",
		Size:    "n/a",
		MemUsed: "n/a",
		MenRes:  "n/a",
		Delay:   "n/a",
		UpTime:  "n/a",
		Engine:  "n/a",
		Version: "n/a",
	}
}
