package mot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const (
	defaultReplicationLagWarning  = 60 * time.Second
	defaultReplicationLagCritical = 300 * time.Second
)

type DoctorOptions struct {
	MinimumSeverity        Severity
	NodeConcurrency        int
	ReplicationLagWarning  time.Duration
	ReplicationLagCritical time.Duration
	IncludeSystemDB        bool
	IncludeOplogWindow     bool
}

type DoctorResult struct {
	ClusterType       ClusterType         `json:"clusterType"`
	CollectedAt       time.Time           `json:"collectedAt"`
	Findings          []DiagnosticFinding `json:"findings"`
	CollectorStatuses []CollectorStatus   `json:"collectorStatuses"`
	Summary           FindingSummary      `json:"summary"`
}

type optionalInt64 struct {
	Value   int64
	Present bool
}

type doctorNodeSnapshot struct {
	ReplicaSet       string
	Shard            string
	Address          string
	Uptime           optionalInt64
	ConnectionsUsed  optionalInt64
	ConnectionsFree  optionalInt64
	QueueTotal       optionalInt64
	CacheMax         optionalInt64
	CacheUsed        optionalInt64
	EvictionPressure optionalInt64
}

// Doctor 执行一次只读、有界的 MongoDB 健康巡检。
func (c *Client) Doctor(ctx context.Context, opts DoctorOptions) (result *DoctorResult, err error) {
	opts, err = normalizeDoctorOptions(opts)
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
	result = &DoctorResult{ClusterType: convertClusterType(cluster.Type), CollectedAt: time.Now().UTC()}
	if gate, allowed := diagnosticCapabilityGate("replica_status", result.ClusterType, cluster.MaxWireVersion, true); !allowed {
		result.CollectorStatuses = []CollectorStatus{gate}
		return result, nil
	}
	var collectorErrors []error
	successfulReplicaSets := 0

	collect := func(conn *pkgmongo.Conn, shard string) {
		findings, statuses, collectErr := c.collectDoctorReplicaSet(ctx, conn, shard, opts, result.CollectedAt)
		result.Findings = append(result.Findings, findings...)
		result.CollectorStatuses = append(result.CollectorStatuses, statuses...)
		if collectErr != nil {
			collectorErrors = append(collectorErrors, collectErr)
			return
		}
		successfulReplicaSets++
	}

	switch cluster.Type {
	case pkgmongo.ClusterRepl:
		collect(c.conn, "")
	case pkgmongo.ClusterShard:
		shards, listErr := c.conn.ListShards(ctx)
		if listErr != nil {
			return nil, fmt.Errorf("list shards: %w", listErr)
		}
		for _, shard := range shards.Shards {
			if cancelErr := contextError(ctx); cancelErr != nil {
				collectorErrors = append(collectorErrors, cancelErr)
				break
			}
			replicaSet, addresses, parseErr := parseShardHost(shard.Host)
			scope := FindingScope{Type: ScopeReplicaSet, ReplicaSet: replicaSet, Shard: shard.Id}
			if parseErr != nil {
				collectorErrors = append(collectorErrors, parseErr)
				result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("replica_status", scope, parseErr))
				continue
			}
			conn, connectErr := c.connectAddress(ctx, addresses, derivedConnectionOptions{ReplicaSet: replicaSet, Direct: boolPointer(false)})
			if connectErr != nil {
				collectorErrors = append(collectorErrors, connectErr)
				result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("replica_status", scope, connectErr))
				continue
			}
			collect(conn, shard.Id)
			c.closeDerivedConnection(ctx, conn)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedTopology, cluster.Type)
	}

	sanitizeAndSortFindings(result.Findings)
	result.Findings = filterFindingsByMinimumSeverity(result.Findings, opts.MinimumSeverity)
	sortCollectorStatuses(result.CollectorStatuses)
	result.Summary = summarizeFindings(result.Findings)
	if len(collectorErrors) == 0 {
		return result, nil
	}
	joined := errors.Join(collectorErrors...)
	if successfulReplicaSets == 0 && len(result.Findings) == 0 {
		return result, joined
	}
	return result, newDiagnosticPartialError("doctor", result, joined)
}

