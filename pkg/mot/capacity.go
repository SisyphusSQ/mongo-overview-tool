package mot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

const capacitySchemaVersion = 1

type CapacityOptions struct {
	Databases          []string
	Collections        []string
	IncludeSystemDB    bool
	IncludeFreeStorage bool
	MaxCollections     int
	Concurrency        int
}

type CapacityIdentity struct {
	TopologyType ClusterType `json:"topologyType"`
	Digest       string      `json:"digest"`
}

type CollectionCapacity struct {
	Namespace            string          `json:"namespace"`
	Count                *int64          `json:"count,omitempty"`
	AverageObjectBytes   *float64        `json:"averageObjectBytes,omitempty"`
	DataSizeBytes        *int64          `json:"dataSizeBytes,omitempty"`
	StorageSizeBytes     *int64          `json:"storageSizeBytes,omitempty"`
	IndexSizeBytes       *int64          `json:"indexSizeBytes,omitempty"`
	FreeStorageSizeBytes *int64          `json:"freeStorageSizeBytes,omitempty"`
	CompressionRatio     *float64        `json:"compressionRatio,omitempty"`
	IndexToDataRatio     *float64        `json:"indexToDataRatio,omitempty"`
	Sharded              bool            `json:"sharded"`
	Shards               []ShardCapacity `json:"shards,omitempty"`
}

type ShardCapacity struct {
	Shard                string   `json:"shard"`
	Host                 string   `json:"host,omitempty"`
	Count                *int64   `json:"count,omitempty"`
	AverageObjectBytes   *float64 `json:"averageObjectBytes,omitempty"`
	DataSizeBytes        *int64   `json:"dataSizeBytes,omitempty"`
	StorageSizeBytes     *int64   `json:"storageSizeBytes,omitempty"`
	IndexSizeBytes       *int64   `json:"indexSizeBytes,omitempty"`
	FreeStorageSizeBytes *int64   `json:"freeStorageSizeBytes,omitempty"`
}

type DatabaseCapacity struct {
	Name                      string               `json:"name"`
	Objects                   *int64               `json:"objects,omitempty"`
	DataSizeBytes             *int64               `json:"dataSizeBytes,omitempty"`
	StorageSizeBytes          *int64               `json:"storageSizeBytes,omitempty"`
	IndexSizeBytes            *int64               `json:"indexSizeBytes,omitempty"`
	TotalSizeBytes            *int64               `json:"totalSizeBytes,omitempty"`
	FSUsedSizeBytes           *int64               `json:"fsUsedSizeBytes,omitempty"`
	FSTotalSizeBytes          *int64               `json:"fsTotalSizeBytes,omitempty"`
	FreeStorageSizeBytes      *int64               `json:"freeStorageSizeBytes,omitempty"`
	IndexFreeStorageSizeBytes *int64               `json:"indexFreeStorageSizeBytes,omitempty"`
	TotalFreeStorageSizeBytes *int64               `json:"totalFreeStorageSizeBytes,omitempty"`
	Filesystems               []FilesystemCapacity `json:"filesystems,omitempty"`
	Collections               []CollectionCapacity `json:"collections"`
}

type FilesystemCapacity struct {
	Shard            string `json:"shard,omitempty"`
	Host             string `json:"host,omitempty"`
	FSUsedSizeBytes  *int64 `json:"fsUsedSizeBytes,omitempty"`
	FSTotalSizeBytes *int64 `json:"fsTotalSizeBytes,omitempty"`
}

type CapacityResult struct {
	SchemaVersion     int                 `json:"schemaVersion"`
	ClusterIdentity   CapacityIdentity    `json:"clusterIdentity"`
	CollectedAt       time.Time           `json:"collectedAt"`
	Databases         []DatabaseCapacity  `json:"databases"`
	Findings          []DiagnosticFinding `json:"findings"`
	CollectorStatuses []CollectorStatus   `json:"collectorStatuses"`
}

type CapacityDelta struct {
	Before        *int64   `json:"before,omitempty"`
	After         *int64   `json:"after,omitempty"`
	Delta         *int64   `json:"delta,omitempty"`
	AveragePerDay *float64 `json:"averagePerDay,omitempty"`
}

