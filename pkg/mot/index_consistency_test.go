package mot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

type fakeIndexConsistencySource struct {
	mu             sync.Mutex
	version        string
	versionErr     error
	shards         map[string]indexShardTarget
	shardsErr      error
	routing        map[string]pkgmongo.IndexRoutingSnapshot
	routingErrors  map[string]error
	observed       map[string][]string
	builds         map[string][]string
	metadata       map[string][]pkgmongo.MetadataIndexInconsistency
	metadataErrors map[string]error
	stats          map[string][]pkgmongo.CanonicalIndexDefinition
	statsErrors    map[string]error
	direct         map[string][]pkgmongo.CanonicalIndexDefinition
	directErrors   map[string]error
	metadataCalls  []string
	statsCalls     []string
	directCalls    []string
	versionCalls   int
	shardCalls     int
	routingCalls   int
}

func (f *fakeIndexConsistencySource) ServerVersion(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versionCalls++
	return f.version, f.versionErr
}

func (f *fakeIndexConsistencySource) Shards(context.Context) (map[string]indexShardTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shardCalls++
	return f.shards, f.shardsErr
}

func (f *fakeIndexConsistencySource) Routing(_ context.Context, ref indexCollectionRef) (pkgmongo.IndexRoutingSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routingCalls++
	key := ref.Database + "." + ref.Collection
	return f.routing[key], f.routingErrors[key]
}

func (f *fakeIndexConsistencySource) Visibility(_ context.Context, ref indexCollectionRef) (indexCollectionVisibility, error) {
	key := ref.Database + "." + ref.Collection
	return indexCollectionVisibility{Shards: f.observed[key], IndexBuilds: f.builds[key]}, nil
}

func (f *fakeIndexConsistencySource) Metadata(_ context.Context, database, collection string) ([]pkgmongo.MetadataIndexInconsistency, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := database + "." + collection
	f.metadataCalls = append(f.metadataCalls, key)
	return f.metadata[key], f.metadataErrors[key]
}

func (f *fakeIndexConsistencySource) Stats(_ context.Context, ref indexCollectionRef) ([]pkgmongo.CanonicalIndexDefinition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ref.Database + "." + ref.Collection
	f.statsCalls = append(f.statsCalls, key)
	return f.stats[key], f.statsErrors[key]
}

func (f *fakeIndexConsistencySource) Direct(_ context.Context, ref indexCollectionRef, target indexShardTarget) ([]pkgmongo.CanonicalIndexDefinition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := ref.Database + "." + ref.Collection + ":" + target.Shard
	f.directCalls = append(f.directCalls, key)
	return f.direct[key], f.directErrors[key]
}

func TestIndexConsistencyStrategyVersionBoundaries(t *testing.T) {
	// 场景：MongoDB 3.4、4.2.4 和 7.0 是三段一致性采集策略的精确断点，范围外版本不得猜测执行。
	tests := []struct {
		name        string
		version     string
		want        IndexConsistencyStrategy
		wantAllowed bool
	}{
		{name: "below 3.4", version: "3.2.22", wantAllowed: false},
		{name: "3.4 direct", version: "3.4.24", want: IndexConsistencyDirectListIndexes, wantAllowed: true},
		{name: "4.2.3 direct", version: "4.2.3", want: IndexConsistencyDirectListIndexes, wantAllowed: true},
		{name: "4.2.4 index stats", version: "4.2.4", want: IndexConsistencyIndexStats, wantAllowed: true},
		{name: "4.4 index stats", version: "4.4.29", want: IndexConsistencyIndexStats, wantAllowed: true},
		{name: "5.x index stats", version: "5.0.31", want: IndexConsistencyIndexStats, wantAllowed: true},
		{name: "6.x index stats", version: "6.0.20", want: IndexConsistencyIndexStats, wantAllowed: true},
		{name: "7.x official", version: "7.0.18", want: IndexConsistencyMetadataCheck, wantAllowed: true},
		{name: "enterprise suffix", version: "7.0.18-11", want: IndexConsistencyMetadataCheck, wantAllowed: true},
		{name: "release candidate suffix", version: "7.0.0-rc0", want: IndexConsistencyMetadataCheck, wantAllowed: true},
		{name: "8.x excluded", version: "8.0.8", wantAllowed: false},
		{name: "invalid", version: "development", wantAllowed: false},
		{name: "non-version prefix", version: "release7.0.0", wantAllowed: false},
		{name: "missing patch", version: "4.2", wantAllowed: false},
		{name: "missing minor and patch", version: "7", wantAllowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, allowed := indexConsistencyStrategyForVersion(tt.version)
			if got != tt.want || allowed != tt.wantAllowed {
				t.Fatalf("indexConsistencyStrategyForVersion(%q) = (%q, %t), want (%q, %t)", tt.version, got, allowed, tt.want, tt.wantAllowed)
			}
		})
	}
}