func (c *Client) collectDoctorReplicaSet(
	ctx context.Context,
	conn *pkgmongo.Conn,
	shard string,
	opts DoctorOptions,
	now time.Time,
) ([]DiagnosticFinding, []CollectorStatus, error) {
	status, err := conn.RsStatus(ctx)
	scope := FindingScope{Type: ScopeReplicaSet, Shard: shard}
	if err != nil {
		collectorStatus := failedCollectorStatus("replica_status", scope, err)
		if collectorStatus.State == CapabilityUnauthorized || collectorStatus.State == CapabilityUnsupported {
			return nil, []CollectorStatus{collectorStatus}, nil
		}
		return nil, []CollectorStatus{collectorStatus}, err
	}
	scope.ReplicaSet = status.Set
	statuses := []CollectorStatus{{Name: "replica_status", State: CapabilitySupported, Scope: scope}}
	nodes := make([]doctorNodeSnapshot, 0, len(status.Members))
	var collectorErrors []error
	var mu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	limit := semaphore.NewWeighted(int64(opts.NodeConcurrency))
	for _, member := range status.Members {
		if member.State == pkgmongo.StateArbiter {
			continue
		}
		if acquireErr := acquireDiagnosticSlot(groupCtx, limit); acquireErr != nil {
			mu.Lock()
			collectorErrors = append(collectorErrors, acquireErr)
			mu.Unlock()
			break
		}
		member := member
		group.Go(func() error {
			defer limit.Release(1)
			nodeScope := FindingScope{Type: ScopeNode, ReplicaSet: status.Set, Shard: shard, Node: member.Name}
			nodeConn, connectErr := c.connectAddress(groupCtx, member.Name, derivedConnectionOptions{Direct: boolPointer(true)})
			if connectErr != nil {
				mu.Lock()
				statuses = append(statuses, failedCollectorStatus("server_status", nodeScope, connectErr))
				if !isUnauthorizedError(connectErr) {
					collectorErrors = append(collectorErrors, connectErr)
				}
				mu.Unlock()
				return nil
			}
			snapshot, snapshotErr := nodeConn.DiagnosticServerStatus(groupCtx, 5*time.Second)
			c.closeDerivedConnection(groupCtx, nodeConn)
			mu.Lock()
			defer mu.Unlock()
			if snapshotErr != nil {
				statuses = append(statuses, failedCollectorStatus("server_status", nodeScope, snapshotErr))
				if !isUnauthorizedError(snapshotErr) && !isUnsupportedDiagnosticError(snapshotErr) {
					collectorErrors = append(collectorErrors, snapshotErr)
				}
				return nil
			}
			statuses = append(statuses, CollectorStatus{Name: "server_status", State: CapabilitySupported, Scope: nodeScope})
			nodes = append(nodes, doctorNodeFromServerStatus(status.Set, shard, member.Name, snapshot))
			return nil
		})
	}
	_ = group.Wait()

	findings := evaluateDoctorReplicaSet(status, shard, nodes, now, opts)
	if cancelErr := contextError(ctx); cancelErr != nil {
		collectorErrors = append(collectorErrors, cancelErr)
		return findings, statuses, errors.Join(collectorErrors...)
	}
	diskFindings, diskStatuses, diskErrors := c.collectDoctorDisk(ctx, conn, shard, status.Set, opts.IncludeSystemDB)
	findings = append(findings, diskFindings...)
	statuses = append(statuses, diskStatuses...)
	collectorErrors = append(collectorErrors, diskErrors...)
	if opts.IncludeOplogWindow {
		oplog, oplogErr := conn.OplogWindow(ctx, 5*time.Second)
		switch {
		case oplogErr == nil:
			statuses = append(statuses, CollectorStatus{Name: "oplog_window", State: CapabilitySupported, Scope: scope})
			findings = append(findings, evaluateOplogWindow(status, shard, oplog)...)
		case isUnauthorizedError(oplogErr):
			statuses = append(statuses, CollectorStatus{Name: "oplog_window", State: CapabilityUnauthorized, Scope: scope, ReasonCode: "unauthorized", Message: "当前用户无权读取 local.oplog.rs"})
		case errors.Is(oplogErr, drivermongo.ErrNoDocuments):
			statuses = append(statuses, CollectorStatus{Name: "oplog_window", State: CapabilityUnsupported, Scope: scope, ReasonCode: "oplog_unavailable", Message: "未找到 local.oplog.rs"})
		default:
			collectorErrors = append(collectorErrors, oplogErr)
			statuses = append(statuses, failedCollectorStatus("oplog_window", scope, oplogErr))
		}
	}
	return findings, statuses, errors.Join(collectorErrors...)
}