type DatabaseCapacityDiff struct {
	Name    string        `json:"name"`
	State   string        `json:"state"`
	Objects CapacityDelta `json:"objects"`
	Data    CapacityDelta `json:"data"`
	Storage CapacityDelta `json:"storage"`
	Index   CapacityDelta `json:"index"`
}

type CollectionCapacityDiff struct {
	Namespace string        `json:"namespace"`
	State     string        `json:"state"`
	Count     CapacityDelta `json:"count"`
	Data      CapacityDelta `json:"data"`
	Storage   CapacityDelta `json:"storage"`
	Index     CapacityDelta `json:"index"`
}

type CapacityDiffResult struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	ClusterIdentity   CapacityIdentity         `json:"clusterIdentity"`
	BeforeCollectedAt time.Time                `json:"beforeCollectedAt"`
	AfterCollectedAt  time.Time                `json:"afterCollectedAt"`
	Duration          time.Duration            `json:"duration"`
	Comparable        bool                     `json:"comparable"`
	Databases         []DatabaseCapacityDiff   `json:"databases"`
	Collections       []CollectionCapacityDiff `json:"collections"`
}

// Capacity 采集稳定、脱敏且 presence-aware 的容量快照；SDK 不写本地文件。
func (c *Client) Capacity(ctx context.Context, opts CapacityOptions) (result *CapacityResult, err error) {
	opts, err = normalizeCapacityOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	defer func() { err = mapContextError(err) }()
	cluster, err := pkgmongo.DetectCluster(ctx, c.conn)
	if err != nil {
		return nil, err
	}
	if gate, allowed := diagnosticCapabilityGate("collection_capacity", convertClusterType(cluster.Type), cluster.MaxWireVersion, true); !allowed {
		return &CapacityResult{SchemaVersion: capacitySchemaVersion, CollectedAt: time.Now().UTC(), CollectorStatuses: []CollectorStatus{gate}}, nil
	}
	var shardTargets []hotspotTarget
	var targetStatuses []CollectorStatus
	var targetErrors []error
	if cluster.Type == pkgmongo.ClusterShard {
		shardTargets, targetStatuses, targetErrors = c.capacityPrimaryTargets(ctx)
	}
	identity, err := c.capacityIdentity(ctx, cluster.Type)
	if err != nil {
		return nil, err
	}
	refs, err := c.indexCollectionRefs(ctx, IndexAuditOptions{Databases: opts.Databases, AllDatabases: len(opts.Databases) == 0, Collections: opts.Collections, IncludeSystemDB: opts.IncludeSystemDB, MaxCollections: opts.MaxCollections})
	if err != nil {
		return nil, err
	}
	if len(refs) > opts.MaxCollections {
		return nil, invalidOptions("selected %d collections, exceeds max %d", len(refs), opts.MaxCollections)
	}
	result = &CapacityResult{SchemaVersion: capacitySchemaVersion, ClusterIdentity: identity, CollectedAt: time.Now().UTC(), CollectorStatuses: targetStatuses}
	freeStorageGate, freeStorageAllowed := diagnosticCapabilityGate("free_storage", convertClusterType(cluster.Type), cluster.MaxWireVersion, opts.IncludeFreeStorage)
	if opts.IncludeFreeStorage && !freeStorageAllowed {
		opts.IncludeFreeStorage = false
		result.CollectorStatuses = append(result.CollectorStatuses, freeStorageGate)
	}
	byDatabase := make(map[string]*DatabaseCapacity)
	for _, ref := range refs {
		if byDatabase[ref.Database] == nil {
			byDatabase[ref.Database] = &DatabaseCapacity{Name: ref.Database}
		}
	}
	var mu sync.Mutex
	collectorErrors := append([]error(nil), targetErrors...)
	group, groupCtx := errgroup.WithContext(ctx)
	limit := semaphore.NewWeighted(int64(opts.Concurrency))
	for _, ref := range refs {
		if acquireErr := acquireDiagnosticSlot(groupCtx, limit); acquireErr != nil {
			mu.Lock()
			collectorErrors = append(collectorErrors, acquireErr)
			mu.Unlock()
			break
		}
		ref := ref
		group.Go(func() error {
			defer limit.Release(1)
			scope := FindingScope{Type: ScopeNamespace, Database: ref.Database, Namespace: ref.Database + "." + ref.Collection}
			snapshot, collectErr := c.conn.CollectionCapacity(groupCtx, ref.Database, ref.Collection, false, 5*time.Second)
			var freeSnapshot pkgmongo.CollectionCapacitySnapshot
			var freeErr error
			if collectErr == nil && opts.IncludeFreeStorage {
				freeSnapshot, freeErr = c.conn.CollectionCapacity(groupCtx, ref.Database, ref.Collection, true, 5*time.Second)
			}
			var shards []ShardCapacity
			var shardStatuses []CollectorStatus
			var shardErrors []error
			if collectErr == nil && len(shardTargets) > 0 {
				shards, shardStatuses, shardErrors = c.collectShardCapacityDetails(groupCtx, ref, shardTargets, opts.IncludeFreeStorage)
			}
			mu.Lock()
			defer mu.Unlock()
			if collectErr != nil {
				result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("collection_capacity", scope, collectErr))
				if !isUnauthorizedError(collectErr) && !isUnsupportedDiagnosticError(collectErr) {
					collectorErrors = append(collectorErrors, collectErr)
				}
				return nil
			}
			if opts.IncludeFreeStorage {
				switch {
				case freeErr == nil:
					snapshot.FreeStorageSizeBytes = freeSnapshot.FreeStorageSizeBytes
					mergeShardFreeStorage(&snapshot, freeSnapshot)
					result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{Name: "free_storage", State: CapabilitySupported, Scope: scope, ReasonCode: "expensive_opt_in"})
				case isUnauthorizedError(freeErr):
					result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("free_storage", scope, freeErr))
				case isUnsupportedDiagnosticError(freeErr):
					result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{Name: "free_storage", State: CapabilityUnsupported, Scope: scope, ReasonCode: "unsupported_version", Message: "$collStats storageStats 在当前服务器上不可用"})
				default:
					collectorErrors = append(collectorErrors, freeErr)
					result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("free_storage", scope, freeErr))
				}
			}
			collection := collectionCapacityFromMongo(snapshot)
			if len(shards) > 0 {
				collection.Shards = shards
				collection.Sharded = true
			}
			result.CollectorStatuses = append(result.CollectorStatuses, shardStatuses...)
			collectorErrors = append(collectorErrors, shardErrors...)
			byDatabase[ref.Database].Collections = append(byDatabase[ref.Database].Collections, collection)
			result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{Name: "collection_capacity", State: CapabilitySupported, Scope: scope})
			return nil
		})
	}
	_ = group.Wait()
	for _, database := range sortedCapacityDatabaseNames(byDatabase) {
		db := byDatabase[database]
		snapshot, collectErr := c.conn.DatabaseCapacity(ctx, database, false, 5*time.Second)
		scope := FindingScope{Type: ScopeDatabase, Database: database}
		if len(shardTargets) > 0 {
			filesystems, filesystemStatuses, filesystemErrors := c.collectShardDatabaseFilesystems(ctx, database, shardTargets)
			db.Filesystems = filesystems
			result.CollectorStatuses = append(result.CollectorStatuses, filesystemStatuses...)
			collectorErrors = append(collectorErrors, filesystemErrors...)
		}
		if collectErr != nil {
			result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("database_capacity", scope, collectErr))
			if !isUnauthorizedError(collectErr) && !isUnsupportedDiagnosticError(collectErr) {
				collectorErrors = append(collectorErrors, collectErr)
			}
		} else {
			applyDatabaseCapacity(db, snapshot)
			result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{Name: "database_capacity", State: CapabilitySupported, Scope: scope})
			if opts.IncludeFreeStorage {
				freeSnapshot, freeErr := c.conn.DatabaseCapacity(ctx, database, true, 5*time.Second)
				switch {
				case freeErr == nil:
					db.FreeStorageSizeBytes = freeSnapshot.FreeStorageSizeBytes
					db.IndexFreeStorageSizeBytes = freeSnapshot.IndexFreeStorageSizeBytes
					db.TotalFreeStorageSizeBytes = freeSnapshot.TotalFreeStorageSizeBytes
				case isUnauthorizedError(freeErr):
					result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("database_free_storage", scope, freeErr))
				case isUnsupportedDiagnosticError(freeErr):
					result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{Name: "database_free_storage", State: CapabilityUnsupported, Scope: scope, ReasonCode: "unsupported_version", Message: "dbStats freeStorage 在当前服务器上不可用"})
				default:
					collectorErrors = append(collectorErrors, freeErr)
					result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("database_free_storage", scope, freeErr))
				}
			}
		}
		sort.SliceStable(db.Collections, func(i, j int) bool { return db.Collections[i].Namespace < db.Collections[j].Namespace })
		result.Databases = append(result.Databases, *db)
	}
	if freeStorageGate.State == CapabilitySkipped {
		result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{Name: "free_storage", State: CapabilitySkipped, Scope: FindingScope{Type: ScopeCluster}, ReasonCode: "not_requested", Message: "free storage 仅在显式 opt-in 时采集"})
	}
	sortCollectorStatuses(result.CollectorStatuses)
	if len(collectorErrors) > 0 {
		if len(result.Databases) == 0 {
			return result, errors.Join(collectorErrors...)
		}
		return result, newDiagnosticPartialError("capacity", result, errors.Join(collectorErrors...))
	}
	return result, nil
}

