package mot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

type IndexConsistencyStrategy string

const (
	IndexConsistencyDirectListIndexes IndexConsistencyStrategy = "direct_list_indexes"
	IndexConsistencyIndexStats        IndexConsistencyStrategy = "index_stats"
	IndexConsistencyMetadataCheck     IndexConsistencyStrategy = "check_metadata_consistency"
)

type IndexConsistencyState string

const (
	IndexConsistencyConsistent   IndexConsistencyState = "consistent"
	IndexConsistencyInconsistent IndexConsistencyState = "inconsistent"
	IndexConsistencyInconclusive IndexConsistencyState = "inconclusive"
	IndexConsistencySkipped      IndexConsistencyState = "skipped"
)

type IndexConsistencyCoverage string

const (
	IndexConsistencyCoverageComplete   IndexConsistencyCoverage = "complete"
	IndexConsistencyCoverageIncomplete IndexConsistencyCoverage = "incomplete"
	IndexConsistencyCoverageSkipped    IndexConsistencyCoverage = "skipped"
)

type IndexConsistencyDifference struct {
	Code            string          `json:"code"`
	IndexName       string          `json:"indexName,omitempty"`
	Shards          []string        `json:"shards,omitempty"`
	Key             []IndexKeyField `json:"key,omitempty"`
	Fingerprint     string          `json:"fingerprint,omitempty"`
	DifferingFields []string        `json:"differingFields,omitempty"`
	SourceType      string          `json:"sourceType,omitempty"`
}

type IndexConsistencyFallback struct {
	From       IndexConsistencyStrategy `json:"from"`
	To         IndexConsistencyStrategy `json:"to"`
	ReasonCode string                   `json:"reasonCode"`
}

type IndexConsistencySummary struct {
	Consistent   int `json:"consistent"`
	Inconsistent int `json:"inconsistent"`
	Inconclusive int `json:"inconclusive"`
	Skipped      int `json:"skipped"`
}

func summarizeIndexConsistency(collections []CollectionIndexAudit) IndexConsistencySummary {
	var result IndexConsistencySummary
	for _, collection := range collections {
		switch collection.State {
		case IndexConsistencyConsistent:
			result.Consistent++
		case IndexConsistencyInconsistent:
			result.Inconsistent++
		case IndexConsistencyInconclusive:
			result.Inconclusive++
		case IndexConsistencySkipped:
			result.Skipped++
		}
	}
	return result
}

type indexConsistencyEvaluation struct {
	State          IndexConsistencyState
	Coverage       IndexConsistencyCoverage
	ObservedShards []string
	Differences    []IndexConsistencyDifference
	Findings       []DiagnosticFinding
}

type indexShardTarget struct {
	Shard      string
	ReplicaSet string
	Addresses  string
}

type indexCollectionVisibility struct {
	Shards      []string
	IndexBuilds []string
}

type indexConsistencySource interface {
	ServerVersion(context.Context) (string, error)
	Shards(context.Context) (map[string]indexShardTarget, error)
	Routing(context.Context, indexCollectionRef) (pkgmongo.IndexRoutingSnapshot, error)
	Visibility(context.Context, indexCollectionRef) (indexCollectionVisibility, error)
	Metadata(context.Context, string, string) ([]pkgmongo.MetadataIndexInconsistency, error)
	Stats(context.Context, indexCollectionRef) ([]pkgmongo.CanonicalIndexDefinition, error)
	Direct(context.Context, indexCollectionRef, indexShardTarget) ([]pkgmongo.CanonicalIndexDefinition, error)
}

type officialConsistencyScope struct {
	issues        []pkgmongo.MetadataIndexInconsistency
	err           error
	databaseScope bool
	pending       bool
}

type indexRoutingOutcome struct {
	snapshot pkgmongo.IndexRoutingSnapshot
	err      error
}

func collectIndexConsistency(ctx context.Context, refs []indexCollectionRef, opts IndexAuditOptions, source indexConsistencySource) ([]CollectionIndexAudit, []CollectorStatus, []error) {
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = defaultOverviewNodeConcurrency
	}
	routings := collectIndexRoutingOutcomes(ctx, refs, concurrency, source)
	hasSharded := false
	for _, outcome := range routings {
		if outcome.err == nil && outcome.snapshot.Sharded {
			hasSharded = true
			break
		}
	}

	var strategy IndexConsistencyStrategy
	var targets map[string]indexShardTarget
	var targetErr error
	var unsupportedStatus *CollectorStatus
	if hasSharded {
		version, versionErr := source.ServerVersion(ctx)
		if versionErr != nil {
			targetErr = versionErr
		} else if selected, ok := indexConsistencyStrategyForVersion(version); ok {
			strategy = selected
			targets, targetErr = source.Shards(ctx)
		} else {
			status := CollectorStatus{
				Name: "index_consistency", State: CapabilityUnsupported, Scope: FindingScope{Type: ScopeCluster},
				ReasonCode: "unsupported_server_version", Message: "MongoDB 服务器版本不在 3.4-7.x 支持范围内",
			}
			unsupportedStatus = &status
		}
	}

	databaseScopes := make(map[string]officialConsistencyScope)
	if strategy == IndexConsistencyMetadataCheck && targetErr == nil && len(opts.Collections) == 0 {
		shardedDatabases := make(map[string]struct{})
		for _, ref := range refs {
			outcome := routings[ref.Database+"."+ref.Collection]
			if ref.Type == "collection" && outcome.err == nil && outcome.snapshot.Sharded {
				shardedDatabases[ref.Database] = struct{}{}
			}
		}
		for _, database := range mapKeys(shardedDatabases) {
			issues, collectErr := source.Metadata(ctx, database, "")
			databaseScopes[database] = officialConsistencyScope{issues: issues, err: collectErr, databaseScope: true}
			if ctx.Err() != nil {
				break
			}
		}
	}

	limit := semaphore.NewWeighted(int64(concurrency))
	group, groupCtx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	var collections []CollectionIndexAudit
	var statuses []CollectorStatus
	var collectorErrors []error
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
			var official officialConsistencyScope
			if strategy == IndexConsistencyMetadataCheck {
				if len(opts.Collections) == 0 {
					official = databaseScopes[ref.Database]
				} else {
					official.pending = true
				}
			}
			outcome := routings[ref.Database+"."+ref.Collection]
			collection, itemStatuses, itemErrors := auditIndexConsistencyCollection(groupCtx, ref, outcome, strategy, targets, targetErr, unsupportedStatus, official, source)
			mu.Lock()
			collections = append(collections, collection)
			statuses = append(statuses, itemStatuses...)
			collectorErrors = append(collectorErrors, itemErrors...)
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	if unsupportedStatus != nil {
		statuses = append(statuses, *unsupportedStatus)
	}
	sort.SliceStable(collections, func(i, j int) bool { return collections[i].Namespace < collections[j].Namespace })
	sortCollectorStatuses(statuses)
	return collections, statuses, collectorErrors
}