func evaluateOplogWindow(status pkgmongo.RsStatus, shard string, window pkgmongo.OplogWindowSnapshot) []DiagnosticFinding {
	duration := window.Latest.Sub(window.Earliest)
	if duration <= 0 {
		return nil
	}
	var primaryApplied time.Time
	for _, member := range status.Members {
		if member.State == pkgmongo.StatePrimary {
			primaryApplied = member.LastAppliedWallTime
			if primaryApplied.IsZero() {
				primaryApplied = member.OptimeDate
			}
			break
		}
	}
	if primaryApplied.IsZero() {
		return nil
	}
	var findings []DiagnosticFinding
	for _, member := range status.Members {
		if member.State != pkgmongo.StateSecondary {
			continue
		}
		applied := member.LastAppliedWallTime
		if applied.IsZero() {
			applied = member.OptimeDate
		}
		if applied.IsZero() || !primaryApplied.After(applied) {
			continue
		}
		lag := primaryApplied.Sub(applied)
		severity, code := Severity(""), ""
		switch {
		case lag >= duration:
			severity, code = SeverityCritical, "replication.lag_exceeds_oplog_window"
		case lag >= duration*8/10:
			severity, code = SeverityWarning, "replication.lag_near_oplog_window"
		}
		if code == "" {
			continue
		}
		findings = append(findings, DiagnosticFinding{Code: code, Severity: severity, Scope: FindingScope{Type: ScopeNode, ReplicaSet: status.Set, Shard: shard, Node: member.Name}, Summary: "secondary 复制延迟接近或超过 oplog 可恢复窗口", Evidence: map[string]any{"lagSeconds": lag.Seconds(), "oplogWindowSeconds": duration.Seconds()}})
	}
	return findings
}

func (c *Client) collectDoctorDisk(
	ctx context.Context,
	conn *pkgmongo.Conn,
	shard string,
	replicaSet string,
	includeSystemDB bool,
) ([]DiagnosticFinding, []CollectorStatus, []error) {
	dbs, err := conn.Client.ListDatabaseNames(ctx, map[string]any{})
	if err != nil {
		scope := FindingScope{Type: ScopeReplicaSet, ReplicaSet: replicaSet, Shard: shard}
		if isUnauthorizedError(err) {
			return nil, []CollectorStatus{failedCollectorStatus("database_stats", scope, err)}, nil
		}
		return nil, []CollectorStatus{failedCollectorStatus("database_stats", scope, err)}, []error{err}
	}
	findings := make([]DiagnosticFinding, 0)
	statuses := make([]CollectorStatus, 0, len(dbs))
	collectorErrors := make([]error, 0)
	var highestUsedRatio float64
	var highestUsedBytes, highestTotalBytes int64
	for _, db := range dbs {
		if !includeSystemDB && slices.Contains(systemDatabases, db) {
			continue
		}
		scope := FindingScope{Type: ScopeDatabase, ReplicaSet: replicaSet, Shard: shard, Database: db}
		stats, statsErr := conn.DBStatus(ctx, db)
		if statsErr != nil {
			statuses = append(statuses, failedCollectorStatus("database_stats", scope, statsErr))
			if !isUnauthorizedError(statsErr) && !isUnsupportedDiagnosticError(statsErr) {
				collectorErrors = append(collectorErrors, statsErr)
			}
			continue
		}
		statuses = append(statuses, CollectorStatus{Name: "database_stats", State: CapabilitySupported, Scope: scope})
		if stats.FsTotalSize <= 0 {
			continue
		}
		ratio := float64(stats.FsUsedSize) / float64(stats.FsTotalSize)
		if ratio > highestUsedRatio {
			highestUsedRatio, highestUsedBytes, highestTotalBytes = ratio, stats.FsUsedSize, stats.FsTotalSize
		}
	}
	severity, code := Severity(""), ""
	switch {
	case highestUsedRatio >= 0.95:
		severity, code = SeverityCritical, "storage.fs_headroom_critical"
	case highestUsedRatio >= 0.85:
		severity, code = SeverityWarning, "storage.fs_headroom_low"
	}
	if code != "" {
		findings = append(findings, DiagnosticFinding{
			Code: code, Severity: severity, Scope: FindingScope{Type: ScopeReplicaSet, ReplicaSet: replicaSet, Shard: shard},
			Summary:  "MongoDB 所在文件系统可用空间不足",
			Evidence: map[string]any{"usedRatio": highestUsedRatio, "usedBytes": highestUsedBytes, "totalBytes": highestTotalBytes},
		})
	}
	return findings, statuses, collectorErrors
}

