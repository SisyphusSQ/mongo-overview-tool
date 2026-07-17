package clioutput

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
)

func TestPrintCollectionStatsCheckShardModes(t *testing.T) {
	// 测试 check-shard 默认只展示未分片集合，show-all 展示全部集合。（风险复现用例）
	withColorDisabled(t)
	result := &mot.CollectionStatsResult{
		Databases: []mot.DatabaseStats{
			{
				Name:             "app",
				StorageSizeBytes: 1024,
				Collections: []mot.CollectionStats{
					{Namespace: "app.unsharded", Count: 10},
					{Namespace: "app.sharded", Count: 20, IsSharded: true},
				},
			},
		},
	}

	var defaultOutput bytes.Buffer
	err := PrintCollectionStats(&defaultOutput, result, CollectionStatsPrintOptions{
		ShardView: true,
		ShowAll:   false,
	})
	if err != nil {
		t.Fatalf("PrintCollectionStats failed: %v", err)
	}
	if !strings.Contains(defaultOutput.String(), "app.unsharded") {
		t.Fatalf("default output omitted unsharded collection:\n%s", defaultOutput.String())
	}
	if strings.Contains(defaultOutput.String(), "app.sharded") {
		t.Fatalf("default output included sharded collection:\n%s", defaultOutput.String())
	}

	var showAllOutput bytes.Buffer
	err = PrintCollectionStats(&showAllOutput, result, CollectionStatsPrintOptions{
		ShardView: true,
		ShowAll:   true,
	})
	if err != nil {
		t.Fatalf("PrintCollectionStats failed: %v", err)
	}
	for _, namespace := range []string{"app.unsharded", "app.sharded"} {
		if !strings.Contains(showAllOutput.String(), namespace) {
			t.Fatalf("show-all output omitted %s:\n%s", namespace, showAllOutput.String())
		}
	}
}

func TestPrintDiagnosticResultGoldenAndRedactionBoundary(t *testing.T) {
	// 场景：table 与 JSON 都只渲染 SDK 的脱敏字段，并稳定保留 finding/status。
	withColorDisabled(t)
	result := &mot.DoctorResult{
		ClusterType:       mot.ClusterReplicaSet,
		Findings:          []mot.DiagnosticFinding{{Code: "replica.primary_missing", Severity: mot.SeverityCritical, Scope: mot.FindingScope{Type: mot.ScopeReplicaSet, ReplicaSet: "rs0"}, Summary: "副本集当前没有 PRIMARY"}},
		CollectorStatuses: []mot.CollectorStatus{{Name: "replica_status", State: mot.CapabilitySupported, Scope: mot.FindingScope{Type: mot.ScopeReplicaSet, ReplicaSet: "rs0"}}},
	}
	var table bytes.Buffer
	if err := PrintDiagnosticResult(&table, result, FormatTable); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "diagnostics_doctor.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if table.String() != string(want) {
		t.Fatalf("table output mismatch\n--- got ---\n%s\n--- want ---\n%s", table.String(), want)
	}
	var jsonOutput bytes.Buffer
	if err := PrintDiagnosticResult(&jsonOutput, result, FormatJSON); err != nil {
		t.Fatal(err)
	}
	jsonWant, err := os.ReadFile(filepath.Join("testdata", "diagnostics_doctor.json.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if jsonOutput.String() != string(jsonWant) {
		t.Fatalf("JSON golden mismatch\n--- got ---\n%s\n--- want ---\n%s", jsonOutput.String(), jsonWant)
	}
	for _, forbidden := range []string{"mongodb://", "password", "command", "filter", "session"} {
		if strings.Contains(jsonOutput.String(), forbidden) {
			t.Fatalf("JSON contains forbidden value %q: %s", forbidden, jsonOutput.String())
		}
	}
}