func (c *Client) capacityPrimaryTargets(ctx context.Context) ([]hotspotTarget, []CollectorStatus, []error) {
	shards, err := c.conn.ListShards(ctx)
	if err != nil {
		return nil, nil, []error{err}
	}
	var targets []hotspotTarget
	var statuses []CollectorStatus
	var collectorErrors []error
	for _, shard := range shards.Shards {
		if contextError(ctx) != nil {
			collectorErrors = append(collectorErrors, contextError(ctx))
			break
		}
		replicaSet, addresses, parseErr := parseShardHost(shard.Host)
		scope := FindingScope{Type: ScopeReplicaSet, Shard: shard.Id, ReplicaSet: replicaSet}
		if parseErr != nil {
			collectorErrors = append(collectorErrors, parseErr)
			statuses = append(statuses, failedCollectorStatus("collection_capacity", scope, parseErr))
			continue
		}
		conn, connectErr := c.connectAddress(ctx, addresses, derivedConnectionOptions{ReplicaSet: replicaSet, Direct: boolPointer(false)})
		if connectErr != nil {
			collectorErrors = append(collectorErrors, connectErr)
			statuses = append(statuses, failedCollectorStatus("collection_capacity", scope, connectErr))
			continue
		}
		status, statusErr := conn.RsStatus(ctx)
		c.closeDerivedConnection(ctx, conn)
		if statusErr != nil {
			collectorErrors = append(collectorErrors, statusErr)
			statuses = append(statuses, failedCollectorStatus("collection_capacity", scope, statusErr))
			continue
		}
		found := false
		for _, member := range status.Members {
			if member.Health == 1 && member.State == pkgmongo.StatePrimary {
				targets = append(targets, hotspotTarget{Shard: shard.Id, ReplicaSet: status.Set, Address: member.Name})
				found = true
				break
			}
		}
		if !found {
			targetErr := fmt.Errorf("no healthy primary for shard %s", shard.Id)
			collectorErrors = append(collectorErrors, targetErr)
			statuses = append(statuses, failedCollectorStatus("collection_capacity", scope, targetErr))
		}
	}
	return targets, statuses, collectorErrors
}