func doctorNodeFromServerStatus(replicaSet, shard, address string, snapshot pkgmongo.ServerStatusSnapshot) doctorNodeSnapshot {
	result := doctorNodeSnapshot{ReplicaSet: replicaSet, Shard: shard, Address: address}
	result.Uptime = optionalInt64FromPointer(snapshot.Uptime)
	result.ConnectionsUsed = optionalInt64FromPointer(snapshot.Connections.Current)
	result.ConnectionsFree = optionalInt64FromPointer(snapshot.Connections.Available)
	result.CacheMax = optionalInt64FromPointer(snapshot.WiredTiger.Cache.MaximumBytesConfigured)
	result.CacheUsed = optionalInt64FromPointer(snapshot.WiredTiger.Cache.BytesInCache)
	result.EvictionPressure = optionalInt64FromPointer(snapshot.WiredTiger.Cache.ApplicationEviction)
	queue := int64(0)
	present := false
	for _, value := range []*int64{snapshot.Global.CurrentQueue.Total, snapshot.Global.CurrentQueue.Readers, snapshot.Global.CurrentQueue.Writers} {
		if value != nil {
			queue += *value
			present = true
		}
	}
	result.QueueTotal = optionalInt64{Value: queue, Present: present}
	return result
}

func optionalInt64FromPointer(value *int64) optionalInt64 {
	if value == nil {
		return optionalInt64{}
	}
	return optionalInt64{Value: *value, Present: true}
}

func failedCollectorStatus(name string, scope FindingScope, err error) CollectorStatus {
	state, reason := CapabilityFailed, "collector_failed"
	switch {
	case isUnauthorizedError(err):
		state, reason = CapabilityUnauthorized, "unauthorized"
	case isUnsupportedDiagnosticError(err):
		state, reason = CapabilityUnsupported, "unsupported_version"
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCancelled):
		reason = "timeout"
	}
	message := safeCollectorMessage(state, reason)
	if capability, ok := diagnosticCapability(name); ok && state == CapabilityUnsupported && capability.MinimumVersion != "" {
		message = "当前服务器不满足 collector 最低能力版本 " + capability.MinimumVersion
	}
	return CollectorStatus{Name: name, State: state, Scope: scope, ReasonCode: reason, Message: message}
}

func safeCollectorMessage(state CapabilityState, reason string) string {
	if state == CapabilityUnauthorized {
		return "当前用户无权执行该采集项"
	}
	if state == CapabilityUnsupported {
		return "当前服务器或拓扑不支持该采集项"
	}
	if reason == "timeout" {
		return "采集被取消或超时"
	}
	return "采集失败；原始错误未写入公共结果"
}

func filterFindingsByMinimumSeverity(findings []DiagnosticFinding, minimum Severity) []DiagnosticFinding {
	maxRank := severityRank(minimum)
	result := make([]DiagnosticFinding, 0, len(findings))
	for _, finding := range findings {
		if severityRank(finding.Severity) <= maxRank {
			result = append(result, finding)
		}
	}
	return result
}

func defaultDoctorOptions() DoctorOptions {
	return DoctorOptions{
		MinimumSeverity:        SeverityInfo,
		NodeConcurrency:        defaultOverviewNodeConcurrency,
		ReplicationLagWarning:  defaultReplicationLagWarning,
		ReplicationLagCritical: defaultReplicationLagCritical,
	}
}

