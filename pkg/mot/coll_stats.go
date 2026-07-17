package mot

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/sync/errgroup"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const defaultCollectionStatsConcurrency = 20

var systemDatabases = []string{"admin", "config", "local"}

// CollectionStats 返回数据库和集合统计信息。
func (c *Client) CollectionStats(ctx context.Context, opts CollectionStatsOptions) (result *CollectionStatsResult, err error) {
	defer func() {
		err = mapContextError(err)
	}()
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	if opts.RequireShardedCluster || opts.ShardedOnly {
		isSharding, err := c.conn.IsSharding(ctx)
		if err != nil {
			return nil, err
		}
		if !isSharding {
			return nil, fmt.Errorf("%w", ErrNotSharded)
		}
	}
	limit := opts.Concurrency
	if limit <= 0 {
		limit = defaultCollectionStatsConcurrency
	}

	dbs, err := c.conn.Client.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	result = &CollectionStatsResult{}
	for _, db := range dbs {
		if !opts.IncludeSystemDB && slices.Contains(systemDatabases, db) {
			continue
		}
		if len(opts.Databases) != 0 && !slices.Contains(opts.Databases, db) {
			continue
		}
		dbStats, err := c.databaseStats(ctx, db, opts, limit)
		if err != nil {
			return nil, err
		}
		result.Databases = append(result.Databases, dbStats)
	}
	return result, nil
}

func (c *Client) databaseStats(ctx context.Context, db string, opts CollectionStatsOptions, limit int) (DatabaseStats, error) {
	dbConn := c.conn.Client.Database(db)
	colls, err := dbConn.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return DatabaseStats{}, err
	}

	var (
		lock      sync.Mutex
		collStats = make([]CollectionStats, 0)
		group     errgroup.Group
	)
	group.SetLimit(limit)
	for _, coll := range colls {
		coll := coll
		if len(opts.Collections) != 0 && !slices.Contains(opts.Collections, coll) {
			continue
		}
		group.Go(func() error {
			var stats pkgmongo.CollStats
			if err := dbConn.RunCommand(ctx, bson.D{{Key: "collStats", Value: coll}}).Decode(&stats); err != nil {
				return err
			}
			if opts.ShardedOnly && !stats.Sharded {
				return nil
			}
			lock.Lock()
			defer lock.Unlock()
			collStats = append(collStats, CollectionStats{
				Namespace:        stats.Ns,
				Count:            stats.Count,
				AvgObjectBytes:   stats.AvgObjSize,
				StorageSizeBytes: stats.StorageSize,
				IsSharded:        stats.Sharded,
				IndexCount:       stats.NIndexes,
				TotalIndexBytes:  stats.TotalIndexSize,
			})
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return DatabaseStats{}, err
	}
	sort.Slice(collStats, func(i, j int) bool {
		if collStats[i].Count == collStats[j].Count {
			return collStats[i].Namespace < collStats[j].Namespace
		}
		return collStats[i].Count > collStats[j].Count
	})

	rawDBStats, err := c.conn.DBStatus(ctx, db)
	if err != nil {
		return DatabaseStats{}, err
	}
	return DatabaseStats{
		Name:             db,
		StorageSizeBytes: rawDBStats.StorageSize,
		Collections:      collStats,
	}, nil
}
