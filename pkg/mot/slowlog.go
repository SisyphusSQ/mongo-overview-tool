package mot

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sync/errgroup"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

const defaultSlowlogConcurrency = 5

type slowlogDatabaseLoader func(ctx context.Context, addr, db string, sort SlowlogSort) (DatabaseSlowlogSummary, bool, error)

// SlowlogSummary 返回慢日志聚合结果。
func (c *Client) SlowlogSummary(ctx context.Context, opts SlowlogOptions) (result *SlowlogSummaryResult, err error) {
	defer func() {
		err = mapContextError(err)
	}()
	if err := c.requireMemberConnectionURI(); err != nil {
		return nil, err
	}
	if opts.Sort == "" {
		opts.Sort = SlowlogSortCount
	}
	if !isValidSlowlogSort(opts.Sort) {
		return nil, invalidOptions("invalid slowlog sort %q", opts.Sort)
	}

	cluster, err := pkgmongo.DetectCluster(ctx, c.conn)
	if err != nil {
		return nil, err
	}
	result = &SlowlogSummaryResult{ClusterType: convertClusterType(cluster.Type)}
	switch cluster.Type {
	case pkgmongo.ClusterRepl:
		rs, err := c.replicaSetSlowlogSummary(ctx, c.conn, opts)
		if err != nil {
			return nil, err
		}
		result.ReplicaSets = append(result.ReplicaSets, rs)
	case pkgmongo.ClusterShard:
		shards, err := c.conn.ListShards(ctx)
		if err != nil {
			return nil, err
		}
		for _, shard := range shards.Shards {
			replicaSet, addresses, err := parseShardHost(shard.Host)
			if err != nil {
				return nil, err
			}
			conn, err := c.connectAddress(ctx, addresses, derivedConnectionOptions{
				ReplicaSet: replicaSet,
				Direct:     boolPointer(false),
			})
			if err != nil {
				return nil, err
			}
			rs, err := c.replicaSetSlowlogSummary(ctx, conn, opts)
			c.closeDerivedConnection(ctx, conn)
			if err != nil {
				return nil, err
			}
			if rs.Name == "" {
				rs.Name = shard.Id
			}
			result.ReplicaSets = append(result.ReplicaSets, rs)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedTopology, cluster.Type)
	}
	return result, nil
}

// SlowlogDetail 返回单个 queryHash 的原始慢日志文档和索引信息。
func (c *Client) SlowlogDetail(ctx context.Context, db, queryHash string) (result *SlowlogDetailResult, err error) {
	defer func() {
		err = mapContextError(err)
	}()
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	if db == "" {
		return nil, invalidOptions("database is required")
	}
	if queryHash == "" {
		return nil, invalidOptions("query hash is required")
	}
	if strings.TrimSpace(c.uri) == "" {
		return slowlogDetailFromConnection(ctx, c.conn, db, queryHash)
	}

	summary, err := c.SlowlogSummary(ctx, SlowlogOptions{
		Databases:   []string{db},
		Sort:        SlowlogSortCount,
		Concurrency: defaultSlowlogConcurrency,
	})
	if err != nil {
		return nil, err
	}
	address := findSlowlogAddress(summary, db, queryHash)
	if address == "" {
		return nil, drivermongo.ErrNoDocuments
	}
	conn, err := c.connectAddress(ctx, address, derivedConnectionOptions{
		Database: db,
		Direct:   boolPointer(true),
	})
	if err != nil {
		return nil, err
	}
	defer c.closeDerivedConnection(ctx, conn)
	return slowlogDetailFromConnection(ctx, conn, db, queryHash)
}

func findSlowlogAddress(summary *SlowlogSummaryResult, db, queryHash string) string {
	if summary == nil {
		return ""
	}
	for _, replicaSet := range summary.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				if database.Database != db {
					continue
				}
				for _, item := range database.Items {
					if item.QueryHash == queryHash {
						return host.Address
					}
				}
			}
		}
	}
	return ""
}

func slowlogDetailFromConnection(ctx context.Context, conn *pkgmongo.Conn, db, queryHash string) (*SlowlogDetailResult, error) {
	slow, err := conn.GetSlowDetail(ctx, db, queryHash)
	if err != nil {
		return nil, err
	}
	namespace, ok := slow["ns"].(string)
	if !ok || namespace == "" {
		return nil, fmt.Errorf("%w: slowlog ns is missing", ErrInvalidOptions)
	}
	parts := strings.SplitN(namespace, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("%w: invalid slowlog namespace %s", ErrInvalidOptions, namespace)
	}

	cur, err := conn.Client.Database(db).Collection(parts[1]).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = cur.Close(closeCtx)
	}()

	var indexes []bson.M
	if err := cur.All(ctx, &indexes); err != nil {
		return nil, err
	}
	return &SlowlogDetailResult{
		Namespace: namespace,
		Slowlog:   slow,
		Indexes:   indexes,
	}, nil
}

