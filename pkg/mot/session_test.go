package mot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

func TestNewCollectorSessionDefaultsAndValidation(t *testing.T) {
	// 测试 CollectorSession 使用安全默认并发，并拒绝负数并发配置。
	client := newDisconnectedTestClient(t)

	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	if session.maxConcurrency != defaultCollectorSessionConcurrency {
		t.Fatalf("max concurrency = %d, want %d", session.maxConcurrency, defaultCollectorSessionConcurrency)
	}

	if _, err := client.NewCollectorSession(CollectorSessionOptions{MaxConcurrency: -1}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("negative concurrency error = %v, want ErrInvalidOptions", err)
	}
}

func TestCollectorSessionCloseIsIdempotentAndRejectsNewCalls(t *testing.T) {
	// 测试 CollectorSession 关闭后拒绝新能力调用，重复关闭保持幂等。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	tests := []struct {
		name string
		call func() error
	}{
		{name: "overview", call: func() error { _, err := session.Overview(context.Background(), OverviewOptions{}); return err }},
		{name: "collection stats", call: func() error {
			_, err := session.CollectionStats(context.Background(), CollectionStatsOptions{})
			return err
		}},
		{name: "doctor", call: func() error { _, err := session.Doctor(context.Background(), DoctorOptions{}); return err }},
		{name: "current operations", call: func() error {
			_, err := session.CurrentOperations(context.Background(), CurrentOperationsOptions{})
			return err
		}},
		{name: "hotspot", call: func() error { _, err := session.Hotspot(context.Background(), HotspotOptions{}); return err }},
		{name: "index audit", call: func() error { _, err := session.IndexAudit(context.Background(), IndexAuditOptions{}); return err }},
		{name: "capacity", call: func() error { _, err := session.Capacity(context.Background(), CapacityOptions{}); return err }},
		{name: "slowlog summary", call: func() error { _, err := session.SlowlogSummary(context.Background(), SlowlogOptions{}); return err }},
		{name: "slowlog detail", call: func() error { _, err := session.SlowlogDetail(context.Background(), "db", "hash"); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrCollectorSessionClosed) {
				t.Fatalf("call error = %v, want ErrCollectorSessionClosed", err)
			}
		})
	}
}

func TestCollectorSessionStatsReturnsIndependentSnapshot(t *testing.T) {
	// 测试 Stats 返回独立 map，调用方修改快照不会污染 session 内部统计。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	session.recordCapability("overview", 10*time.Millisecond, nil)

	first := session.Stats()
	first.Capabilities["overview"] = CollectorCapabilityStats{}
	second := session.Stats()
	if second.Capabilities["overview"].Calls != 1 {
		t.Fatalf("capability stats mutated through snapshot: %#v", second.Capabilities["overview"])
	}
}

func TestCollectorSessionRemoteLimitIsShared(t *testing.T) {
	// 测试多个并发调用共享同一个 session 远程操作上限。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{MaxConcurrency: 2})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}

	var current atomic.Int64
	var maximum atomic.Int64
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			release, acquireErr := session.acquireRemoteSlot(context.Background())
			if acquireErr != nil {
				t.Errorf("acquireRemoteSlot failed: %v", acquireErr)
				return
			}
			running := current.Add(1)
			updateMaximum(&maximum, running)
			time.Sleep(10 * time.Millisecond)
			current.Add(-1)
			release()
		}()
	}
	group.Wait()

	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	stats := session.Stats()
	if stats.RemoteOperations != 8 || stats.PeakRemoteOperations != 2 {
		t.Fatalf("unexpected remote stats: %#v", stats)
	}
}