func (c *Client) collectShardCapacityDetails(ctx context.Context, ref indexCollectionRef, targets []hotspotTarget, includeFreeStorage bool) ([]ShardCapacity, []CollectorStatus, []error) {
	var result []ShardCapacity
	var statuses []CollectorStatus
	var collectorErrors []error
	for _, target := range targets {
		if contextError(ctx) != nil {
			collectorErrors = append(collectorErrors, contextError(ctx))
			break
		}
		scope := FindingScope{Type: ScopeNamespace, Shard: target.Shard, ReplicaSet: target.ReplicaSet, Node: target.Address, Database: ref.Database, Namespace: ref.Database + "." + ref.Collection}
		conn, connectErr := c.connectAddress(ctx, target.Address, derivedConnectionOptions{Direct: boolPointer(true)})
		if connectErr != nil {
			collectorErrors = append(collectorErrors, connectErr)
			statuses = append(statuses, failedCollectorStatus("collection_capacity", scope, connectErr))
			continue
		}
		collections, listErr := conn.Client.Database(ref.Database).ListCollectionNames(ctx, bson.D{{Key: "name", Value: ref.Collection}})
		if listErr != nil {
			c.closeDerivedConnection(ctx, conn)
			status := failedCollectorStatus("collection_capacity", scope, listErr)
			statuses = append(statuses, status)
			if status.State == CapabilityFailed {
				collectorErrors = append(collectorErrors, listErr)
			}
			continue
		}
		if len(collections) == 0 {
			c.closeDerivedConnection(ctx, conn)
			statuses = append(statuses, CollectorStatus{Name: "collection_capacity", State: CapabilitySkipped, Scope: scope, ReasonCode: "namespace_not_on_shard", Message: "该 shard 不承载此 namespace"})
			continue
		}
		snapshot, collectErr := conn.CollectionCapacity(ctx, ref.Database, ref.Collection, includeFreeStorage, 5*time.Second)
		c.closeDerivedConnection(ctx, conn)
		if collectErr != nil {
			status := failedCollectorStatus("collection_capacity", scope, collectErr)
			statuses = append(statuses, status)
			if status.State == CapabilityFailed {
				collectorErrors = append(collectorErrors, collectErr)
			}
			continue
		}
		result = append(result, ShardCapacity{Shard: target.Shard, Host: target.Address, Count: snapshot.Count, AverageObjectBytes: snapshot.AverageObjectBytes, DataSizeBytes: snapshot.DataSizeBytes, StorageSizeBytes: snapshot.StorageSizeBytes, IndexSizeBytes: snapshot.TotalIndexSizeBytes, FreeStorageSizeBytes: snapshot.FreeStorageSizeBytes})
		statuses = append(statuses, CollectorStatus{Name: "collection_capacity", State: CapabilitySupported, Scope: scope})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Shard != result[j].Shard {
			return result[i].Shard < result[j].Shard
		}
		return result[i].Host < result[j].Host
	})
	return result, statuses, collectorErrors
}

