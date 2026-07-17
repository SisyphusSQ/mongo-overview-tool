package mot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const (
	defaultCollectorSessionConcurrency = 4
	legacyCollectorSessionConcurrency  = 1 << 30
)

// CollectorSessionOptions 控制单次请求内共享 collector 的并发边界。
type CollectorSessionOptions struct {
	MaxConcurrency int
}

// CollectorCapabilityStats 汇总单个能力在 session 内的调用结果和耗时。
type CollectorCapabilityStats struct {
	Calls          int64
	Successes      int64
	PartialResults int64
	Failures       int64
	TotalDuration  time.Duration
}

// CollectorSessionStats 汇总请求级发现、连接和能力执行统计。
type CollectorSessionStats struct {
	TopologyLoads              int64
	ShardInventoryLoads        int64
	DatabaseInventoryLoads     int64
	CollectionInventoryLoads   int64
	ReplicaSetInventoryLoads   int64
	DerivedConnectionsOpened   int64
	DerivedConnectionCacheHits int64
	DerivedConnectionFailures  int64
	RemoteOperations           int64
	PeakRemoteOperations       int64
	Capabilities               map[string]CollectorCapabilityStats
}

// CollectorSession 在一次上层请求内共享只读 collector 资源。
type CollectorSession struct {
	client            *Client
	maxConcurrency    int
	remoteLimit       *semaphore.Weighted
	connectionFactory derivedConnectionFactory
	connectionGroup   singleflight.Group
	discoverySource   collectorDiscoverySource
	discoveryGroup    singleflight.Group
	catalogSource     collectorCatalogSource
	catalogGroup      singleflight.Group
	replicaSetSource  collectorReplicaSetSource
	replicaSetGroup   singleflight.Group

	mu                      sync.RWMutex
	closed                  bool
	legacy                  bool
	closeOnce               sync.Once
	closeErr                error
	connections             map[derivedConnectionKey]*pkgmongo.Conn
	slowlogLocations        map[slowlogLocationKey]string
	slowlogDetailLoader     slowlogDetailLoader
	topology                *pkgmongo.ClusterInfo
	shardInventory          *pkgmongo.ShStatus
	databaseInventory       []string
	replicaSetDatabaseNames map[string][]string
	collectionInventory     map[string][]indexCollectionMetadata
	replicaSetInventories   map[string]replicaSetInventory
	remoteInFlight          int64
	stats                   CollectorSessionStats
}

type derivedConnectionFactory interface {
	Connect(ctx context.Context, addr string, target derivedConnectionOptions) (*pkgmongo.Conn, error)
	Close(ctx context.Context, conn *pkgmongo.Conn) error
}

type clientDerivedConnectionFactory struct {
	client *Client
}

type derivedConnectionKey struct {
	address       string
	replicaSet    string
	directSet     bool
	directEnabled bool
}

type derivedConnectionLoad struct {
	conn      *pkgmongo.Conn
	opened    bool
	delivered atomic.Bool
}

// NewCollectorSession 创建一个不拥有基础 Client 的请求级采集会话。
func (c *Client) NewCollectorSession(opts CollectorSessionOptions) (*CollectorSession, error) {
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	if opts.MaxConcurrency < 0 {
		return nil, invalidOptions("max concurrency must not be negative")
	}
	if opts.MaxConcurrency == 0 {
		opts.MaxConcurrency = defaultCollectorSessionConcurrency
	}
	return c.newCollectorSession(opts.MaxConcurrency), nil
}

