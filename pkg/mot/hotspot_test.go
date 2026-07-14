package mot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCalculateHotspotUsesActualDurationAndHandlesNamespaces(t *testing.T) {
	// 场景：rate 使用实际采样间隔；新 namespace 以 0 为基线，消失 namespace 不生成负值。
	start := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	first := hotspotSnapshot{
		CollectedAt: start,
		Nodes: []hotspotNodeSnapshot{{
			Identity: "p1", Address: "n1", Uptime: optionalInt64{Value: 100, Present: true},
			Counters: map[string]int64{"query": 100},
			Namespaces: map[string]hotspotNamespaceCounter{
				"db.old": {ReadCount: 5, ReadTimeMicros: 50},
			},
		}},
	}
	second := hotspotSnapshot{
		CollectedAt: start.Add(12 * time.Second),
		Nodes: []hotspotNodeSnapshot{{
			Identity: "p1", Address: "n1", Uptime: optionalInt64{Value: 112, Present: true},
			Counters: map[string]int64{"query": 124},
			Namespaces: map[string]hotspotNamespaceCounter{
				"db.new": {ReadCount: 12, ReadTimeMicros: 120},
			},
		}},
	}

	result := calculateHotspot(first, second, HotspotOptions{TopN: 10})
	if result.EffectiveDuration != 12*time.Second {
		t.Fatalf("effective duration = %s, want 12s", result.EffectiveDuration)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Rates["query"] != 2 {
		t.Fatalf("node rates = %#v, want query=2", result.Nodes)
	}
	if len(result.Namespaces) != 1 || result.Namespaces[0].Namespace != "db.new" || result.Namespaces[0].ReadPerSecond != 1 {
		t.Fatalf("namespace rates = %#v", result.Namespaces)
	}
}

func TestCalculateHotspotRejectsCounterReset(t *testing.T) {
	// 场景：uptime 或累计 counter 下降表示重启/reset，该节点不得生成错误 delta。
	start := time.Now().UTC()
	first := hotspotSnapshot{CollectedAt: start, Nodes: []hotspotNodeSnapshot{{Identity: "p1", Address: "n1", Uptime: optionalInt64{Value: 100, Present: true}, Counters: map[string]int64{"query": 100}}}}
	second := hotspotSnapshot{CollectedAt: start.Add(time.Second), Nodes: []hotspotNodeSnapshot{{Identity: "p1", Address: "n1", Uptime: optionalInt64{Value: 1, Present: true}, Counters: map[string]int64{"query": 2}}}}

	result := calculateHotspot(first, second, HotspotOptions{TopN: 10})
	if len(result.Nodes) != 0 {
		t.Fatalf("reset node produced rates: %#v", result.Nodes)
	}
	assertFindingCode(t, result.Findings, "node.counter_reset", SeverityWarning)
}

func TestWaitForHotspotSampleRespectsContext(t *testing.T) {
	// 场景：采样等待被取消时立即返回 ErrCancelled，不启动第二快照。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForHotspotSample(ctx, time.Hour)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("wait error = %v, want ErrCancelled", err)
	}
}

func TestCalculateHotspotTreatsMissingUptimeAsUnknownAndNamespaceResetAsIncomparable(t *testing.T) {
	// 场景：uptime 缺失不能伪装成零；namespace counter 下降必须显式标记 reset，不能 clamp 成零 rate。
	start := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	first := hotspotSnapshot{CollectedAt: start, Nodes: []hotspotNodeSnapshot{{Identity: "n", Address: "n", Counters: map[string]int64{"query": 1}, Namespaces: map[string]hotspotNamespaceCounter{"db.c": {ReadCount: 10}}}}}
	second := hotspotSnapshot{CollectedAt: start.Add(time.Second), Nodes: []hotspotNodeSnapshot{{Identity: "n", Address: "n", Counters: map[string]int64{"query": 2}, Namespaces: map[string]hotspotNamespaceCounter{"db.c": {ReadCount: 1}}}}}
	result := calculateHotspot(first, second, HotspotOptions{TopN: 10})
	assertFindingCode(t, result.Findings, "hotspot.namespace_counter_reset", SeverityInfo)
	if len(result.Namespaces) != 0 {
		t.Fatalf("namespaces = %#v, want reset omitted", result.Namespaces)
	}
}

