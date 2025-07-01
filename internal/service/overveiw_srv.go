package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cast"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/model"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

var _ OverviewSrv = (*OverviewSrvImpl)(nil)

type OverviewSrv interface {
	GetOverview() error
	Close()

	handleRepl(conn *mongo.Conn) error
	handleNode(repl, addr, stateStr string) (*model.OverviewStats, error)

	printReplHosts() error
	printShHosts(shards mongo.ShStatus)
}

type clusterType string
type nodeType string

const (
	repl  clusterType = "repl"
	shard clusterType = "sharding"

	primary   nodeType = "PRIMARY"
	secondary nodeType = "SECONDARY"
	arbiter   nodeType = "ARBITER"
)

type OverviewSrvImpl struct {
	ctx context.Context

	clusterType clusterType
	repl        map[string]string // replName -> replUri

	cfg  *config.BaseCfg
	conn *mongo.Conn

	printSrv PrintSrv
}

func NewOverviewSrv(ctx context.Context, cfg *config.BaseCfg, conn *mongo.Conn) (OverviewSrv, error) {
	o := &OverviewSrvImpl{
		ctx:      ctx,
		cfg:      cfg,
		conn:     conn,
		repl:     make(map[string]string),
		printSrv: NewPrintSrv(),
	}

	isSharding, err := conn.IsSharding(o.ctx)
	if err != nil {
		return nil, err
	}

	if isSharding {
		o.clusterType = shard
		shStatus, err := conn.ListShards(o.ctx)
		if err != nil {
			l.Logger.Errorf("Failed to get shStatus: %v", err)
			return nil, err
		}

		for _, sh := range shStatus.Shards {
			uri := sh.GetUri()
			if uri == "" {
				return nil, fmt.Errorf("sh uri is empty, shardRs: %s", sh.Id)
			}
			o.repl[sh.Id] = uri
		}
	} else {
		o.clusterType = repl
		master, err := conn.IsMaster(o.ctx)
		if err != nil {
			l.Logger.Errorf("Failed to get rsStatus: %v", err)
			return nil, err
		}

		o.repl[master.SetName] = master.Me
	}

	return o, nil
}

func (o *OverviewSrvImpl) GetOverview() error {
	o.printSrv.Ahead(o.conn.URI)
	switch o.clusterType {
	case repl:
		err := o.printReplHosts()
		if err != nil {
			l.Logger.Errorf("Failed to get replHosts: %v", err)
			return err
		}

		err = o.handleRepl(o.conn)
		if err != nil {
			l.Logger.Errorf("Failed to get overview: %v", err)
			return err
		}
	case shard:
		listShards, err := o.conn.ListShards(o.ctx)
		if err != nil {
			l.Logger.Errorf("Failed to list shards: %v", err)
			return err
		}
		o.printShHosts(listShards)

		for _, s := range listShards.Shards {
			split := strings.Split(s.Host, "/")
			if len(split) != 2 {
				return fmt.Errorf("invalid shard host: %s", s.Host)
			}

			conn, err := mongo.NewMongoConn(o.cfg.ConcatUri(split[1]))
			if err != nil {
				return fmt.Errorf("new conn err: %v", err)
			}

			err = o.handleRepl(conn)
			if err != nil {
				l.Logger.Errorf("Failed to get overview: %v", err)
				return err
			}
		}
	}
	return nil
}

func (o *OverviewSrvImpl) handleRepl(conn *mongo.Conn) error {
	stats := make([]*model.OverviewStats, 0)
	RsStatus, err := conn.RsStatus(o.ctx)
	if err != nil {
		l.Logger.Errorf("Failed to get rsStatus: %v", err)
		return err
	}

	var (
		priOpTime uint32
		secOpTime = make(map[string]uint32)
	)

	for _, m := range RsStatus.Members {
		s, err := o.handleNode(RsStatus.Set, m.Name, m.StateStr)
		if err != nil {
			l.Logger.Errorf("Failed to handleNode: %v", err)
			return err
		}
		if m.StateStr == string(primary) {
			s.Delay = "0s"
			priOpTime = m.Optime.Ts.T
		} else if m.StateStr == string(secondary) {
			secOpTime[m.Name] = m.Optime.Ts.T
		}

		stats = append(stats, s)
	}

	for _, stat := range stats {
		if t, ok := secOpTime[stat.Addr]; ok {
			delay := priOpTime - t
			stat.Delay = fmt.Sprintf("%ds", delay)
		}
	}

	// print stats
	o.printSrv.OverviewRepl(stats)
	return nil
}