func collectIndexRoutingOutcomes(ctx context.Context, refs []indexCollectionRef, concurrency int, source indexConsistencySource) map[string]indexRoutingOutcome {
	result := make(map[string]indexRoutingOutcome)
	limit := semaphore.NewWeighted(int64(concurrency))
	group, groupCtx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	var acquireErr error
	for _, ref := range refs {
		if ref.Type != "collection" {
			continue
		}
		if err := acquireDiagnosticSlot(groupCtx, limit); err != nil {
			acquireErr = err
			break
		}
		ref := ref
		group.Go(func() error {
			defer limit.Release(1)
			snapshot, err := source.Routing(groupCtx, ref)
			mu.Lock()
			result[ref.Database+"."+ref.Collection] = indexRoutingOutcome{snapshot: snapshot, err: err}
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	if acquireErr == nil && ctx.Err() != nil {
		acquireErr = mapContextError(ctx.Err())
	}
	for _, ref := range refs {
		if ref.Type != "collection" {
			continue
		}
		namespace := ref.Database + "." + ref.Collection
		if _, exists := result[namespace]; !exists {
			if acquireErr == nil {
				acquireErr = errors.New("routing collector did not run")
			}
			result[namespace] = indexRoutingOutcome{err: acquireErr}
		}
	}
	return result
}

func auditIndexConsistencyCollection(ctx context.Context, ref indexCollectionRef, routingOutcome indexRoutingOutcome, strategy IndexConsistencyStrategy, targets map[string]indexShardTarget, targetErr error, unsupportedStatus *CollectorStatus, official officialConsistencyScope, source indexConsistencySource) (CollectionIndexAudit, []CollectorStatus, []error) {
	namespace := ref.Database + "." + ref.Collection
	result := CollectionIndexAudit{Namespace: namespace, State: IndexConsistencySkipped, Coverage: IndexConsistencyCoverageSkipped}
	if ref.Type != "collection" {
		status := consistencyStatus(namespace, strategy, CapabilitySkipped, "unsupported_collection_type")
		result.ConsistencyStatuses = []CollectorStatus{status}
		return result, result.ConsistencyStatuses, nil
	}

	if routingOutcome.err != nil {
		status := failedConsistencyStatus(namespace, strategy, routingOutcome.err)
		result.State, result.Coverage = IndexConsistencyInconclusive, IndexConsistencyCoverageIncomplete
		result.ConsistencyStatuses = []CollectorStatus{status}
		return result, result.ConsistencyStatuses, consistencyCollectorErrors(routingOutcome.err)
	}
	routing := routingOutcome.snapshot
	result.Sharded = routing.Sharded
	result.ExpectedShards = append([]string(nil), routing.ExpectedShards...)
	if !routing.Sharded {
		status := consistencyStatus(namespace, strategy, CapabilitySkipped, "unsharded_collection")
		result.ConsistencyStatuses = []CollectorStatus{status}
		return result, result.ConsistencyStatuses, nil
	}
	if unsupportedStatus != nil {
		status := *unsupportedStatus
		status.Scope = FindingScope{Type: ScopeNamespace, Namespace: namespace}
		result.ConsistencyStatuses = []CollectorStatus{status}
		return result, result.ConsistencyStatuses, nil
	}
	if targetErr != nil {
		result.State, result.Coverage = IndexConsistencyInconclusive, IndexConsistencyCoverageIncomplete
		result.Differences = []IndexConsistencyDifference{{Code: "index.consistency_inconclusive", Shards: append([]string(nil), routing.ExpectedShards...)}}
		result.Findings = []DiagnosticFinding{consistencyFinding(namespace, result.Differences[0], SeverityInfo)}
		status := failedConsistencyStatus(namespace, strategy, targetErr)
		result.ConsistencyStatuses = []CollectorStatus{status}
		return result, result.ConsistencyStatuses, consistencyCollectorErrors(targetErr)
	}
	if len(routing.ExpectedShards) == 0 {
		result.State, result.Coverage = IndexConsistencyInconclusive, IndexConsistencyCoverageIncomplete
		result.Differences = []IndexConsistencyDifference{{Code: "index.consistency_inconclusive"}}
		result.Findings = []DiagnosticFinding{consistencyFinding(namespace, result.Differences[0], SeverityInfo)}
		status := consistencyStatus(namespace, strategy, CapabilityFailed, "expected_shards_unavailable")
		result.ConsistencyStatuses = []CollectorStatus{status}
		return result, result.ConsistencyStatuses, nil
	}
	for _, shard := range routing.ExpectedShards {
		if _, exists := targets[shard]; !exists {
			result.State, result.Coverage = IndexConsistencyInconclusive, IndexConsistencyCoverageIncomplete
			result.Differences = []IndexConsistencyDifference{{Code: "index.consistency_inconclusive", Shards: []string{shard}}}
			result.Findings = []DiagnosticFinding{consistencyFinding(namespace, result.Differences[0], SeverityInfo)}
			status := consistencyStatus(namespace, strategy, CapabilityFailed, "shard_target_missing")
			result.ConsistencyStatuses = []CollectorStatus{status}
			return result, result.ConsistencyStatuses, nil
		}
	}

	if official.pending {
		official.issues, official.err = source.Metadata(ctx, ref.Database, ref.Collection)
		official.pending = false
	}
	visibility, observedErr := source.Visibility(ctx, ref)
	observed := visibility.Shards
	if observedErr != nil {
		observed = nil
	}
	result.ObservedShards = append([]string(nil), observed...)

	var evaluation indexConsistencyEvaluation
	var fallback *IndexConsistencyFallback
	var statuses []CollectorStatus
	var collectorErrors []error
	finalStrategy := strategy
	if strategy == IndexConsistencyMetadataCheck && official.err == nil {
		evaluation = evaluateOfficialIndexConsistency(namespace, routing.ExpectedShards, observed, metadataIssuesForNamespace(namespace, official))
		statuses = append(statuses, consistencyStatus(namespace, strategy, CapabilitySupported, "complete"))
	} else {
		if strategy == IndexConsistencyMetadataCheck {
			reason := consistencyReasonCode(official.err)
			statuses = append(statuses, failedConsistencyStatus(namespace, strategy, official.err))
			collectorErrors = append(collectorErrors, consistencyCollectorErrors(official.err)...)
			if ctx.Err() != nil || reason == "timeout" {
				evaluation = incompleteConsistencyEvaluation(namespace, routing.ExpectedShards, observed)
				return mergeConsistencyEvaluation(result, strategy, nil, evaluation, statuses), statuses, collectorErrors
			}
			fallback = &IndexConsistencyFallback{From: strategy, To: IndexConsistencyIndexStats, ReasonCode: reason}
			finalStrategy = IndexConsistencyIndexStats
		}
		evaluation, finalStrategy, fallback, statuses, collectorErrors = collectLegacyIndexConsistency(
			ctx, ref, routing.ExpectedShards, targets, visibility.IndexBuilds, finalStrategy, fallback, source, statuses, collectorErrors,
		)
	}

	if observedErr != nil || len(intersectExpectedShards(routing.ExpectedShards, observed)) != len(routing.ExpectedShards) {
		applyConsistencyCoverageGap(namespace, &evaluation, missingObservedShards(routing.ExpectedShards, observed))
		statuses = append(statuses, failedConsistencyVisibilityStatus(namespace, observedErr))
		collectorErrors = append(collectorErrors, consistencyCollectorErrors(observedErr)...)
	} else {
		statuses = append(statuses, CollectorStatus{
			Name: "index_consistency_visibility", State: CapabilitySupported,
			Scope: FindingScope{Type: ScopeNamespace, Namespace: namespace}, ReasonCode: "complete",
		})
	}
	applyCollectionIndexBuilds(namespace, &evaluation, visibility.IndexBuilds)
	result = mergeConsistencyEvaluation(result, finalStrategy, fallback, evaluation, statuses)
	return result, statuses, collectorErrors
}

func metadataIssuesForNamespace(namespace string, scope officialConsistencyScope) []pkgmongo.MetadataIndexInconsistency {
	if !scope.databaseScope {
		return scope.issues
	}
	result := make([]pkgmongo.MetadataIndexInconsistency, 0, len(scope.issues))
	for _, issue := range scope.issues {
		if issue.Namespace == namespace {
			result = append(result, issue)
		}
	}
	return result
}

func applyCollectionIndexBuilds(namespace string, evaluation *indexConsistencyEvaluation, builds []string) {
	if len(builds) == 0 {
		return
	}
	existing := make(map[string]struct{})
	for _, difference := range evaluation.Differences {
		if difference.Code == "index.build_in_progress" {
			existing[difference.IndexName] = struct{}{}
		}
	}
	for _, name := range builds {
		if name == "" {
			continue
		}
		if _, exists := existing[name]; exists {
			continue
		}
		difference := IndexConsistencyDifference{Code: "index.build_in_progress", IndexName: name}
		evaluation.Differences = append(evaluation.Differences, difference)
		evaluation.Findings = append(evaluation.Findings, consistencyFinding(namespace, difference, SeverityInfo))
		existing[name] = struct{}{}
	}
	if evaluation.State == IndexConsistencyConsistent {
		evaluation.State = IndexConsistencyInconclusive
	}
	normalizeAndSortConsistencyDifferences(evaluation.Differences)
	evaluation.Findings = consistencyFindingsForDifferences(namespace, evaluation.Differences)
}

func collectLegacyIndexConsistency(ctx context.Context, ref indexCollectionRef, expected []string, targets map[string]indexShardTarget, collectionBuilds []string, strategy IndexConsistencyStrategy, fallback *IndexConsistencyFallback, source indexConsistencySource, statuses []CollectorStatus, collectorErrors []error) (indexConsistencyEvaluation, IndexConsistencyStrategy, *IndexConsistencyFallback, []CollectorStatus, []error) {
	namespace := ref.Database + "." + ref.Collection
	if strategy == IndexConsistencyIndexStats {
		definitions, err := source.Stats(ctx, ref)
		observations := definitionsByShard(definitions)
		markBuildingDefinitions(observations, collectionBuilds)
		complete := err == nil && len(observedExpectedShards(expected, observations)) == len(expected)
		if complete {
			statuses = append(statuses, consistencyStatus(namespace, strategy, CapabilitySupported, "complete"))
			evaluation, confirmationErrors, attempted := confirmLegacyDifferences(ctx, ref, expected, targets, collectionBuilds, observations, source)
			collectorErrors = append(collectorErrors, consistencyCollectorErrors(confirmationErrors...)...)
			if attempted {
				if len(confirmationErrors) > 0 {
					statuses = append(statuses, failedConsistencyStatus(namespace, IndexConsistencyDirectListIndexes, errors.Join(confirmationErrors...)))
				} else {
					statuses = append(statuses, consistencyStatus(namespace, IndexConsistencyDirectListIndexes, CapabilitySupported, "confirmation_complete"))
				}
			}
			return evaluation, strategy, fallback, statuses, collectorErrors
		}
		reason := consistencyReasonCode(err)
		if err == nil {
			reason = "incomplete_coverage"
		}
		statuses = append(statuses, failedConsistencyStatusWithReason(namespace, strategy, err, reason))
		collectorErrors = append(collectorErrors, consistencyCollectorErrors(err)...)
		if ctx.Err() != nil || reason == "timeout" {
			return incompleteConsistencyEvaluation(namespace, expected, mapKeysFromDefinitions(observations)), strategy, fallback, statuses, collectorErrors
		}
		if fallback == nil {
			fallback = &IndexConsistencyFallback{From: strategy, To: IndexConsistencyDirectListIndexes, ReasonCode: reason}
		} else {
			fallback.To = IndexConsistencyDirectListIndexes
		}
		strategy = IndexConsistencyDirectListIndexes
	}

	first, directErrors := collectDirectDefinitions(ctx, ref, expected, targets, source, nil)
	markBuildingDefinitions(first, collectionBuilds)
	evaluation, confirmationErrors, _ := confirmLegacyDifferences(ctx, ref, expected, targets, collectionBuilds, first, source)
	allDirectErrors := append(directErrors, confirmationErrors...)
	collectorErrors = append(collectorErrors, consistencyCollectorErrors(allDirectErrors...)...)
	if len(allDirectErrors) > 0 {
		statuses = append(statuses, failedConsistencyStatus(namespace, strategy, errors.Join(allDirectErrors...)))
	} else {
		statuses = append(statuses, consistencyStatus(namespace, strategy, CapabilitySupported, "complete"))
	}
	return evaluation, strategy, fallback, statuses, collectorErrors
}

func confirmLegacyDifferences(ctx context.Context, ref indexCollectionRef, expected []string, targets map[string]indexShardTarget, collectionBuilds []string, first map[string][]pkgmongo.CanonicalIndexDefinition, source indexConsistencySource) (indexConsistencyEvaluation, []error, bool) {
	comparisonShards := observedExpectedShards(expected, first)
	if len(comparisonShards) < 2 {
		return evaluateLegacyIndexConsistency(ref.Database+"."+ref.Collection, expected, first, nil), nil, false
	}
	buildingNames, buildingSemantics := buildingIndexKeys(first)
	candidates := compareLegacyDefinitions(comparisonShards, first, buildingNames, buildingSemantics)
	if len(candidates) == 0 {
		return evaluateLegacyIndexConsistency(ref.Database+"."+ref.Collection, expected, first, nil), nil, false
	}
	if ctx.Err() != nil {
		return evaluateLegacyIndexConsistency(ref.Database+"."+ref.Collection, expected, first, nil), []error{ctx.Err()}, true
	}
	affected := affectedConsistencyShards(comparisonShards, first, candidates)
	second := cloneDefinitionObservations(first)
	for shard := range affected {
		delete(second, shard)
	}
	confirmed, confirmationErrors := collectDirectDefinitions(ctx, ref, comparisonShards, targets, source, affected)
	markBuildingDefinitions(confirmed, collectionBuilds)
	for shard, definitions := range confirmed {
		second[shard] = definitions
	}
	return evaluateLegacyIndexConsistency(ref.Database+"."+ref.Collection, expected, first, second), confirmationErrors, true
}

func markBuildingDefinitions(observations map[string][]pkgmongo.CanonicalIndexDefinition, builds []string) {
	if len(builds) == 0 {
		return
	}
	buildSet := make(map[string]struct{}, len(builds))
	for _, name := range builds {
		buildSet[name] = struct{}{}
	}
	for shard, definitions := range observations {
		for index := range definitions {
			if _, building := buildSet[definitions[index].Name]; building {
				definitions[index].Building = true
			}
		}
		observations[shard] = definitions
	}
}

func collectDirectDefinitions(ctx context.Context, ref indexCollectionRef, expected []string, targets map[string]indexShardTarget, source indexConsistencySource, only map[string]struct{}) (map[string][]pkgmongo.CanonicalIndexDefinition, []error) {
	result := make(map[string][]pkgmongo.CanonicalIndexDefinition)
	var collectorErrors []error
	for _, shard := range expected {
		if only != nil {
			if _, wanted := only[shard]; !wanted {
				continue
			}
		}
		if ctx.Err() != nil {
			collectorErrors = append(collectorErrors, ctx.Err())
			break
		}
		definitions, err := source.Direct(ctx, ref, targets[shard])
		if err != nil {
			collectorErrors = append(collectorErrors, err)
			continue
		}
		result[shard] = definitions
	}
	return result, collectorErrors
}

func definitionsByShard(definitions []pkgmongo.CanonicalIndexDefinition) map[string][]pkgmongo.CanonicalIndexDefinition {
	result := make(map[string][]pkgmongo.CanonicalIndexDefinition)
	for _, definition := range definitions {
		if definition.Shard == "" {
			continue
		}
		result[definition.Shard] = append(result[definition.Shard], definition)
	}
	return result
}

func affectedConsistencyShards(expected []string, first map[string][]pkgmongo.CanonicalIndexDefinition, differences []IndexConsistencyDifference) map[string]struct{} {
	result := make(map[string]struct{})
	for _, difference := range differences {
		for _, shard := range difference.Shards {
			result[shard] = struct{}{}
		}
		if difference.Code == "index.missing_on_shard" {
			for _, shard := range expected {
				if _, observed := first[shard]; observed {
					result[shard] = struct{}{}
				}
			}
		}
	}
	return result
}

func cloneDefinitionObservations(source map[string][]pkgmongo.CanonicalIndexDefinition) map[string][]pkgmongo.CanonicalIndexDefinition {
	result := make(map[string][]pkgmongo.CanonicalIndexDefinition, len(source))
	for shard, definitions := range source {
		result[shard] = append([]pkgmongo.CanonicalIndexDefinition(nil), definitions...)
	}
	return result
}

func incompleteConsistencyEvaluation(namespace string, expected, observed []string) indexConsistencyEvaluation {
	difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive", Shards: missingObservedShards(expected, observed)}
	return indexConsistencyEvaluation{
		State: IndexConsistencyInconclusive, Coverage: IndexConsistencyCoverageIncomplete,
		ObservedShards: intersectExpectedShards(expected, observed), Differences: []IndexConsistencyDifference{difference},
		Findings: []DiagnosticFinding{consistencyFinding(namespace, difference, SeverityInfo)},
	}
}

func applyConsistencyCoverageGap(namespace string, evaluation *indexConsistencyEvaluation, missing []string) {
	evaluation.Coverage = IndexConsistencyCoverageIncomplete
	if evaluation.State != IndexConsistencyInconsistent {
		evaluation.State = IndexConsistencyInconclusive
	}
	for _, difference := range evaluation.Differences {
		if difference.Code == "index.consistency_inconclusive" {
			return
		}
	}
	difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive", Shards: missing}
	evaluation.Differences = append(evaluation.Differences, difference)
	normalizeAndSortConsistencyDifferences(evaluation.Differences)
	evaluation.Findings = consistencyFindingsForDifferences(namespace, evaluation.Differences)
}

func mergeConsistencyEvaluation(result CollectionIndexAudit, strategy IndexConsistencyStrategy, fallback *IndexConsistencyFallback, evaluation indexConsistencyEvaluation, statuses []CollectorStatus) CollectionIndexAudit {
	result.State = evaluation.State
	result.Strategy = strategy
	result.Coverage = evaluation.Coverage
	result.Fallback = fallback
	result.ObservedShards = append([]string(nil), evaluation.ObservedShards...)
	result.Differences = append([]IndexConsistencyDifference(nil), evaluation.Differences...)
	result.Findings = append(result.Findings, evaluation.Findings...)
	result.ConsistencyStatuses = append([]CollectorStatus(nil), statuses...)
	sanitizeAndSortFindings(result.Findings)
	sortCollectorStatuses(result.ConsistencyStatuses)
	return result
}

func consistencyCapabilityName(strategy IndexConsistencyStrategy) string {
	switch strategy {
	case IndexConsistencyDirectListIndexes:
		return "index_consistency_direct"
	case IndexConsistencyIndexStats:
		return "index_consistency_index_stats"
	case IndexConsistencyMetadataCheck:
		return "index_consistency_metadata_check"
	default:
		return "index_consistency_visibility"
	}
}

func consistencyStatus(namespace string, strategy IndexConsistencyStrategy, state CapabilityState, reason string) CollectorStatus {
	return CollectorStatus{Name: consistencyCapabilityName(strategy), State: state, Scope: FindingScope{Type: ScopeNamespace, Namespace: namespace}, ReasonCode: reason}
}

func failedConsistencyStatus(namespace string, strategy IndexConsistencyStrategy, err error) CollectorStatus {
	return failedConsistencyStatusWithReason(namespace, strategy, err, consistencyReasonCode(err))
}

func failedConsistencyStatusWithReason(namespace string, strategy IndexConsistencyStrategy, err error, reason string) CollectorStatus {
	if err == nil {
		err = errors.New(reason)
	}
	status := failedCollectorStatus(consistencyCapabilityName(strategy), FindingScope{Type: ScopeNamespace, Namespace: namespace}, err)
	status.ReasonCode = reason
	return status
}

func failedConsistencyVisibilityStatus(namespace string, err error) CollectorStatus {
	scope := FindingScope{Type: ScopeNamespace, Namespace: namespace}
	if err == nil {
		return CollectorStatus{
			Name: "index_consistency_visibility", State: CapabilityFailed, Scope: scope,
			ReasonCode: "incomplete_coverage", Message: "collStats shard 可见范围不完整",
		}
	}
	return failedCollectorStatus("index_consistency_visibility", scope, err)
}

func consistencyReasonCode(err error) string {
	switch {
	case err == nil:
		return "incomplete_coverage"
	case errors.Is(err, pkgmongo.ErrIndexConsistencyFieldsMissing):
		return "missing_required_fields"
	case isUnauthorizedError(err):
		return "unauthorized"
	case isUnsupportedDiagnosticError(err):
		return "unsupported_version"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrCancelled):
		return "timeout"
	default:
		return "collector_failed"
	}
}

func consistencyCollectorErrors(values ...error) []error {
	var result []error
	for _, err := range values {
		if err == nil || isUnauthorizedError(err) || isUnsupportedDiagnosticError(err) {
			continue
		}
		result = append(result, err)
	}
	return result
}

func mapKeysFromDefinitions(values map[string][]pkgmongo.CanonicalIndexDefinition) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func evaluateLegacyIndexConsistency(namespace string, expected []string, first, second map[string][]pkgmongo.CanonicalIndexDefinition) indexConsistencyEvaluation {
	result := indexConsistencyEvaluation{
		Coverage:       IndexConsistencyCoverageComplete,
		ObservedShards: observedExpectedShards(expected, first),
	}
	if len(expected) == 0 {
		result.State = IndexConsistencyInconclusive
		result.Coverage = IndexConsistencyCoverageIncomplete
		result.Differences = append(result.Differences, IndexConsistencyDifference{Code: "index.consistency_inconclusive"})
		result.Findings = append(result.Findings, consistencyFinding(namespace, result.Differences[len(result.Differences)-1], SeverityInfo))
		return result
	}
	comparisonShards := result.ObservedShards
	if len(comparisonShards) != len(expected) {
		result.Coverage = IndexConsistencyCoverageIncomplete
		difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive", Shards: missingExpectedShards(expected, first)}
		result.Differences = append(result.Differences, difference)
		result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityInfo))
	}

	buildingNames, buildingSemantics := buildingIndexKeys(first)

	firstCandidates := compareLegacyDefinitions(comparisonShards, first, buildingNames, buildingSemantics)
	var stable []IndexConsistencyDifference
	var changed bool
	if len(firstCandidates) > 0 {
		if second == nil {
			changed = true
			result.Coverage = IndexConsistencyCoverageIncomplete
		} else if len(observedExpectedShards(comparisonShards, second)) != len(comparisonShards) {
			changed = true
			result.Coverage = IndexConsistencyCoverageIncomplete
		} else {
			secondBuildingNames, secondBuildingSemantics := buildingIndexKeys(second)
			for name := range secondBuildingNames {
				buildingNames[name] = struct{}{}
			}
			for fingerprint := range secondBuildingSemantics {
				buildingSemantics[fingerprint] = struct{}{}
			}
			secondCandidates := compareLegacyDefinitions(comparisonShards, second, buildingNames, buildingSemantics)
			confirmed := make(map[string]IndexConsistencyDifference, len(secondCandidates))
			for _, candidate := range secondCandidates {
				confirmed[consistencyDifferenceSignature(candidate)] = candidate
			}
			for _, candidate := range firstCandidates {
				if _, ok := confirmed[consistencyDifferenceSignature(candidate)]; ok {
					stable = append(stable, candidate)
				} else {
					changed = true
				}
			}
		}
	}
	for name := range buildingNames {
		difference := IndexConsistencyDifference{Code: "index.build_in_progress", IndexName: name}
		result.Differences = append(result.Differences, difference)
		result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityInfo))
	}

	for _, difference := range stable {
		result.Differences = append(result.Differences, difference)
		result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityWarning))
	}
	if changed {
		difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive"}
		result.Differences = append(result.Differences, difference)
		result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityInfo))
	}

	switch {
	case len(stable) > 0:
		result.State = IndexConsistencyInconsistent
	case len(buildingNames) > 0 || changed || result.Coverage == IndexConsistencyCoverageIncomplete:
		result.State = IndexConsistencyInconclusive
	default:
		result.State = IndexConsistencyConsistent
	}
	normalizeAndSortConsistencyDifferences(result.Differences)
	result.Findings = consistencyFindingsForDifferences(namespace, result.Differences)
	return result
}

