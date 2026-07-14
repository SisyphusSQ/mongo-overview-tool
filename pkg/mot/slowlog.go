package mot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

const defaultSlowlogConcurrency = 5

type slowlogDatabaseLoader func(ctx context.Context, addr, db string, sort SlowlogSort) (DatabaseSlowlogSummary, bool, error)

// SlowlogSummary 返回慢日志聚合结果。
func (c *Client) SlowlogSummary(ctx context.Context, opts SlowlogOptions) (result *SlowlogSummaryResult, err error) {
	defer func() {
		err = mapContextError(err)
	}()
	if err := c.requireMemberConnectionURI(); err != nil {
		return nil, err
	}
	if opts.Sort == "" {
		opts.Sort = SlowlogSortCount
	}
	if !isValidSlowlogSort(opts.Sort) {
		return nil, invalidOptions("invalid slowlog sort %q", opts.Sort)
	}

	cluster, err := pkgmongo.DetectCluster(ctx, c.conn)
	if err != nil {
		return nil, err
	}
	result = &SlowlogSummaryResult{ClusterType: convertClusterType(cluster.Type)}
	if gate, allowed := diagnosticCapabilityGate("slowlog_insight", result.ClusterType, cluster.MaxWireVersion, true); !allowed {
		result.CollectorStatuses = []CollectorStatus{gate}
		return result, nil
	}
	var collectorErrors []error
	switch cluster.Type {
	case pkgmongo.ClusterRepl:
		rs, statuses, collectErrors := c.replicaSetSlowlogSummary(ctx, c.conn, opts)
		result.ReplicaSets = append(result.ReplicaSets, rs)
		result.CollectorStatuses = append(result.CollectorStatuses, statuses...)
		collectorErrors = append(collectorErrors, collectErrors...)
	case pkgmongo.ClusterShard:
		shards, err := c.conn.ListShards(ctx)
		if err != nil {
			return nil, err
		}
		for _, shard := range shards.Shards {
			if contextError(ctx) != nil {
				collectorErrors = append(collectorErrors, contextError(ctx))
				break
			}
			replicaSet, addresses, err := parseShardHost(shard.Host)
			if err != nil {
				collectorErrors = append(collectorErrors, err)
				result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("slowlog_insight", FindingScope{Type: ScopeReplicaSet, Shard: shard.Id}, err))
				continue
			}
			conn, err := c.connectAddress(ctx, addresses, derivedConnectionOptions{
				ReplicaSet: replicaSet,
				Direct:     boolPointer(false),
			})
			if err != nil {
				collectorErrors = append(collectorErrors, err)
				result.CollectorStatuses = append(result.CollectorStatuses, failedCollectorStatus("slowlog_insight", FindingScope{Type: ScopeReplicaSet, Shard: shard.Id, ReplicaSet: replicaSet}, err))
				continue
			}
			rs, statuses, collectErrors := c.replicaSetSlowlogSummary(ctx, conn, opts)
			c.closeDerivedConnection(ctx, conn)
			if rs.Name == "" {
				rs.Name = shard.Id
			}
			result.ReplicaSets = append(result.ReplicaSets, rs)
			result.CollectorStatuses = append(result.CollectorStatuses, statuses...)
			collectorErrors = append(collectorErrors, collectErrors...)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedTopology, cluster.Type)
	}
	for _, replicaSet := range result.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				result.Findings = append(result.Findings, database.Findings...)
			}
		}
	}
	sanitizeAndSortFindings(result.Findings)
	sortCollectorStatuses(result.CollectorStatuses)
	if len(collectorErrors) > 0 {
		if len(result.ReplicaSets) == 0 {
			return result, errors.Join(collectorErrors...)
		}
		return result, newDiagnosticPartialError("slowlog", result, errors.Join(collectorErrors...))
	}
	return result, nil
}

