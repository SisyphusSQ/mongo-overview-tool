package mot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

const (
	defaultIndexObservation = 7 * 24 * time.Hour
	defaultMaxCollections   = 500
)

type IndexAuditCheck string

const (
	IndexCheckUnused      IndexAuditCheck = "unused"
	IndexCheckRedundant   IndexAuditCheck = "redundant"
	IndexCheckSpace       IndexAuditCheck = "space"
	IndexCheckBuilding    IndexAuditCheck = "building"
	IndexCheckConsistency IndexAuditCheck = "consistency"
)

type IndexAuditOptions struct {
	Databases       []string
	AllDatabases    bool
	Collections     []string
	Checks          []IndexAuditCheck
	IncludeSystemDB bool
	MinObservation  time.Duration
	MaxCollections  int
	Concurrency     int
}

type IndexKeyField struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

type IndexObservation struct {
	Name                          string          `json:"name"`
	Key                           []IndexKeyField `json:"key"`
	Host                          string          `json:"host"`
	Shard                         string          `json:"shard,omitempty"`
	Ops                           int64           `json:"ops"`
	Since                         time.Time       `json:"since"`
	SizeBytes                     *int64          `json:"sizeBytes,omitempty"`
	Unique                        bool            `json:"unique"`
	Sparse                        bool            `json:"sparse"`
	Hidden                        bool            `json:"hidden"`
	Partial                       bool            `json:"partial"`
	WildcardProjection            bool            `json:"wildcardProjection"`
	CollationFingerprint          string          `json:"collationFingerprint,omitempty"`
	PartialFilterFingerprint      string          `json:"partialFilterFingerprint,omitempty"`
	WildcardProjectionFingerprint string          `json:"wildcardProjectionFingerprint,omitempty"`
	ExpireAfterSeconds            *int64          `json:"expireAfterSeconds,omitempty"`
	SpecialType                   string          `json:"specialType,omitempty"`
	Building                      bool            `json:"building"`
}

type CollectionIndexAudit struct {
	Namespace           string                       `json:"namespace"`
	Sharded             bool                         `json:"sharded"`
	State               IndexConsistencyState        `json:"state,omitempty"`
	Strategy            IndexConsistencyStrategy     `json:"strategy,omitempty"`
	ExpectedShards      []string                     `json:"expectedShards,omitempty"`
	ObservedShards      []string                     `json:"observedShards,omitempty"`
	Coverage            IndexConsistencyCoverage     `json:"coverage,omitempty"`
	Fallback            *IndexConsistencyFallback    `json:"fallback,omitempty"`
	Differences         []IndexConsistencyDifference `json:"differences,omitempty"`
	ConsistencyStatuses []CollectorStatus            `json:"consistencyStatuses,omitempty"`
	DataSizeBytes       *int64                       `json:"dataSizeBytes,omitempty"`
	IndexSizeBytes      *int64                       `json:"indexSizeBytes,omitempty"`
	IndexToDataRatio    *float64                     `json:"indexToDataRatio,omitempty"`
	Indexes             []IndexObservation           `json:"indexes"`
	Findings            []DiagnosticFinding          `json:"findings"`
}

type IndexAuditResult struct {
	CollectedAt        time.Time               `json:"collectedAt"`
	ConsistencySummary IndexConsistencySummary `json:"consistencySummary"`
	Collections        []CollectionIndexAudit  `json:"collections"`
	Findings           []DiagnosticFinding     `json:"findings"`
	CollectorStatuses  []CollectorStatus       `json:"collectorStatuses"`
}

type indexCollectionRef struct {
	Database   string
	Collection string
	Type       string
}

type indexCollectionMetadata struct {
	Name string `bson:"name"`
	Type string `bson:"type"`
}

type indexAuditTargetCollection struct {
	indexes  []IndexObservation
	statuses []CollectorStatus
	errors   []error
}

type indexAuditTargetLoader func(ctx context.Context, target hotspotTarget) indexAuditTargetCollection

