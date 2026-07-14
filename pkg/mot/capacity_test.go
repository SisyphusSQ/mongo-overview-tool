package mot

import (
	"testing"
	"time"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

func TestDiffCapacityPreservesUnavailableAndCollectionLifecycle(t *testing.T) {
	// 场景：缺失指标不能按零计算，新增集合也不显示为巨大增长。
	identity := CapacityIdentity{TopologyType: ClusterReplicaSet, Digest: "same"}
	beforeCount, afterCount := int64(1), int64(3)
	before := CapacityResult{SchemaVersion: 1, ClusterIdentity: identity, CollectedAt: time.Unix(10, 0), Databases: []DatabaseCapacity{{Collections: []CollectionCapacity{{Namespace: "db.c", Count: &beforeCount}}}}}
	after := CapacityResult{SchemaVersion: 1, ClusterIdentity: identity, CollectedAt: time.Unix(20, 0), Databases: []DatabaseCapacity{{Collections: []CollectionCapacity{{Namespace: "db.c", Count: &afterCount}, {Namespace: "db.new"}}}}}
	result, err := DiffCapacity(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Collections[0].Count.Delta == nil || *result.Collections[0].Count.Delta != 2 {
		t.Fatalf("existing delta = %#v", result.Collections[0])
	}
	if result.Collections[0].Data.Delta != nil {
		t.Fatalf("missing data delta = %#v, want unavailable", result.Collections[0].Data)
	}
	if result.Collections[1].State != "added" || result.Collections[1].Count.Delta != nil {
		t.Fatalf("added collection = %#v", result.Collections[1])
	}
}

func TestDiffCapacityRejectsDifferentCluster(t *testing.T) {
	// 场景：不同集群 identity 必须拒绝比较，不能生成误导增长率。
	before := CapacityResult{SchemaVersion: 1, ClusterIdentity: CapacityIdentity{TopologyType: ClusterReplicaSet, Digest: "a"}, CollectedAt: time.Unix(10, 0)}
	after := CapacityResult{SchemaVersion: 1, ClusterIdentity: CapacityIdentity{TopologyType: ClusterReplicaSet, Digest: "b"}, CollectedAt: time.Unix(20, 0)}
	if _, err := DiffCapacity(before, after); err == nil {
		t.Fatal("DiffCapacity() error = nil, want different cluster error")
	}
}

func TestCollectionCapacityDoesNotProduceInfiniteRatio(t *testing.T) {
	// 场景：逻辑数据非零但 allocated storage 为零时，不能产生 Infinity/NaN 破坏 JSON。
	data, storage := int64(100), int64(0)
	result := collectionCapacityFromMongo(pkgmongo.CollectionCapacitySnapshot{Namespace: "db.c", DataSizeBytes: &data, StorageSizeBytes: &storage})
	if result.CompressionRatio != nil {
		t.Fatalf("compression ratio = %#v, want unavailable", result.CompressionRatio)
	}
}

func TestCollectionCapacityPreservesShardDetails(t *testing.T) {
	// 场景：分片集合必须保留 shard/host 明细，同时维持安全的 aggregate 字段。
	count := int64(2)
	result := collectionCapacityFromMongo(pkgmongo.CollectionCapacitySnapshot{Namespace: "db.c", Sharded: true, Shards: []pkgmongo.CollectionCapacityShardSnapshot{{Shard: "s1", Host: "n1", Count: &count}}})
	if len(result.Shards) != 1 || result.Shards[0].Shard != "s1" || result.Shards[0].Host != "n1" {
		t.Fatalf("shards = %#v", result.Shards)
	}
}

func TestDiffCapacityIncludesDatabaseAndDailyDelta(t *testing.T) {
	// 场景：离线 diff 同时输出 database delta 和基于真实窗口的平均日增量。
	identity := CapacityIdentity{TopologyType: ClusterReplicaSet, Digest: "same"}
	beforeData, afterData := int64(100), int64(160)
	before := CapacityResult{SchemaVersion: 1, ClusterIdentity: identity, CollectedAt: time.Unix(0, 0), Databases: []DatabaseCapacity{{Name: "db", DataSizeBytes: &beforeData}}}
	after := CapacityResult{SchemaVersion: 1, ClusterIdentity: identity, CollectedAt: time.Unix(48*3600, 0), Databases: []DatabaseCapacity{{Name: "db", DataSizeBytes: &afterData}}}
	result, err := DiffCapacity(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Databases) != 1 || result.Databases[0].Data.Delta == nil || *result.Databases[0].Data.Delta != 60 || result.Databases[0].Data.AveragePerDay == nil || *result.Databases[0].Data.AveragePerDay != 30 {
		t.Fatalf("database diff = %#v", result.Databases)
	}
}
