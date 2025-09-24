package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/sync/errgroup"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

var _ SlowlogSrv = (*SlowlogSrvImpl)(nil)

type SlowlogSrv interface {
	GetOverview() error
	handleReplOverview(conn *mongo.Conn) error
	handleNodeOverview(addr, db string) ([]*mongo.SlowlogView, error)

	GetSlowDetail() error

	Close()
}

var (
	filterDBs = []string{"admin", "config", "local"}
)

type SlowlogSrvImpl struct {
	sync.Mutex
	ctx context.Context

	clusterType clusterType
	repl        map[string]string // replName -> replUri

	cfg  *config.SlowlogConfig
	conn *mongo.Conn

	printSrv PrintSrv
}

func NewSlowlogSrv(ctx context.Context, cfg *config.SlowlogConfig, conn *mongo.Conn) (SlowlogSrv, error) {
	s := &SlowlogSrvImpl{
		ctx:      ctx,
		cfg:      cfg,
		conn:     conn,
		repl:     make(map[string]string),
		printSrv: NewPrintSrv(),
	}

	isSharding, err := conn.IsSharding(s.ctx)
	if err != nil {
		return nil, err
	}

	if isSharding {
		s.clusterType = shard
		shStatus, err := conn.ListShards(s.ctx)
		if err != nil {
			l.Logger.Errorf("Failed to get shStatus: %v", err)
			return nil, err
		}

		for _, sh := range shStatus.Shards {
			uri := sh.GetUri()
			if uri == "" {
				return nil, fmt.Errorf("sh uri is empty, shardRs: %s", sh.Id)
			}
			s.repl[sh.Id] = uri
		}
	} else {
		s.clusterType = repl
		master, err := conn.IsMaster(s.ctx)
		if err != nil {
			l.Logger.Errorf("Failed to get rsStatus: %v", err)
			return nil, err
		}

		s.repl[master.SetName] = master.Me
	}

	return s, nil
}

func (s *SlowlogSrvImpl) GetOverview() error {
	s.printSrv.Ahead(s.conn.URI)
	switch s.clusterType {
	case repl:
		if err := s.printReplHosts(); err != nil {
			l.Logger.Errorf("Failed to get replHosts: %v", err)
			return err
		}

		if err := s.handleReplOverview(s.conn); err != nil {
			l.Logger.Errorf("Failed to get handleReplOverview, err: %v", err)
			return err
		}
	case shard:
		listShards, err := s.conn.ListShards(s.ctx)
		if err != nil {
			l.Logger.Errorf("Failed to list shards: %v", err)
			return err
		}

		for _, sh := range listShards.Shards {
			split := strings.Split(sh.Host, "/")
			if len(split) != 2 {
				return fmt.Errorf("invalid shard host: %s", sh.Host)
			}

			conn, err := mongo.NewMongoConn(s.cfg.ConcatUri(split[1]))
			if err != nil {
				return fmt.Errorf("new conn err: %v", err)
			}

			if err = s.handleReplOverview(conn); err != nil {
				l.Logger.Errorf("Failed to get handleReplOverview, err: %v", err)
				return err
			}
		}
	default:
		return fmt.Errorf("clusterType not support, clusterType: %s", s.clusterType)
	}

	return nil
}

func (s *SlowlogSrvImpl) printReplHosts() error {
	hosts := make([]string, 0)
	rsStatus, err := s.conn.RsStatus(s.ctx)
	if err != nil {
		return err
	}
	for _, m := range rsStatus.Members {
		hosts = append(hosts, m.Name)
	}
	s.printSrv.Hosts(hosts)
	return nil
}

