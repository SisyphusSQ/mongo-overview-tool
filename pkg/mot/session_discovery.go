package mot

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/singleflight"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

type collectorDiscoverySource interface {
	DetectClusterTopology(ctx context.Context, conn *pkgmongo.Conn) (*pkgmongo.ClusterInfo, error)
	ListShards(ctx context.Context, conn *pkgmongo.Conn) (pkgmongo.ShStatus, error)
}

type mongoCollectorDiscoverySource struct{}

func (mongoCollectorDiscoverySource) DetectClusterTopology(ctx context.Context, conn *pkgmongo.Conn) (*pkgmongo.ClusterInfo, error) {
	return pkgmongo.DetectClusterTopology(ctx, conn)
}

func (mongoCollectorDiscoverySource) ListShards(ctx context.Context, conn *pkgmongo.Conn) (pkgmongo.ShStatus, error) {
	return conn.ListShards(ctx)
}

func (c *Client) detectCluster(ctx context.Context) (*pkgmongo.ClusterInfo, error) {
	if c != nil && c.session != nil {
		return c.session.detectCluster(ctx)
	}
	return pkgmongo.DetectCluster(ctx, c.conn)
}

func (c *Client) detectClusterTopology(ctx context.Context) (*pkgmongo.ClusterInfo, error) {
	if c != nil && c.session != nil {
		return c.session.clusterTopology(ctx)
	}
	return pkgmongo.DetectClusterTopology(ctx, c.conn)
}

func (c *Client) listShards(ctx context.Context) (pkgmongo.ShStatus, error) {
	if c != nil && c.session != nil {
		return c.session.shards(ctx)
	}
	return c.conn.ListShards(ctx)
}

func (s *CollectorSession) detectCluster(ctx context.Context) (*pkgmongo.ClusterInfo, error) {
	topology, err := s.clusterTopology(ctx)
	if err != nil {
		return nil, err
	}
	if topology.Type != pkgmongo.ClusterShard {
		return topology, nil
	}
	shards, err := s.shards(ctx)
	if err != nil {
		return nil, fmt.Errorf("list shards: %w", err)
	}
	for _, shard := range shards.Shards {
		uri := shard.GetUri()
		if uri == "" {
			return nil, fmt.Errorf("sh uri is empty, shardRs: %s", shard.Id)
		}
		topology.Repl[shard.Id] = uri
	}
	return topology, nil
}

func (s *CollectorSession) clusterTopology(ctx context.Context) (*pkgmongo.ClusterInfo, error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	cached := cloneClusterInfo(s.topology)
	s.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	resultChannel := s.discoveryGroup.DoChan("topology", func() (any, error) {
		s.mu.RLock()
		cached := cloneClusterInfo(s.topology)
		s.mu.RUnlock()
		if cached != nil {
			return cached, nil
		}
		release, err := s.acquireRemoteSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		loaded, err := s.discoverySource.DetectClusterTopology(ctx, s.client.conn)
		if err != nil {
			return nil, mapContextError(err)
		}
		loaded = cloneClusterInfo(loaded)
		s.mu.Lock()
		s.topology = loaded
		s.stats.TopologyLoads++
		s.mu.Unlock()
		return cloneClusterInfo(loaded), nil
	})
	result, err := waitForDiscoveryResult[*pkgmongo.ClusterInfo](ctx, resultChannel)
	return cloneClusterInfo(result), err
}

func (s *CollectorSession) shards(ctx context.Context) (pkgmongo.ShStatus, error) {
	if err := s.requireOpen(); err != nil {
		return pkgmongo.ShStatus{}, err
	}
	s.mu.RLock()
	if s.shardInventory != nil {
		cached := cloneShardStatus(*s.shardInventory)
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	resultChannel := s.discoveryGroup.DoChan("shards", func() (any, error) {
		s.mu.RLock()
		if s.shardInventory != nil {
			cached := cloneShardStatus(*s.shardInventory)
			s.mu.RUnlock()
			return cached, nil
		}
		s.mu.RUnlock()
		release, err := s.acquireRemoteSlot(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		loaded, err := s.discoverySource.ListShards(ctx, s.client.conn)
		if err != nil {
			return nil, mapContextError(err)
		}
		loaded = cloneShardStatus(loaded)
		s.mu.Lock()
		s.shardInventory = &loaded
		s.stats.ShardInventoryLoads++
		s.mu.Unlock()
		return cloneShardStatus(loaded), nil
	})
	result, err := waitForDiscoveryResult[pkgmongo.ShStatus](ctx, resultChannel)
	return cloneShardStatus(result), err
}

func waitForDiscoveryResult[T any](ctx context.Context, resultChannel <-chan singleflight.Result) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return zero, mapContextError(ctx.Err())
	case result := <-resultChannel:
		if result.Err != nil {
			return zero, result.Err
		}
		value, ok := result.Val.(T)
		if !ok {
			return zero, errors.New("collector discovery cache returned an invalid value")
		}
		return value, nil
	}
}

func cloneClusterInfo(source *pkgmongo.ClusterInfo) *pkgmongo.ClusterInfo {
	if source == nil {
		return nil
	}
	result := *source
	result.Repl = make(map[string]string, len(source.Repl))
	for name, uri := range source.Repl {
		result.Repl[name] = uri
	}
	return &result
}

func cloneShardStatus(source pkgmongo.ShStatus) pkgmongo.ShStatus {
	result := source
	result.Shards = append([]pkgmongo.Shard(nil), source.Shards...)
	return result
}