func (c *Client) collectShardDatabaseFilesystems(ctx context.Context, database string, targets []hotspotTarget) ([]FilesystemCapacity, []CollectorStatus, []error) {
	var result []FilesystemCapacity
	var statuses []CollectorStatus
	var collectorErrors []error
	for _, target := range targets {
		if contextError(ctx) != nil {
			collectorErrors = append(collectorErrors, contextError(ctx))
			break
		}
		scope := FindingScope{Type: ScopeNode, Shard: target.Shard, ReplicaSet: target.ReplicaSet, Node: target.Address, Database: database}
		conn, connectErr := c.connectAddress(ctx, target.Address, derivedConnectionOptions{Direct: boolPointer(true)})
		if connectErr != nil {
			collectorErrors = append(collectorErrors, connectErr)
			statuses = append(statuses, failedCollectorStatus("database_filesystem", scope, connectErr))
			continue
		}
		snapshot, collectErr := conn.DatabaseCapacity(ctx, database, false, 5*time.Second)
		c.closeDerivedConnection(ctx, conn)
		if collectErr != nil {
			status := failedCollectorStatus("database_filesystem", scope, collectErr)
			statuses = append(statuses, status)
			if status.State == CapabilityFailed {
				collectorErrors = append(collectorErrors, collectErr)
			}
			continue
		}
		result = append(result, FilesystemCapacity{Shard: target.Shard, Host: target.Address, FSUsedSizeBytes: snapshot.FSUsedSizeBytes, FSTotalSizeBytes: snapshot.FSTotalSizeBytes})
		statuses = append(statuses, CollectorStatus{Name: "database_filesystem", State: CapabilitySupported, Scope: scope})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Shard != result[j].Shard {
			return result[i].Shard < result[j].Shard
		}
		return result[i].Host < result[j].Host
	})
	return result, statuses, collectorErrors
}

func mergeShardFreeStorage(target *pkgmongo.CollectionCapacitySnapshot, source pkgmongo.CollectionCapacitySnapshot) {
	for index := range target.Shards {
		for _, shard := range source.Shards {
			if target.Shards[index].Shard == shard.Shard && (target.Shards[index].Host == "" || shard.Host == "" || target.Shards[index].Host == shard.Host) {
				target.Shards[index].FreeStorageSizeBytes = shard.FreeStorageSizeBytes
				if target.Shards[index].Host == "" {
					target.Shards[index].Host = shard.Host
				}
				break
			}
		}
	}
}

func normalizeCapacityOptions(opts CapacityOptions) (CapacityOptions, error) {
	if opts.MaxCollections < 0 || opts.Concurrency < 0 {
		return CapacityOptions{}, invalidOptions("max collections and concurrency must not be negative")
	}
	if opts.MaxCollections == 0 {
		opts.MaxCollections = defaultMaxCollections
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = defaultOverviewNodeConcurrency
	}
	return opts, nil
}