func (o *OverviewSrvImpl) handleNode(repl, addr, stateStr string) (*model.OverviewStats, error) {
	s := model.NewOverviewStats(repl, addr, stateStr)
	if stateStr == string(arbiter) {
		return s, nil
	}

	conn, err := mongo.NewMongoConn(o.cfg.ConcatUri(addr))
	if err != nil {
		l.Logger.Errorf("Failed to get conn: %v", err)
		return s, nil
	}
	defer conn.Close()

	status, err := conn.ServerStatus(o.ctx)
	if err != nil {
		l.Logger.Errorf("Failed to get server status: %v", err)
		return nil, err
	}
	s.Version = cast.ToString(status["version"])
	s.UpTime = (cast.ToDuration(status["uptime"]) * time.Second).String()

	if connections, ok := status["connections"].(bson.M); ok {
		s.Conn = cast.ToString(connections["current"])
	}
	if mem, ok := status["mem"].(bson.M); ok {
		s.MenRes = cast.ToString(mem["resident"])
	}

	// engine only can be wiredTiger
	var (
		cqr, cqw   int
		acr, acw   int
		ctro, ctwo int
	)

	if gl, ok := status["global"].(bson.M); ok {
		if cr, ok := gl["currentQueue"].(bson.M); ok {
			cqr = cast.ToInt(cr["readers"])
			cqw = cast.ToInt(cr["writers"])
		}

		if ac, ok := gl["activeClients"].(bson.M); ok {
			acr = cast.ToInt(ac["readers"])
			acw = cast.ToInt(ac["writers"])
		}
	}

	if wt, ok := status["wiredTiger"].(bson.M); ok {
		if ct, ok := wt["concurrentTransactions"].(bson.M); ok {
			if r, ok := ct["read"].(bson.M); ok {
				ctro = cast.ToInt(r["out"])
			}

			if w, ok := ct["write"].(bson.M); ok {
				ctwo = cast.ToInt(w["out"])
			}
		}

		if ch, ok := wt["cache"].(bson.M); ok {
			if cast.ToInt64(ch["maximum bytes configured"]) > 0 {
				s.CacheSize = cast.ToInt64(ch["maximum bytes configured"])
				s.CacheUsed = cast.ToInt64(ch["bytes currently in the cache"])

				s.Size = strings.ReplaceAll(humanize.Bytes(uint64(s.CacheSize)), " ", "")
				s.MenRes = strings.ReplaceAll(humanize.Bytes(uint64(s.CacheUsed)), " ", "")
				s.MemUsed = fmt.Sprintf("%.1f%%", float64(s.CacheUsed)*100.0/float64(s.CacheSize))
			}
		}
	}

	s.AR = cast.ToString(ctro)
	s.AW = cast.ToString(ctwo)

	qr := cqr + acr - ctro
	if qr < 0 {
		qr = 0
	}
	qw := cqw + acw - ctwo
	if qw < 0 {
		qw = 0
	}
	s.QR = cast.ToString(qr)
	s.QW = cast.ToString(qw)

	return s, nil
}

func (o *OverviewSrvImpl) printReplHosts() error {
	hosts := make([]string, 0)
	rsStatus, err := o.conn.RsStatus(o.ctx)
	if err != nil {
		return err
	}
	for _, m := range rsStatus.Members {
		hosts = append(hosts, strings.Split(m.Name, ":")[0])
	}
	o.printSrv.Hosts(hosts)
	return nil
}

func (o *OverviewSrvImpl) printShHosts(shards mongo.ShStatus) {
	hosts := make([]string, 0)
	for _, s := range shards.Shards {
		split := strings.Split(s.Host, "/")
		for _, hp := range strings.Split(split[1], ",") {
			addr := strings.Split(hp, ":")[0]
			if slices.Contains(hosts, addr) {
				continue
			}
			hosts = append(hosts, addr)
		}
	}
	o.printSrv.Hosts(hosts)
}

func (o *OverviewSrvImpl) Close() {
	if o.conn != nil {
		_ = o.conn.Close()
	}
}