// SlowlogDetail 返回单个 queryHash 的原始慢日志文档和索引信息。
func (c *Client) SlowlogDetail(ctx context.Context, db, queryHash string) (result *SlowlogDetailResult, err error) {
	defer func() {
		err = mapContextError(err)
	}()
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	if db == "" {
		return nil, invalidOptions("database is required")
	}
	if queryHash == "" {
		return nil, invalidOptions("query hash is required")
	}
	if strings.TrimSpace(c.uri) == "" {
		return slowlogDetailFromConnection(ctx, c.conn, db, queryHash)
	}

	summary, err := c.SlowlogSummary(ctx, SlowlogOptions{
		Databases:   []string{db},
		Sort:        SlowlogSortCount,
		Concurrency: defaultSlowlogConcurrency,
	})
	if err != nil {
		return nil, err
	}
	address := findSlowlogAddress(summary, db, queryHash)
	if address == "" {
		return nil, drivermongo.ErrNoDocuments
	}
	conn, err := c.connectAddress(ctx, address, derivedConnectionOptions{
		Database: db,
		Direct:   boolPointer(true),
	})
	if err != nil {
		return nil, err
	}
	defer c.closeDerivedConnection(ctx, conn)
	return slowlogDetailFromConnection(ctx, conn, db, queryHash)
}

func findSlowlogAddress(summary *SlowlogSummaryResult, db, queryHash string) string {
	if summary == nil {
		return ""
	}
	for _, replicaSet := range summary.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				if database.Database != db {
					continue
				}
				for _, item := range database.Items {
					if item.QueryHash == queryHash {
						return host.Address
					}
				}
			}
		}
	}
	return ""
}

func slowlogDetailFromConnection(ctx context.Context, conn *pkgmongo.Conn, db, queryHash string) (*SlowlogDetailResult, error) {
	slow, err := conn.GetSlowDetail(ctx, db, queryHash)
	if err != nil {
		return nil, err
	}
	namespace, ok := slow["ns"].(string)
	if !ok || namespace == "" {
		return nil, fmt.Errorf("%w: slowlog ns is missing", ErrInvalidOptions)
	}
	parts := strings.SplitN(namespace, ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, fmt.Errorf("%w: invalid slowlog namespace %s", ErrInvalidOptions, namespace)
	}

	cur, err := conn.Client.Database(db).Collection(parts[1]).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = cur.Close(closeCtx)
	}()

	var indexes []bson.M
	if err := cur.All(ctx, &indexes); err != nil {
		return nil, err
	}
	return &SlowlogDetailResult{
		Namespace: namespace,
		Slowlog:   slow,
		Indexes:   indexes,
	}, nil
}

func (c *Client) replicaSetSlowlogSummary(ctx context.Context, conn *pkgmongo.Conn, opts SlowlogOptions) (ReplicaSetSlowlogSummary, []CollectorStatus, []error) {
	rsStatus, err := conn.RsStatus(ctx)
	if err != nil {
		scope := FindingScope{Type: ScopeReplicaSet}
		status := failedCollectorStatus("slowlog_insight", scope, err)
		if status.State == CapabilityUnauthorized || status.State == CapabilityUnsupported {
			return ReplicaSetSlowlogSummary{}, []CollectorStatus{status}, nil
		}
		return ReplicaSetSlowlogSummary{}, []CollectorStatus{status}, []error{err}
	}
	result := ReplicaSetSlowlogSummary{Name: rsStatus.Set}
	dbs, err := conn.Client.ListDatabaseNames(ctx, bson.M{})
	if err != nil {
		scope := FindingScope{Type: ScopeReplicaSet, ReplicaSet: rsStatus.Set}
		status := failedCollectorStatus("slowlog_insight", scope, err)
		if isUnauthorizedError(err) {
			return result, []CollectorStatus{status}, nil
		}
		return result, []CollectorStatus{status}, []error{err}
	}
	filteredDBs := make([]string, 0, len(dbs))
	for _, db := range dbs {
		if slices.Contains(systemDatabases, db) {
			continue
		}
		if len(opts.Databases) != 0 && !slices.Contains(opts.Databases, db) {
			continue
		}
		filteredDBs = append(filteredDBs, db)
	}
	var statuses []CollectorStatus
	var collectorErrors []error
	for _, member := range rsStatus.Members {
		if member.State != pkgmongo.StatePrimary && member.State != pkgmongo.StateSecondary {
			continue
		}
		host := HostSlowlogSummary{
			Address: member.Name,
			State:   member.State.String(),
		}
		var hostStatuses []CollectorStatus
		var hostErrors []error
		host.Databases, hostStatuses, hostErrors = collectSlowlogDatabaseSummariesPartial(
			ctx,
			member.Name,
			rsStatus.Set,
			filteredDBs,
			opts.Sort,
			opts.Concurrency,
			c.databaseSlowlogSummary,
		)
		statuses = append(statuses, hostStatuses...)
		collectorErrors = append(collectorErrors, hostErrors...)
		result.Hosts = append(result.Hosts, host)
	}
	return result, statuses, collectorErrors
}