func (c *Client) capacityIdentity(ctx context.Context, clusterType pkgmongo.ClusterType) (CapacityIdentity, error) {
	inputs := []string{string(clusterType)}
	switch clusterType {
	case pkgmongo.ClusterRepl:
		status, err := c.conn.RsStatus(ctx)
		if err != nil {
			return CapacityIdentity{}, err
		}
		inputs = append(inputs, status.Set)
		for _, member := range status.Members {
			inputs = append(inputs, strings.ToLower(member.Name))
		}
	case pkgmongo.ClusterShard:
		shards, err := c.conn.ListShards(ctx)
		if err != nil {
			return CapacityIdentity{}, err
		}
		for _, shard := range shards.Shards {
			replicaSet, addresses, parseErr := parseShardHost(shard.Host)
			if parseErr != nil {
				return CapacityIdentity{}, parseErr
			}
			inputs = append(inputs, shard.Id, replicaSet)
			for _, address := range strings.Split(addresses, ",") {
				inputs = append(inputs, strings.ToLower(strings.TrimSpace(address)))
			}
		}
	}
	sort.Strings(inputs)
	digest := sha256.Sum256([]byte(strings.Join(inputs, "\x00")))
	return CapacityIdentity{TopologyType: convertClusterType(clusterType), Digest: hex.EncodeToString(digest[:])}, nil
}

func collectionCapacityFromMongo(snapshot pkgmongo.CollectionCapacitySnapshot) CollectionCapacity {
	result := CollectionCapacity{
		Namespace: snapshot.Namespace, Count: snapshot.Count, AverageObjectBytes: snapshot.AverageObjectBytes,
		DataSizeBytes: snapshot.DataSizeBytes, StorageSizeBytes: snapshot.StorageSizeBytes,
		IndexSizeBytes: snapshot.TotalIndexSizeBytes, FreeStorageSizeBytes: snapshot.FreeStorageSizeBytes,
		Sharded: snapshot.Sharded,
	}
	if snapshot.DataSizeBytes != nil && *snapshot.DataSizeBytes > 0 {
		if snapshot.StorageSizeBytes != nil && *snapshot.StorageSizeBytes > 0 {
			ratio := float64(*snapshot.DataSizeBytes) / float64(*snapshot.StorageSizeBytes)
			result.CompressionRatio = &ratio
		}
		if snapshot.TotalIndexSizeBytes != nil {
			ratio := float64(*snapshot.TotalIndexSizeBytes) / float64(*snapshot.DataSizeBytes)
			result.IndexToDataRatio = &ratio
		}
	}
	for _, shard := range snapshot.Shards {
		result.Shards = append(result.Shards, ShardCapacity{Shard: shard.Shard, Host: shard.Host, Count: shard.Count, AverageObjectBytes: shard.AverageObjectBytes, DataSizeBytes: shard.DataSizeBytes, StorageSizeBytes: shard.StorageSizeBytes, IndexSizeBytes: shard.TotalIndexSizeBytes, FreeStorageSizeBytes: shard.FreeStorageSizeBytes})
	}
	sort.SliceStable(result.Shards, func(i, j int) bool {
		if result.Shards[i].Shard != result.Shards[j].Shard {
			return result.Shards[i].Shard < result.Shards[j].Shard
		}
		return result.Shards[i].Host < result.Shards[j].Host
	})
	return result
}

func applyDatabaseCapacity(target *DatabaseCapacity, source pkgmongo.DatabaseCapacitySnapshot) {
	target.Objects = source.Objects
	target.DataSizeBytes = source.DataSizeBytes
	target.StorageSizeBytes = source.StorageSizeBytes
	target.IndexSizeBytes = source.IndexSizeBytes
	target.TotalSizeBytes = source.TotalSizeBytes
	target.FSUsedSizeBytes = source.FSUsedSizeBytes
	target.FSTotalSizeBytes = source.FSTotalSizeBytes
	target.FreeStorageSizeBytes = source.FreeStorageSizeBytes
	target.IndexFreeStorageSizeBytes = source.IndexFreeStorageSizeBytes
	target.TotalFreeStorageSizeBytes = source.TotalFreeStorageSizeBytes
}