func (c *Client) newCollectorSession(maxConcurrency int) *CollectorSession {
	sessionClient := *c
	session := &CollectorSession{
		client:                  &sessionClient,
		maxConcurrency:          maxConcurrency,
		remoteLimit:             semaphore.NewWeighted(int64(maxConcurrency)),
		connectionFactory:       clientDerivedConnectionFactory{client: c},
		discoverySource:         mongoCollectorDiscoverySource{},
		catalogSource:           mongoCollectorCatalogSource{},
		replicaSetSource:        mongoCollectorReplicaSetSource{},
		connections:             make(map[derivedConnectionKey]*pkgmongo.Conn),
		collectionInventory:     make(map[string][]indexCollectionMetadata),
		replicaSetDatabaseNames: make(map[string][]string),
		replicaSetInventories:   make(map[string]replicaSetInventory),
		slowlogLocations:        make(map[slowlogLocationKey]string),
		slowlogDetailLoader:     slowlogDetailFromConnection,
		stats: CollectorSessionStats{
			Capabilities: make(map[string]CollectorCapabilityStats),
		},
	}
	sessionClient.session = session
	return session
}

func withEphemeralCollectorSession[T any](ctx context.Context, client *Client, run func(*CollectorSession) (T, error)) (T, error) {
	var zero T
	if client == nil {
		return zero, invalidOptions("client is not initialized")
	}
	if run == nil {
		return zero, invalidOptions("collector session operation is required")
	}
	session := client.newCollectorSession(legacyCollectorSessionConcurrency)
	session.legacy = true
	defer func() {
		closeCtx, cancel := cleanupContext(ctx)
		defer cancel()
		if err := session.Close(closeCtx); err != nil {
			client.logger.Warnf("failed to close ephemeral collector session; detail suppressed")
		}
	}()
	return run(session)
}

// Close 幂等关闭 session 创建的全部派生连接，不关闭基础 Client。
func (s *CollectorSession) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		keys := make([]derivedConnectionKey, 0, len(s.connections))
		for key := range s.connections {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].stringValue() < keys[j].stringValue() })
		connections := make([]*pkgmongo.Conn, 0, len(keys))
		for _, key := range keys {
			connections = append(connections, s.connections[key])
		}
		s.connections = make(map[derivedConnectionKey]*pkgmongo.Conn)
		s.mu.Unlock()

		closeErrors := make([]error, 0)
		for _, conn := range connections {
			if err := s.connectionFactory.Close(ctx, conn); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		s.closeErr = mapContextError(errors.Join(closeErrors...))
	})
	return s.closeErr
}