// IndexAudit 执行通用索引使用、定义、空间和跨 shard 一致性审计。
func (c *Client) IndexAudit(ctx context.Context, opts IndexAuditOptions) (result *IndexAuditResult, err error) {
	if c != nil && c.session == nil {
		return withEphemeralCollectorSession(ctx, c, func(session *CollectorSession) (*IndexAuditResult, error) {
			return session.IndexAudit(ctx, opts)
		})
	}
	opts, err = normalizeIndexAuditOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := c.requireMemberConnectionURI(); err != nil {
		return nil, err
	}
	defer func() { err = mapContextError(err) }()
	cluster, err := c.detectClusterTopology(ctx)
	if err != nil {
		return nil, err
	}
	result = &IndexAuditResult{CollectedAt: time.Now().UTC()}
	consistencyRequested := includesIndexCheck(opts.Checks, IndexCheckConsistency)
	generalRequested := includesGeneralIndexCheck(opts.Checks)
	if err := validateIndexConsistencyTopology(cluster.Type, consistencyRequested); err != nil {
		return nil, err
	}
	if generalRequested {
		if gate, allowed := diagnosticCapabilityGate("index_usage", convertClusterType(cluster.Type), cluster.MaxWireVersion, true); !allowed {
			result.CollectorStatuses = []CollectorStatus{gate}
			return result, nil
		}
	}
	refs, err := c.indexCollectionRefs(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, invalidOptions("no collections selected")
	}
	if len(refs) > opts.MaxCollections {
		return nil, invalidOptions("selected %d collections, exceeds max %d", len(refs), opts.MaxCollections)
	}
	collectionsByNamespace := make(map[string]CollectionIndexAudit, len(refs))
	var collectorErrors []error
	if consistencyRequested {
		collections, statuses, consistencyErrors := collectIndexConsistency(ctx, refs, opts, clientIndexConsistencySource{client: c})
		if len(collections) == 0 && len(consistencyErrors) > 0 {
			return nil, errors.Join(consistencyErrors...)
		}
		for _, collection := range collections {
			collectionsByNamespace[collection.Namespace] = collection
		}
		result.CollectorStatuses = append(result.CollectorStatuses, statuses...)
		collectorErrors = append(collectorErrors, consistencyErrors...)
	}
	if generalRequested {
		targets, targetStatuses, discoveryErrors := c.discoverHotspotTargets(ctx, cluster.Type)
		result.CollectorStatuses = append(result.CollectorStatuses, targetStatuses...)
		collectorErrors = append(collectorErrors, discoveryErrors...)
		var mu sync.Mutex
		group, groupCtx := errgroup.WithContext(ctx)
		capabilityLimit := semaphore.NewWeighted(int64(opts.Concurrency))
		for _, ref := range refs {
			if ref.Type != "collection" {
				continue
			}
			ref := ref
			group.Go(func() error {
				collection, statuses, collectErrors := c.collectIndexAuditCollection(groupCtx, ref, targets, opts, result.CollectedAt, cluster.Type == pkgmongo.ClusterRepl, capabilityLimit)
				mu.Lock()
				collectionsByNamespace[collection.Namespace] = mergeIndexAuditCollections(collectionsByNamespace[collection.Namespace], collection)
				result.CollectorStatuses = append(result.CollectorStatuses, statuses...)
				collectorErrors = append(collectorErrors, collectErrors...)
				mu.Unlock()
				return nil
			})
		}
		_ = group.Wait()
	}
	result.Collections = make([]CollectionIndexAudit, 0, len(collectionsByNamespace))
	for _, collection := range collectionsByNamespace {
		result.Collections = append(result.Collections, collection)
		result.Findings = append(result.Findings, collection.Findings...)
	}
	sort.SliceStable(result.Collections, func(i, j int) bool { return result.Collections[i].Namespace < result.Collections[j].Namespace })
	result.ConsistencySummary = summarizeIndexConsistency(result.Collections)
	sanitizeAndSortFindings(result.Findings)
	sortCollectorStatuses(result.CollectorStatuses)
	if len(collectorErrors) > 0 {
		if len(result.Collections) == 0 {
			return result, errors.Join(collectorErrors...)
		}
		return result, newDiagnosticPartialError("index-audit", result, errors.Join(collectorErrors...))
	}
	return result, nil
}

