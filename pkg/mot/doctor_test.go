package mot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

func TestCollectDoctorShardsHonorsConcurrencyAndOrder(t *testing.T) {
	// 场景：显式 session 并发采集多个 shard，但聚合仍保持 shard 清单顺序。
	shards := []pkgmongo.Shard{{Id: "shard-1"}, {Id: "shard-2"}, {Id: "shard-3"}, {Id: "shard-4"}}
	var current atomic.Int64
	var maximum atomic.Int64

	result := collectDoctorShards(context.Background(), shards, 2, func(_ context.Context, shard pkgmongo.Shard) doctorShardCollection {
		running := current.Add(1)
		defer current.Add(-1)
		updateMaximum(&maximum, running)
		time.Sleep(10 * time.Millisecond)
		return doctorShardCollection{statuses: []CollectorStatus{{Name: shard.Id}}, successful: true}
	})
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	for i, shard := range shards {
		if len(result[i].statuses) != 1 || result[i].statuses[0].Name != shard.Id {
			t.Fatalf("result order changed: %#v", result)
		}
	}
}

func TestDoctorValidatesOptionsBeforeConnecting(t *testing.T) {
	// 场景：非法阈值必须在任何 MongoDB 调用前返回 ErrInvalidOptions。
	_, err := (&Client{}).Doctor(context.Background(), DoctorOptions{
		ReplicationLagWarning:  10 * time.Minute,
		ReplicationLagCritical: time.Minute,
	})
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("Doctor error = %v, want ErrInvalidOptions", err)
	}
}

func TestFilterFindingsByMinimumSeverity(t *testing.T) {
	// 场景：minimum-severity 只影响返回 finding，不改变 collector status。
	findings := []DiagnosticFinding{
		{Code: "i", Severity: SeverityInfo},
		{Code: "w", Severity: SeverityWarning},
		{Code: "c", Severity: SeverityCritical},
	}
	got := filterFindingsByMinimumSeverity(findings, SeverityWarning)
	if len(got) != 2 || got[0].Code != "w" || got[1].Code != "c" {
		t.Fatalf("filtered findings = %#v", got)
	}
}

func TestEvaluateDoctorReplicaSetDeterministicHealth(t *testing.T) {
	// 场景：缺失 PRIMARY 且可写多数不可用时必须产生 critical finding。
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, time.UTC)
	status := pkgmongo.RsStatus{
		Set:                "rs0",
		Date:               now,
		WriteMajorityCount: 2,
		Members: []pkgmongo.RsMember{
			{Name: "n1:27017", State: pkgmongo.StateSecondary, StateStr: "SECONDARY", Health: 1},
			{Name: "n2:27017", State: pkgmongo.StateDown, StateStr: "DOWN", Health: 0},
		},
	}

	findings := evaluateDoctorReplicaSet(status, "", nil, now, defaultDoctorOptions())
	assertFindingCode(t, findings, "replica.primary_missing", SeverityCritical)
	assertFindingCode(t, findings, "replica.majority_unavailable", SeverityCritical)
	assertFindingCode(t, findings, "replica.member_unhealthy", SeverityCritical)
}

func TestEvaluateDoctorReplicaSetAvoidsStaticLagFalsePositive(t *testing.T) {
	// 场景：静止集群所有数据节点最后写入时间都旧但一致时，不得按 wall clock 误报复制延迟。
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, time.UTC)
	lastWrite := now.Add(-24 * time.Hour)
	status := pkgmongo.RsStatus{
		Set:                "rs0",
		Date:               now,
		WriteMajorityCount: 2,
		Members: []pkgmongo.RsMember{
			{Name: "n1:27017", State: pkgmongo.StatePrimary, StateStr: "PRIMARY", Health: 1, OptimeDate: lastWrite},
			{Name: "n2:27017", State: pkgmongo.StateSecondary, StateStr: "SECONDARY", Health: 1, OptimeDate: lastWrite},
		},
	}

	findings := evaluateDoctorReplicaSet(status, "", nil, now, defaultDoctorOptions())
	assertNoFindingCode(t, findings, "replica.lag_high")
	assertNoFindingCode(t, findings, "replica.lag_critical")
}

func TestEvaluateDoctorNodeRequiresCombinedWiredTigerEvidence(t *testing.T) {
	// 场景：cache 使用率高但 eviction/排队无增长时不告警；出现组合压力证据后才告警。
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, time.UTC)
	node := doctorNodeSnapshot{
		ReplicaSet: "rs0",
		Address:    "n1:27017",
		CacheMax:   optionalInt64{Value: 100, Present: true},
		CacheUsed:  optionalInt64{Value: 95, Present: true},
	}
	findings := evaluateDoctorNode(node, now)
	assertNoFindingCode(t, findings, "storage.eviction_pressure")
	assertNoFindingCode(t, findings, "storage.cache_pressure_inconclusive")

	node.EvictionPressure = optionalInt64{Value: 3, Present: true}
	node.QueueTotal = optionalInt64{Value: 2, Present: true}
	findings = evaluateDoctorNode(node, now)
	assertFindingCode(t, findings, "storage.cache_pressure_inconclusive", SeverityInfo)
}

func assertFindingCode(t *testing.T, findings []DiagnosticFinding, code string, severity Severity) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			if finding.Severity != severity {
				t.Fatalf("finding %s severity = %s, want %s", code, finding.Severity, severity)
			}
			return
		}
	}
	t.Fatalf("finding %s not found in %#v", code, findings)
}

func assertNoFindingCode(t *testing.T, findings []DiagnosticFinding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			t.Fatalf("unexpected finding %s in %#v", code, findings)
		}
	}
}