// Stats 返回不会暴露内部可变 map 的统计快照。
func (s *CollectorSession) Stats() CollectorSessionStats {
	if s == nil {
		return CollectorSessionStats{Capabilities: make(map[string]CollectorCapabilityStats)}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := s.stats
	result.Capabilities = make(map[string]CollectorCapabilityStats, len(s.stats.Capabilities))
	for name, stats := range s.stats.Capabilities {
		result.Capabilities[name] = stats
	}
	return result
}

func (s *CollectorSession) derivedConnection(ctx context.Context, addr string, target derivedConnectionOptions) (*pkgmongo.Conn, error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := newDerivedConnectionKey(addr, target)
	s.mu.Lock()
	if conn := s.connections[key]; conn != nil {
		s.stats.DerivedConnectionCacheHits++
		s.mu.Unlock()
		return conn, nil
	}
	s.mu.Unlock()

	resultChannel := s.connectionGroup.DoChan(key.stringValue(), func() (any, error) {
		s.mu.RLock()
		conn := s.connections[key]
		s.mu.RUnlock()
		if conn != nil {
			return &derivedConnectionLoad{conn: conn}, nil
		}

		created, err := s.connectionFactory.Connect(ctx, addr, target)
		if err != nil {
			s.mu.Lock()
			s.stats.DerivedConnectionFailures++
			s.mu.Unlock()
			return nil, mapContextError(err)
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			closeCtx, cancel := cleanupContext(ctx)
			defer cancel()
			_ = s.connectionFactory.Close(closeCtx, created)
			return nil, ErrCollectorSessionClosed
		}
		s.connections[key] = created
		s.stats.DerivedConnectionsOpened++
		s.mu.Unlock()
		return &derivedConnectionLoad{conn: created, opened: true}, nil
	})

	select {
	case <-ctx.Done():
		return nil, mapContextError(ctx.Err())
	case result := <-resultChannel:
		if result.Err != nil {
			return nil, result.Err
		}
		loaded, ok := result.Val.(*derivedConnectionLoad)
		if !ok || loaded == nil || loaded.conn == nil {
			return nil, errors.New("derived connection cache returned an invalid connection")
		}
		cacheHit := !loaded.opened || !loaded.delivered.CompareAndSwap(false, true)
		if cacheHit {
			s.mu.Lock()
			s.stats.DerivedConnectionCacheHits++
			s.mu.Unlock()
		}
		return loaded.conn, nil
	}
}

func newDerivedConnectionKey(addr string, target derivedConnectionOptions) derivedConnectionKey {
	key := derivedConnectionKey{address: addr, replicaSet: target.ReplicaSet}
	if target.Direct != nil {
		key.directSet = true
		key.directEnabled = *target.Direct
	}
	return key
}

func (k derivedConnectionKey) stringValue() string {
	return fmt.Sprintf("%s\x00%s\x00%t\x00%t", k.address, k.replicaSet, k.directSet, k.directEnabled)
}

func (f clientDerivedConnectionFactory) Connect(ctx context.Context, addr string, target derivedConnectionOptions) (*pkgmongo.Conn, error) {
	return f.client.connectAddress(ctx, addr, target)
}

func (f clientDerivedConnectionFactory) Close(ctx context.Context, conn *pkgmongo.Conn) error {
	if conn == nil {
		return nil
	}
	return conn.CloseWithContext(ctx)
}

// Overview 在当前 session 内执行集群概览。
func (s *CollectorSession) Overview(ctx context.Context, opts OverviewOptions) (result *OverviewResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("overview", time.Since(startedAt), err) }()
	return s.client.Overview(ctx, opts)
}

// CollectionStats 在当前 session 内执行集合统计。
func (s *CollectorSession) CollectionStats(ctx context.Context, opts CollectionStatsOptions) (result *CollectionStatsResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("collection_stats", time.Since(startedAt), err) }()
	return s.client.CollectionStats(ctx, opts)
}

// Doctor 在当前 session 内执行健康巡检。
func (s *CollectorSession) Doctor(ctx context.Context, opts DoctorOptions) (result *DoctorResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("doctor", time.Since(startedAt), err) }()
	return s.client.Doctor(ctx, opts)
}

// CurrentOperations 在当前 session 内采集活跃操作。
func (s *CollectorSession) CurrentOperations(ctx context.Context, opts CurrentOperationsOptions) (result *CurrentOperationsResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("current_operations", time.Since(startedAt), err) }()
	return s.client.CurrentOperations(ctx, opts)
}

// Hotspot 在当前 session 内执行热点采样。
func (s *CollectorSession) Hotspot(ctx context.Context, opts HotspotOptions) (result *HotspotResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("hotspot", time.Since(startedAt), err) }()
	return s.client.Hotspot(ctx, opts)
}

// IndexAudit 在当前 session 内执行索引审计。
func (s *CollectorSession) IndexAudit(ctx context.Context, opts IndexAuditOptions) (result *IndexAuditResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("index_audit", time.Since(startedAt), err) }()
	return s.client.IndexAudit(ctx, opts)
}

// Capacity 在当前 session 内采集容量快照。
func (s *CollectorSession) Capacity(ctx context.Context, opts CapacityOptions) (result *CapacityResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("capacity", time.Since(startedAt), err) }()
	return s.client.Capacity(ctx, opts)
}

// SlowlogSummary 在当前 session 内聚合慢日志。
func (s *CollectorSession) SlowlogSummary(ctx context.Context, opts SlowlogOptions) (result *SlowlogSummaryResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("slowlog_summary", time.Since(startedAt), err) }()
	result, err = s.client.SlowlogSummary(ctx, opts)
	if result != nil {
		s.recordSlowlogLocations(result)
	}
	return result, err
}