func TestCollectorSessionClientUsesSharedRemoteLimit(t *testing.T) {
	// 测试 session 内部 Client 的 collector 任务接入同一个全局并发限制。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	firstRelease, err := session.client.acquireRemoteSlot(context.Background())
	if err != nil {
		t.Fatalf("first acquireRemoteSlot failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := session.client.acquireRemoteSlot(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("second acquireRemoteSlot error = %v, want ErrCancelled", err)
	}
	firstRelease()
}

func TestCollectorSessionConnectionWaitHonorsContextCancellation(t *testing.T) {
	// 测试等待同键 singleflight 建连时，调用方 ctx 取消能够及时返回且不污染成功缓存。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{delay: 50 * time.Millisecond}
	session.connectionFactory = factory

	firstDone := make(chan error, 1)
	go func() {
		_, connectErr := session.derivedConnection(context.Background(), "node-1:27017", derivedConnectionOptions{Direct: boolPointer(true)})
		firstDone <- connectErr
	}()
	time.Sleep(5 * time.Millisecond)
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := session.derivedConnection(waitCtx, "node-1:27017", derivedConnectionOptions{Direct: boolPointer(true)}); !errors.Is(err, ErrCancelled) {
		t.Fatalf("waiting connection error = %v, want ErrCancelled", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first connection failed: %v", err)
	}
	if session.Stats().DerivedConnectionsOpened != 1 {
		t.Fatalf("unexpected connection stats: %#v", session.Stats())
	}
}

func TestCollectorSessionStatsSupportsConcurrentReads(t *testing.T) {
	// 测试 capability 更新期间并发读取 Stats 不产生竞态，并始终返回独立 map。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(2)
		go func() {
			defer group.Done()
			session.recordCapability("overview", time.Millisecond, nil)
		}()
		go func() {
			defer group.Done()
			stats := session.Stats()
			stats.Capabilities["mutated"] = CollectorCapabilityStats{}
		}()
	}
	group.Wait()
	stats := session.Stats()
	if stats.Capabilities["overview"].Calls != 8 {
		t.Fatalf("overview calls = %d, want 8", stats.Capabilities["overview"].Calls)
	}
	if _, exists := stats.Capabilities["mutated"]; exists {
		t.Fatal("Stats map mutation leaked into session")
	}
}

func TestCollectorSessionCachesConcurrentDerivedConnectionsWithoutDatabaseKey(t *testing.T) {
	// 测试同一节点的不同数据库并发请求复用一个派生连接。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{}
	session.connectionFactory = factory

	connections := make([]*pkgmongo.Conn, 8)
	var group sync.WaitGroup
	for index := range connections {
		group.Add(1)
		go func() {
			defer group.Done()
			conn, connectErr := session.derivedConnection(context.Background(), "node-1:27017", derivedConnectionOptions{
				Database: fmt.Sprintf("db-%d", index),
				Direct:   boolPointer(true),
			})
			if connectErr != nil {
				t.Errorf("derivedConnection failed: %v", connectErr)
				return
			}
			connections[index] = conn
		}()
	}
	group.Wait()

	if factory.connectCount() != 1 {
		t.Fatalf("connect count = %d, want 1", factory.connectCount())
	}
	for _, conn := range connections[1:] {
		if conn != connections[0] {
			t.Fatalf("connection was not reused: first=%p current=%p", connections[0], conn)
		}
	}
	stats := session.Stats()
	if stats.DerivedConnectionsOpened != 1 || stats.DerivedConnectionCacheHits != 7 {
		t.Fatalf("unexpected connection stats: %#v", stats)
	}
}

func TestCollectorSessionDoesNotCacheFailedConnection(t *testing.T) {
	// 测试首次建连失败不会污染缓存，后续请求能够重新建连。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{failures: 1}
	session.connectionFactory = factory

	if _, err := session.derivedConnection(context.Background(), "node-1:27017", derivedConnectionOptions{Direct: boolPointer(true)}); err == nil {
		t.Fatal("first derivedConnection unexpectedly succeeded")
	}
	if _, err := session.derivedConnection(context.Background(), "node-1:27017", derivedConnectionOptions{Direct: boolPointer(true)}); err != nil {
		t.Fatalf("second derivedConnection failed: %v", err)
	}
	if factory.connectCount() != 2 {
		t.Fatalf("connect count = %d, want 2", factory.connectCount())
	}
	if session.Stats().DerivedConnectionFailures != 1 {
		t.Fatalf("failure stats = %#v", session.Stats())
	}
}

func TestCollectorSessionDoesNotCountSharedConnectionFailureAsCacheHit(t *testing.T) {
	// 测试并发调用共享同一次建连失败时，等待者不能被统计为缓存命中。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{failures: 1, delay: 30 * time.Millisecond}
	session.connectionFactory = factory

	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = session.derivedConnection(context.Background(), "node-1:27017", derivedConnectionOptions{Direct: boolPointer(true)})
		}()
	}
	group.Wait()
	stats := session.Stats()
	if stats.DerivedConnectionsOpened != 0 || stats.DerivedConnectionFailures != 1 || stats.DerivedConnectionCacheHits != 0 {
		t.Fatalf("unexpected failed connection stats: %#v", stats)
	}
}

