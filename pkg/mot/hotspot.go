package mot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const (
	defaultHotspotDuration = 10 * time.Second
	defaultHotspotTopN     = 10
)

type HotspotOptions struct {
	Duration        time.Duration
	TopN            int
	NodeConcurrency int
	Databases       []string
	IncludeSystemDB bool
}

type NodeHotspot struct {
	Shard            string              `json:"shard,omitempty"`
	Host             string              `json:"host"`
	Rates            map[string]float64  `json:"rates"`
	Deltas           map[string]int64    `json:"deltas,omitempty"`
	AverageLatencies map[string]*float64 `json:"averageLatenciesMicros"`
	Gauges           map[string]int64    `json:"gauges,omitempty"`
}

type NamespaceHotspot struct {
	Shard           string  `json:"shard,omitempty"`
	Host            string  `json:"host"`
	Namespace       string  `json:"namespace"`
	ReadPerSecond   float64 `json:"readPerSecond"`
	WritePerSecond  float64 `json:"writePerSecond"`
	ReadTimeMicros  int64   `json:"readTimeMicros"`
	WriteTimeMicros int64   `json:"writeTimeMicros"`
	TotalTimeMicros int64   `json:"totalTimeMicros"`
}

type HotspotResult struct {
	ClusterType       ClusterType         `json:"clusterType"`
	StartedAt         time.Time           `json:"startedAt"`
	FinishedAt        time.Time           `json:"finishedAt"`
	EffectiveDuration time.Duration       `json:"effectiveDuration"`
	Nodes             []NodeHotspot       `json:"nodes"`
	Namespaces        []NamespaceHotspot  `json:"namespaces"`
	Findings          []DiagnosticFinding `json:"findings"`
	CollectorStatuses []CollectorStatus   `json:"collectorStatuses"`
}

type hotspotNamespaceCounter struct {
	ReadCount       int64
	WriteCount      int64
	ReadTimeMicros  int64
	WriteTimeMicros int64
}

type hotspotNodeSnapshot struct {
	Identity    string
	Shard       string
	Address     string
	CollectedAt time.Time
	Uptime      optionalInt64
	Counters    map[string]int64
	Gauges      map[string]int64
	Namespaces  map[string]hotspotNamespaceCounter
}

type hotspotSnapshot struct {
	CollectedAt time.Time
	Nodes       []hotspotNodeSnapshot
}

type hotspotTarget struct {
	ReplicaSet string
	Shard      string
	Address    string
}

// Hotspot 采集 mongod 数据节点的两次累计快照，并按实际间隔计算热点 rate。
func (c *Client) Hotspot(ctx context.Context, opts HotspotOptions) (result *HotspotResult, err error) {
	opts, err = normalizeHotspotOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := c.requireMemberConnectionURI(); err != nil {
		return nil, err
	}
	defer func() { err = mapContextError(err) }()

	cluster, err := pkgmongo.DetectCluster(ctx, c.conn)
	if err != nil {
		return nil, err
	}
	clusterType := convertClusterType(cluster.Type)
	if gate, allowed := diagnosticCapabilityGate("hotspot_snapshot", clusterType, cluster.MaxWireVersion, true); !allowed {
		return &HotspotResult{ClusterType: clusterType, CollectorStatuses: []CollectorStatus{gate}}, nil
	}
	targets, targetStatuses, targetErrors := c.discoverHotspotTargets(ctx, cluster.Type)
	if len(targets) == 0 {
		return &HotspotResult{ClusterType: clusterType, CollectorStatuses: targetStatuses}, errors.Join(targetErrors...)
	}
	first, firstStatuses, firstErrors := c.collectHotspotSnapshot(ctx, targets, opts)
	if len(first.Nodes) == 0 {
		return &HotspotResult{ClusterType: convertClusterType(cluster.Type), StartedAt: first.CollectedAt, CollectorStatuses: append(targetStatuses, firstStatuses...)}, errors.Join(firstErrors...)
	}
	if waitErr := waitForHotspotSample(ctx, opts.Duration); waitErr != nil {
		partial := &HotspotResult{ClusterType: convertClusterType(cluster.Type), StartedAt: first.CollectedAt, CollectorStatuses: append(targetStatuses, firstStatuses...)}
		sortCollectorStatuses(partial.CollectorStatuses)
		return partial, newDiagnosticPartialError("hotspot", partial, waitErr)
	}
	second, secondStatuses, secondErrors := c.collectHotspotSnapshot(ctx, targets, opts)
	resultValue := calculateHotspot(first, second, opts)
	resultValue.ClusterType = clusterType
	resultValue.CollectorStatuses = append(resultValue.CollectorStatuses, targetStatuses...)
	resultValue.CollectorStatuses = append(resultValue.CollectorStatuses, firstStatuses...)
	resultValue.CollectorStatuses = append(resultValue.CollectorStatuses, secondStatuses...)
	sortCollectorStatuses(resultValue.CollectorStatuses)
	result = &resultValue
	collectorErrors := append(targetErrors, firstErrors...)
	collectorErrors = append(collectorErrors, secondErrors...)
	if len(collectorErrors) > 0 {
		return result, newDiagnosticPartialError("hotspot", result, errors.Join(collectorErrors...))
	}
	return result, nil
}