func TestCalculateHotspotUsesPerNodeCollectionTime(t *testing.T) {
	// 场景：节点采集耗时不同，rate 必须使用该节点两次完成时间而不是全局近似窗口。
	start := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	first := hotspotSnapshot{CollectedAt: start, Nodes: []hotspotNodeSnapshot{{Identity: "n", Address: "n", CollectedAt: start.Add(time.Second), Counters: map[string]int64{"query": 10}}}}
	second := hotspotSnapshot{CollectedAt: start.Add(10 * time.Second), Nodes: []hotspotNodeSnapshot{{Identity: "n", Address: "n", CollectedAt: start.Add(13 * time.Second), Counters: map[string]int64{"query": 34}}}}
	result := calculateHotspot(first, second, HotspotOptions{TopN: 10})
	if len(result.Nodes) != 1 || result.Nodes[0].Rates["query"] != 2 {
		t.Fatalf("nodes = %#v", result.Nodes)
	}
}

func TestCalculateHotspotRequiresWindowEvidenceAndPreservesUnavailableLatency(t *testing.T) {
	// 场景：queue 需在两个快照持续存在；连接拒绝为 critical；零操作平均延迟保持 unavailable。
	start := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	first := hotspotSnapshot{CollectedAt: start, Nodes: []hotspotNodeSnapshot{{
		Identity: "n", Address: "n", CollectedAt: start,
		Counters: map[string]int64{"connectionsRejected": 1, "readLatencyMicros": 100, "readLatencyOps": 1, "wtApplicationEviction": 2},
		Gauges:   map[string]int64{"queueTotal": 1},
	}}}
	second := hotspotSnapshot{CollectedAt: start.Add(time.Second), Nodes: []hotspotNodeSnapshot{{
		Identity: "n", Address: "n", CollectedAt: start.Add(time.Second),
		Counters: map[string]int64{"connectionsRejected": 2, "readLatencyMicros": 300, "readLatencyOps": 3, "wtApplicationEviction": 3},
		Gauges:   map[string]int64{"queueTotal": 2},
	}}}
	result := calculateHotspot(first, second, HotspotOptions{TopN: 10})
	assertFindingCode(t, result.Findings, "connection.rejected_during_sample", SeverityCritical)
	assertFindingCode(t, result.Findings, "operation.queue_sustained", SeverityWarning)
	assertFindingCode(t, result.Findings, "storage.eviction_pressure", SeverityWarning)
	if len(result.Nodes) != 1 || result.Nodes[0].AverageLatencies["read"] == nil || *result.Nodes[0].AverageLatencies["read"] != 100 {
		t.Fatalf("average latency = %#v", result.Nodes)
	}
	if result.Nodes[0].AverageLatencies["write"] != nil {
		t.Fatalf("zero-operation write latency = %#v, want unavailable", result.Nodes[0].AverageLatencies["write"])
	}
}

func TestCalculateHotspotDoesNotWarnOnSingleQueueSnapshot(t *testing.T) {
	// 场景：仅第二快照有排队不足以判定持续排队。
	start := time.Now().UTC()
	first := hotspotSnapshot{CollectedAt: start, Nodes: []hotspotNodeSnapshot{{Identity: "n", Address: "n", CollectedAt: start, Counters: map[string]int64{"query": 1}, Gauges: map[string]int64{"queueTotal": 0}}}}
	second := hotspotSnapshot{CollectedAt: start.Add(time.Second), Nodes: []hotspotNodeSnapshot{{Identity: "n", Address: "n", CollectedAt: start.Add(time.Second), Counters: map[string]int64{"query": 2}, Gauges: map[string]int64{"queueTotal": 1}}}}
	result := calculateHotspot(first, second, HotspotOptions{TopN: 10})
	assertNoFindingCode(t, result.Findings, "operation.queue_sustained")
}