func TestCollectorSessionCloseClosesCachedConnectionsOnce(t *testing.T) {
	// 测试 Close 只关闭 session 缓存的派生连接，并且重复调用不会重复断开。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{}
	session.connectionFactory = factory

	if _, err := session.derivedConnection(context.Background(), "node-1:27017", derivedConnectionOptions{Direct: boolPointer(true)}); err != nil {
		t.Fatalf("first derivedConnection failed: %v", err)
	}
	if _, err := session.derivedConnection(context.Background(), "node-2:27017", derivedConnectionOptions{Direct: boolPointer(true)}); err != nil {
		t.Fatalf("second derivedConnection failed: %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if factory.closeCount() != 2 {
		t.Fatalf("close count = %d, want 2", factory.closeCount())
	}
}

func TestCollectorSessionClientRoutesDerivedConnectionsThroughSession(t *testing.T) {
	// 测试 session 内部 Client 自动复用连接，能力内部 cleanup 不会提前断开缓存连接。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{}
	session.connectionFactory = factory

	first, err := session.client.connectAddress(context.Background(), "node-1:27017", derivedConnectionOptions{
		Database: "db-1",
		Direct:   boolPointer(true),
	})
	if err != nil {
		t.Fatalf("first connectAddress failed: %v", err)
	}
	second, err := session.client.connectAddress(context.Background(), "node-1:27017", derivedConnectionOptions{
		Database: "db-2",
		Direct:   boolPointer(true),
	})
	if err != nil {
		t.Fatalf("second connectAddress failed: %v", err)
	}
	if first != second || factory.connectCount() != 1 {
		t.Fatalf("session client did not reuse connection: first=%p second=%p connects=%d", first, second, factory.connectCount())
	}

	session.client.closeDerivedConnection(context.Background(), first)
	if factory.closeCount() != 0 {
		t.Fatalf("capability cleanup closed cached connection: closes=%d", factory.closeCount())
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if factory.closeCount() != 1 {
		t.Fatalf("session close count = %d, want 1", factory.closeCount())
	}
}

func TestCollectorSessionCachesTopologyAndShardInventoryAcrossConcurrentCalls(t *testing.T) {
	// 测试并发能力共享拓扑和 shard 清单，调用方修改结果不会污染 session 缓存。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	source := &recordingDiscoverySource{delay: 10 * time.Millisecond}
	session.discoverySource = source

	results := make([]*pkgmongo.ClusterInfo, 8)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			cluster, detectErr := session.client.detectCluster(context.Background())
			if detectErr != nil {
				t.Errorf("detectCluster failed: %v", detectErr)
				return
			}
			results[index] = cluster
		}()
	}
	group.Wait()

	if topologyLoads, shardLoads := source.counts(); topologyLoads != 1 || shardLoads != 1 {
		t.Fatalf("discovery loads = topology:%d shards:%d, want 1/1", topologyLoads, shardLoads)
	}
	if len(results[0].Repl) != 2 {
		t.Fatalf("replica set count = %d, want 2", len(results[0].Repl))
	}
	delete(results[0].Repl, "shard-1")
	again, err := session.client.detectCluster(context.Background())
	if err != nil {
		t.Fatalf("second detectCluster failed: %v", err)
	}
	if len(again.Repl) != 2 {
		t.Fatalf("cached topology was mutated: %#v", again.Repl)
	}
	stats := session.Stats()
	if stats.TopologyLoads != 1 || stats.ShardInventoryLoads != 1 {
		t.Fatalf("unexpected discovery stats: %#v", stats)
	}
}

