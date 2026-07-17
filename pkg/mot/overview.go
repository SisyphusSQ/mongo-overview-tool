package mot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cast"
	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const defaultOverviewNodeConcurrency = 1

type nodeOverviewEnricher func(ctx context.Context, node NodeOverview) (NodeOverview, error)
type shardOverviewLoader func(ctx context.Context, shard pkgmongo.Shard) (ReplicaSetOverview, error)

// Overview 返回 MongoDB 副本集或分片集群的结构化概览。
func (c *Client) Overview(ctx context.Context, opts OverviewOptions) (result *OverviewResult, err error) {
	if c != nil && c.session == nil {
		return withEphemeralCollectorSession(ctx, c, func(session *CollectorSession) (*OverviewResult, error) {
			return session.Overview(ctx, opts)
		})
	}
	defer func() {
		err = mapContextError(err)
	}()
	if err := c.requireMemberConnectionURI(); err != nil {
		return nil, err
	}
	cluster, err := c.detectCluster(ctx)
	if err != nil {
		return nil, err
	}
	result = &OverviewResult{
		ClusterType: convertClusterType(cluster.Type),
	}
	nodeConcurrency := opts.NodeConcurrency
	if nodeConcurrency <= 0 {
		nodeConcurrency = defaultOverviewNodeConcurrency
	}
	nodeLimit := semaphore.NewWeighted(int64(nodeConcurrency))

	switch cluster.Type {
	case pkgmongo.ClusterRepl:
		rs, err := c.replicaSetOverview(ctx, c.conn, "base", opts.NodeConcurrency, nodeLimit)
		if err != nil {
			return nil, err
		}
		result.ReplicaSets = append(result.ReplicaSets, rs)
		result.Hosts = append(result.Hosts, replicaHosts(rs.Nodes)...)
	case pkgmongo.ClusterShard:
		shards, err := c.listShards(ctx)
		if err != nil {
			return nil, err
		}
		result.Hosts = shardHosts(shards)
		if c.session != nil && !c.session.legacy {
			result.ReplicaSets, err = collectShardOverviews(ctx, shards.Shards, c.session.maxConcurrency, func(ctx context.Context, shard pkgmongo.Shard) (ReplicaSetOverview, error) {
				return c.shardOverview(ctx, shard, opts.NodeConcurrency, nodeLimit)
			})
			if err != nil {
				return nil, err
			}
			break
		}
		for _, shard := range shards.Shards {
			rs, err := c.shardOverview(ctx, shard, opts.NodeConcurrency, nodeLimit)
			if err != nil {
				return nil, err
			}
			result.ReplicaSets = append(result.ReplicaSets, rs)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedTopology, cluster.Type)
	}

	if !opts.IncludeHosts {
		result.Hosts = nil
	}
	return result, nil
}

func (c *Client) shardOverview(ctx context.Context, shard pkgmongo.Shard, nodeConcurrency int, nodeLimit *semaphore.Weighted) (ReplicaSetOverview, error) {
	replicaSet, addresses, err := parseShardHost(shard.Host)
	if err != nil {
		return ReplicaSetOverview{}, err
	}
	conn, err := c.connectAddress(ctx, addresses, derivedConnectionOptions{
		ReplicaSet: replicaSet,
		Direct:     boolPointer(false),
	})
	if err != nil {
		return ReplicaSetOverview{}, err
	}
	defer c.closeDerivedConnection(ctx, conn)

	result, err := c.replicaSetOverview(ctx, conn, replicaSet, nodeConcurrency, nodeLimit)
	if err != nil {
		return ReplicaSetOverview{}, err
	}
	if result.Name == "" {
		result.Name = shard.Id
	}
	return result, nil
}