func evaluateOfficialIndexConsistency(namespace string, expected, observed []string, issues []pkgmongo.MetadataIndexInconsistency) indexConsistencyEvaluation {
	result := indexConsistencyEvaluation{
		Coverage:       IndexConsistencyCoverageComplete,
		ObservedShards: intersectExpectedShards(expected, observed),
	}
	if len(expected) == 0 || len(result.ObservedShards) != len(expected) {
		result.Coverage = IndexConsistencyCoverageIncomplete
		difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive", Shards: missingObservedShards(expected, observed)}
		result.Differences = append(result.Differences, difference)
		result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityInfo))
	}

	deterministic := 0
	inconclusive := false
	for _, issue := range issues {
		if issue.Namespace != "" && issue.Namespace != namespace {
			continue
		}
		switch issue.SourceType {
		case "InconsistentIndex":
			mapped := false
			if len(issue.MissingFromShards) > 0 {
				difference := IndexConsistencyDifference{
					Code: "index.missing_on_shard", IndexName: issue.IndexName,
					Shards: append([]string(nil), issue.MissingFromShards...), Fingerprint: issue.Fingerprint, SourceType: issue.SourceType,
				}
				result.Differences = append(result.Differences, difference)
				result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityWarning))
				deterministic++
				mapped = true
			}
			if issue.PropertiesDiffer || len(issue.InconsistentFields) > 0 {
				difference := IndexConsistencyDifference{
					Code: "index.spec_mismatch", IndexName: issue.IndexName,
					Shards: append([]string(nil), expected...), DifferingFields: append([]string(nil), issue.InconsistentFields...),
					Fingerprint: issue.Fingerprint, SourceType: issue.SourceType,
				}
				result.Differences = append(result.Differences, difference)
				result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityWarning))
				deterministic++
				mapped = true
			}
			if !mapped {
				inconclusive = true
				difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive", IndexName: issue.IndexName, SourceType: issue.SourceType}
				result.Differences = append(result.Differences, difference)
				result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityInfo))
			}
		case "MissingShardKeyIndex", "RangeDeletionMissingShardKeyIndex":
			difference := IndexConsistencyDifference{
				Code: "index.shard_key_support_missing", Shards: nonEmptyStrings(issue.Shard), SourceType: issue.SourceType,
			}
			result.Differences = append(result.Differences, difference)
			result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityWarning))
			deterministic++
		default:
			if knownNonIndexMetadataSourceType(issue.SourceType) {
				continue
			}
			inconclusive = true
			difference := IndexConsistencyDifference{Code: "index.consistency_inconclusive", SourceType: issue.SourceType}
			result.Differences = append(result.Differences, difference)
			result.Findings = append(result.Findings, consistencyFinding(namespace, difference, SeverityInfo))
		}
	}

	switch {
	case deterministic > 0:
		result.State = IndexConsistencyInconsistent
	case result.Coverage == IndexConsistencyCoverageIncomplete || inconclusive:
		result.State = IndexConsistencyInconclusive
	default:
		result.State = IndexConsistencyConsistent
	}
	normalizeAndSortConsistencyDifferences(result.Differences)
	result.Findings = consistencyFindingsForDifferences(namespace, result.Differences)
	return result
}

