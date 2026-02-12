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

var _ CollStatsSrv = (*CollStatsSrvImpl)(nil)

type CollStatsSrv interface {
	Stats(isSh bool) error
	Close()
}

type CollStatsSrvImpl struct {
	ctx        context.Context
	db2Check   []string
	coll2Check []string

	cfg  *config.StatsConfig
	conn *mongo.Conn

	printSrv PrintSrv
}

func NewCollStatsSrv(ctx context.Context, cfg *config.StatsConfig, conn *mongo.Conn, checkSh bool) (CollStatsSrv, error) {
	if checkSh {
		isSharding, err := conn.IsSharding(ctx)
		if err != nil {
			l.Logger.Errorf("Failed to check sharding: %v", err)
			return nil, err
		}
		if !isSharding {
			return nil, fmt.Errorf("not sharding")
		}
	}

	c := &CollStatsSrvImpl{
		ctx:      ctx,
		cfg:      cfg,
		conn:     conn,
		printSrv: NewPrintSrv(),
	}

	if cfg.Database != "" {
		c.db2Check = strings.Split(strings.TrimSpace(cfg.Database), ",")
	}
	if cfg.Collection != "" {
		c.coll2Check = strings.Split(strings.TrimSpace(cfg.Collection), ",")
		cfg.ShowAll = true
	}

	return c, nil
}

func (c *CollStatsSrvImpl) Stats(isSh bool) error {
	var g errgroup.Group
	g.SetLimit(50)
	c.printSrv.Ahead(c.cfg.BuildUri)

	dbs, err := c.conn.Client.ListDatabaseNames(c.ctx, bson.D{})
	if err != nil {
		l.Logger.Errorf("Failed to list databases: %v", err)
		return err
	}

	for _, db := range dbs {
		var (
			lock      sync.Mutex
			maxWidth  int
			collStats = make([]mongo.CollStats, 0)
		)

		if slices.Contains(config.FilterDBs, db) {
			l.Logger.Debugf("system database[%s], skip...", db)
			continue
		}

		if len(c.db2Check) != 0 && !slices.Contains(c.db2Check, db) {
			continue
		}

		l.Logger.Debugf("Now checking for Database: %s", db)

		dbConn := c.conn.Client.Database(db)
		colls, err := dbConn.ListCollectionNames(c.ctx, bson.D{})
		if err != nil {
			l.Logger.Errorf("Failed to list collections: %v", err)
			return err
		}

		for _, coll := range colls {
			if len(c.coll2Check) != 0 && !slices.Contains(c.coll2Check, coll) {
				continue
			}

			l.Logger.Debugf("Now checking for Collection: %s", coll)
			g.Go(func() error {
				var stats mongo.CollStats
				err := dbConn.RunCommand(c.ctx, bson.D{{"collStats", coll}}).Decode(&stats)
				if err != nil {
					l.Logger.Errorf("Failed to get collection[%s] stats: %v", coll, err)
					return err
				}

				lock.Lock()
				defer lock.Unlock()
				collStats = append(collStats, stats)
				if len(stats.Ns)-maxWidth > 0 {
					maxWidth = len(stats.Ns)
				}

				return nil
			})
		}

		if err = g.Wait(); err != nil {
			l.Logger.Errorf("Failed to check sharding: %v", err)
			return err
		}

		collStats = slices.SortedFunc(func(yield func(mongo.CollStats) bool) {
			for _, node := range collStats {
				if !yield(node) {
					return
				}
			}
		}, func(a, b mongo.CollStats) int {
			return -int(a.Count - b.Count)
		})

		maxWidth += 2
		dbStats, err := c.conn.DBStatus(c.ctx, db)
		if err != nil {
			l.Logger.Errorf("Failed to get DB status: %v", err)
			return err
		}
		// sharding's dbStats hasn't db name, so we need to add it manually
		dbStats.DB = db

		if isSh {
			c.printSrv.ShardDatabase(dbStats, maxWidth)
		} else {
			c.printSrv.Database(dbStats, maxWidth)
		}

		for _, s := range collStats {
			if isSh {
				c.printSrv.ShardCollStats(s, c.cfg.ShowAll)
			} else {
				c.printSrv.CollStats(s)
			}
		}
		c.printSrv.PrintBlankLine()
	}

	return nil
}

func (c *CollStatsSrvImpl) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}
