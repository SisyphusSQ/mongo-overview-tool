//go:build integration

package mot

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const liveE2ETimeout = 3 * time.Minute

func TestLiveSDKReadOnlyE2E(t *testing.T) {
	// 测试真实复制集或分片集群上的全部 SDK 只读主路径，不执行 bulk 写入。
	host := requireLiveEnv(t, "MOT_TEST_MONGO_HOST")
	username := requireLiveEnv(t, "MONGO_USER")
	password := requireLiveEnv(t, "MONGO_PASS")
	expectedCluster := ClusterType(requireLiveEnv(t, "MOT_TEST_EXPECT_CLUSTER"))
	if expectedCluster != ClusterReplicaSet && expectedCluster != ClusterSharded {
		t.Fatalf("MOT_TEST_EXPECT_CLUSTER must be %q or %q", ClusterReplicaSet, ClusterSharded)
	}
	port, err := strconv.Atoi(requireLiveEnv(t, "MOT_TEST_MONGO_PORT"))
	if err != nil || port <= 0 {
		t.Fatalf("MOT_TEST_MONGO_PORT must be a positive integer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveE2ETimeout)
	defer cancel()

	var direct *bool
	if expectedCluster == ClusterSharded {
		direct = boolPointer(false)
	}
	client, err := NewClient(ctx, Options{
		Host:           host,
		Port:           port,
		Username:       username,
		Password:       password,
		AuthSource:     defaultAuthSource,
		ConnectTimeout: 15 * time.Second,
		Direct:         direct,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer closeCancel()
		if err := client.Close(closeCtx); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	}()

	overview, err := client.Overview(ctx, OverviewOptions{
		IncludeHosts:    true,
		NodeConcurrency: 2,
	})
	if err != nil {
		t.Fatalf("Overview failed: %v", err)
	}
	if overview.ClusterType != expectedCluster {
		t.Fatalf("Overview cluster type = %q, want %q", overview.ClusterType, expectedCluster)
	}
	if len(overview.ReplicaSets) == 0 || liveNodeCount(overview) == 0 {
		t.Fatalf("Overview returned no replica sets or nodes")
	}

	database, collection := liveNamespace(t, ctx, client)
	statsOpts := CollectionStatsOptions{
		Databases:       []string{database},
		Collections:     []string{collection},
		IncludeSystemDB: true,
		Concurrency:     1,
	}
	if expectedCluster == ClusterSharded {
		statsOpts.RequireShardedCluster = true
	}
	stats, err := client.CollectionStats(ctx, statsOpts)
	if err != nil {
		t.Fatalf("CollectionStats failed: %v", err)
	}
	if liveCollectionCount(stats) == 0 {
		t.Fatalf("CollectionStats returned no collection")
	}

	doctor, doctorErr := client.Doctor(ctx, DoctorOptions{NodeConcurrency: 2})
	if doctorErr != nil && !errors.Is(doctorErr, ErrPartialResult) {
		t.Fatalf("Doctor failed: %v", doctorErr)
	}
	if doctor == nil || len(doctor.CollectorStatuses) == 0 {
		t.Fatalf("Doctor returned no collector status")
	}

	operations, operationsErr := client.CurrentOperations(ctx, CurrentOperationsOptions{AllUsers: true, Limit: 20, MaxTime: 5 * time.Second})
	if operationsErr != nil && !errors.Is(operationsErr, ErrPartialResult) {
		t.Fatalf("CurrentOperations failed: %v", operationsErr)
	}
	if operations == nil || len(operations.CollectorStatuses) == 0 {
		t.Fatalf("CurrentOperations returned no collector status")
	}

	hotspot, hotspotErr := client.Hotspot(ctx, HotspotOptions{Duration: 100 * time.Millisecond, TopN: 5, NodeConcurrency: 2, Databases: []string{database}})
	if hotspotErr != nil && !errors.Is(hotspotErr, ErrPartialResult) {
		t.Fatalf("Hotspot failed: %v", hotspotErr)
	}
	if hotspot == nil || hotspot.EffectiveDuration <= 0 {
		t.Fatalf("Hotspot returned no comparable sampling window")
	}

	indexAudit, indexAuditErr := client.IndexAudit(ctx, IndexAuditOptions{Databases: []string{database}, Collections: []string{collection}, Checks: []IndexAuditCheck{IndexCheckUnused, IndexCheckRedundant, IndexCheckSpace, IndexCheckBuilding}, MinObservation: time.Hour, MaxCollections: 1, Concurrency: 1})
	if indexAuditErr != nil && !errors.Is(indexAuditErr, ErrPartialResult) {
		t.Fatalf("IndexAudit failed: %v", indexAuditErr)
	}
	if indexAudit == nil || len(indexAudit.Collections) != 1 {
		t.Fatalf("IndexAudit returned no selected collection")
	}

	capacity, capacityErr := client.Capacity(ctx, CapacityOptions{Databases: []string{database}, Collections: []string{collection}, MaxCollections: 1, Concurrency: 1})
	if capacityErr != nil && !errors.Is(capacityErr, ErrPartialResult) {
		t.Fatalf("Capacity failed: %v", capacityErr)
	}
	if capacity == nil || len(capacity.Databases) != 1 {
		t.Fatalf("Capacity returned no selected database")
	}

	if expectedCluster == ClusterReplicaSet {
		_, err := client.CollectionStats(ctx, CollectionStatsOptions{
			Databases:             []string{database},
			Collections:           []string{collection},
			RequireShardedCluster: true,
		})
		if !errors.Is(err, ErrNotSharded) {
			t.Fatalf("replica set sharding guard error = %v, want ErrNotSharded", err)
		}
	}

	slowlog, err := client.SlowlogSummary(ctx, SlowlogOptions{
		Sort:        SlowlogSortCount,
		Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("SlowlogSummary failed: %v", err)
	}
	if len(slowlog.ReplicaSets) == 0 {
		t.Fatalf("SlowlogSummary returned no replica sets")
	}
	slowlogDatabase, queryHash, ok := firstLiveSlowlogItem(slowlog)
	if !ok {
		fields, inspectErr := inspectLiveSlowlogFields(ctx, client)
		if inspectErr != nil {
			t.Fatalf("SlowlogSummary returned no detail candidate; inspect profiler fields: %v", inspectErr)
		}
		t.Fatalf(
			"SlowlogSummary returned no detail candidate: profilerDocument=%t queryHash=%t planCacheKey=%t",
			fields.Found,
			fields.QueryHash,
			fields.PlanCacheKey,
		)
	}
	detail, err := client.SlowlogDetail(ctx, slowlogDatabase, queryHash)
	if err != nil {
		t.Fatalf("SlowlogDetail failed: %v", err)
	}
	if detail.Namespace == "" || len(detail.Slowlog) == 0 {
		t.Fatalf("SlowlogDetail returned an empty result")
	}

	t.Logf(
		"live read-only E2E passed: clusterType=%s replicaSets=%d nodes=%d collections=%d doctorStatuses=%d ops=%d hotspotNamespaces=%d indexCollections=%d capacityDatabases=%d slowlogReplicaSets=%d slowlogItems=%d detailIndexes=%d",
		overview.ClusterType,
		len(overview.ReplicaSets),
		liveNodeCount(overview),
		liveCollectionCount(stats),
		len(doctor.CollectorStatuses),
		len(operations.Operations),
		len(hotspot.Namespaces),
		len(indexAudit.Collections),
		len(capacity.Databases),
		len(slowlog.ReplicaSets),
		liveSlowlogItemCount(slowlog),
		len(detail.Indexes),
	)
}

func TestLiveIndexConsistencyReadOnlyE2E(t *testing.T) {
	// 场景：在维护者预置的分片集合上验证真实版本策略、expected shards 与只读 consistency 路径；测试本身不创建索引或数据。
	if ClusterType(requireLiveEnv(t, "MOT_TEST_EXPECT_CLUSTER")) != ClusterSharded {
		t.Skip("index consistency live E2E requires a sharded cluster")
	}
	host := requireLiveEnv(t, "MOT_TEST_MONGO_HOST")
	username := requireLiveEnv(t, "MONGO_USER")
	password := requireLiveEnv(t, "MONGO_PASS")
	database := requireLiveEnv(t, "MOT_TEST_CONSISTENCY_DATABASE")
	collection := requireLiveEnv(t, "MOT_TEST_CONSISTENCY_COLLECTION")
	port, err := strconv.Atoi(requireLiveEnv(t, "MOT_TEST_MONGO_PORT"))
	if err != nil || port <= 0 {
		t.Fatalf("MOT_TEST_MONGO_PORT must be a positive integer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), liveE2ETimeout)
	defer cancel()
	client, err := NewClient(ctx, Options{
		Host: host, Port: port, Username: username, Password: password,
		AuthSource: defaultAuthSource, ConnectTimeout: 15 * time.Second, Direct: boolPointer(false),
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer closeCancel()
		if err := client.Close(closeCtx); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	}()

	version, err := client.conn.ServerVersion(ctx)
	if err != nil {
		t.Fatalf("ServerVersion failed: %v", err)
	}
	wantStrategy, allowed := indexConsistencyStrategyForVersion(version)
	if !allowed {
		t.Fatalf("server version %q is outside the supported 3.4-7.x range", version)
	}
	result, operationErr := client.IndexAudit(ctx, IndexAuditOptions{
		Databases: []string{database}, Collections: []string{collection},
		Checks: []IndexAuditCheck{IndexCheckConsistency}, MaxCollections: 1, Concurrency: 1,
	})
	namespace := database + "." + collection
	if err := validateLiveIndexConsistencyResult(result, operationErr, wantStrategy, namespace); err != nil {
		t.Fatalf("live index consistency gate failed: %v", err)
	}
	item := result.Collections[0]
	t.Logf("live index consistency passed: strategy=%s state=%s coverage=%s expectedShards=%d fallback=%t", item.Strategy, item.State, item.Coverage, len(item.ExpectedShards), item.Fallback != nil)
}

func requireLiveEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Skipf("%s is required for live integration tests", key)
	}
	return value
}

func selectLiveNamespace(t *testing.T, ctx context.Context, client *Client) (string, string) {
	t.Helper()
	dbNames, err := client.conn.Client.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		t.Fatalf("ListDatabaseNames failed: %v", err)
	}
	sort.Strings(dbNames)
	for _, database := range dbNames {
		if strings.HasPrefix(database, "_") || strings.HasPrefix(database, "system") ||
			strings.EqualFold(database, "admin") || strings.EqualFold(database, "config") || strings.EqualFold(database, "local") {
			continue
		}
		collections, err := client.conn.Client.Database(database).ListCollectionNames(ctx, bson.D{})
		if err != nil {
			continue
		}
		sort.Strings(collections)
		for _, collection := range collections {
			if collection != "" && !strings.HasPrefix(collection, "system.") {
				return database, collection
			}
		}
	}
	t.Fatal("no accessible non-system database and collection found")
	return "", ""
}

func liveNamespace(t *testing.T, ctx context.Context, client *Client) (string, string) {
	t.Helper()
	database := strings.TrimSpace(os.Getenv("MOT_TEST_DATABASE"))
	collection := strings.TrimSpace(os.Getenv("MOT_TEST_COLLECTION"))
	if database == "" && collection == "" {
		return selectLiveNamespace(t, ctx, client)
	}
	if database == "" || collection == "" {
		t.Fatal("MOT_TEST_DATABASE and MOT_TEST_COLLECTION must be provided together")
	}
	return database, collection
}

func liveNodeCount(result *OverviewResult) int {
	count := 0
	for _, replicaSet := range result.ReplicaSets {
		count += len(replicaSet.Nodes)
	}
	return count
}

func liveCollectionCount(result *CollectionStatsResult) int {
	count := 0
	for _, database := range result.Databases {
		count += len(database.Collections)
	}
	return count
}

func firstLiveSlowlogItem(result *SlowlogSummaryResult) (string, string, bool) {
	for _, replicaSet := range result.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				for _, item := range database.Items {
					if database.Database != "" && item.QueryHash != "" {
						return database.Database, item.QueryHash, true
					}
				}
			}
		}
	}
	return "", "", false
}

func liveSlowlogItemCount(result *SlowlogSummaryResult) int {
	count := 0
	for _, replicaSet := range result.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				count += len(database.Items)
			}
		}
	}
	return count
}