func validateIndexConsistencyTopology(clusterType pkgmongo.ClusterType, requested bool) error {
	if requested && clusterType != pkgmongo.ClusterShard {
		return fmt.Errorf("%w: index consistency requires a mongos connection", ErrUnsupportedTopology)
	}
	return nil
}

func includesGeneralIndexCheck(checks []IndexAuditCheck) bool {
	for _, check := range checks {
		if check != IndexCheckConsistency {
			return true
		}
	}
	return false
}

func mergeIndexAuditCollections(consistency, general CollectionIndexAudit) CollectionIndexAudit {
	if consistency.Namespace == "" {
		return general
	}
	consistency.DataSizeBytes = general.DataSizeBytes
	consistency.IndexSizeBytes = general.IndexSizeBytes
	consistency.IndexToDataRatio = general.IndexToDataRatio
	consistency.Indexes = append(consistency.Indexes, general.Indexes...)
	consistency.Findings = append(consistency.Findings, general.Findings...)
	sanitizeAndSortFindings(consistency.Findings)
	return consistency
}

func normalizeIndexAuditOptions(opts IndexAuditOptions) (IndexAuditOptions, error) {
	if opts.MinObservation < 0 || opts.MaxCollections < 0 || opts.Concurrency < 0 {
		return IndexAuditOptions{}, invalidOptions("duration, max collections and concurrency must not be negative")
	}
	if !opts.AllDatabases && len(opts.Databases) == 0 {
		return IndexAuditOptions{}, invalidOptions("databases or all databases is required")
	}
	if opts.AllDatabases && len(opts.Databases) > 0 {
		return IndexAuditOptions{}, invalidOptions("databases and all databases are mutually exclusive")
	}
	if opts.MinObservation == 0 {
		opts.MinObservation = defaultIndexObservation
	}
	if opts.MaxCollections == 0 {
		opts.MaxCollections = defaultMaxCollections
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = defaultOverviewNodeConcurrency
	}
	if len(opts.Checks) == 0 {
		opts.Checks = []IndexAuditCheck{IndexCheckUnused, IndexCheckRedundant, IndexCheckSpace, IndexCheckBuilding, IndexCheckConsistency}
	}
	for _, check := range opts.Checks {
		switch check {
		case IndexCheckUnused, IndexCheckRedundant, IndexCheckSpace, IndexCheckBuilding, IndexCheckConsistency:
		default:
			return IndexAuditOptions{}, invalidOptions("unknown index audit check %q", check)
		}
	}
	return opts, nil
}

func (c *Client) indexCollectionRefs(ctx context.Context, opts IndexAuditOptions) ([]indexCollectionRef, error) {
	dbs, err := c.databaseNames(ctx)
	if err != nil {
		return nil, err
	}
	var refs []indexCollectionRef
	for _, database := range dbs {
		if !opts.IncludeSystemDB && isSystemDatabase(database) {
			continue
		}
		if !opts.AllDatabases && !stringIncluded(opts.Databases, database) {
			continue
		}
		metadataValues, listErr := c.collectionMetadata(ctx, database)
		if listErr != nil {
			return nil, listErr
		}
		refs = append(refs, selectIndexCollectionRefs(database, metadataValues, opts.Collections)...)
		if opts.MaxCollections > 0 && len(refs) > opts.MaxCollections {
			return nil, invalidOptions("selected collections exceed max %d", opts.MaxCollections)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].Database+"."+refs[i].Collection < refs[j].Database+"."+refs[j].Collection
	})
	return refs, nil
}