func knownNonIndexMetadataSourceType(sourceType string) bool {
	switch sourceType {
	case "CollectionAuxiliaryMetadataMismatch", "CollectionOptionsMismatch", "CollectionUUIDMismatch",
		"CorruptedChunkShardKey", "CorruptedZoneShardKey", "HiddenShardedCollection", "MisplacedCollection",
		"MissingLocalCollection", "MissingRoutingTable", "RoutingTableMissingMaxKey", "RoutingTableMissingMinKey",
		"RoutingTableRangeGap", "RoutingTableRangeOverlap", "ShardCatalogCacheCollectionMetadataMismatch",
		"TrackedUnshardedCollectionHasInvalidKey", "TrackedUnshardedCollectionHasMultipleChunks", "ZonesRangeOverlap":
		return true
	default:
		return false
	}
}

func intersectExpectedShards(expected, observed []string) []string {
	observedSet := make(map[string]struct{}, len(observed))
	for _, shard := range observed {
		observedSet[shard] = struct{}{}
	}
	var result []string
	for _, shard := range expected {
		if _, ok := observedSet[shard]; ok {
			result = append(result, shard)
		}
	}
	return result
}

func missingObservedShards(expected, observed []string) []string {
	observedSet := make(map[string]struct{}, len(observed))
	for _, shard := range observed {
		observedSet[shard] = struct{}{}
	}
	var result []string
	for _, shard := range expected {
		if _, ok := observedSet[shard]; !ok {
			result = append(result, shard)
		}
	}
	return result
}