func (c *Client) discoverHotspotTargets(ctx context.Context, clusterType pkgmongo.ClusterType) ([]hotspotTarget, []CollectorStatus, []error) {
	var targets []hotspotTarget
	var statuses []CollectorStatus
	var collectorErrors []error
	collectReplicaSet := func(conn *pkgmongo.Conn, shard string) {
		status, err := conn.RsStatus(ctx)
		scope := FindingScope{Type: ScopeReplicaSet, Shard: shard}
		if err != nil {
			collectorErrors = append(collectorErrors, err)
			statuses = append(statuses, failedCollectorStatus("hotspot_targets", scope, err))
			return
		}
		scope.ReplicaSet = status.Set
		statuses = append(statuses, CollectorStatus{Name: "hotspot_targets", State: CapabilitySupported, Scope: scope})
		for _, member := range status.Members {
			if member.Health != 1 || (member.State != pkgmongo.StatePrimary && member.State != pkgmongo.StateSecondary) {
				continue
			}
			targets = append(targets, hotspotTarget{ReplicaSet: status.Set, Shard: shard, Address: member.Name})
		}
	}
	switch clusterType {
	case pkgmongo.ClusterRepl:
		collectReplicaSet(c.conn, "")
	case pkgmongo.ClusterShard:
		shards, err := c.conn.ListShards(ctx)
		if err != nil {
			return nil, nil, []error{fmt.Errorf("list shards: %w", err)}
		}
		for _, shard := range shards.Shards {
			if cancelErr := contextError(ctx); cancelErr != nil {
				collectorErrors = append(collectorErrors, cancelErr)
				break
			}
			replicaSet, addresses, parseErr := parseShardHost(shard.Host)
			if parseErr != nil {
				collectorErrors = append(collectorErrors, parseErr)
				continue
			}
			conn, connectErr := c.connectAddress(ctx, addresses, derivedConnectionOptions{ReplicaSet: replicaSet, Direct: boolPointer(false)})
			if connectErr != nil {
				collectorErrors = append(collectorErrors, connectErr)
				continue
			}
			collectReplicaSet(conn, shard.Id)
			c.closeDerivedConnection(ctx, conn)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Shard != targets[j].Shard {
			return targets[i].Shard < targets[j].Shard
		}
		return targets[i].Address < targets[j].Address
	})
	return targets, statuses, collectorErrors
}