func selectIndexCollectionRefs(database string, metadata []indexCollectionMetadata, collections []string) []indexCollectionRef {
	refs := make([]indexCollectionRef, 0, len(metadata))
	for _, item := range metadata {
		if len(collections) > 0 && !stringIncluded(collections, item.Name) {
			continue
		}
		refs = append(refs, indexCollectionRef{Database: database, Collection: item.Name, Type: item.Type})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].Database+"."+refs[i].Collection < refs[j].Database+"."+refs[j].Collection
	})
	return refs
}

func (c *Client) collectIndexAuditCollection(ctx context.Context, ref indexCollectionRef, targets []hotspotTarget, opts IndexAuditOptions, now time.Time, ownershipKnown bool, capabilityLimit *semaphore.Weighted) (CollectionIndexAudit, []CollectorStatus, []error) {
	namespace := ref.Database + "." + ref.Collection
	result := CollectionIndexAudit{Namespace: namespace}
	var statuses []CollectorStatus
	var collectorErrors []error
	var capacity pkgmongo.CollectionCapacitySnapshot
	release, acquireErr := c.acquireCapabilityRemoteSlot(ctx, capabilityLimit)
	if acquireErr != nil {
		return result, nil, []error{acquireErr}
	}
	capacity, capacityErr := c.conn.CollectionCapacity(ctx, ref.Database, ref.Collection, false, 5*time.Second)
	release()
	if capacityErr == nil {
		result.DataSizeBytes, result.IndexSizeBytes = capacity.DataSizeBytes, capacity.TotalIndexSizeBytes
		if result.DataSizeBytes != nil && *result.DataSizeBytes > 0 && result.IndexSizeBytes != nil {
			ratio := float64(*result.IndexSizeBytes) / float64(*result.DataSizeBytes)
			result.IndexToDataRatio = &ratio
		}
	} else {
		collectorErrors = append(collectorErrors, capacityErr)
	}
	targetResults := collectIndexAuditTargets(ctx, targets, func(ctx context.Context, target hotspotTarget) indexAuditTargetCollection {
		scope := FindingScope{Type: ScopeNamespace, ReplicaSet: target.ReplicaSet, Shard: target.Shard, Node: target.Address, Database: ref.Database, Namespace: namespace}
		release, acquireErr := c.acquireCapabilityRemoteSlot(ctx, capabilityLimit)
		if acquireErr != nil {
			return indexAuditTargetCollection{errors: []error{acquireErr}}
		}
		defer release()
		conn, connectErr := c.connectAddress(ctx, target.Address, derivedConnectionOptions{Direct: boolPointer(true)})
		if connectErr != nil {
			return indexAuditTargetCollection{
				statuses: []CollectorStatus{failedCollectorStatus("index_usage", scope, connectErr)},
				errors:   []error{connectErr},
			}
		}
		defer c.closeDerivedConnection(ctx, conn)
		stats, statsErr := conn.IndexStats(ctx, ref.Database, ref.Collection, 5*time.Second)
		if statsErr != nil {
			item := indexAuditTargetCollection{}
			if isUnsupportedDiagnosticError(statsErr) {
				item.statuses = append(item.statuses, CollectorStatus{Name: "index_usage", State: CapabilityUnsupported, Scope: scope, ReasonCode: "unsupported_version", Message: "$indexStats 在当前服务器上不可用"})
			} else if isUnauthorizedError(statsErr) {
				item.statuses = append(item.statuses, failedCollectorStatus("index_usage", scope, statsErr))
			} else {
				item.errors = append(item.errors, statsErr)
				item.statuses = append(item.statuses, failedCollectorStatus("index_usage", scope, statsErr))
			}
			return item
		}
		item := indexAuditTargetCollection{statuses: []CollectorStatus{{Name: "index_usage", State: CapabilitySupported, Scope: scope}}}
		for _, stat := range stats {
			observation := indexObservationFromMongo(stat, target.Shard)
			if capacity.IndexSizes != nil {
				if size, ok := capacity.IndexSizes[stat.Name]; ok {
					observation.SizeBytes = &size
				}
			}
			observation.Building = stringIncluded(capacity.IndexBuilds, stat.Name)
			item.indexes = append(item.indexes, observation)
		}
		return item
	})
	for _, item := range targetResults {
		result.Indexes = append(result.Indexes, item.indexes...)
		statuses = append(statuses, item.statuses...)
		collectorErrors = append(collectorErrors, item.errors...)
	}
	result.Findings = evaluateIndexAuditCollection(result, len(targets), opts, now, ownershipKnown)
	sort.SliceStable(result.Indexes, func(i, j int) bool {
		if result.Indexes[i].Name != result.Indexes[j].Name {
			return result.Indexes[i].Name < result.Indexes[j].Name
		}
		if result.Indexes[i].Shard != result.Indexes[j].Shard {
			return result.Indexes[i].Shard < result.Indexes[j].Shard
		}
		return result.Indexes[i].Host < result.Indexes[j].Host
	})
	return result, statuses, collectorErrors
}