func (c *Client) replicaSetSlowlogSummary(ctx context.Context, conn *pkgmongo.Conn, opts SlowlogOptions) (ReplicaSetSlowlogSummary, error) {
	rsStatus, err := conn.RsStatus(ctx)
	if err != nil {
		return ReplicaSetSlowlogSummary{}, err
	}
	result := ReplicaSetSlowlogSummary{Name: rsStatus.Set}
	for _, member := range rsStatus.Members {
		if member.State != pkgmongo.StatePrimary && member.State != pkgmongo.StateSecondary {
			continue
		}
		host := HostSlowlogSummary{
			Address: member.Name,
			State:   member.State.String(),
		}
		dbs, err := conn.Client.ListDatabaseNames(ctx, bson.M{})
		if err != nil {
			return ReplicaSetSlowlogSummary{}, err
		}
		filteredDBs := make([]string, 0, len(dbs))
		for _, db := range dbs {
			if slices.Contains(systemDatabases, db) {
				continue
			}
			if len(opts.Databases) != 0 && !slices.Contains(opts.Databases, db) {
				continue
			}
			filteredDBs = append(filteredDBs, db)
		}
		host.Databases, err = collectSlowlogDatabaseSummaries(
			ctx,
			member.Name,
			filteredDBs,
			opts.Sort,
			opts.Concurrency,
			c.databaseSlowlogSummary,
		)
		if err != nil {
			return ReplicaSetSlowlogSummary{}, err
		}
		result.Hosts = append(result.Hosts, host)
	}
	return result, nil
}

func collectSlowlogDatabaseSummaries(
	ctx context.Context,
	addr string,
	dbs []string,
	sort SlowlogSort,
	concurrency int,
	load slowlogDatabaseLoader,
) ([]DatabaseSlowlogSummary, error) {
	if load == nil {
		return nil, invalidOptions("slowlog database loader is required")
	}
	limit := concurrency
	if limit <= 0 {
		limit = defaultSlowlogConcurrency
	}

	type summarySlot struct {
		summary DatabaseSlowlogSummary
		ok      bool
	}
	slots := make([]summarySlot, len(dbs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)
	for i, db := range dbs {
		i, db := i, db
		group.Go(func() error {
			summary, ok, err := load(groupCtx, addr, db, sort)
			if err != nil {
				return err
			}
			slots[i] = summarySlot{summary: summary, ok: ok}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}

	result := make([]DatabaseSlowlogSummary, 0, len(slots))
	for _, slot := range slots {
		if slot.ok {
			result = append(result, slot.summary)
		}
	}
	return result, nil
}

func (c *Client) databaseSlowlogSummary(ctx context.Context, addr, db string, sort SlowlogSort) (DatabaseSlowlogSummary, bool, error) {
	conn, err := c.connectAddress(ctx, addr, derivedConnectionOptions{
		Database: db,
		Direct:   boolPointer(true),
	})
	if err != nil {
		return DatabaseSlowlogSummary{}, false, err
	}
	defer c.closeDerivedConnection(ctx, conn)

	colls, err := conn.Client.Database(db).ListCollectionNames(ctx, bson.D{{Key: "name", Value: "system.profile"}})
	if err != nil {
		return DatabaseSlowlogSummary{}, false, err
	}
	if len(colls) == 0 {
		return DatabaseSlowlogSummary{}, false, nil
	}
	logs, err := conn.GetSlowLogView(ctx, db, string(sort))
	if err != nil {
		return DatabaseSlowlogSummary{}, false, err
	}
	if len(logs) == 0 {
		return DatabaseSlowlogSummary{}, false, nil
	}

	summary := DatabaseSlowlogSummary{Database: db}
	for i, log := range logs {
		item := SlowlogSummaryItem{
			Namespace: log.Ns,
			Operation: log.Op,
			QueryHash: log.QueryHash,
			Count:     log.Cnt,
			MaxMillis: log.MaxMills,
			MinMillis: log.MinMills,
			MaxDocs:   log.MaxDocs,
			FirstTime: log.MinTs,
			LastTime:  log.MaxTs,
		}
		summary.Items = append(summary.Items, item)
		summary.Total += log.Cnt
		if i == 0 || log.MinTs.Before(summary.FirstTime) {
			summary.FirstTime = log.MinTs
		}
		if i == 0 || log.MaxTs.After(summary.LastTime) {
			summary.LastTime = log.MaxTs
		}
	}
	return summary, true, nil
}

func isValidSlowlogSort(sort SlowlogSort) bool {
	switch sort {
	case SlowlogSortCount, SlowlogSortMaxMillis, SlowlogSortMaxDocs:
		return true
	default:
		return false
	}
}