func collectSlowlogDatabaseSummariesPartial(ctx context.Context, addr, replicaSet string, dbs []string, sortValue SlowlogSort, concurrency int, load slowlogDatabaseLoader) ([]DatabaseSlowlogSummary, []CollectorStatus, []error) {
	limit := concurrency
	if limit <= 0 {
		limit = defaultSlowlogConcurrency
	}
	type slot struct {
		summary DatabaseSlowlogSummary
		ok      bool
		status  CollectorStatus
		err     error
	}
	slots := make([]slot, len(dbs))
	group, groupCtx := errgroup.WithContext(ctx)
	semaphoreLimit := semaphore.NewWeighted(int64(limit))
	var dispatchErr error
	for index, database := range dbs {
		if acquireErr := acquireDiagnosticSlot(groupCtx, semaphoreLimit); acquireErr != nil {
			dispatchErr = acquireErr
			break
		}
		index, database := index, database
		group.Go(func() error {
			defer semaphoreLimit.Release(1)
			scope := FindingScope{Type: ScopeDatabase, ReplicaSet: replicaSet, Node: addr, Database: database}
			summary, ok, err := load(groupCtx, addr, database, sortValue)
			item := slot{summary: summary, ok: ok, err: err}
			switch {
			case err != nil:
				item.status = failedCollectorStatus("slowlog_insight", scope, err)
			case !ok:
				item.status = CollectorStatus{Name: "slowlog_insight", State: CapabilitySkipped, Scope: scope, ReasonCode: "profiler_unavailable", Message: "system.profile 不存在或没有可聚合记录"}
			default:
				item.status = CollectorStatus{Name: "slowlog_insight", State: CapabilitySupported, Scope: scope}
			}
			slots[index] = item
			return nil
		})
	}
	_ = group.Wait()
	var summaries []DatabaseSlowlogSummary
	var statuses []CollectorStatus
	var collectorErrors []error
	if dispatchErr != nil {
		collectorErrors = append(collectorErrors, dispatchErr)
	}
	for _, item := range slots {
		if item.status.Name != "" {
			statuses = append(statuses, item.status)
		}
		if item.ok {
			summaries = append(summaries, item.summary)
		}
		if item.err != nil && !isUnauthorizedError(item.err) && !isUnsupportedDiagnosticError(item.err) {
			collectorErrors = append(collectorErrors, item.err)
		}
	}
	return summaries, statuses, collectorErrors
}