type liveSlowlogFields struct {
	Found        bool
	QueryHash    bool
	PlanCacheKey bool
}

func inspectLiveSlowlogFields(ctx context.Context, client *Client) (liveSlowlogFields, error) {
	shards, err := client.conn.ListShards(ctx)
	if err != nil {
		return liveSlowlogFields{}, err
	}
	for _, shard := range shards.Shards {
		replicaSet, addresses, err := parseShardHost(shard.Host)
		if err != nil {
			return liveSlowlogFields{}, err
		}
		seed, err := client.connectAddress(ctx, addresses, derivedConnectionOptions{
			ReplicaSet: replicaSet,
			Direct:     boolPointer(false),
		})
		if err != nil {
			return liveSlowlogFields{}, err
		}
		rsStatus, err := seed.RsStatus(ctx)
		if err != nil {
			client.closeDerivedConnection(ctx, seed)
			return liveSlowlogFields{}, err
		}
		dbNames, err := seed.Client.ListDatabaseNames(ctx, bson.D{})
		client.closeDerivedConnection(ctx, seed)
		if err != nil {
			return liveSlowlogFields{}, err
		}

		for _, member := range rsStatus.Members {
			if member.State != pkgmongo.StatePrimary && member.State != pkgmongo.StateSecondary {
				continue
			}
			memberConn, err := client.connectAddress(ctx, member.Name, derivedConnectionOptions{Direct: boolPointer(true)})
			if err != nil {
				return liveSlowlogFields{}, err
			}
			for _, database := range dbNames {
				if database == "admin" || database == "config" || database == "local" {
					continue
				}
				var document bson.M
				err := memberConn.Client.Database(database).Collection("system.profile").FindOne(
					ctx,
					bson.D{},
					options.FindOne().
						SetSort(bson.D{{Key: "ts", Value: -1}}).
						SetProjection(bson.D{
							{Key: "queryHash", Value: 1},
							{Key: "planCacheKey", Value: 1},
						}),
				).Decode(&document)
				if errors.Is(err, drivermongo.ErrNoDocuments) {
					continue
				}
				if err != nil {
					client.closeDerivedConnection(ctx, memberConn)
					return liveSlowlogFields{}, err
				}
				client.closeDerivedConnection(ctx, memberConn)
				_, hasQueryHash := document["queryHash"]
				_, hasPlanCacheKey := document["planCacheKey"]
				return liveSlowlogFields{
					Found:        true,
					QueryHash:    hasQueryHash,
					PlanCacheKey: hasPlanCacheKey,
				}, nil
			}
			client.closeDerivedConnection(ctx, memberConn)
		}
	}
	return liveSlowlogFields{}, nil
}