func sortedCapacityDatabaseNames(values map[string]*DatabaseCapacity) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DiffCapacity 纯离线比较同一集群的两个兼容快照。
func DiffCapacity(before, after CapacityResult) (*CapacityDiffResult, error) {
	if before.SchemaVersion != capacitySchemaVersion || after.SchemaVersion != capacitySchemaVersion {
		return nil, invalidOptions("unsupported capacity schema version")
	}
	if before.ClusterIdentity.Digest == "" || before.ClusterIdentity.Digest != after.ClusterIdentity.Digest || before.ClusterIdentity.TopologyType != after.ClusterIdentity.TopologyType {
		return nil, invalidOptions("capacity snapshots belong to different clusters")
	}
	if !after.CollectedAt.After(before.CollectedAt) {
		return nil, invalidOptions("after snapshot must be newer than before snapshot")
	}
	result := &CapacityDiffResult{SchemaVersion: capacitySchemaVersion, ClusterIdentity: before.ClusterIdentity, BeforeCollectedAt: before.CollectedAt, AfterCollectedAt: after.CollectedAt, Duration: after.CollectedAt.Sub(before.CollectedAt), Comparable: true}
	beforeDatabases := capacityDatabasesByName(before)
	afterDatabases := capacityDatabasesByName(after)
	for _, name := range sortedUnionKeys(beforeDatabases, afterDatabases) {
		previous, hadBefore := beforeDatabases[name]
		current, hasAfter := afterDatabases[name]
		state := capacityLifecycleState(hadBefore, hasAfter)
		item := DatabaseCapacityDiff{Name: name, State: state}
		if hadBefore && hasAfter {
			item.Objects = capacityDelta(previous.Objects, current.Objects, result.Duration)
			item.Data = capacityDelta(previous.DataSizeBytes, current.DataSizeBytes, result.Duration)
			item.Storage = capacityDelta(previous.StorageSizeBytes, current.StorageSizeBytes, result.Duration)
			item.Index = capacityDelta(previous.IndexSizeBytes, current.IndexSizeBytes, result.Duration)
		}
		result.Databases = append(result.Databases, item)
	}
	beforeCollections := flattenCapacityCollections(before)
	afterCollections := flattenCapacityCollections(after)
	names := make(map[string]struct{}, len(beforeCollections)+len(afterCollections))
	for name := range beforeCollections {
		names[name] = struct{}{}
	}
	for name := range afterCollections {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		previous, hadBefore := beforeCollections[name]
		current, hasAfter := afterCollections[name]
		state := capacityLifecycleState(hadBefore, hasAfter)
		item := CollectionCapacityDiff{Namespace: name, State: state}
		if hadBefore && hasAfter {
			item.Count = capacityDelta(previous.Count, current.Count, result.Duration)
			item.Data = capacityDelta(previous.DataSizeBytes, current.DataSizeBytes, result.Duration)
			item.Storage = capacityDelta(previous.StorageSizeBytes, current.StorageSizeBytes, result.Duration)
			item.Index = capacityDelta(previous.IndexSizeBytes, current.IndexSizeBytes, result.Duration)
		}
		result.Collections = append(result.Collections, item)
	}
	return result, nil
}

func flattenCapacityCollections(result CapacityResult) map[string]CollectionCapacity {
	values := make(map[string]CollectionCapacity)
	for _, database := range result.Databases {
		for _, collection := range database.Collections {
			values[collection.Namespace] = collection
		}
	}
	return values
}

func capacityDelta(before, after *int64, duration time.Duration) CapacityDelta {
	result := CapacityDelta{Before: before, After: after}
	if before != nil && after != nil {
		delta := *after - *before
		result.Delta = &delta
		if duration > 0 {
			perDay := float64(delta) / duration.Hours() * 24
			result.AveragePerDay = &perDay
		}
	}
	return result
}

func capacityDatabasesByName(result CapacityResult) map[string]DatabaseCapacity {
	values := make(map[string]DatabaseCapacity, len(result.Databases))
	for _, database := range result.Databases {
		values[database.Name] = database
	}
	return values
}

func sortedUnionKeys[T any](before, after map[string]T) []string {
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func capacityLifecycleState(hadBefore, hasAfter bool) string {
	if !hadBefore {
		return "added"
	}
	if !hasAfter {
		return "removed"
	}
	return "existing"
}