func collectSlowlogDatabaseSummaries(
	ctx context.Context,
	addr string,
	dbs []string,
	sort SlowlogSort,
	concurrency int,
	load slowlogDatabaseLoader,
) ([]DatabaseSlowlogSummary, error) {
	if load == nil {
		return nil, invalidOptions("slowlog database loader is required")
	}
	limit := concurrency
	if limit <= 0 {
		limit = defaultSlowlogConcurrency
	}

	type summarySlot struct {
		summary DatabaseSlowlogSummary
		ok      bool
	}
	slots := make([]summarySlot, len(dbs))
	group, groupCtx := errgroup.WithContext(ctx)
	semaphoreLimit := semaphore.NewWeighted(int64(limit))
	var dispatchErr error
	for i, db := range dbs {
		if acquireErr := acquireDiagnosticSlot(groupCtx, semaphoreLimit); acquireErr != nil {
			dispatchErr = acquireErr
			break
		}
		i, db := i, db
		group.Go(func() error {
			defer semaphoreLimit.Release(1)
			summary, ok, err := load(groupCtx, addr, db, sort)
			if err != nil {
				return err
			}
			slots[i] = summarySlot{summary: summary, ok: ok}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	if dispatchErr != nil {
		return nil, dispatchErr
	}

	result := make([]DatabaseSlowlogSummary, 0, len(slots))
	for _, slot := range slots {
		if slot.ok {
			result = append(result, slot.summary)
		}
	}
	return result, nil
}

func (c *Client) databaseSlowlogSummary(ctx context.Context, addr, db string, sort SlowlogSort) (DatabaseSlowlogSummary, bool, error) {
	conn, err := c.connectAddress(ctx, addr, derivedConnectionOptions{
		Database: db,
		Direct:   boolPointer(true),
	})
	if err != nil {
		return DatabaseSlowlogSummary{}, false, err
	}
	defer c.closeDerivedConnection(ctx, conn)

	colls, err := conn.Client.Database(db).ListCollectionNames(ctx, bson.D{{Key: "name", Value: "system.profile"}})
	if err != nil {
		return DatabaseSlowlogSummary{}, false, err
	}
	if len(colls) == 0 {
		return DatabaseSlowlogSummary{}, false, nil
	}
	logs, err := conn.GetSlowLogView(ctx, db, string(sort))
	if err != nil {
		return DatabaseSlowlogSummary{}, false, err
	}
	if len(logs) == 0 {
		return DatabaseSlowlogSummary{}, false, nil
	}

	summary := DatabaseSlowlogSummary{Database: db}
	plansByQuery := make(map[string]string)
	for i, log := range logs {
		item, findings := convertSlowlogView(*log)
		summary.Items = append(summary.Items, item)
		summary.Findings = append(summary.Findings, findings...)
		summary.Total += log.Cnt
		planKey := log.Ns + "\x00" + log.QueryHash
		if previous, exists := plansByQuery[planKey]; exists && previous != log.PlanSummary {
			summary.Findings = append(summary.Findings, DiagnosticFinding{Code: "query.plan_changed", Severity: SeverityInfo, Scope: FindingScope{Type: ScopeNamespace, Namespace: log.Ns}, Summary: "同一 query hash 在观察窗口内出现多个 plan summary", Evidence: map[string]any{"queryHash": log.QueryHash}})
		} else if !exists {
			plansByQuery[planKey] = log.PlanSummary
		}
		if i == 0 || log.MinTs.Before(summary.FirstTime) {
			summary.FirstTime = log.MinTs
		}
		if i == 0 || log.MaxTs.After(summary.LastTime) {
			summary.LastTime = log.MaxTs
		}
	}
	return summary, true, nil
}

func convertSlowlogView(log pkgmongo.SlowlogView) (SlowlogSummaryItem, []DiagnosticFinding) {
	item := SlowlogSummaryItem{
		Namespace: log.Ns, Operation: log.Op, QueryHash: log.QueryHash,
		Count: log.Cnt, MaxMillis: log.MaxMills, MinMillis: log.MinMills, MaxDocs: log.MaxDocs,
		FirstTime: log.MinTs, LastTime: log.MaxTs, PlanSummary: log.PlanSummary,
		MaxKeysExamined: log.MaxKeysExamined, MaxDocsExamined: log.MaxDocsExamined,
		MaxDocsReturned: log.MaxDocsReturned, MaxPlanningMicros: log.MaxPlanningMicros,
		MaxCPUNanos: log.MaxCPUNanos, ErrorCount: log.ErrorCount,
		CollectionScanCount: log.CollectionScanCount,
	}
	for _, appName := range log.AppNames {
		if appName != "" {
			item.AppNames = append(item.AppNames, anonymizeAppName(appName))
		}
	}
	sort.Strings(item.AppNames)
	item.AppNames = slices.Compact(item.AppNames)
	if len(item.AppNames) > 10 {
		item.AppNames = item.AppNames[:10]
	}
	item.WorstDocsToReturned = safeExaminedRatio(log.MaxDocsExamined, log.MaxDocsReturned)
	item.WorstKeysToReturned = safeExaminedRatio(log.MaxKeysExamined, log.MaxDocsReturned)

	scope := FindingScope{Type: ScopeNamespace, Namespace: log.Ns}
	findings := make([]DiagnosticFinding, 0)
	if log.CollectionScanCount > 0 || strings.Contains(strings.ToUpper(log.PlanSummary), "COLLSCAN") {
		findings = append(findings, DiagnosticFinding{
			Code: "query.collection_scan", Severity: SeverityWarning, Scope: scope,
			Summary:  "Profiler 记录中出现 collection scan",
			Evidence: map[string]any{"count": log.CollectionScanCount, "queryHash": log.QueryHash},
		})
	}
	if item.WorstDocsToReturned != nil && *item.WorstDocsToReturned >= 100 {
		findings = append(findings, DiagnosticFinding{
			Code: "query.docs_examined_high", Severity: SeverityWarning, Scope: scope,
			Summary:  "扫描文档数显著高于返回文档数",
			Evidence: map[string]any{"ratio": *item.WorstDocsToReturned, "queryHash": log.QueryHash},
		})
	}
	if log.MaxDocsExamined != nil && *log.MaxDocsExamined >= 100 && log.MaxDocsReturned != nil && *log.MaxDocsReturned == 0 {
		findings = append(findings, DiagnosticFinding{
			Code: "query.zero_return_scan", Severity: SeverityWarning, Scope: scope,
			Summary:  "查询扫描了大量文档但没有返回结果",
			Evidence: map[string]any{"docsExamined": *log.MaxDocsExamined, "queryHash": log.QueryHash},
		})
	}
	if log.ErrorCount > 0 {
		findings = append(findings, DiagnosticFinding{
			Code: "query.error_observed", Severity: SeverityWarning, Scope: scope,
			Summary:  "Profiler 记录中观察到失败操作",
			Evidence: map[string]any{"count": log.ErrorCount, "queryHash": log.QueryHash},
		})
	}
	if log.MaxPlanningMicros != nil && log.MaxMills > 0 && *log.MaxPlanningMicros >= log.MaxMills*500 {
		findings = append(findings, DiagnosticFinding{
			Code: "query.planning_time_high", Severity: SeverityInfo, Scope: scope,
			Summary:  "查询规划耗时占总耗时比例较高",
			Evidence: map[string]any{"planningMicros": *log.MaxPlanningMicros, "maxMillis": log.MaxMills},
		})
	}
	return item, findings
}

func safeExaminedRatio(examined, returned *int64) *float64 {
	if examined == nil || returned == nil || *returned <= 0 {
		return nil
	}
	ratio := float64(*examined) / float64(*returned)
	return &ratio
}

func isValidSlowlogSort(sort SlowlogSort) bool {
	switch sort {
	case SlowlogSortCount, SlowlogSortMaxMillis, SlowlogSortMaxDocs:
		return true
	default:
		return false
	}
}