func normalizeDoctorOptions(opts DoctorOptions) (DoctorOptions, error) {
	defaults := defaultDoctorOptions()
	if opts.MinimumSeverity == "" {
		opts.MinimumSeverity = defaults.MinimumSeverity
	}
	if err := validateSeverity(opts.MinimumSeverity); err != nil {
		return DoctorOptions{}, err
	}
	if opts.NodeConcurrency < 0 {
		return DoctorOptions{}, invalidOptions("node concurrency must not be negative")
	}
	if opts.NodeConcurrency == 0 {
		opts.NodeConcurrency = defaults.NodeConcurrency
	}
	if opts.ReplicationLagWarning <= 0 {
		opts.ReplicationLagWarning = defaults.ReplicationLagWarning
	}
	if opts.ReplicationLagCritical <= 0 {
		opts.ReplicationLagCritical = defaults.ReplicationLagCritical
	}
	if opts.ReplicationLagWarning >= opts.ReplicationLagCritical {
		return DoctorOptions{}, invalidOptions("replication lag warning must be lower than critical")
	}
	return opts, nil
}

func evaluateDoctorReplicaSet(
	status pkgmongo.RsStatus,
	shard string,
	nodes []doctorNodeSnapshot,
	now time.Time,
	opts DoctorOptions,
) []DiagnosticFinding {
	scope := FindingScope{Type: ScopeReplicaSet, ReplicaSet: status.Set, Shard: shard}
	findings := make([]DiagnosticFinding, 0)
	healthyDataMembers := 0
	primaryFound := false
	var primaryWrite time.Time

	for _, member := range status.Members {
		memberScope := FindingScope{Type: ScopeNode, ReplicaSet: status.Set, Shard: shard, Node: member.Name}
		switch member.State {
		case pkgmongo.StatePrimary:
			primaryFound = member.Health == 1
			if primaryFound {
				healthyDataMembers++
			}
			primaryWrite = memberWriteTime(member)
			if !member.ElectionDate.IsZero() && now.After(member.ElectionDate) && now.Sub(member.ElectionDate) <= 15*time.Minute {
				findings = append(findings, DiagnosticFinding{Code: "replica.recent_election", Severity: SeverityWarning, Scope: memberScope, Summary: "PRIMARY 最近 15 分钟内发生过选举", Evidence: map[string]any{"secondsSinceElection": now.Sub(member.ElectionDate).Seconds()}})
			}
		case pkgmongo.StateSecondary:
			if member.Health == 1 {
				healthyDataMembers++
			}
		case pkgmongo.StateArbiter:
			findings = append(findings, DiagnosticFinding{
				Code: "replica.arbiter_present", Severity: SeverityInfo, Scope: memberScope,
				Summary:  "副本集包含仲裁节点",
				Evidence: map[string]any{"state": member.StateStr},
			})
		case pkgmongo.StateRecovering, pkgmongo.StateStartup2:
			findings = append(findings, DiagnosticFinding{
				Code: "replica.member_recovering", Severity: SeverityWarning, Scope: memberScope,
				Summary:  "副本集成员正在恢复或同步",
				Evidence: map[string]any{"state": member.StateStr},
			})
		}

		if member.Health != 1 || member.State == pkgmongo.StateDown || member.State == pkgmongo.StateUnknown || member.State == pkgmongo.StateRollback {
			findings = append(findings, DiagnosticFinding{
				Code: "replica.member_unhealthy", Severity: SeverityCritical, Scope: memberScope,
				Summary:        "副本集成员不可用或状态异常",
				Evidence:       map[string]any{"health": member.Health, "state": member.StateStr},
				Recommendation: fmt.Sprintf("检查节点 %s 的复制状态与网络路径", member.Name),
			})
		}
		if member.LastHeartbeatMessage != "" && member.Health == 1 {
			findings = append(findings, DiagnosticFinding{
				Code: "replica.heartbeat_error", Severity: SeverityWarning, Scope: memberScope,
				Summary: "副本集成员最近一次 heartbeat 返回异常",
			})
		}
		if member.Health == 1 && !member.LastHeartbeat.IsZero() && now.After(member.LastHeartbeat) && now.Sub(member.LastHeartbeat) > 30*time.Second {
			findings = append(findings, DiagnosticFinding{Code: "replica.heartbeat_stale", Severity: SeverityWarning, Scope: memberScope, Summary: "副本集成员 heartbeat 超过 30 秒未更新", Evidence: map[string]any{"heartbeatAgeSeconds": now.Sub(member.LastHeartbeat).Seconds()}})
		}
	}

	if !primaryFound {
		findings = append(findings, DiagnosticFinding{
			Code: "replica.primary_missing", Severity: SeverityCritical, Scope: scope,
			Summary: "副本集当前没有可用 PRIMARY",
		})
	}
	required := status.WriteMajorityCount
	if required <= 0 {
		required = len(status.Members)/2 + 1
	}
	if healthyDataMembers < required {
		findings = append(findings, DiagnosticFinding{
			Code: "replica.majority_unavailable", Severity: SeverityCritical, Scope: scope,
			Summary:  "副本集当前无法满足 majority 写入确认",
			Evidence: map[string]any{"available": healthyDataMembers, "required": required},
		})
	}

	if !primaryWrite.IsZero() {
		for _, member := range status.Members {
			if member.State != pkgmongo.StateSecondary || member.Health != 1 {
				continue
			}
			secondaryWrite := memberWriteTime(member)
			if secondaryWrite.IsZero() {
				continue
			}
			lag := primaryWrite.Sub(secondaryWrite)
			if lag < 0 {
				lag = 0
			}
			severity, code := Severity(""), ""
			switch {
			case lag >= opts.ReplicationLagCritical:
				severity, code = SeverityCritical, "replica.lag_critical"
			case lag >= opts.ReplicationLagWarning:
				severity, code = SeverityWarning, "replica.lag_high"
			}
			if code != "" {
				findings = append(findings, DiagnosticFinding{
					Code: code, Severity: severity,
					Scope:    FindingScope{Type: ScopeNode, ReplicaSet: status.Set, Shard: shard, Node: member.Name},
					Summary:  "SECONDARY 复制进度落后于 PRIMARY",
					Evidence: map[string]any{"lagSeconds": lag.Seconds()},
				})
			}
		}
	}
	for _, node := range nodes {
		findings = append(findings, evaluateDoctorNode(node, now)...)
	}
	return findings
}

