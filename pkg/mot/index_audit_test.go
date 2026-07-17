package mot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

func TestCollectIndexAuditTargetsRunsConcurrentlyAndPreservesOrder(t *testing.T) {
	// 场景：同一 namespace 的节点任务并发执行，聚合顺序仍与稳定 target 清单一致。
	targets := []hotspotTarget{{Address: "node-1"}, {Address: "node-2"}, {Address: "node-3"}}
	var current atomic.Int64
	var maximum atomic.Int64

	result := collectIndexAuditTargets(context.Background(), targets, func(_ context.Context, target hotspotTarget) indexAuditTargetCollection {
		running := current.Add(1)
		defer current.Add(-1)
		updateMaximum(&maximum, running)
		time.Sleep(10 * time.Millisecond)
		return indexAuditTargetCollection{indexes: []IndexObservation{{Host: target.Address}}}
	})
	if maximum.Load() <= 1 {
		t.Fatalf("maximum concurrency = %d, want greater than 1", maximum.Load())
	}
	for i, target := range targets {
		if len(result[i].indexes) != 1 || result[i].indexes[0].Host != target.Address {
			t.Fatalf("result order changed: %#v", result)
		}
	}
}

func TestValidateIndexConsistencyTopologyRequiresMongosOnlyWhenRequested(t *testing.T) {
	// 场景：默认 consistency 在副本集入口必须整体拒绝；显式通用 checks 保持既有副本集能力。
	if err := validateIndexConsistencyTopology(pkgmongo.ClusterRepl, false); err != nil {
		t.Fatalf("general-only topology error = %v", err)
	}
	if err := validateIndexConsistencyTopology(pkgmongo.ClusterShard, true); err != nil {
		t.Fatalf("sharded consistency topology error = %v", err)
	}
	if err := validateIndexConsistencyTopology(pkgmongo.ClusterRepl, true); !errors.Is(err, ErrUnsupportedTopology) {
		t.Fatalf("replica consistency error = %v, want ErrUnsupportedTopology", err)
	}
}

func TestEvaluateIndexAuditRequiresCompleteObservationForUnused(t *testing.T) {
	// 场景：部分节点不可达时，即使已返回节点 ops 为 0，也只能给 inconclusive。
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	result := CollectionIndexAudit{Namespace: "db.c", Indexes: []IndexObservation{{Name: "a_1", Ops: 0, Since: now.Add(-8 * 24 * time.Hour)}}}
	findings := evaluateIndexAuditCollection(result, 2, IndexAuditOptions{Checks: []IndexAuditCheck{IndexCheckUnused}, MinObservation: 7 * 24 * time.Hour}, now)
	if len(findings) != 1 || findings[0].Code != "index.usage_inconclusive" || findings[0].Severity != SeverityInfo {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestEvaluateIndexAuditRequiresCompatiblePropertiesForPrefix(t *testing.T) {
	// 场景：key 前缀相同但 unique 属性不同，不能判定为疑似冗余。
	result := CollectionIndexAudit{Namespace: "db.c", Indexes: []IndexObservation{
		{Name: "a_1", Key: []IndexKeyField{{Field: "a", Order: "1"}}, Unique: true},
		{Name: "a_1_b_1", Key: []IndexKeyField{{Field: "a", Order: "1"}, {Field: "b", Order: "1"}}},
	}}
	findings := evaluateIndexAuditCollection(result, 1, IndexAuditOptions{Checks: []IndexAuditCheck{IndexCheckRedundant}}, time.Now())
	if len(findings) != 0 {
		t.Fatalf("findings = %#v, want none", findings)
	}
}

func TestEvaluateIndexAuditShardedOwnershipUnknownIsInconclusive(t *testing.T) {
	// 场景：TOO-304 routing 未实现时，分片 namespace 不能以全体集群节点作为 unused 期望集合。
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	result := CollectionIndexAudit{Namespace: "db.c", Indexes: []IndexObservation{{Name: "a_1", Since: now.Add(-8 * 24 * time.Hour)}}}
	findings := evaluateIndexAuditCollection(result, 1, IndexAuditOptions{Checks: []IndexAuditCheck{IndexCheckUnused}, MinObservation: 7 * 24 * time.Hour}, now, false)
	if len(findings) != 1 || findings[0].Code != "index.usage_inconclusive" || findings[0].Evidence["ownershipCoverage"] != "unknown" {
		t.Fatalf("findings = %#v", findings)
	}
}