func collectShardOverviews(ctx context.Context, shards []pkgmongo.Shard, concurrency int, load shardOverviewLoader) ([]ReplicaSetOverview, error) {
	if load == nil {
		return nil, invalidOptions("shard overview loader is required")
	}
	if concurrency <= 0 {
		concurrency = defaultCollectorSessionConcurrency
	}

	result := make([]ReplicaSetOverview, len(shards))
	errs := make([]error, len(shards))
	group := new(errgroup.Group)
	group.SetLimit(concurrency)
	for i := range shards {
		i := i
		group.Go(func() error {
			result[i], errs[i] = load(ctx, shards[i])
			return nil
		})
	}
	_ = group.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (c *Client) replicaSetOverview(ctx context.Context, conn *pkgmongo.Conn, inventoryKey string, concurrency int, nodeLimit *semaphore.Weighted) (ReplicaSetOverview, error) {
	release, err := c.acquireRemoteSlot(ctx)
	if err != nil {
		return ReplicaSetOverview{}, err
	}
	rsStatus, err := conn.RsStatus(ctx)
	release()
	if err != nil {
		return ReplicaSetOverview{}, err
	}
	c.rememberReplicaSetInventory(inventoryKey, rsStatus)
	result := ReplicaSetOverview{Name: rsStatus.Set}

	var primaryOptime int64
	secondaryOptimes := make(map[string]int64)
	for _, member := range rsStatus.Members {
		node := NodeOverview{
			ReplicaSet: rsStatus.Set,
			Address:    member.Name,
			State:      member.StateStr,
			Uptime:     time.Duration(member.Uptime) * time.Second,
		}
		if member.StateStr == string(pkgmongo.NodePrimary) {
			primaryOptime = int64(member.Optime.Ts.T)
			node.ReplicationLag = 0
		}
		if member.StateStr == string(pkgmongo.NodeSecondary) {
			secondaryOptimes[member.Name] = int64(member.Optime.Ts.T)
		}
		result.Nodes = append(result.Nodes, node)
	}

	result.Nodes, err = enrichNodeOverviews(ctx, result.Nodes, concurrency, func(ctx context.Context, node NodeOverview) (NodeOverview, error) {
		return c.enrichNodeOverviewWithLimit(ctx, node, nodeLimit)
	})
	if err != nil {
		return ReplicaSetOverview{}, err
	}

	for i := range result.Nodes {
		node := &result.Nodes[i]
		if optime, ok := secondaryOptimes[node.Address]; ok {
			lagSeconds := primaryOptime - optime
			if lagSeconds < 0 {
				lagSeconds = 0
			}
			node.ReplicationLag = time.Duration(lagSeconds) * time.Second
		}
	}
	return result, nil
}

func enrichNodeOverviews(ctx context.Context, nodes []NodeOverview, concurrency int, enrich nodeOverviewEnricher) ([]NodeOverview, error) {
	if enrich == nil {
		return nil, invalidOptions("node overview enricher is required")
	}
	limit := concurrency
	if limit <= 0 {
		limit = defaultOverviewNodeConcurrency
	}

	result := append([]NodeOverview(nil), nodes...)
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)
	for i := range result {
		if result[i].State == string(pkgmongo.NodeArbiter) {
			continue
		}
		i := i
		group.Go(func() error {
			node, err := enrich(groupCtx, result[i])
			if err != nil {
				return err
			}
			result[i] = node
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) enrichNodeOverview(ctx context.Context, node NodeOverview) (NodeOverview, error) {
	return c.enrichNodeOverviewWithLimit(ctx, node, nil)
}

func (c *Client) enrichNodeOverviewWithLimit(ctx context.Context, node NodeOverview, nodeLimit *semaphore.Weighted) (NodeOverview, error) {
	release, err := c.acquireCapabilityRemoteSlot(ctx, nodeLimit)
	if err != nil {
		return node, err
	}
	defer release()

	conn, err := c.connectAddress(ctx, node.Address, derivedConnectionOptions{Direct: boolPointer(true)})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCancelled) {
			return node, mapContextError(err)
		}
		c.logger.Warnf("failed to connect node %s: %v", node.Address, err)
		return node, nil
	}
	defer c.closeDerivedConnection(ctx, conn)

	status, err := conn.ServerStatus(ctx)
	if err != nil {
		return node, err
	}
	node.Version = cast.ToString(status["version"])
	if uptime := cast.ToInt64(status["uptime"]); uptime > 0 {
		node.Uptime = time.Duration(uptime) * time.Second
	}
	if connections, ok := status["connections"].(bson.M); ok {
		node.ConnectionsCurrent = cast.ToInt64(connections["current"])
	}

	var (
		queuedReaders  int64
		queuedWriters  int64
		activeReaders  int64
		activeWriters  int64
		runningReaders int64
		runningWriters int64
	)
	if global, ok := status["global"].(bson.M); ok {
		if currentQueue, ok := global["currentQueue"].(bson.M); ok {
			queuedReaders = cast.ToInt64(currentQueue["readers"])
			queuedWriters = cast.ToInt64(currentQueue["writers"])
		}
		if activeClients, ok := global["activeClients"].(bson.M); ok {
			activeReaders = cast.ToInt64(activeClients["readers"])
			activeWriters = cast.ToInt64(activeClients["writers"])
		}
	}
	if wiredTiger, ok := status["wiredTiger"].(bson.M); ok {
		if transactions, ok := wiredTiger["concurrentTransactions"].(bson.M); ok {
			if read, ok := transactions["read"].(bson.M); ok {
				runningReaders = cast.ToInt64(read["out"])
			}
			if write, ok := transactions["write"].(bson.M); ok {
				runningWriters = cast.ToInt64(write["out"])
			}
		}
		if cache, ok := wiredTiger["cache"].(bson.M); ok {
			node.CacheSizeBytes = cast.ToInt64(cache["maximum bytes configured"])
			node.CacheUsedBytes = cast.ToInt64(cache["bytes currently in the cache"])
		}
	}

	node.ActiveReaders = runningReaders
	node.ActiveWriters = runningWriters
	node.QueueReaders = nonNegative(queuedReaders + activeReaders - runningReaders)
	node.QueueWriters = nonNegative(queuedWriters + activeWriters - runningWriters)
	return node, nil
}

func replicaHosts(nodes []NodeOverview) []string {
	hosts := make([]string, 0, len(nodes))
	for _, node := range nodes {
		host := strings.Split(node.Address, ":")[0]
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func shardHosts(shards pkgmongo.ShStatus) []string {
	hosts := make([]string, 0)
	for _, shard := range shards.Shards {
		_, addresses, err := parseShardHost(shard.Host)
		if err != nil {
			continue
		}
		for _, hp := range strings.Split(addresses, ",") {
			host := strings.Split(hp, ":")[0]
			if !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