func TestCollectorSessionSlowlogDetailUsesSummaryLocation(t *testing.T) {
	// 测试 SlowlogSummary 已建立定位索引时，Detail 直接访问目标节点而不重新扫描集群。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	factory := &recordingDerivedConnectionFactory{}
	session.connectionFactory = factory
	session.recordSlowlogLocations(&SlowlogSummaryResult{ReplicaSets: []ReplicaSetSlowlogSummary{
		{Name: "rs1", Hosts: []HostSlowlogSummary{
			{Address: "node-2:27017", Databases: []DatabaseSlowlogSummary{
				{Database: "orders", Items: []SlowlogSummaryItem{{QueryHash: "hash-1"}}},
			}},
		}},
	}})

	var loadedAddress string
	session.slowlogDetailLoader = func(_ context.Context, conn *pkgmongo.Conn, db, queryHash string) (*SlowlogDetailResult, error) {
		loadedAddress = conn.URI
		if db != "orders" || queryHash != "hash-1" {
			t.Fatalf("unexpected detail lookup: db=%s queryHash=%s", db, queryHash)
		}
		return &SlowlogDetailResult{Namespace: "orders.items"}, nil
	}

	result, err := session.SlowlogDetail(context.Background(), "orders", "hash-1")
	if err != nil {
		t.Fatalf("SlowlogDetail failed: %v", err)
	}
	if result.Namespace != "orders.items" || loadedAddress != "node-2:27017" {
		t.Fatalf("unexpected detail result=%#v address=%s", result, loadedAddress)
	}
	if factory.connectCount() != 1 {
		t.Fatalf("connect count = %d, want 1", factory.connectCount())
	}
}

func TestWithEphemeralCollectorSessionPreservesLegacyConcurrencyAndCloses(t *testing.T) {
	// 测试旧 Client API 使用临时 session，执行完成后关闭且不增加额外全局并发限制。
	client := newDisconnectedTestClient(t)
	var captured *CollectorSession
	result, err := withEphemeralCollectorSession(context.Background(), client, func(session *CollectorSession) (string, error) {
		captured = session
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("withEphemeralCollectorSession failed: %v", err)
	}
	if result != "ok" {
		t.Fatalf("result = %q, want ok", result)
	}
	if captured == nil || captured.maxConcurrency != legacyCollectorSessionConcurrency || !captured.legacy {
		t.Fatalf("unexpected ephemeral session: %#v", captured)
	}
	if err := captured.requireOpen(); !errors.Is(err, ErrCollectorSessionClosed) {
		t.Fatalf("ephemeral session error = %v, want ErrCollectorSessionClosed", err)
	}
}

func TestCollectorSessionCachesBaseCatalogAcrossCapabilities(t *testing.T) {
	// 测试基础数据库与集合目录在并发 capability 间只加载一次，并返回独立副本。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	source := &recordingCatalogSource{delay: 10 * time.Millisecond}
	session.catalogSource = source

	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			databases, loadErr := session.client.databaseNames(context.Background())
			if loadErr != nil || fmt.Sprint(databases) != "[db-1 db-2]" {
				t.Errorf("databaseNames = %v, err=%v", databases, loadErr)
			}
			metadata, loadErr := session.client.collectionMetadata(context.Background(), "db-1")
			if loadErr != nil || len(metadata) != 2 {
				t.Errorf("collectionMetadata = %v, err=%v", metadata, loadErr)
			}
		}()
	}
	group.Wait()

	if databaseLoads, collectionLoads := source.counts(); databaseLoads != 1 || collectionLoads != 1 {
		t.Fatalf("catalog loads = databases:%d collections:%d, want 1/1", databaseLoads, collectionLoads)
	}
	databases, err := session.client.databaseNames(context.Background())
	if err != nil {
		t.Fatalf("databaseNames failed: %v", err)
	}
	databases[0] = "mutated"
	again, err := session.client.databaseNames(context.Background())
	if err != nil || again[0] != "db-1" {
		t.Fatalf("cached databases were mutated: %v, err=%v", again, err)
	}
	stats := session.Stats()
	if stats.DatabaseInventoryLoads != 1 || stats.CollectionInventoryLoads != 1 {
		t.Fatalf("unexpected catalog stats: %#v", stats)
	}
}