func collectIndexAuditTargets(ctx context.Context, targets []hotspotTarget, load indexAuditTargetLoader) []indexAuditTargetCollection {
	if load == nil {
		return []indexAuditTargetCollection{{errors: []error{invalidOptions("index audit target loader is required")}}}
	}
	result := make([]indexAuditTargetCollection, len(targets))
	group, groupCtx := errgroup.WithContext(ctx)
	for i := range targets {
		i := i
		group.Go(func() error {
			result[i] = load(groupCtx, targets[i])
			return nil
		})
	}
	_ = group.Wait()
	return result
}

func evaluateIndexAuditCollection(collection CollectionIndexAudit, expectedNodes int, opts IndexAuditOptions, now time.Time, ownershipKnown ...bool) []DiagnosticFinding {
	canDetermineOwnership := len(ownershipKnown) == 0 || ownershipKnown[0]
	byName := make(map[string][]IndexObservation)
	for _, observation := range collection.Indexes {
		byName[observation.Name] = append(byName[observation.Name], observation)
	}
	var findings []DiagnosticFinding
	if includesIndexCheck(opts.Checks, IndexCheckUnused) {
		for name, observations := range byName {
			if name == "_id_" {
				continue
			}
			complete, unused := canDetermineOwnership && len(observations) == expectedNodes && expectedNodes > 0, true
			for _, observation := range observations {
				if observation.Ops != 0 {
					unused = false
				}
				if observation.Since.IsZero() || now.Sub(observation.Since) < opts.MinObservation || observation.Building {
					complete = false
				}
			}
			code, severity, summary := "index.unused_candidate", SeverityWarning, "索引在完整观察窗口内未记录使用，需结合查询模式人工复核"
			if !complete {
				code, severity, summary = "index.usage_inconclusive", SeverityInfo, "索引使用证据不完整，不能判定为长期零使用"
			}
			if unused || !complete {
				evidence := map[string]any{"indexName": name, "observedNodes": len(observations), "expectedNodes": expectedNodes}
				if !canDetermineOwnership {
					evidence["ownershipCoverage"] = "unknown"
				}
				if len(observations) > 0 {
					evidence["unique"] = observations[0].Unique
					evidence["sparse"] = observations[0].Sparse
					evidence["partial"] = observations[0].Partial
					evidence["hidden"] = observations[0].Hidden
					evidence["specialType"] = observations[0].SpecialType
					evidence["ttl"] = observations[0].ExpireAfterSeconds != nil
				}
				findings = append(findings, DiagnosticFinding{Code: code, Severity: severity, Scope: FindingScope{Type: ScopeNamespace, Namespace: collection.Namespace}, Summary: summary, Evidence: evidence})
			}
		}
	}
	if includesIndexCheck(opts.Checks, IndexCheckRedundant) {
		definitions := firstIndexDefinitions(byName)
		for i := range definitions {
			for j := range definitions {
				if i == j || !compatibleIndexProperties(definitions[i], definitions[j]) || !keyPrefix(definitions[i].Key, definitions[j].Key) {
					continue
				}
				findings = append(findings, DiagnosticFinding{Code: "index.redundant_prefix_candidate", Severity: SeverityInfo, Scope: FindingScope{Type: ScopeNamespace, Namespace: collection.Namespace}, Summary: "索引 key pattern 是另一索引的前缀，需结合慢日志和查询模式人工复核", Evidence: map[string]any{"indexName": definitions[i].Name, "coveringIndexName": definitions[j].Name}})
			}
		}
	}
	if includesIndexCheck(opts.Checks, IndexCheckBuilding) {
		for name, observations := range byName {
			for _, observation := range observations {
				if observation.Building {
					findings = append(findings, DiagnosticFinding{Code: "index.build_in_progress", Severity: SeverityInfo, Scope: FindingScope{Type: ScopeNamespace, Namespace: collection.Namespace}, Summary: "索引正在构建，使用结论暂不稳定", Evidence: map[string]any{"indexName": name}})
					break
				}
			}
		}
	}
	if includesIndexCheck(opts.Checks, IndexCheckSpace) && collection.IndexToDataRatio != nil {
		findings = append(findings, DiagnosticFinding{Code: "index.space_ratio", Severity: SeverityInfo, Scope: FindingScope{Type: ScopeNamespace, Namespace: collection.Namespace}, Summary: "索引与逻辑数据空间占比，仅作为容量复核证据", Evidence: map[string]any{"indexToDataRatio": *collection.IndexToDataRatio}})
	}
	sanitizeAndSortFindings(findings)
	return findings
}