func TestPrintAllDiagnosticCommandsGolden(t *testing.T) {
	// 场景：ops、hotspot、index-audit、capacity 与 diff 的 table schema 保持稳定，并保留 collector scope。
	withColorDisabled(t)
	count, data, storage, indexSize, delta := int64(2), int64(100), int64(80), int64(20), int64(5)
	results := []struct {
		name  string
		value any
	}{
		{"ops", &mot.CurrentOperationsResult{ClusterType: mot.ClusterReplicaSet, Visibility: "all_users", Source: "aggregation", Operations: []mot.CurrentOperation{{Host: "node", Namespace: "db.c", Operation: "query", RunningDuration: 3 * time.Second}}, CollectorStatuses: []mot.CollectorStatus{{Name: "current_operations", State: mot.CapabilitySupported, Scope: mot.FindingScope{Type: mot.ScopeCluster}}}}},
		{"hotspot", &mot.HotspotResult{ClusterType: mot.ClusterSharded, EffectiveDuration: 2 * time.Second, Namespaces: []mot.NamespaceHotspot{{Shard: "s0", Host: "node", Namespace: "db.c", ReadPerSecond: 1.5, WritePerSecond: 0.5, TotalTimeMicros: 40}}, CollectorStatuses: []mot.CollectorStatus{{Name: "hotspot", State: mot.CapabilitySupported, Scope: mot.FindingScope{Type: mot.ScopeNode, Shard: "s0", Node: "node"}}}}},
		{"index", &mot.IndexAuditResult{Collections: []mot.CollectionIndexAudit{{Namespace: "db.c", Indexes: []mot.IndexObservation{{Name: "a_1", Shard: "s0", Host: "node", Ops: 0, Since: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), SizeBytes: &indexSize}}}}, CollectorStatuses: []mot.CollectorStatus{{Name: "index_usage", State: mot.CapabilitySupported, Scope: mot.FindingScope{Type: mot.ScopeNamespace, Namespace: "db.c"}}}}},
		{"capacity", &mot.CapacityResult{SchemaVersion: 1, ClusterIdentity: mot.CapacityIdentity{TopologyType: mot.ClusterReplicaSet, Digest: "digest"}, Databases: []mot.DatabaseCapacity{{Name: "db", Collections: []mot.CollectionCapacity{{Namespace: "db.c", Count: &count, DataSizeBytes: &data, StorageSizeBytes: &storage, IndexSizeBytes: &indexSize}}}}, CollectorStatuses: []mot.CollectorStatus{{Name: "collection_capacity", State: mot.CapabilitySupported, Scope: mot.FindingScope{Type: mot.ScopeNamespace, Namespace: "db.c"}}}}},
		{"diff", &mot.CapacityDiffResult{Duration: 24 * time.Hour, Collections: []mot.CollectionCapacityDiff{{Namespace: "db.c", State: "existing", Count: mot.CapacityDelta{Delta: &delta}}}}},
	}
	var output bytes.Buffer
	for _, item := range results {
		fmt.Fprintf(&output, "=== %s ===\n", item.name)
		if err := PrintDiagnosticResult(&output, item.value, FormatTable); err != nil {
			t.Fatal(err)
		}
		var jsonOutput bytes.Buffer
		if err := PrintDiagnosticResult(&jsonOutput, item.value, FormatJSON); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"mongodb://", "password", "commandDocument", "sessionId"} {
			if strings.Contains(jsonOutput.String(), forbidden) {
				t.Fatalf("%s JSON leaked %q", item.name, forbidden)
			}
		}
	}
	want, err := os.ReadFile(filepath.Join("testdata", "diagnostics_all.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != string(want) {
		t.Fatalf("diagnostic golden mismatch\n--- got ---\n%s\n--- want ---\n%s", output.String(), want)
	}
}

func TestPrintIndexConsistencyGoldenAndRedaction(t *testing.T) {
	// 场景：一致性 table/JSON 稳定展示四态 summary、coverage、fallback 和安全 fingerprint，禁止原始定义泄漏。
	result := &mot.IndexAuditResult{
		CollectedAt:        time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC),
		ConsistencySummary: mot.IndexConsistencySummary{Inconsistent: 1},
		Collections: []mot.CollectionIndexAudit{{
			Namespace: "app.orders", Sharded: true, State: mot.IndexConsistencyInconsistent,
			Strategy: mot.IndexConsistencyDirectListIndexes, ExpectedShards: []string{"shard-a", "shard-b"},
			ObservedShards: []string{"shard-a"}, Coverage: mot.IndexConsistencyCoverageIncomplete,
			Fallback: &mot.IndexConsistencyFallback{From: mot.IndexConsistencyIndexStats, To: mot.IndexConsistencyDirectListIndexes, ReasonCode: "incomplete_coverage"},
			Differences: []mot.IndexConsistencyDifference{{
				Code: "index.missing_on_shard", IndexName: "tenant_1", Shards: []string{"shard-b"},
				Key: []mot.IndexKeyField{{Field: "tenant", Order: "1"}}, Fingerprint: "safe-fingerprint",
			}},
			Indexes: []mot.IndexObservation{},
		}},
		Findings: []mot.DiagnosticFinding{{
			Code: "index.missing_on_shard", Severity: mot.SeverityWarning,
			Scope: mot.FindingScope{Type: mot.ScopeNamespace, Namespace: "app.orders"}, Summary: "索引在部分 expected shards 上缺失",
		}},
		CollectorStatuses: []mot.CollectorStatus{{
			Name: "index_consistency_direct", State: mot.CapabilityFailed,
			Scope: mot.FindingScope{Type: mot.ScopeNamespace, Namespace: "app.orders"}, ReasonCode: "incomplete_coverage",
		}},
	}
	for _, tt := range []struct {
		format string
		golden string
	}{
		{format: FormatTable, golden: "diagnostics_index_consistency.golden"},
		{format: FormatJSON, golden: "diagnostics_index_consistency.json.golden"},
	} {
		var output bytes.Buffer
		if err := PrintDiagnosticResult(&output, result, tt.format); err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", tt.golden))
		if err != nil {
			t.Fatal(err)
		}
		if output.String() != string(want) {
			t.Fatalf("%s output mismatch\n--- got ---\n%s\n--- want ---\n%s", tt.format, output.String(), want)
		}
		for _, forbidden := range []string{"private-value", "partialFilterExpression", "mongodb://", "server detail"} {
			if strings.Contains(output.String(), forbidden) {
				t.Fatalf("%s output leaked %q", tt.format, forbidden)
			}
		}
	}
}