func (c *Client) collectHotspotSnapshot(ctx context.Context, targets []hotspotTarget, opts HotspotOptions) (hotspotSnapshot, []CollectorStatus, []error) {
	snapshot := hotspotSnapshot{CollectedAt: time.Now().UTC()}
	var statuses []CollectorStatus
	var collectorErrors []error
	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	limit := semaphore.NewWeighted(int64(opts.NodeConcurrency))
	for _, target := range targets {
		if acquireErr := acquireDiagnosticSlot(groupCtx, limit); acquireErr != nil {
			mu.Lock()
			collectorErrors = append(collectorErrors, acquireErr)
			mu.Unlock()
			break
		}
		target := target
		group.Go(func() error {
			defer limit.Release(1)
			scope := FindingScope{Type: ScopeNode, ReplicaSet: target.ReplicaSet, Shard: target.Shard, Node: target.Address}
			conn, connectErr := c.connectAddress(groupCtx, target.Address, derivedConnectionOptions{Direct: boolPointer(true)})
			if connectErr != nil {
				mu.Lock()
				collectorErrors = append(collectorErrors, connectErr)
				statuses = append(statuses, failedCollectorStatus("hotspot_snapshot", scope, connectErr))
				mu.Unlock()
				return nil
			}
			defer c.closeDerivedConnection(groupCtx, conn)
			serverStatus, statusErr := conn.DiagnosticServerStatus(groupCtx, 5*time.Second)
			top, topErr := conn.Top(groupCtx, 5*time.Second)
			if statusErr != nil || topErr != nil {
				collectErr := errors.Join(statusErr, topErr)
				mu.Lock()
				if isUnsupportedDiagnosticError(collectErr) {
					statuses = append(statuses, CollectorStatus{Name: "hotspot_snapshot", State: CapabilityUnsupported, Scope: scope, ReasonCode: "unsupported_version", Message: "热点采集在当前服务器上不可用"})
				} else if isUnauthorizedError(collectErr) {
					statuses = append(statuses, failedCollectorStatus("hotspot_snapshot", scope, collectErr))
				} else {
					collectorErrors = append(collectorErrors, collectErr)
					statuses = append(statuses, failedCollectorStatus("hotspot_snapshot", scope, collectErr))
				}
				mu.Unlock()
				return nil
			}
			node := hotspotNodeSnapshot{
				Identity: target.ReplicaSet + "/" + target.Address, Shard: target.Shard, Address: target.Address,
				CollectedAt: time.Now().UTC(), Counters: make(map[string]int64), Gauges: make(map[string]int64), Namespaces: make(map[string]hotspotNamespaceCounter),
			}
			if serverStatus.Uptime != nil {
				node.Uptime = optionalInt64{Value: *serverStatus.Uptime, Present: true}
			}
			addHotspotCounter(node.Counters, "insert", serverStatus.OpCounters.Insert)
			addHotspotCounter(node.Counters, "query", serverStatus.OpCounters.Query)
			addHotspotCounter(node.Counters, "update", serverStatus.OpCounters.Update)
			addHotspotCounter(node.Counters, "delete", serverStatus.OpCounters.Delete)
			addHotspotCounter(node.Counters, "getmore", serverStatus.OpCounters.GetMore)
			addHotspotCounter(node.Counters, "command", serverStatus.OpCounters.Command)
			addHotspotCounter(node.Counters, "networkBytesIn", serverStatus.Network.BytesIn)
			addHotspotCounter(node.Counters, "networkBytesOut", serverStatus.Network.BytesOut)
			addHotspotCounter(node.Counters, "connectionsCreated", serverStatus.Connections.TotalCreated)
			addHotspotCounter(node.Counters, "connectionsRejected", serverStatus.Connections.Rejected)
			addHotspotCounter(node.Counters, "wtApplicationEviction", serverStatus.WiredTiger.Cache.ApplicationEviction)
			addHotspotCounter(node.Counters, "wtPagesRead", serverStatus.WiredTiger.Cache.PagesReadIntoCache)
			addHotspotCounter(node.Counters, "wtPagesWritten", serverStatus.WiredTiger.Cache.PagesWrittenFromCache)
			addHotspotCounter(node.Counters, "readTicketWaitMicros", serverStatus.Queues.Execution.Reads.TotalTimeQueuedMicros)
			addHotspotCounter(node.Counters, "writeTicketWaitMicros", serverStatus.Queues.Execution.Writes.TotalTimeQueuedMicros)
			addHotspotCounter(node.Counters, "documentsInserted", serverStatus.Metrics.Document.Inserted)
			addHotspotCounter(node.Counters, "documentsUpdated", serverStatus.Metrics.Document.Updated)
			addHotspotCounter(node.Counters, "documentsDeleted", serverStatus.Metrics.Document.Deleted)
			addHotspotCounter(node.Counters, "documentsReturned", serverStatus.Metrics.Document.Returned)
			addHotspotCounter(node.Counters, "readLatencyMicros", serverStatus.OpLatencies.Reads.Latency)
			addHotspotCounter(node.Counters, "readLatencyOps", serverStatus.OpLatencies.Reads.Ops)
			addHotspotCounter(node.Counters, "writeLatencyMicros", serverStatus.OpLatencies.Writes.Latency)
			addHotspotCounter(node.Counters, "writeLatencyOps", serverStatus.OpLatencies.Writes.Ops)
			addHotspotCounter(node.Counters, "commandLatencyMicros", serverStatus.OpLatencies.Commands.Latency)
			addHotspotCounter(node.Counters, "commandLatencyOps", serverStatus.OpLatencies.Commands.Ops)
			addHotspotGauge(node.Gauges, "queueTotal", serverStatus.Global.CurrentQueue.Total)
			addHotspotGauge(node.Gauges, "queueReaders", serverStatus.Global.CurrentQueue.Readers)
			addHotspotGauge(node.Gauges, "queueWriters", serverStatus.Global.CurrentQueue.Writers)
			addHotspotGauge(node.Gauges, "readTicketsAvailable", serverStatus.WiredTiger.ConcurrentTransactions.Read.Available)
			addHotspotGauge(node.Gauges, "writeTicketsAvailable", serverStatus.WiredTiger.ConcurrentTransactions.Write.Available)
			for namespace, counter := range top.Namespaces {
				if !hotspotNamespaceAllowed(namespace, opts) {
					continue
				}
				node.Namespaces[namespace] = hotspotNamespaceCounter(counter)
			}
			mu.Lock()
			snapshot.Nodes = append(snapshot.Nodes, node)
			statuses = append(statuses, CollectorStatus{Name: "hotspot_snapshot", State: CapabilitySupported, Scope: scope})
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	snapshot.CollectedAt = time.Now().UTC()
	sort.SliceStable(snapshot.Nodes, func(i, j int) bool { return snapshot.Nodes[i].Identity < snapshot.Nodes[j].Identity })
	sortCollectorStatuses(statuses)
	return snapshot, statuses, collectorErrors
}

func addHotspotCounter(target map[string]int64, name string, value *int64) {
	if value != nil {
		target[name] = *value
	}
}

func addHotspotGauge(target map[string]int64, name string, value *int64) {
	if value != nil {
		target[name] = *value
	}
}

func hotspotNamespaceAllowed(namespace string, opts HotspotOptions) bool {
	database, _, found := strings.Cut(namespace, ".")
	if !found || database == "" {
		return false
	}
	if !opts.IncludeSystemDB {
		for _, systemDB := range systemDatabases {
			if database == systemDB {
				return false
			}
		}
	}
	if len(opts.Databases) == 0 {
		return true
	}
	for _, allowed := range opts.Databases {
		if database == allowed {
			return true
		}
	}
	return false
}

func normalizeHotspotOptions(opts HotspotOptions) (HotspotOptions, error) {
	if opts.Duration < 0 {
		return HotspotOptions{}, invalidOptions("duration must be greater than zero")
	}
	if opts.TopN < 0 {
		return HotspotOptions{}, invalidOptions("top must not be negative")
	}
	if opts.NodeConcurrency < 0 {
		return HotspotOptions{}, invalidOptions("node concurrency must not be negative")
	}
	if opts.Duration == 0 {
		opts.Duration = defaultHotspotDuration
	}
	if opts.TopN == 0 {
		opts.TopN = defaultHotspotTopN
	}
	if opts.NodeConcurrency == 0 {
		opts.NodeConcurrency = defaultOverviewNodeConcurrency
	}
	return opts, nil
}

func waitForHotspotSample(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return contextError(ctx)
	case <-timer.C:
		return nil
	}
}

func calculateHotspot(first, second hotspotSnapshot, opts HotspotOptions) HotspotResult {
	result := HotspotResult{
		StartedAt: first.CollectedAt, FinishedAt: second.CollectedAt,
		EffectiveDuration: second.CollectedAt.Sub(first.CollectedAt),
	}
	if result.EffectiveDuration <= 0 {
		return result
	}
	firstByIdentity := make(map[string]hotspotNodeSnapshot, len(first.Nodes))
	secondByIdentity := make(map[string]hotspotNodeSnapshot, len(second.Nodes))
	for _, node := range first.Nodes {
		firstByIdentity[node.Identity] = node
	}
	for _, node := range second.Nodes {
		secondByIdentity[node.Identity] = node
		previous, ok := firstByIdentity[node.Identity]
		if !ok {
			continue
		}
		if (node.Uptime.Present && previous.Uptime.Present && node.Uptime.Value < previous.Uptime.Value) || countersReset(previous.Counters, node.Counters) {
			result.Findings = append(result.Findings, DiagnosticFinding{
				Code: "node.counter_reset", Severity: SeverityWarning,
				Scope:   FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address},
				Summary: "采样期间节点重启或累计计数器重置，未计算该节点 rate",
			})
			result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{
				Name: "hotspot", State: CapabilityFailed,
				Scope:      FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address},
				ReasonCode: "counter_reset", Message: "采样窗口内计数器不可比较",
			})
			continue
		}
		nodeDuration := node.CollectedAt.Sub(previous.CollectedAt)
		if nodeDuration <= 0 {
			nodeDuration = result.EffectiveDuration
		}
		if nodeDuration <= 0 {
			continue
		}
		seconds := nodeDuration.Seconds()
		rates := make(map[string]float64)
		deltas := make(map[string]int64)
		for name, current := range node.Counters {
			previousValue, present := previous.Counters[name]
			if !present || current < previousValue {
				continue
			}
			delta := current - previousValue
			deltas[name] = delta
			rates[name] = float64(delta) / seconds
		}
		averageLatencies := map[string]*float64{"read": nil, "write": nil, "command": nil}
		setAverageLatency(averageLatencies, "read", deltas["readLatencyMicros"], deltas["readLatencyOps"])
		setAverageLatency(averageLatencies, "write", deltas["writeLatencyMicros"], deltas["writeLatencyOps"])
		setAverageLatency(averageLatencies, "command", deltas["commandLatencyMicros"], deltas["commandLatencyOps"])
		result.Nodes = append(result.Nodes, NodeHotspot{Shard: node.Shard, Host: node.Address, Rates: rates, Deltas: hotspotMetricDeltas(deltas), AverageLatencies: averageLatencies, Gauges: node.Gauges})
		if rates["connectionsRejected"] > 0 {
			result.Findings = append(result.Findings, DiagnosticFinding{Code: "connection.rejected_during_sample", Severity: SeverityCritical, Scope: FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address}, Summary: "采样窗口内出现连接拒绝", Evidence: map[string]any{"rejected": deltas["connectionsRejected"], "rejectedPerSecond": rates["connectionsRejected"]}})
		}
		if hasHotspotGauge(previous.Gauges, "queueTotal") && hasHotspotGauge(node.Gauges, "queueTotal") && previous.Gauges["queueTotal"] > 0 && node.Gauges["queueTotal"] > 0 {
			result.Findings = append(result.Findings, DiagnosticFinding{Code: "operation.queue_sustained", Severity: SeverityWarning, Scope: FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address}, Summary: "两个快照均观察到操作排队", Evidence: map[string]any{"firstQueueTotal": previous.Gauges["queueTotal"], "secondQueueTotal": node.Gauges["queueTotal"]}})
		}
		evictionDelta := deltas["wtApplicationEviction"] + deltas["wtPagesRead"] + deltas["wtPagesWritten"]
		latencyDelta := deltas["readLatencyMicros"] + deltas["writeLatencyMicros"] + deltas["commandLatencyMicros"]
		if evictionDelta > 0 && latencyDelta > 0 {
			result.Findings = append(result.Findings, DiagnosticFinding{Code: "storage.eviction_pressure", Severity: SeverityWarning, Scope: FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address}, Summary: "采样窗口内 eviction 与操作延迟同时增长", Evidence: map[string]any{"evictionDelta": evictionDelta, "latencyMicrosDelta": latencyDelta}})
		}
		result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{
			Name: "hotspot", State: CapabilitySupported,
			Scope: FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address},
		})
		for namespace, current := range node.Namespaces {
			previousCounter := previous.Namespaces[namespace]
			if _, existed := previous.Namespaces[namespace]; existed && namespaceCountersReset(previousCounter, current) {
				result.Findings = append(result.Findings, DiagnosticFinding{Code: "hotspot.namespace_counter_reset", Severity: SeverityInfo, Scope: FindingScope{Type: ScopeNamespace, Shard: node.Shard, Node: node.Address, Namespace: namespace}, Summary: "namespace 累计计数器在采样窗口内重置，未计算该项 rate"})
				continue
			}
			readCount := nonNegative(current.ReadCount - previousCounter.ReadCount)
			writeCount := nonNegative(current.WriteCount - previousCounter.WriteCount)
			readTime := nonNegative(current.ReadTimeMicros - previousCounter.ReadTimeMicros)
			writeTime := nonNegative(current.WriteTimeMicros - previousCounter.WriteTimeMicros)
			if readCount == 0 && writeCount == 0 && readTime == 0 && writeTime == 0 {
				continue
			}
			result.Namespaces = append(result.Namespaces, NamespaceHotspot{
				Shard: node.Shard, Host: node.Address, Namespace: namespace,
				ReadPerSecond: float64(readCount) / seconds, WritePerSecond: float64(writeCount) / seconds,
				ReadTimeMicros: readTime, WriteTimeMicros: writeTime, TotalTimeMicros: readTime + writeTime,
			})
		}
	}
	for identity, node := range firstByIdentity {
		if _, ok := secondByIdentity[identity]; ok {
			continue
		}
		result.CollectorStatuses = append(result.CollectorStatuses, CollectorStatus{
			Name: "hotspot", State: CapabilityFailed,
			Scope:      FindingScope{Type: ScopeNode, Shard: node.Shard, Node: node.Address},
			ReasonCode: "node_unreachable", Message: "第二快照未返回该节点",
		})
	}
	sort.SliceStable(result.Namespaces, func(i, j int) bool {
		left, right := result.Namespaces[i], result.Namespaces[j]
		if left.TotalTimeMicros != right.TotalTimeMicros {
			return left.TotalTimeMicros > right.TotalTimeMicros
		}
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		if left.Shard != right.Shard {
			return left.Shard < right.Shard
		}
		return left.Host < right.Host
	})
	if opts.TopN > 0 && len(result.Namespaces) > opts.TopN {
		result.Namespaces = result.Namespaces[:opts.TopN]
	}
	for _, namespace := range result.Namespaces {
		evidence := map[string]any{"readPerSecond": namespace.ReadPerSecond, "writePerSecond": namespace.WritePerSecond, "totalTimeMicros": namespace.TotalTimeMicros}
		scope := FindingScope{Type: ScopeNamespace, Shard: namespace.Shard, Node: namespace.Host, Namespace: namespace.Namespace}
		if namespace.ReadPerSecond > 0 || namespace.ReadTimeMicros > 0 {
			result.Findings = append(result.Findings, DiagnosticFinding{Code: "hotspot.namespace_read", Severity: SeverityInfo, Scope: scope, Summary: "namespace 在采样窗口内位于读热点列表", Evidence: evidence})
		}
		if namespace.WritePerSecond > 0 || namespace.WriteTimeMicros > 0 {
			result.Findings = append(result.Findings, DiagnosticFinding{Code: "hotspot.namespace_write", Severity: SeverityInfo, Scope: scope, Summary: "namespace 在采样窗口内位于写热点列表", Evidence: evidence})
		}
	}
	sanitizeAndSortFindings(result.Findings)
	sortCollectorStatuses(result.CollectorStatuses)
	return result
}

func hasHotspotGauge(gauges map[string]int64, name string) bool { _, ok := gauges[name]; return ok }

func setAverageLatency(target map[string]*float64, name string, latencyMicros, operations int64) {
	if operations <= 0 {
		return
	}
	average := float64(latencyMicros) / float64(operations)
	target[name] = &average
}

func hotspotMetricDeltas(deltas map[string]int64) map[string]int64 {
	result := make(map[string]int64)
	for _, name := range []string{"wtApplicationEviction", "wtPagesRead", "wtPagesWritten", "readTicketWaitMicros", "writeTicketWaitMicros"} {
		if value, ok := deltas[name]; ok {
			result[name] = value
		}
	}
	return result
}

func namespaceCountersReset(first, second hotspotNamespaceCounter) bool {
	return second.ReadCount < first.ReadCount || second.WriteCount < first.WriteCount || second.ReadTimeMicros < first.ReadTimeMicros || second.WriteTimeMicros < first.WriteTimeMicros
}

func countersReset(first, second map[string]int64) bool {
	for name, previous := range first {
		if current, ok := second[name]; ok && current < previous {
			return true
		}
	}
	return false
}