func TestCollectorSessionCachesSlowlogDatabasesPerReplicaSet(t *testing.T) {
	// 测试 Slowlog 数据库目录按 replica set 隔离缓存，同一 replica set 的并发调用只加载一次。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	source := &recordingCatalogSource{delay: 10 * time.Millisecond}
	session.catalogSource = source

	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			databases, loadErr := session.client.replicaSetDatabaseNames(context.Background(), session.client.conn, "rs-1")
			if loadErr != nil || fmt.Sprint(databases) != "[db-1 db-2]" {
				t.Errorf("replicaSetDatabaseNames = %v, err=%v", databases, loadErr)
			}
		}()
	}
	group.Wait()
	if _, err := session.client.replicaSetDatabaseNames(context.Background(), session.client.conn, "rs-2"); err != nil {
		t.Fatalf("second replica set database load failed: %v", err)
	}

	if databaseLoads, _ := source.counts(); databaseLoads != 2 {
		t.Fatalf("database loads = %d, want 2", databaseLoads)
	}
	if session.Stats().DatabaseInventoryLoads != 2 {
		t.Fatalf("unexpected database inventory stats: %#v", session.Stats())
	}
}

func TestCollectorSessionCachesReplicaSetInventory(t *testing.T) {
	// 测试发现类能力共享 replica set 成员清单，但每个调用方获得独立副本。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	source := &recordingReplicaSetSource{delay: 10 * time.Millisecond}
	session.replicaSetSource = source

	results := make([]replicaSetInventory, 8)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			inventory, loadErr := session.client.replicaSetInventory(context.Background(), session.client.conn, "rs1")
			if loadErr != nil {
				t.Errorf("replicaSetInventory failed: %v", loadErr)
				return
			}
			results[index] = inventory
		}()
	}
	group.Wait()

	if source.count() != 1 {
		t.Fatalf("replica set loads = %d, want 1", source.count())
	}
	results[0].Members[0].Name = "mutated"
	again, err := session.client.replicaSetInventory(context.Background(), session.client.conn, "rs1")
	if err != nil || again.Members[0].Name != "node-1:27017" {
		t.Fatalf("cached replica set inventory was mutated: %#v, err=%v", again, err)
	}
	if session.Stats().ReplicaSetInventoryLoads != 1 {
		t.Fatalf("unexpected replica set stats: %#v", session.Stats())
	}
}

func TestCollectorSessionReusesMembersExtractedFromFreshReplicaStatus(t *testing.T) {
	// 测试 Overview/Doctor 的实时 replica status 只提取成员清单，后续 capability 不再重复发现成员。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	source := &recordingReplicaSetSource{}
	session.replicaSetSource = source
	session.rememberReplicaSetInventory("rs1", pkgmongo.RsStatus{Set: "rs1", Members: []pkgmongo.RsMember{{Name: "node-1:27017"}}})

	inventory, err := session.client.replicaSetInventory(context.Background(), session.client.conn, "rs1")
	if err != nil {
		t.Fatalf("replicaSetInventory failed: %v", err)
	}
	if source.count() != 0 || len(inventory.Members) != 1 || inventory.Members[0].Name != "node-1:27017" {
		t.Fatalf("fresh members were not reused: inventory=%#v loads=%d", inventory, source.count())
	}
}