func nonEmptyStrings(values ...string) []string {
	var result []string
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func compareLegacyDefinitions(expected []string, observations map[string][]pkgmongo.CanonicalIndexDefinition, buildingNames, buildingSemantics map[string]struct{}) []IndexConsistencyDifference {
	bySemantic := make(map[string]map[string][]pkgmongo.CanonicalIndexDefinition)
	byName := make(map[string]map[string]pkgmongo.CanonicalIndexDefinition)
	for shard, definitions := range observations {
		for _, definition := range definitions {
			if _, building := buildingNames[definition.Name]; building {
				continue
			}
			if _, building := buildingSemantics[definition.SemanticFingerprint]; building {
				continue
			}
			if bySemantic[definition.SemanticFingerprint] == nil {
				bySemantic[definition.SemanticFingerprint] = make(map[string][]pkgmongo.CanonicalIndexDefinition)
			}
			bySemantic[definition.SemanticFingerprint][shard] = append(bySemantic[definition.SemanticFingerprint][shard], definition)
			if byName[definition.Name] == nil {
				byName[definition.Name] = make(map[string]pkgmongo.CanonicalIndexDefinition)
			}
			byName[definition.Name][shard] = definition
		}
	}

	handledNames := make(map[string]struct{})
	var result []IndexConsistencyDifference
	for semantic, shards := range bySemantic {
		if len(shards) != len(expected) {
			continue
		}
		names := make(map[string]struct{})
		var reference pkgmongo.CanonicalIndexDefinition
		for _, shard := range expected {
			definitions := shards[shard]
			for _, definition := range definitions {
				names[definition.Name] = struct{}{}
				if reference.Name == "" {
					reference = definition
				}
			}
		}
		if len(names) <= 1 {
			continue
		}
		nameValues := mapKeys(names)
		for _, name := range nameValues {
			handledNames[name] = struct{}{}
		}
		result = append(result, IndexConsistencyDifference{
			Code: "index.name_mismatch", IndexName: strings.Join(nameValues, ","),
			Shards: append([]string(nil), expected...), Key: publicIndexKey(reference.Key), Fingerprint: semantic,
		})
	}

	for name, shards := range byName {
		if _, handled := handledNames[name]; handled {
			continue
		}
		var reference pkgmongo.CanonicalIndexDefinition
		for _, shard := range expected {
			if definition, exists := shards[shard]; exists {
				reference = definition
				break
			}
		}
		if len(shards) != len(expected) {
			result = append(result, IndexConsistencyDifference{
				Code: "index.missing_on_shard", IndexName: name, Shards: missingDefinitionShards(expected, shards),
				Key: publicIndexKey(reference.Key), Fingerprint: reference.SemanticFingerprint,
			})
			continue
		}
		semantics := make(map[string]struct{})
		definitions := make([]pkgmongo.CanonicalIndexDefinition, 0, len(shards))
		for _, definition := range shards {
			semantics[definition.SemanticFingerprint] = struct{}{}
			definitions = append(definitions, definition)
		}
		if len(semantics) > 1 {
			result = append(result, IndexConsistencyDifference{
				Code: "index.spec_mismatch", IndexName: name, Shards: append([]string(nil), expected...),
				Key: publicIndexKey(reference.Key), Fingerprint: aggregateFingerprints(mapKeys(semantics)),
				DifferingFields: differingIndexFields(definitions),
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return consistencyDifferenceSignature(result[i]) < consistencyDifferenceSignature(result[j])
	})
	return result
}

func buildingIndexKeys(observations map[string][]pkgmongo.CanonicalIndexDefinition) (map[string]struct{}, map[string]struct{}) {
	names := make(map[string]struct{})
	semantics := make(map[string]struct{})
	for _, definitions := range observations {
		for _, definition := range definitions {
			if definition.Building {
				names[definition.Name] = struct{}{}
				semantics[definition.SemanticFingerprint] = struct{}{}
			}
		}
	}
	return names, semantics
}

func observedExpectedShards(expected []string, observations map[string][]pkgmongo.CanonicalIndexDefinition) []string {
	var result []string
	for _, shard := range expected {
		if _, ok := observations[shard]; ok {
			result = append(result, shard)
		}
	}
	return result
}

func missingExpectedShards(expected []string, observations map[string][]pkgmongo.CanonicalIndexDefinition) []string {
	var result []string
	for _, shard := range expected {
		if _, ok := observations[shard]; !ok {
			result = append(result, shard)
		}
	}
	return result
}

func missingDefinitionShards(expected []string, definitions map[string]pkgmongo.CanonicalIndexDefinition) []string {
	var result []string
	for _, shard := range expected {
		if _, ok := definitions[shard]; !ok {
			result = append(result, shard)
		}
	}
	return result
}

func differingIndexFields(definitions []pkgmongo.CanonicalIndexDefinition) []string {
	fields := make(map[string]struct{})
	for _, definition := range definitions {
		for field := range definition.FieldFingerprints {
			if field != "name" {
				fields[field] = struct{}{}
			}
		}
	}
	var result []string
	for field := range fields {
		values := make(map[string]struct{})
		for _, definition := range definitions {
			value, ok := definition.FieldFingerprints[field]
			if !ok {
				value = "<missing>"
			}
			values[value] = struct{}{}
		}
		if len(values) > 1 {
			result = append(result, field)
		}
	}
	sort.Strings(result)
	return result
}

func publicIndexKey(key []pkgmongo.IndexKeySnapshot) []IndexKeyField {
	result := make([]IndexKeyField, 0, len(key))
	for _, field := range key {
		result = append(result, IndexKeyField{Field: field.Field, Order: field.Order})
	}
	return result
}

func aggregateFingerprints(values []string) string {
	sort.Strings(values)
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:])
}

func consistencyDifferenceSignature(difference IndexConsistencyDifference) string {
	key := make([]string, 0, len(difference.Key))
	for _, field := range difference.Key {
		key = append(key, field.Field+"="+field.Order)
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", difference.Code, difference.IndexName,
		strings.Join(difference.Shards, ","), strings.Join(key, ","), difference.Fingerprint,
		strings.Join(difference.DifferingFields, ","), difference.SourceType)
}

func normalizeAndSortConsistencyDifferences(differences []IndexConsistencyDifference) {
	for index := range differences {
		sort.Strings(differences[index].Shards)
		sort.Strings(differences[index].DifferingFields)
	}
	sort.SliceStable(differences, func(i, j int) bool {
		return consistencyDifferenceSignature(differences[i]) < consistencyDifferenceSignature(differences[j])
	})
}

func consistencyFindingsForDifferences(namespace string, differences []IndexConsistencyDifference) []DiagnosticFinding {
	result := make([]DiagnosticFinding, 0, len(differences))
	for _, difference := range differences {
		severity := SeverityInfo
		switch difference.Code {
		case "index.missing_on_shard", "index.name_mismatch", "index.spec_mismatch", "index.shard_key_support_missing":
			severity = SeverityWarning
		}
		result = append(result, consistencyFinding(namespace, difference, severity))
	}
	sanitizeAndSortFindings(result)
	return result
}

func consistencyFinding(namespace string, difference IndexConsistencyDifference, severity Severity) DiagnosticFinding {
	summaries := map[string]string{
		"index.missing_on_shard":          "索引在部分 expected shards 上缺失",
		"index.name_mismatch":             "语义相同的索引在不同 shards 上名称不一致",
		"index.spec_mismatch":             "同名索引的持久化定义在不同 shards 上不一致",
		"index.build_in_progress":         "索引正在构建，一致性结论暂不稳定",
		"index.consistency_inconclusive":  "索引一致性证据不完整或两次观察发生变化",
		"index.shard_key_support_missing": "分片集合缺少支持 shard key 的索引",
	}
	evidence := map[string]any{
		"indexName": difference.IndexName, "shards": difference.Shards,
		"fingerprint": difference.Fingerprint, "differingFields": difference.DifferingFields,
		"sourceType": difference.SourceType,
	}
	return DiagnosticFinding{
		Code: difference.Code, Severity: severity,
		Scope:   FindingScope{Type: ScopeNamespace, Namespace: namespace},
		Summary: summaries[difference.Code], Evidence: evidence,
	}
}

func mapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func indexConsistencyStrategyForVersion(version string) (IndexConsistencyStrategy, bool) {
	major, minor, patch, ok := parseMongoDBVersion(version)
	if !ok || compareVersion(major, minor, patch, 3, 4, 0) < 0 || major >= 8 {
		return "", false
	}
	if compareVersion(major, minor, patch, 4, 2, 4) < 0 {
		return IndexConsistencyDirectListIndexes, true
	}
	if major < 7 {
		return IndexConsistencyIndexStats, true
	}
	return IndexConsistencyMetadataCheck, true
}

func parseMongoDBVersion(version string) (major, minor, patch int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 3 {
		return 0, 0, 0, false
	}
	values := []*int{&major, &minor, &patch}
	for index := 0; index < len(values); index++ {
		if index >= len(parts) {
			break
		}
		digits := parts[index]
		if end := strings.IndexFunc(digits, func(r rune) bool { return !unicode.IsDigit(r) }); end >= 0 {
			digits = digits[:end]
		}
		if digits == "" {
			return 0, 0, 0, false
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			return 0, 0, 0, false
		}
		*values[index] = value
	}
	return major, minor, patch, true
}

func compareVersion(major, minor, patch, wantMajor, wantMinor, wantPatch int) int {
	for _, pair := range [][2]int{{major, wantMajor}, {minor, wantMinor}, {patch, wantPatch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}