func indexObservationFromMongo(stat pkgmongo.IndexStatSnapshot, shard string) IndexObservation {
	key := make([]IndexKeyField, 0, len(stat.Key))
	for _, field := range stat.Key {
		key = append(key, IndexKeyField{Field: field.Key, Order: fmt.Sprint(field.Value)})
	}
	return IndexObservation{Name: stat.Name, Key: key, Host: stat.Host, Shard: shard, Ops: stat.Ops, Since: stat.Since, Unique: stat.Unique, Sparse: stat.Sparse, Hidden: stat.Hidden, Partial: stat.Partial, WildcardProjection: stat.WildcardProjection, CollationFingerprint: stat.CollationFingerprint, PartialFilterFingerprint: stat.PartialFilterFingerprint, WildcardProjectionFingerprint: stat.WildcardProjectionFingerprint, ExpireAfterSeconds: stat.ExpireAfterSeconds, SpecialType: stat.SpecialType}
}

func firstIndexDefinitions(byName map[string][]IndexObservation) []IndexObservation {
	result := make([]IndexObservation, 0, len(byName))
	for _, values := range byName {
		if len(values) > 0 {
			result = append(result, values[0])
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func compatibleIndexProperties(a, b IndexObservation) bool {
	return a.Unique == b.Unique && a.Sparse == b.Sparse && a.Hidden == b.Hidden &&
		a.PartialFilterFingerprint == b.PartialFilterFingerprint &&
		a.WildcardProjectionFingerprint == b.WildcardProjectionFingerprint &&
		a.CollationFingerprint == b.CollationFingerprint &&
		equalOptionalInt64(a.ExpireAfterSeconds, b.ExpireAfterSeconds)
}

func keyPrefix(a, b []IndexKeyField) bool {
	if len(a) >= len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalOptionalInt64(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func includesIndexCheck(checks []IndexAuditCheck, target IndexAuditCheck) bool {
	for _, check := range checks {
		if check == target {
			return true
		}
	}
	return false
}
func stringIncluded(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func isSystemDatabase(database string) bool { return stringIncluded(systemDatabases, database) }