type recordingReplicaSetSource struct {
	mu    sync.Mutex
	loads int
	delay time.Duration
}

func (s *recordingReplicaSetSource) ReplicaSetStatus(context.Context, *pkgmongo.Conn) (pkgmongo.RsStatus, error) {
	s.mu.Lock()
	s.loads++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return pkgmongo.RsStatus{Set: "rs1", Members: []pkgmongo.RsMember{
		{Name: "node-1:27017", State: pkgmongo.StatePrimary, Health: 1},
		{Name: "node-2:27017", State: pkgmongo.StateSecondary, Health: 1},
	}}, nil
}

func (s *recordingReplicaSetSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

type recordingCatalogSource struct {
	mu              sync.Mutex
	databaseLoads   int
	collectionLoads int
	delay           time.Duration
}

func (s *recordingCatalogSource) ListDatabaseNames(context.Context, *pkgmongo.Conn) ([]string, error) {
	s.mu.Lock()
	s.databaseLoads++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return []string{"db-1", "db-2"}, nil
}

func (s *recordingCatalogSource) ListCollections(context.Context, *pkgmongo.Conn, string) ([]indexCollectionMetadata, error) {
	s.mu.Lock()
	s.collectionLoads++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return []indexCollectionMetadata{{Name: "orders", Type: "collection"}, {Name: "orders_view", Type: "view"}}, nil
}

func (s *recordingCatalogSource) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.databaseLoads, s.collectionLoads
}

type recordingDiscoverySource struct {
	mu            sync.Mutex
	topologyLoads int
	shardLoads    int
	delay         time.Duration
}

func (s *recordingDiscoverySource) DetectClusterTopology(context.Context, *pkgmongo.Conn) (*pkgmongo.ClusterInfo, error) {
	s.mu.Lock()
	s.topologyLoads++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return &pkgmongo.ClusterInfo{Type: pkgmongo.ClusterShard, Repl: make(map[string]string), MaxWireVersion: 21}, nil
}

func (s *recordingDiscoverySource) ListShards(context.Context, *pkgmongo.Conn) (pkgmongo.ShStatus, error) {
	s.mu.Lock()
	s.shardLoads++
	s.mu.Unlock()
	time.Sleep(s.delay)
	return pkgmongo.ShStatus{Shards: []pkgmongo.Shard{
		{Id: "shard-1", Host: "rs1/node-1:27017,node-2:27017"},
		{Id: "shard-2", Host: "rs2/node-3:27017,node-4:27017"},
	}}, nil
}

func (s *recordingDiscoverySource) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topologyLoads, s.shardLoads
}

type recordingDerivedConnectionFactory struct {
	mu       sync.Mutex
	connects int
	closes   int
	failures int
	delay    time.Duration
}

func (f *recordingDerivedConnectionFactory) Connect(_ context.Context, addr string, _ derivedConnectionOptions) (*pkgmongo.Conn, error) {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	if f.failures > 0 {
		f.failures--
		return nil, errors.New("connect failed")
	}
	return &pkgmongo.Conn{URI: addr}, nil
}

func (f *recordingDerivedConnectionFactory) Close(context.Context, *pkgmongo.Conn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}

func (f *recordingDerivedConnectionFactory) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

func (f *recordingDerivedConnectionFactory) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func newDisconnectedTestClient(t testing.TB) *Client {
	t.Helper()
	driverClient, err := drivermongo.NewClient(options.Client().ApplyURI("mongodb://127.0.0.1:27017/admin"))
	if err != nil {
		t.Fatalf("mongo.NewClient failed: %v", err)
	}
	client, err := NewClientFromMongoClient(context.Background(), driverClient, ClientOptions{
		URI: "mongodb://127.0.0.1:27017/admin",
	})
	if err != nil {
		t.Fatalf("NewClientFromMongoClient failed: %v", err)
	}
	return client
}