func TestPrintDiagnosticPartialAndNoResultGolden(t *testing.T) {
	// 场景：部分覆盖与完全无结果都必须保留 collector status，table 不能误报 healthy。
	withColorDisabled(t)
	partial := &mot.DoctorResult{
		ClusterType: mot.ClusterSharded,
		Findings:    []mot.DiagnosticFinding{{Code: "operation.queue_sustained", Severity: mot.SeverityWarning, Scope: mot.FindingScope{Type: mot.ScopeNode, Shard: "s0", Node: "n1"}, Summary: "两个快照均观察到操作排队"}},
		CollectorStatuses: []mot.CollectorStatus{
			{Name: "server_status", State: mot.CapabilitySupported, Scope: mot.FindingScope{Type: mot.ScopeNode, Shard: "s0", Node: "n1"}},
			{Name: "server_status", State: mot.CapabilityFailed, Scope: mot.FindingScope{Type: mot.ScopeNode, Shard: "s0", Node: "n2"}, ReasonCode: "collector_failed"},
		},
	}
	noResult := &mot.DoctorResult{ClusterType: mot.ClusterReplicaSet, CollectorStatuses: []mot.CollectorStatus{{Name: "replica_status", State: mot.CapabilityUnauthorized, Scope: mot.FindingScope{Type: mot.ScopeCluster}, ReasonCode: "unauthorized"}}}
	var output bytes.Buffer
	fmt.Fprintln(&output, "=== partial ===")
	if err := PrintDiagnosticResult(&output, partial, FormatTable); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(&output, "=== no-result ===")
	if err := PrintDiagnosticResult(&output, noResult, FormatTable); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "diagnostics_states.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != string(want) {
		t.Fatalf("state golden mismatch\n--- got ---\n%s\n--- want ---\n%s", output.String(), want)
	}
}

func TestPrintSlowlogSummaryFixture(t *testing.T) {
	// 测试 slowlog formatter 保留副本集、节点、数据库和聚合字段。
	withColorDisabled(t)
	result := &mot.SlowlogSummaryResult{
		ClusterType: mot.ClusterReplicaSet,
		ReplicaSets: []mot.ReplicaSetSlowlogSummary{
			{
				Name: "rs0",
				Hosts: []mot.HostSlowlogSummary{
					{
						Address: "node-1:27017",
						State:   "PRIMARY",
						Databases: []mot.DatabaseSlowlogSummary{
							{
								Database: "app",
								Total:    3,
								Items: []mot.SlowlogSummaryItem{
									{Namespace: "app.orders", QueryHash: "legacy:0123456789ABCDEF", Operation: "query", Count: 3},
								},
							},
						},
					},
				},
			},
		},
	}

	var output bytes.Buffer
	if err := PrintSlowlogSummary(&output, result, SlowlogPrintOptions{}); err != nil {
		t.Fatalf("PrintSlowlogSummary failed: %v", err)
	}
	for _, value := range []string{"ReplSet: rs0", "node-1:27017", "Database: app", "app.orders", "legacy:0123456789ABCDEF  query"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("slowlog output omitted %q:\n%s", value, output.String())
		}
	}
}

func TestBulkObserverDryRunFixture(t *testing.T) {
	// 测试 bulk observer 的 dry-run summary 和完成提示。
	withColorDisabled(t)
	var output bytes.Buffer
	observer, err := NewBulkObserver(&output, "", BulkObserverOptions{
		Action:     "bulk-delete",
		Database:   "app",
		Collection: "events",
		Filter:     `{status: "expired"}`,
		BatchSize:  100,
		Pause:      100 * time.Millisecond,
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("NewBulkObserver failed: %v", err)
	}
	defer observer.Close()

	observer.OnBulkStart(context.Background(), 12)
	observer.OnBulkDone(context.Background(), mot.BulkResult{DryRun: true, MatchedTotal: 12})
	for _, value := range []string{"bulk-delete Summary", "Database:   app", "Matched:    12", "Mode:       DRY-RUN", "Count only"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("bulk output omitted %q:\n%s", value, output.String())
		}
	}
}

func withColorDisabled(t *testing.T) {
	t.Helper()
	previous := color.NoColor
	color.NoColor = true
	t.Cleanup(func() {
		color.NoColor = previous
	})
}