func TestDiagnosticCapabilitiesRegisterIndexConsistencyStrategies(t *testing.T) {
	// 场景：三段 collector 必须各自在共享 capability registry 登记，避免业务代码私自发明版本和拓扑边界。
	want := map[string]bool{
		"index_consistency_direct":         false,
		"index_consistency_index_stats":    false,
		"index_consistency_metadata_check": false,
		"index_consistency_visibility":     false,
	}
	for _, capability := range DiagnosticCapabilities() {
		if _, ok := want[capability.Name]; ok {
			want[capability.Name] = true
			if capability.Cost != CapabilityCostBounded {
				t.Fatalf("capability %q cost = %q, want bounded", capability.Name, capability.Cost)
			}
			if len(capability.Topologies) != 1 || capability.Topologies[0] != ClusterSharded {
				t.Fatalf("capability %q topologies = %#v, want sharding only", capability.Name, capability.Topologies)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("capability %q is not registered", name)
		}
	}
}

func TestSelectIndexCollectionRefsIncludesViewsBeforeGate(t *testing.T) {
	// 场景：view 和普通集合都必须先进入选中范围，后续才能统一计入 MaxCollections 并分别输出 skipped。
	metadata := []indexCollectionMetadata{
		{Name: "orders", Type: "collection"},
		{Name: "orders_view", Type: "view"},
		{Name: "events", Type: "collection"},
	}

	got := selectIndexCollectionRefs("app", metadata, nil)
	want := []indexCollectionRef{
		{Database: "app", Collection: "events", Type: "collection"},
		{Database: "app", Collection: "orders", Type: "collection"},
		{Database: "app", Collection: "orders_view", Type: "view"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectIndexCollectionRefs() = %#v, want %#v", got, want)
	}

	filtered := selectIndexCollectionRefs("app", metadata, []string{"orders_view"})
	if !reflect.DeepEqual(filtered, []indexCollectionRef{{Database: "app", Collection: "orders_view", Type: "view"}}) {
		t.Fatalf("filtered refs = %#v", filtered)
	}
}

func TestEvaluateLegacyIndexConsistencyStateMachine(t *testing.T) {
	// 场景：整 shard 缺失、稳定差异、构建中和并发变化必须得到互斥且可解释的最终状态。
	expected := []string{"shard-a", "shard-b"}
	indexA := consistencyDefinition("tenant_1", "semantic-a", false, map[string]string{"key": "key-a", "unique": "false"})
	indexB := consistencyDefinition("tenant_1", "semantic-b", false, map[string]string{"key": "key-a", "unique": "true"})
	indexBuilding := consistencyDefinition("tenant_1", "semantic-a", true, map[string]string{"key": "key-a"})
	indexRenamed := consistencyDefinition("tenant_by_name", "semantic-a", false, map[string]string{"key": "key-a"})

	tests := []struct {
		name         string
		expected     []string
		first        map[string][]pkgmongo.CanonicalIndexDefinition
		second       map[string][]pkgmongo.CanonicalIndexDefinition
		wantState    IndexConsistencyState
		wantCoverage IndexConsistencyCoverage
		wantCode     string
	}{
		{
			name: "entire shard observation missing",
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA},
			},
			wantState: IndexConsistencyInconclusive, wantCoverage: IndexConsistencyCoverageIncomplete,
			wantCode: "index.consistency_inconclusive",
		},
		{
			name:     "stable difference plus entire shard coverage gap",
			expected: []string{"shard-a", "shard-b", "shard-c"},
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexB},
			},
			second: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexB},
			},
			wantState: IndexConsistencyInconsistent, wantCoverage: IndexConsistencyCoverageIncomplete,
			wantCode: "index.spec_mismatch",
		},
		{
			name: "stable missing index",
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {},
			},
			second: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {},
			},
			wantState: IndexConsistencyInconsistent, wantCoverage: IndexConsistencyCoverageComplete,
			wantCode: "index.missing_on_shard",
		},
		{
			name: "stable name mismatch",
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexRenamed},
			},
			second: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexRenamed},
			},
			wantState: IndexConsistencyInconsistent, wantCoverage: IndexConsistencyCoverageComplete,
			wantCode: "index.name_mismatch",
		},
		{
			name: "stable spec mismatch",
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexB},
			},
			second: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexB},
			},
			wantState: IndexConsistencyInconsistent, wantCoverage: IndexConsistencyCoverageComplete,
			wantCode: "index.spec_mismatch",
		},
		{
			name: "building suppresses warning",
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexBuilding}, "shard-b": {indexA},
			},
			wantState: IndexConsistencyInconclusive, wantCoverage: IndexConsistencyCoverageComplete,
			wantCode: "index.build_in_progress",
		},
		{
			name: "concurrent change suppresses warning",
			first: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexB},
			},
			second: map[string][]pkgmongo.CanonicalIndexDefinition{
				"shard-a": {indexA}, "shard-b": {indexA},
			},
			wantState: IndexConsistencyInconclusive, wantCoverage: IndexConsistencyCoverageComplete,
			wantCode: "index.consistency_inconclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caseExpected := tt.expected
			if len(caseExpected) == 0 {
				caseExpected = expected
			}
			got := evaluateLegacyIndexConsistency("app.orders", caseExpected, tt.first, tt.second)
			if got.State != tt.wantState || got.Coverage != tt.wantCoverage {
				t.Fatalf("evaluation = %#v, want state=%q coverage=%q", got, tt.wantState, tt.wantCoverage)
			}
			found := false
			for _, finding := range got.Findings {
				if finding.Code == tt.wantCode {
					found = true
				}
			}
			if !found {
				t.Fatalf("findings = %#v, want code %q", got.Findings, tt.wantCode)
			}
			if tt.wantState != IndexConsistencyInconsistent {
				for _, finding := range got.Findings {
					if finding.Severity == SeverityWarning {
						t.Fatalf("non-deterministic result emitted warning: %#v", got.Findings)
					}
				}
			}
		})
	}
}

