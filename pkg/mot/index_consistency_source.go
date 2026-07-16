package mot

import (
	"context"
	"fmt"
	"sort"
	"time"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

const indexConsistencyCollectorTimeout = 5 * time.Second

type clientIndexConsistencySource struct {
	client *Client
}

func (s clientIndexConsistencySource) ServerVersion(ctx context.Context) (string, error) {
	return s.client.conn.ServerVersion(ctx)
}

func (s clientIndexConsistencySource) Shards(ctx context.Context) (map[string]indexShardTarget, error) {
	shards, err := s.client.conn.ListShards(ctx)
	if err != nil {
		return nil, fmt.Errorf("list shards: %w", err)
	}
	result := make(map[string]indexShardTarget, len(shards.Shards))
	for _, shard := range shards.Shards {
		replicaSet, addresses, parseErr := parseShardHost(shard.Host)
		if parseErr != nil {
			return nil, parseErr
		}
		result[shard.Id] = indexShardTarget{Shard: shard.Id, ReplicaSet: replicaSet, Addresses: addresses}
	}
	return result, nil
}

func (s clientIndexConsistencySource) Routing(ctx context.Context, ref indexCollectionRef) (pkgmongo.IndexRoutingSnapshot, error) {
	return s.client.conn.IndexRouting(ctx, ref.Database, ref.Collection, indexConsistencyCollectorTimeout)
}

func (s clientIndexConsistencySource) Visibility(ctx context.Context, ref indexCollectionRef) (indexCollectionVisibility, error) {
	snapshot, err := s.client.conn.CollectionCapacity(ctx, ref.Database, ref.Collection, false, indexConsistencyCollectorTimeout)
	if err != nil {
		return indexCollectionVisibility{}, err
	}
	result := indexCollectionVisibility{IndexBuilds: append([]string(nil), snapshot.IndexBuilds...)}
	for _, shard := range snapshot.Shards {
		if shard.Shard != "" {
			result.Shards = append(result.Shards, shard.Shard)
		}
	}
	sort.Strings(result.Shards)
	sort.Strings(result.IndexBuilds)
	return result, nil
}

func (s clientIndexConsistencySource) Metadata(ctx context.Context, database, collection string) ([]pkgmongo.MetadataIndexInconsistency, error) {
	return s.client.conn.CheckMetadataIndexConsistency(ctx, pkgmongo.MetadataConsistencyRequest{
		Database: database, Collection: collection, BatchSize: 100, MaxTime: indexConsistencyCollectorTimeout,
	})
}

func (s clientIndexConsistencySource) Stats(ctx context.Context, ref indexCollectionRef) ([]pkgmongo.CanonicalIndexDefinition, error) {
	return s.client.conn.IndexConsistencyStats(ctx, ref.Database, ref.Collection, indexConsistencyCollectorTimeout)
}

func (s clientIndexConsistencySource) Direct(ctx context.Context, ref indexCollectionRef, target indexShardTarget) ([]pkgmongo.CanonicalIndexDefinition, error) {
	conn, err := s.client.connectAddress(ctx, target.Addresses, derivedConnectionOptions{
		ReplicaSet: target.ReplicaSet, Direct: boolPointer(false),
	})
	if err != nil {
		return nil, err
	}
	defer s.client.closeDerivedConnection(ctx, conn)
	return conn.ListIndexDefinitions(ctx, ref.Database, ref.Collection, indexConsistencyCollectorTimeout)
}