func evaluateDoctorNode(node doctorNodeSnapshot, now time.Time) []DiagnosticFinding {
	scope := FindingScope{Type: ScopeNode, ReplicaSet: node.ReplicaSet, Shard: node.Shard, Node: node.Address}
	findings := make([]DiagnosticFinding, 0)
	if node.Uptime.Present && node.Uptime.Value >= 0 && node.Uptime.Value < int64(time.Hour/time.Second) {
		findings = append(findings, DiagnosticFinding{
			Code: "node.recent_restart", Severity: SeverityInfo, Scope: scope,
			Summary:  "节点启动时间不足一小时",
			Evidence: map[string]any{"uptimeSeconds": node.Uptime.Value},
		})
	}
	if node.ConnectionsUsed.Present && node.ConnectionsFree.Present {
		total := node.ConnectionsUsed.Value + node.ConnectionsFree.Value
		if total > 0 {
			ratio := float64(node.ConnectionsFree.Value) / float64(total)
			if ratio < 0.05 {
				findings = append(findings, DiagnosticFinding{
					Code: "connection.headroom_critical", Severity: SeverityCritical, Scope: scope,
					Summary: "节点连接余量低于 5%", Evidence: map[string]any{"availableRatio": ratio},
				})
			} else if ratio < 0.10 {
				findings = append(findings, DiagnosticFinding{
					Code: "connection.headroom_low", Severity: SeverityWarning, Scope: scope,
					Summary: "节点连接余量低于 10%", Evidence: map[string]any{"availableRatio": ratio},
				})
			}
		}
	}
	if node.CacheMax.Present && node.CacheUsed.Present && node.CacheMax.Value > 0 {
		cacheRatio := float64(node.CacheUsed.Value) / float64(node.CacheMax.Value)
		if cacheRatio >= 0.90 && node.EvictionPressure.Present && node.EvictionPressure.Value > 0 &&
			node.QueueTotal.Present && node.QueueTotal.Value > 0 {
			findings = append(findings, DiagnosticFinding{
				Code: "storage.cache_pressure_inconclusive", Severity: SeverityInfo, Scope: scope,
				Summary:  "单快照显示高 cache 与排队，但累计 eviction 无法证明当前窗口持续增长",
				Evidence: map[string]any{"cacheRatio": cacheRatio, "evictionPressure": node.EvictionPressure.Value, "queue": node.QueueTotal.Value},
			})
		}
	}
	return findings
}

func memberWriteTime(member pkgmongo.RsMember) time.Time {
	if !member.LastAppliedWallTime.IsZero() {
		return member.LastAppliedWallTime
	}
	return member.OptimeDate
}