func consistencyDefinition(name, semantic string, building bool, fields map[string]string) pkgmongo.CanonicalIndexDefinition {
	return pkgmongo.CanonicalIndexDefinition{
		Name: name, Key: []pkgmongo.IndexKeySnapshot{{Field: "tenant", Order: "1"}},
		SemanticFingerprint: semantic, FullFingerprint: semantic + ":" + name,
		FieldFingerprints: fields, Building: building,
	}
}

func TestEvaluateOfficialIndexConsistencyNormalizesIndexDomainOnly(t *testing.T) {
	// 场景：7.x official 只归一化索引域；已知非索引类型忽略，未知类型不能静默健康。
	expected := []string{"shard-a", "shard-b"}
	issues := []pkgmongo.MetadataIndexInconsistency{
		{SourceType: "InconsistentIndex", Namespace: "app.orders", IndexName: "tenant_1", MissingFromShards: []string{"shard-b"}},
		{SourceType: "InconsistentIndex", Namespace: "app.orders", IndexName: "ttl_1", InconsistentFields: []string{"expireAfterSeconds"}},
		{SourceType: "MissingShardKeyIndex", Namespace: "app.orders", Shard: "shard-a"},
		{SourceType: "MisplacedCollection", Namespace: "app.orders", Shard: "shard-b"},
	}

	got := evaluateOfficialIndexConsistency("app.orders", expected, expected, issues)
	if got.State != IndexConsistencyInconsistent || got.Coverage != IndexConsistencyCoverageComplete {
		t.Fatalf("evaluation = %#v", got)
	}
	for _, code := range []string{"index.missing_on_shard", "index.spec_mismatch", "index.shard_key_support_missing"} {
		if !hasFindingCode(got.Findings, code) {
			t.Fatalf("findings = %#v, want %q", got.Findings, code)
		}
	}
	if len(got.Findings) != 3 {
		t.Fatalf("known non-index issue leaked into findings: %#v", got.Findings)
	}

	unknown := evaluateOfficialIndexConsistency("app.orders", expected, expected, []pkgmongo.MetadataIndexInconsistency{{SourceType: "FutureIndexType", Namespace: "app.orders"}})
	if unknown.State != IndexConsistencyInconclusive || !hasFindingCode(unknown.Findings, "index.consistency_inconclusive") || unknown.Differences[0].SourceType != "FutureIndexType" {
		t.Fatalf("unknown evaluation = %#v", unknown)
	}
	propertyShapeChanged := evaluateOfficialIndexConsistency("app.orders", expected, expected, []pkgmongo.MetadataIndexInconsistency{{
		SourceType: "InconsistentIndex", Namespace: "app.orders", IndexName: "tenant_1",
		PropertiesDiffer: true, Fingerprint: "safe-fingerprint",
	}})
	if propertyShapeChanged.State != IndexConsistencyInconsistent || !hasFindingCode(propertyShapeChanged.Findings, "index.spec_mismatch") || propertyShapeChanged.Differences[0].Fingerprint != "safe-fingerprint" {
		t.Fatalf("property shape evaluation = %#v", propertyShapeChanged)
	}

	incomplete := evaluateOfficialIndexConsistency("app.orders", expected, []string{"shard-a"}, issues[:1])
	if incomplete.State != IndexConsistencyInconsistent || incomplete.Coverage != IndexConsistencyCoverageIncomplete {
		t.Fatalf("inconsistent plus coverage gap = %#v", incomplete)
	}
}

