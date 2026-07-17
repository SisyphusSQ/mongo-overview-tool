package mot

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

func TestCollectSlowlogHostsRunsConcurrentlyAndPreservesOrder(t *testing.T) {
	// 测试 slowlog 成员层并发调度节点，同时保持 replica status 中的稳定顺序。
	members := []pkgmongo.RsMember{
		{Name: "node-1", State: pkgmongo.StatePrimary},
		{Name: "node-2", State: pkgmongo.StateSecondary},
		{Name: "node-3", State: pkgmongo.StateSecondary},
	}
	var current atomic.Int64
	var maximum atomic.Int64
	hosts, _, _ := collectSlowlogHostsPartial(context.Background(), members, func(_ context.Context, member pkgmongo.RsMember) (HostSlowlogSummary, []CollectorStatus, []error) {
		running := current.Add(1)
		defer current.Add(-1)
		updateMaximum(&maximum, running)
		time.Sleep(10 * time.Millisecond)
		return HostSlowlogSummary{Address: member.Name}, nil, nil
	})
	if maximum.Load() <= 1 {
		t.Fatalf("maximum host concurrency = %d, want greater than 1", maximum.Load())
	}
	if len(hosts) != 3 || hosts[0].Address != "node-1" || hosts[1].Address != "node-2" || hosts[2].Address != "node-3" {
		t.Fatalf("host order changed: %#v", hosts)
	}
}

func TestCollectSlowlogShardsHonorsConcurrencyAndOrder(t *testing.T) {
	// 场景：显式 session 并发采集多个 shard，同时保持最终 replica set 顺序稳定。
	shards := []pkgmongo.Shard{{Id: "shard-1"}, {Id: "shard-2"}, {Id: "shard-3"}, {Id: "shard-4"}}
	var current atomic.Int64
	var maximum atomic.Int64

	result := collectSlowlogShards(context.Background(), shards, 2, func(_ context.Context, shard pkgmongo.Shard) slowlogShardCollection {
		running := current.Add(1)
		defer current.Add(-1)
		updateMaximum(&maximum, running)
		time.Sleep(10 * time.Millisecond)
		return slowlogShardCollection{replicaSet: ReplicaSetSlowlogSummary{Name: shard.Id}, include: true}
	})
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	for i, shard := range shards {
		if !result[i].include || result[i].replicaSet.Name != shard.Id {
			t.Fatalf("result order changed: %#v", result)
		}
	}
}

func TestConvertSlowlogViewPreservesPresenceAndAvoidsInvalidRatios(t *testing.T) {
	// 场景：缺失、真实零和可计算 ratio 必须区分，JSON 不得出现 Infinity/NaN。
	zero := int64(0)
	hundred := int64(100)
	raw := pkgmongo.SlowlogView{
		Ns: "db.c", Op: "query", QueryHash: "HASH", PlanSummary: "COLLSCAN",
		MaxDocsExamined: &hundred, MaxDocsReturned: &zero,
		AppNames: []string{"secret-app", "another-app"}, ErrorCount: 1, CollectionScanCount: 2,
	}
	item, findings := convertSlowlogView(raw)
	if item.WorstDocsToReturned != nil {
		t.Fatalf("ratio = %#v, want unavailable for zero returned", item.WorstDocsToReturned)
	}
	assertFindingCode(t, findings, "query.zero_return_scan", SeverityWarning)
	assertFindingCode(t, findings, "query.collection_scan", SeverityWarning)
	if reflect.DeepEqual(item.AppNames, raw.AppNames) {
		t.Fatalf("app names were not anonymized: %#v", item.AppNames)
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" {
		t.Fatal("empty JSON")
	}

	ten := int64(10)
	raw.MaxDocsReturned = &ten
	item, _ = convertSlowlogView(raw)
	if item.WorstDocsToReturned == nil || math.Abs(*item.WorstDocsToReturned-10) > 0.001 {
		t.Fatalf("ratio = %#v, want 10", item.WorstDocsToReturned)
	}

	raw.MaxDocsExamined, raw.MaxDocsReturned = nil, nil
	item, _ = convertSlowlogView(raw)
	if item.MaxDocsExamined != nil || item.MaxDocsReturned != nil || item.WorstDocsToReturned != nil {
		t.Fatalf("missing metrics became present: %#v", item)
	}
}

func TestCollectSlowlogDatabaseSummariesPreservesPartialCoverage(t *testing.T) {
	// 场景：单 database 失败不能丢弃其它成功 insight；权限不足只形成 unauthorized status。
	loader := func(_ context.Context, _ string, database string, _ SlowlogSort) (DatabaseSlowlogSummary, bool, error) {
		switch database {
		case "ok":
			return DatabaseSlowlogSummary{Database: database}, true, nil
		case "denied":
			return DatabaseSlowlogSummary{}, false, drivermongo.CommandError{Code: 13}
		default:
			return DatabaseSlowlogSummary{}, false, errors.New("network")
		}
	}
	summaries, statuses, collectorErrors := collectSlowlogDatabaseSummariesPartial(context.Background(), "node", "rs0", []string{"ok", "denied", "failed"}, SlowlogSortCount, 2, loader)
	if len(summaries) != 1 || len(statuses) != 3 || len(collectorErrors) != 1 {
		t.Fatalf("summaries=%#v statuses=%#v errors=%#v", summaries, statuses, collectorErrors)
	}
	foundUnauthorized := false
	for _, status := range statuses {
		if status.State == CapabilityUnauthorized {
			foundUnauthorized = true
		}
	}
	if !foundUnauthorized {
		t.Fatalf("statuses = %#v, want unauthorized", statuses)
	}
}

func TestFindSlowlogAddress(t *testing.T) {
	// 测试 SlowlogDetail 能从聚合结果定位实际持有 queryHash 的成员节点。
	summary := &SlowlogSummaryResult{
		ReplicaSets: []ReplicaSetSlowlogSummary{
			{
				Hosts: []HostSlowlogSummary{
					{
						Address: "node-a:27017",
						Databases: []DatabaseSlowlogSummary{
							{
								Database: "app",
								Items: []SlowlogSummaryItem{
									{QueryHash: "HASH-A"},
								},
							},
						},
					},
				},
			},
		},
	}

	if got := findSlowlogAddress(summary, "app", "HASH-A"); got != "node-a:27017" {
		t.Fatalf("findSlowlogAddress() = %q, want node-a:27017", got)
	}
	if got := findSlowlogAddress(summary, "app", "missing"); got != "" {
		t.Fatalf("findSlowlogAddress() missing = %q, want empty", got)
	}
	if got := findSlowlogAddress(nil, "app", "HASH-A"); got != "" {
		t.Fatalf("findSlowlogAddress() nil = %q, want empty", got)
	}
}