func (s *SlowlogSrvImpl) handleReplOverview(conn *mongo.Conn) error {
	RsStatus, err := conn.RsStatus(s.ctx)
	if err != nil {
		l.Logger.Errorf("Failed to get rsStatus: %v", err)
		return err
	}

	s.printSrv.Repl(RsStatus.Set)
	s.printSrv.PrintDashedLine()
	for _, m := range RsStatus.Members {
		if m.State != mongo.StatePrimary && m.State != mongo.StateSecondary {
			continue
		}

		s.printSrv.SingleHost(m.Name, m.State)
		var g errgroup.Group
		g.SetLimit(5)

		dbs, err := conn.Client.ListDatabaseNames(s.ctx, bson.M{})
		if err != nil {
			l.Logger.Errorf("Failed to get dbNames: %v", err)
			return err
		}

		for _, db := range dbs {
			if slices.Contains(filterDBs, db) {
				continue
			}

			g.Go(func() error {

				logs, err := s.handleNodeOverview(m.Name, db)
				if err != nil {
					return nil
				}

				if logs == nil || len(logs) == 0 {
					l.Logger.Debugf("db %s has no logs or no system.profile collection", db)
					return nil
				}

				// print
				s.Lock()
				defer s.Unlock()
				s.printSrv.SlowOverView(logs)
				s.printSrv.PrintHorizontalLine()
				return nil
			})
		}

		if err = g.Wait(); err != nil {
			return err
		}
	}

	return nil
}

func (s *SlowlogSrvImpl) handleNodeOverview(addr, db string) ([]*mongo.SlowlogView, error) {
	conn, err := mongo.NewMongoConn(s.cfg.ConcatUriWithAuthDB(addr, db))
	if err != nil {
		l.Logger.Errorf("Failed to get conn: %v", err)
		return nil, nil
	}
	defer conn.Close()

	colls, err := conn.Client.Database(db).ListCollectionNames(s.ctx, bson.D{{"name", "system.profile"}})
	if err != nil {
		l.Logger.Errorf("Failed to get collNames in db[%s], err: %v", db, err)
		return nil, nil
	}
	if len(colls) == 0 {
		return nil, nil
	}

	logs := make([]*mongo.SlowlogView, 0)
	dbSlowLogs, err := conn.GetSlowLogView(s.ctx, db, s.cfg.Sort)
	if err != nil {
		l.Logger.Errorf("Failed to get dbSlowLogView in db[%s], err: %v", db, err)
		return nil, err
	}
	logs = append(logs, dbSlowLogs...)

	return logs, nil
}

func (s *SlowlogSrvImpl) GetSlowDetail() error {
	s.printSrv.Ahead(s.conn.URI)

	slow, err := s.conn.GetSlowDetail(s.ctx, s.cfg.DB, s.cfg.QueryHash)
	if err != nil {
		l.Logger.Errorf("Failed to get slow detail, err: %v", err)
		return err
	}

	coll := strings.Split(slow["ns"].(string), ".")[1]
	if coll == "" {
		// impossible
		return fmt.Errorf("failed to get coll name from ns, ns: %s", slow["ns"].(string))
	}

	var indexes []bson.M
	cur, err := s.conn.Client.Database(s.cfg.DB).Collection(coll).Indexes().List(s.ctx)
	if err != nil {
		l.Logger.Errorf("Failed to get indexes, err: %v", err)
		return err
	}
	defer cur.Close(s.ctx)
	if err = cur.All(s.ctx, &indexes); err != nil {
		l.Logger.Errorf("Failed to get indexes, err: %v", err)
		return err
	}
	l.Logger.Debugf("indexes: %+v", indexes)

	var idx string
	for _, index := range indexes {
		indexesJson, err := bson.MarshalExtJSONIndent(index, false, false, " ", "    ")
		if err != nil {
			l.Logger.Errorf("Failed to marshal indexes, err: %v", err)
			return err
		}
		idx += string(indexesJson) + "\n"
	}

	// 删点一些无用的字段
	delete(slow, "allUsers")
	delete(slow, "protocol")
	delete(slow, "flowControl")
	delete(slow, "queryHash")
	delete(slow, "numYield")
	delete(slow, "locks")
	delete(slow, "execStats")
	slowJson, err := bson.MarshalExtJSONIndent(slow, false, false, " ", "    ")
	if err != nil {
		l.Logger.Errorf("Failed to marshal slow, err: %v", err)
		return err
	}
	s.printSrv.SlowDetail(slow["ns"].(string), idx, string(slowJson))

	return nil
}

func (s *SlowlogSrvImpl) Close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}