// SlowlogDetail 在当前 session 内查询单条慢日志详情。
func (s *CollectorSession) SlowlogDetail(ctx context.Context, db, queryHash string) (result *SlowlogDetailResult, err error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	defer func() { s.recordCapability("slowlog_detail", time.Since(startedAt), err) }()
	if address := s.slowlogAddress(db, queryHash); address != "" {
		release, acquireErr := s.acquireRemoteSlot(ctx)
		if acquireErr != nil {
			return nil, acquireErr
		}
		defer release()
		conn, connectErr := s.derivedConnection(ctx, address, derivedConnectionOptions{
			Database: db,
			Direct:   boolPointer(true),
		})
		if connectErr != nil {
			return nil, connectErr
		}
		return s.slowlogDetailLoader(ctx, conn, db, queryHash)
	}
	return s.client.SlowlogDetail(ctx, db, queryHash)
}

type slowlogLocationKey struct {
	database  string
	queryHash string
}

type slowlogDetailLoader func(ctx context.Context, conn *pkgmongo.Conn, db, queryHash string) (*SlowlogDetailResult, error)

func (s *CollectorSession) recordSlowlogLocations(summary *SlowlogSummaryResult) {
	if s == nil || summary == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, replicaSet := range summary.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				for _, item := range database.Items {
					key := slowlogLocationKey{database: database.Database, queryHash: item.QueryHash}
					if key.database == "" || key.queryHash == "" || s.slowlogLocations[key] != "" {
						continue
					}
					s.slowlogLocations[key] = host.Address
				}
			}
		}
	}
}

func (s *CollectorSession) slowlogAddress(database, queryHash string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.slowlogLocations[slowlogLocationKey{database: database, queryHash: queryHash}]
}

func (s *CollectorSession) requireOpen() error {
	if s == nil || s.client == nil {
		return invalidOptions("collector session is not initialized")
	}
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrCollectorSessionClosed
	}
	return nil
}

func (s *CollectorSession) acquireRemoteSlot(ctx context.Context) (func(), error) {
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.remoteLimit.Acquire(ctx, 1); err != nil {
		return nil, mapContextError(err)
	}
	if err := s.requireOpen(); err != nil {
		s.remoteLimit.Release(1)
		return nil, err
	}

	s.mu.Lock()
	s.remoteInFlight++
	s.stats.RemoteOperations++
	if s.remoteInFlight > s.stats.PeakRemoteOperations {
		s.stats.PeakRemoteOperations = s.remoteInFlight
	}
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.remoteInFlight--
			s.mu.Unlock()
			s.remoteLimit.Release(1)
		})
	}, nil
}

func (c *Client) acquireRemoteSlot(ctx context.Context) (func(), error) {
	if c != nil && c.session != nil {
		return c.session.acquireRemoteSlot(ctx)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func (c *Client) acquireCapabilityRemoteSlot(ctx context.Context, capabilityLimit *semaphore.Weighted) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if capabilityLimit != nil {
		if err := capabilityLimit.Acquire(ctx, 1); err != nil {
			return nil, mapContextError(err)
		}
	}
	releaseRemote, err := c.acquireRemoteSlot(ctx)
	if err != nil {
		if capabilityLimit != nil {
			capabilityLimit.Release(1)
		}
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseRemote()
			if capabilityLimit != nil {
				capabilityLimit.Release(1)
			}
		})
	}, nil
}

func (s *CollectorSession) recordCapability(name string, duration time.Duration, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := s.stats.Capabilities[name]
	stats.Calls++
	stats.TotalDuration += duration
	switch {
	case err == nil:
		stats.Successes++
	case errors.Is(err, ErrPartialResult):
		stats.PartialResults++
	default:
		stats.Failures++
	}
	s.stats.Capabilities[name] = stats
}