func TestEvaluateOfficialIndexConsistencyOrderIsStable(t *testing.T) {
	// 场景：official cursor 不承诺 issue 顺序，公开 differences/findings 必须与输入排列无关。
	expected := []string{"shard-a", "shard-b"}
	issues := []pkgmongo.MetadataIndexInconsistency{
		{SourceType: "FutureTypeB", Namespace: "app.orders"},
		{SourceType: "InconsistentIndex", Namespace: "app.orders", IndexName: "z_1", MissingFromShards: []string{"shard-b", "shard-a"}},
		{SourceType: "FutureTypeA", Namespace: "app.orders"},
		{SourceType: "InconsistentIndex", Namespace: "app.orders", IndexName: "a_1", MissingFromShards: []string{"shard-b"}},
	}
	reversed := append([]pkgmongo.MetadataIndexInconsistency(nil), issues...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	first := evaluateOfficialIndexConsistency("app.orders", expected, expected, issues)
	second := evaluateOfficialIndexConsistency("app.orders", expected, expected, reversed)
	if !reflect.DeepEqual(first.Differences, second.Differences) || !reflect.DeepEqual(first.Findings, second.Findings) {
		t.Fatalf("official order is unstable\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestSummarizeIndexConsistencyKeepsFourStatesSeparate(t *testing.T) {
	// 场景：顶层 summary 必须分别统计四种 collection 状态，不能把 inconclusive 算作健康。
	collections := []CollectionIndexAudit{
		{State: IndexConsistencyConsistent},
		{State: IndexConsistencyConsistent},
		{State: IndexConsistencyInconsistent},
		{State: IndexConsistencyInconclusive},
		{State: IndexConsistencySkipped},
	}
	want := IndexConsistencySummary{Consistent: 2, Inconsistent: 1, Inconclusive: 1, Skipped: 1}
	if got := summarizeIndexConsistency(collections); !reflect.DeepEqual(got, want) {
		t.Fatalf("summarizeIndexConsistency() = %#v, want %#v", got, want)
	}
}

func TestCollectIndexConsistencyOfficialDatabaseScopeDoesNotDoubleRunLegacy(t *testing.T) {
	// 场景：7.x 无 collection filter 时同一数据库只执行一次 official command，成功后不双跑 legacy。
	source := completeFakeConsistencySource("7.0.18")
	source.routing["app.events"] = pkgmongo.IndexRoutingSnapshot{Namespace: "app.events", Sharded: true, ExpectedShards: []string{"shard-a", "shard-b"}}
	source.observed["app.events"] = []string{"shard-a", "shard-b"}
	source.metadata["app."] = []pkgmongo.MetadataIndexInconsistency{
		{SourceType: "InconsistentIndex", Namespace: "app.orders", IndexName: "tenant_1", MissingFromShards: []string{"shard-b"}},
	}

	collections, _, collectorErrors := collectIndexConsistency(
		context.Background(),
		[]indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}, {Database: "app", Collection: "events", Type: "collection"}},
		IndexAuditOptions{Concurrency: 2}, source,
	)
	if len(collectorErrors) != 0 {
		t.Fatalf("collector errors = %v", collectorErrors)
	}
	if !reflect.DeepEqual(source.metadataCalls, []string{"app."}) {
		t.Fatalf("metadata calls = %#v, want one database-scope call", source.metadataCalls)
	}
	if len(source.statsCalls) != 0 || len(source.directCalls) != 0 {
		t.Fatalf("official success double-ran legacy: stats=%#v direct=%#v", source.statsCalls, source.directCalls)
	}
	byNamespace := indexCollectionsByNamespace(collections)
	if byNamespace["app.orders"].State != IndexConsistencyInconsistent || byNamespace["app.events"].State != IndexConsistencyConsistent {
		t.Fatalf("collections = %#v", collections)
	}
}

func TestCollectIndexConsistencyOfficialFailureFallsBackAndKeepsReason(t *testing.T) {
	// 场景：7.x official 失败且 context 有效时降级为 indexStats；最终 strategy 与脱敏原因必须保留。
	source := completeFakeConsistencySource("7.0.18")
	source.metadataErrors["app.orders"] = errors.New("server detail must not leak")
	source.stats["app.orders"] = []pkgmongo.CanonicalIndexDefinition{
		consistencyDefinitionForShard("_id_", "id", "shard-a"),
		consistencyDefinitionForShard("_id_", "id", "shard-b"),
	}

	collections, _, _ := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	got := collections[0]
	if got.State != IndexConsistencyConsistent || got.Strategy != IndexConsistencyIndexStats || got.Fallback == nil {
		t.Fatalf("collection = %#v", got)
	}
	if got.Fallback.From != IndexConsistencyMetadataCheck || got.Fallback.To != IndexConsistencyIndexStats || got.Fallback.ReasonCode != "collector_failed" {
		t.Fatalf("fallback = %#v", got.Fallback)
	}
	if len(source.statsCalls) != 1 || len(source.directCalls) != 0 {
		t.Fatalf("fallback calls: stats=%#v direct=%#v", source.statsCalls, source.directCalls)
	}
}

func TestCollectIndexConsistencySeparatesCollectorAndVisibilityStatuses(t *testing.T) {
	// 场景：official 成功但 collStats visibility 不完整时，两个 collector status 不得互相矛盾。
	source := completeFakeConsistencySource("7.0.18")
	source.observed["app.orders"] = []string{"shard-a"}
	collections, _, _ := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	states := make(map[string]CollectorStatus)
	for _, status := range collections[0].ConsistencyStatuses {
		states[status.Name] = status
	}
	if states["index_consistency_metadata_check"].State != CapabilitySupported || states["index_consistency_visibility"].State != CapabilityFailed || states["index_consistency_visibility"].ReasonCode != "incomplete_coverage" {
		t.Fatalf("statuses = %#v", collections[0].ConsistencyStatuses)
	}
}

func TestCollectIndexConsistencyStatsCoverageFallsBackDirect(t *testing.T) {
	// 场景：4.2.4+ $indexStats 缺整个 expected shard 时必须 direct fallback，不能把 observation 当 expected baseline。
	source := completeFakeConsistencySource("6.0.20")
	source.stats["app.orders"] = []pkgmongo.CanonicalIndexDefinition{consistencyDefinitionForShard("_id_", "id", "shard-a")}
	source.direct["app.orders:shard-a"] = []pkgmongo.CanonicalIndexDefinition{consistencyDefinition("_id_", "id", false, map[string]string{"key": "id"})}
	source.direct["app.orders:shard-b"] = []pkgmongo.CanonicalIndexDefinition{consistencyDefinition("_id_", "id", false, map[string]string{"key": "id"})}

	collections, _, collectorErrors := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if len(collectorErrors) != 0 {
		t.Fatalf("collector errors = %v", collectorErrors)
	}
	got := collections[0]
	if got.State != IndexConsistencyConsistent || got.Strategy != IndexConsistencyDirectListIndexes || got.Fallback == nil || got.Fallback.ReasonCode != "incomplete_coverage" {
		t.Fatalf("collection = %#v", got)
	}
	if len(source.directCalls) != 2 {
		t.Fatalf("direct calls = %#v", source.directCalls)
	}
}

func TestCollectIndexConsistencyDoesNotFallbackAfterCancellation(t *testing.T) {
	// 场景：official 失败后 context 已取消，禁止启动 indexStats 或 direct fallback。
	source := completeFakeConsistencySource("7.0.18")
	source.metadataErrors["app.orders"] = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	collections, _, _ := collectIndexConsistency(
		ctx, []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if len(source.statsCalls) != 0 || len(source.directCalls) != 0 {
		t.Fatalf("fallback started after cancellation: stats=%#v direct=%#v", source.statsCalls, source.directCalls)
	}
	if len(collections) > 0 && collections[0].State == IndexConsistencyConsistent {
		t.Fatalf("cancelled collection reported healthy: %#v", collections[0])
	}
}

func TestCollectIndexConsistencyDoesNotFallbackOnCollectorCancellation(t *testing.T) {
	// 场景：即使父 context 尚可用，collector 返回 cancellation/deadline 也表示当前预算已终止，不得启动下一段 fallback。
	tests := []struct {
		name    string
		version string
		err     error
	}{
		{name: "official canceled", version: "7.0.18", err: context.Canceled},
		{name: "official deadline", version: "7.0.18", err: context.DeadlineExceeded},
		{name: "index stats canceled", version: "6.0.20", err: context.Canceled},
		{name: "index stats deadline", version: "6.0.20", err: context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := completeFakeConsistencySource(tt.version)
			if tt.version == "7.0.18" {
				source.metadataErrors["app.orders"] = tt.err
			} else {
				source.statsErrors["app.orders"] = tt.err
			}

			collections, _, collectorErrors := collectIndexConsistency(
				context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
				IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
			)
			if len(collections) != 1 || collections[0].State != IndexConsistencyInconclusive || collections[0].Coverage != IndexConsistencyCoverageIncomplete {
				t.Fatalf("collections = %#v", collections)
			}
			if len(collectorErrors) != 1 || !errors.Is(collectorErrors[0], tt.err) {
				t.Fatalf("collector errors = %v, want %v", collectorErrors, tt.err)
			}
			if len(source.directCalls) != 0 {
				t.Fatalf("direct fallback started after collector cancellation: %#v", source.directCalls)
			}
			if tt.version == "7.0.18" && len(source.statsCalls) != 0 {
				t.Fatalf("indexStats fallback started after official cancellation: %#v", source.statsCalls)
			}
		})
	}
}

func TestValidateLiveIndexConsistencyResultRequiresCompletePrimaryPath(t *testing.T) {
	// 场景：required live gate 只能由版本主策略的完整结果通过，不能把 partial、fallback 或 inconclusive 当作正向验证。
	valid := &IndexAuditResult{Collections: []CollectionIndexAudit{{
		Namespace: "app.orders", Sharded: true, State: IndexConsistencyConsistent,
		Strategy: IndexConsistencyMetadataCheck, Coverage: IndexConsistencyCoverageComplete,
		ExpectedShards: []string{"shard-a", "shard-b"}, ObservedShards: []string{"shard-a", "shard-b"},
		ConsistencyStatuses: []CollectorStatus{
			{Name: "index_consistency_metadata_check", State: CapabilitySupported, ReasonCode: "complete"},
			{Name: "index_consistency_visibility", State: CapabilitySupported, ReasonCode: "complete"},
		},
	}}}
	if err := validateLiveIndexConsistencyResult(valid, nil, IndexConsistencyMetadataCheck, "app.orders"); err != nil {
		t.Fatalf("valid live result rejected: %v", err)
	}

	tests := []struct {
		name      string
		mutate    func(*IndexAuditResult)
		operation error
	}{
		{name: "partial operation", operation: ErrPartialResult},
		{name: "fallback", mutate: func(result *IndexAuditResult) {
			result.Collections[0].Strategy = IndexConsistencyIndexStats
			result.Collections[0].Fallback = &IndexConsistencyFallback{From: IndexConsistencyMetadataCheck, To: IndexConsistencyIndexStats, ReasonCode: "collector_failed"}
		}},
		{name: "inconclusive", mutate: func(result *IndexAuditResult) { result.Collections[0].State = IndexConsistencyInconclusive }},
		{name: "incomplete coverage", mutate: func(result *IndexAuditResult) { result.Collections[0].Coverage = IndexConsistencyCoverageIncomplete }},
		{name: "missing observed shard", mutate: func(result *IndexAuditResult) { result.Collections[0].ObservedShards = []string{"shard-a"} }},
		{name: "primary capability failed", mutate: func(result *IndexAuditResult) {
			result.Collections[0].ConsistencyStatuses[0].State = CapabilityFailed
		}},
		{name: "visibility incomplete", mutate: func(result *IndexAuditResult) {
			result.Collections[0].ConsistencyStatuses[1].ReasonCode = "incomplete_coverage"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := *valid
			result.Collections = append([]CollectionIndexAudit(nil), valid.Collections...)
			result.Collections[0].ExpectedShards = append([]string(nil), valid.Collections[0].ExpectedShards...)
			result.Collections[0].ObservedShards = append([]string(nil), valid.Collections[0].ObservedShards...)
			result.Collections[0].ConsistencyStatuses = append([]CollectorStatus(nil), valid.Collections[0].ConsistencyStatuses...)
			if tt.mutate != nil {
				tt.mutate(&result)
			}
			if err := validateLiveIndexConsistencyResult(&result, tt.operation, IndexConsistencyMetadataCheck, "app.orders"); err == nil {
				t.Fatal("invalid live result passed")
			}
		})
	}
}

func validateLiveIndexConsistencyResult(result *IndexAuditResult, operationErr error, wantStrategy IndexConsistencyStrategy, namespace string) error {
	if operationErr != nil {
		return fmt.Errorf("index consistency operation did not complete: %w", operationErr)
	}
	if result == nil || len(result.Collections) != 1 {
		return fmt.Errorf("index consistency returned %d collections, want 1", indexConsistencyCollectionCount(result))
	}
	item := result.Collections[0]
	if item.Namespace != namespace || !item.Sharded || len(item.ExpectedShards) == 0 {
		return fmt.Errorf("namespace is not a usable sharded consistency target: namespace=%q sharded=%t expectedShards=%d", item.Namespace, item.Sharded, len(item.ExpectedShards))
	}
	if item.State != IndexConsistencyConsistent && item.State != IndexConsistencyInconsistent {
		return fmt.Errorf("state = %q, want a conclusive state", item.State)
	}
	if item.Coverage != IndexConsistencyCoverageComplete || len(item.ObservedShards) != len(item.ExpectedShards) || len(intersectExpectedShards(item.ExpectedShards, item.ObservedShards)) != len(item.ExpectedShards) {
		return fmt.Errorf("coverage = %q with expected=%d observed=%d, want complete expected-shard coverage", item.Coverage, len(item.ExpectedShards), len(item.ObservedShards))
	}
	if item.Strategy != wantStrategy || item.Fallback != nil {
		return fmt.Errorf("strategy = %q fallback=%t, want primary strategy %q without fallback", item.Strategy, item.Fallback != nil, wantStrategy)
	}
	for _, name := range []string{consistencyCapabilityName(wantStrategy), "index_consistency_visibility"} {
		if !hasCompleteConsistencyStatus(item.ConsistencyStatuses, name) {
			return fmt.Errorf("collector status %q is not supported/complete", name)
		}
	}
	return nil
}

func indexConsistencyCollectionCount(result *IndexAuditResult) int {
	if result == nil {
		return 0
	}
	return len(result.Collections)
}

func hasCompleteConsistencyStatus(statuses []CollectorStatus, name string) bool {
	for _, status := range statuses {
		if status.Name == name && status.State == CapabilitySupported && status.ReasonCode == "complete" {
			return true
		}
	}
	return false
}

func TestCollectIndexConsistencySkipsViewAndUnshardedCollection(t *testing.T) {
	// 场景：view 与普通集合都计入统一 gate，但不得执行 consistency collector，并稳定返回 skipped。
	source := completeFakeConsistencySource("7.0.18")
	source.shardsErr = drivermongo.CommandError{Code: 13, Message: "fixture unauthorized"}
	source.routing["app.local"] = pkgmongo.IndexRoutingSnapshot{Namespace: "app.local"}
	collections, _, collectorErrors := collectIndexConsistency(
		context.Background(), []indexCollectionRef{
			{Database: "app", Collection: "orders_view", Type: "view"},
			{Database: "app", Collection: "local", Type: "collection"},
		}, IndexAuditOptions{Collections: []string{"orders_view", "local"}, Concurrency: 2}, source,
	)
	if len(collectorErrors) != 0 {
		t.Fatalf("collector errors = %v", collectorErrors)
	}
	for _, collection := range collections {
		if collection.State != IndexConsistencySkipped || collection.Coverage != IndexConsistencyCoverageSkipped {
			t.Fatalf("collection = %#v", collection)
		}
	}
	if len(source.statsCalls) != 0 || len(source.directCalls) != 0 {
		t.Fatalf("skipped collection was collected: stats=%#v direct=%#v", source.statsCalls, source.directCalls)
	}
	if len(source.metadataCalls) != 0 {
		t.Fatalf("skipped collection ran official collector: %#v", source.metadataCalls)
	}
	if source.versionCalls != 0 || source.shardCalls != 0 || source.routingCalls != 1 {
		t.Fatalf("skip preflight calls: buildInfo=%d listShards=%d routing=%d", source.versionCalls, source.shardCalls, source.routingCalls)
	}
}

func TestCollectIndexConsistencyListShardsUnauthorizedIsRenderable(t *testing.T) {
	// 场景：routing 已识别分片集合后 listShards 无权限，必须返回 inconclusive/unauthorized，而不是丢失整个结果。
	source := completeFakeConsistencySource("7.0.18")
	source.shardsErr = drivermongo.CommandError{Code: 13, Message: "fixture unauthorized detail"}
	collections, statuses, collectorErrors := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if len(collectorErrors) != 0 || len(collections) != 1 || collections[0].State != IndexConsistencyInconclusive || collections[0].Coverage != IndexConsistencyCoverageIncomplete {
		t.Fatalf("collections=%#v statuses=%#v errors=%v", collections, statuses, collectorErrors)
	}
	if len(collections[0].ConsistencyStatuses) != 1 || collections[0].ConsistencyStatuses[0].State != CapabilityUnauthorized {
		t.Fatalf("collection statuses = %#v", collections[0].ConsistencyStatuses)
	}
	if len(source.metadataCalls) != 0 || len(source.statsCalls) != 0 || len(source.directCalls) != 0 {
		t.Fatalf("collectors ran after listShards failure: metadata=%#v stats=%#v direct=%#v", source.metadataCalls, source.statsCalls, source.directCalls)
	}
}

func TestCollectIndexConsistencyRoutingUnauthorizedIsRenderable(t *testing.T) {
	// 场景：routing metadata 无权限属于 collection 级可渲染结果，不得继续 listShards 或其它 collectors。
	source := completeFakeConsistencySource("7.0.18")
	source.routingErrors["app.orders"] = drivermongo.CommandError{Code: 13, Message: "fixture routing detail"}
	collections, _, collectorErrors := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if len(collectorErrors) != 0 || len(collections) != 1 || collections[0].State != IndexConsistencyInconclusive || collections[0].ConsistencyStatuses[0].State != CapabilityUnauthorized {
		t.Fatalf("collections=%#v errors=%v", collections, collectorErrors)
	}
	if source.shardCalls != 0 || len(source.metadataCalls) != 0 {
		t.Fatalf("downstream calls: listShards=%d metadata=%#v", source.shardCalls, source.metadataCalls)
	}
}

func TestCollectIndexConsistencyListShardsTimeoutStopsFallback(t *testing.T) {
	// 场景：listShards 超时后返回 incomplete coverage 和取消错误，不启动 official/legacy fallback。
	source := completeFakeConsistencySource("7.0.18")
	source.shardsErr = context.DeadlineExceeded
	collections, _, collectorErrors := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if len(collectorErrors) != 1 || !errors.Is(collectorErrors[0], context.DeadlineExceeded) || collections[0].Coverage != IndexConsistencyCoverageIncomplete {
		t.Fatalf("collections=%#v errors=%v", collections, collectorErrors)
	}
	if len(source.metadataCalls) != 0 || len(source.statsCalls) != 0 || len(source.directCalls) != 0 {
		t.Fatalf("fallback ran after timeout: metadata=%#v stats=%#v direct=%#v", source.metadataCalls, source.statsCalls, source.directCalls)
	}
}

func TestCollectIndexConsistencyTreatsCollectionBuildAsInconclusive(t *testing.T) {
	// 场景：旧版本 direct 路径也必须消费 collStats indexBuilds，构建中索引不能输出 consistent。
	source := completeFakeConsistencySource("3.4.24")
	source.builds["app.orders"] = []string{"tenant_1"}
	idIndex := consistencyDefinition("_id_", "id", false, map[string]string{"key": "id"})
	buildingIndex := consistencyDefinition("tenant_1", "tenant", false, map[string]string{"key": "tenant"})
	source.direct["app.orders:shard-a"] = []pkgmongo.CanonicalIndexDefinition{idIndex, buildingIndex}
	source.direct["app.orders:shard-b"] = []pkgmongo.CanonicalIndexDefinition{idIndex}

	collections, _, _ := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if collections[0].State != IndexConsistencyInconclusive || !hasFindingCode(collections[0].Findings, "index.build_in_progress") {
		t.Fatalf("collection = %#v", collections[0])
	}
	for _, finding := range collections[0].Findings {
		if finding.Severity == SeverityWarning {
			t.Fatalf("building index emitted warning: %#v", finding)
		}
	}
}

func TestConfirmLegacyDifferencesRereadsOnlyAffectedShards(t *testing.T) {
	// 场景：legacy 首次发现缺失索引后，复读该索引在 expected shards 上的完整 presence/absence，避免并发 DDL 漏检。
	source := completeFakeConsistencySource("3.4.24")
	source.shards["shard-c"] = indexShardTarget{Shard: "shard-c", ReplicaSet: "rs-c", Addresses: "c:27017"}
	index := consistencyDefinition("tenant_1", "tenant", false, map[string]string{"key": "tenant"})
	first := map[string][]pkgmongo.CanonicalIndexDefinition{
		"shard-a": {index}, "shard-b": {}, "shard-c": {index},
	}
	source.direct["app.orders:shard-a"] = []pkgmongo.CanonicalIndexDefinition{index}
	source.direct["app.orders:shard-b"] = []pkgmongo.CanonicalIndexDefinition{}
	source.direct["app.orders:shard-c"] = []pkgmongo.CanonicalIndexDefinition{index}

	got, confirmationErrors, attempted := confirmLegacyDifferences(
		context.Background(), indexCollectionRef{Database: "app", Collection: "orders", Type: "collection"},
		[]string{"shard-a", "shard-b", "shard-c"}, source.shards, nil, first, source,
	)
	if !attempted || len(confirmationErrors) != 0 {
		t.Fatalf("attempted=%t confirmationErrors=%v", attempted, confirmationErrors)
	}
	if got.State != IndexConsistencyInconsistent || !hasFindingCode(got.Findings, "index.missing_on_shard") {
		t.Fatalf("evaluation = %#v", got)
	}
	if !reflect.DeepEqual(source.directCalls, []string{"app.orders:shard-a", "app.orders:shard-b", "app.orders:shard-c"}) {
		t.Fatalf("direct confirmation calls = %#v", source.directCalls)
	}
}

func TestConfirmLegacyDifferenceFailureIsInconclusiveAndReported(t *testing.T) {
	// 场景：二次确认读失败时不得保留首次 warning，也不能把 direct collector 标为完整成功。
	source := completeFakeConsistencySource("3.4.24")
	index := consistencyDefinition("tenant_1", "tenant", false, map[string]string{"key": "tenant"})
	first := map[string][]pkgmongo.CanonicalIndexDefinition{"shard-a": {index}, "shard-b": {}}
	source.direct["app.orders:shard-a"] = []pkgmongo.CanonicalIndexDefinition{index}
	source.directErrors["app.orders:shard-b"] = errors.New("fixture confirmation failure")

	got, confirmationErrors, attempted := confirmLegacyDifferences(
		context.Background(), indexCollectionRef{Database: "app", Collection: "orders", Type: "collection"},
		[]string{"shard-a", "shard-b"}, source.shards, nil, first, source,
	)
	if !attempted || len(confirmationErrors) != 1 || got.State != IndexConsistencyInconclusive || got.Coverage != IndexConsistencyCoverageIncomplete {
		t.Fatalf("evaluation=%#v attempted=%t errors=%v", got, attempted, confirmationErrors)
	}
	for _, finding := range got.Findings {
		if finding.Severity == SeverityWarning {
			t.Fatalf("unconfirmed warning = %#v", finding)
		}
	}
}

func TestCollectLegacyKeepsStableDifferenceWithIncompleteCoverage(t *testing.T) {
	// 场景：一个 expected shard 不可达时，另外两个 shards 的稳定 spec 差异仍为 inconsistent，同时 coverage 保持 incomplete。
	source := completeFakeConsistencySource("3.4.24")
	source.shards["shard-c"] = indexShardTarget{Shard: "shard-c", ReplicaSet: "rs-c", Addresses: "c:27017"}
	source.routing["app.orders"] = pkgmongo.IndexRoutingSnapshot{
		Namespace: "app.orders", Sharded: true, ExpectedShards: []string{"shard-a", "shard-b", "shard-c"},
	}
	source.observed["app.orders"] = []string{"shard-a", "shard-b", "shard-c"}
	source.direct["app.orders:shard-a"] = []pkgmongo.CanonicalIndexDefinition{consistencyDefinition("tenant_1", "semantic-a", false, map[string]string{"unique": "false"})}
	source.direct["app.orders:shard-b"] = []pkgmongo.CanonicalIndexDefinition{consistencyDefinition("tenant_1", "semantic-b", false, map[string]string{"unique": "true"})}
	source.directErrors["app.orders:shard-c"] = errors.New("fixture shard unavailable")

	collections, _, collectorErrors := collectIndexConsistency(
		context.Background(), []indexCollectionRef{{Database: "app", Collection: "orders", Type: "collection"}},
		IndexAuditOptions{Collections: []string{"orders"}, Concurrency: 1}, source,
	)
	if len(collectorErrors) == 0 || collections[0].State != IndexConsistencyInconsistent || collections[0].Coverage != IndexConsistencyCoverageIncomplete || !hasFindingCode(collections[0].Findings, "index.spec_mismatch") {
		t.Fatalf("collection=%#v collectorErrors=%v", collections[0], collectorErrors)
	}
}

func TestIncludesGeneralIndexCheck(t *testing.T) {
	// 场景：显式 --checks consistency 不得触发 usage/capacity collector，默认混合 checks 仍需执行。
	if includesGeneralIndexCheck([]IndexAuditCheck{IndexCheckConsistency}) {
		t.Fatal("consistency-only selection included general collectors")
	}
	if !includesGeneralIndexCheck([]IndexAuditCheck{IndexCheckConsistency, IndexCheckUnused}) {
		t.Fatal("mixed selection omitted general collectors")
	}
}

func completeFakeConsistencySource(version string) *fakeIndexConsistencySource {
	return &fakeIndexConsistencySource{
		version: version,
		shards: map[string]indexShardTarget{
			"shard-a": {Shard: "shard-a", ReplicaSet: "rs-a", Addresses: "a:27017"},
			"shard-b": {Shard: "shard-b", ReplicaSet: "rs-b", Addresses: "b:27017"},
		},
		routing: map[string]pkgmongo.IndexRoutingSnapshot{
			"app.orders": {Namespace: "app.orders", Sharded: true, ExpectedShards: []string{"shard-a", "shard-b"}},
		},
		routingErrors: map[string]error{},
		observed:      map[string][]string{"app.orders": {"shard-a", "shard-b"}},
		builds:        map[string][]string{},
		metadata:      map[string][]pkgmongo.MetadataIndexInconsistency{}, metadataErrors: map[string]error{},
		stats: map[string][]pkgmongo.CanonicalIndexDefinition{}, statsErrors: map[string]error{},
		direct: map[string][]pkgmongo.CanonicalIndexDefinition{}, directErrors: map[string]error{},
	}
}

func consistencyDefinitionForShard(name, semantic, shard string) pkgmongo.CanonicalIndexDefinition {
	definition := consistencyDefinition(name, semantic, false, map[string]string{"key": semantic})
	definition.Shard = shard
	return definition
}

func indexCollectionsByNamespace(collections []CollectionIndexAudit) map[string]CollectionIndexAudit {
	result := make(map[string]CollectionIndexAudit, len(collections))
	for _, collection := range collections {
		result[collection.Namespace] = collection
	}
	return result
}

func hasFindingCode(findings []DiagnosticFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
