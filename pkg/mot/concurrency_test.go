package mot

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnrichNodeOverviewsHonorsConcurrencyAndOrder(t *testing.T) {
	// 测试节点概览 enrichment 遵守并发上限，并保持输入顺序。
	nodes := []NodeOverview{
		{Address: "node-1", State: "PRIMARY"},
		{Address: "node-2", State: "SECONDARY"},
		{Address: "node-3", State: "SECONDARY"},
		{Address: "node-4", State: "SECONDARY"},
	}
	var current atomic.Int64
	var maximum atomic.Int64

	result, err := enrichNodeOverviews(context.Background(), nodes, 2, func(_ context.Context, node NodeOverview) (NodeOverview, error) {
		running := current.Add(1)
		defer current.Add(-1)
		updateMaximum(&maximum, running)
		time.Sleep(20 * time.Millisecond)
		node.Version = "version-" + node.Address
		return node, nil
	})
	if err != nil {
		t.Fatalf("enrichNodeOverviews failed: %v", err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	for i, node := range result {
		if node.Address != nodes[i].Address || node.Version != "version-"+nodes[i].Address {
			t.Fatalf("result order changed: %+v", result)
		}
	}
}

func TestEnrichNodeOverviewsDefaultsToSerial(t *testing.T) {
	// 测试 Overview 默认保持旧 CLI 的串行节点访问压力模型。
	nodes := []NodeOverview{
		{Address: "node-1", State: "PRIMARY"},
		{Address: "node-2", State: "SECONDARY"},
	}
	var current atomic.Int64
	var maximum atomic.Int64

	_, err := enrichNodeOverviews(context.Background(), nodes, 0, func(_ context.Context, node NodeOverview) (NodeOverview, error) {
		running := current.Add(1)
		defer current.Add(-1)
		updateMaximum(&maximum, running)
		time.Sleep(10 * time.Millisecond)
		return node, nil
	})
	if err != nil {
		t.Fatalf("enrichNodeOverviews failed: %v", err)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrency = %d, want 1", maximum.Load())
	}
}

func TestCollectSlowlogDatabaseSummariesHonorsConcurrencyAndOrder(t *testing.T) {
	// 测试 slowlog 数据库聚合使用配置的并发度，并稳定保留数据库顺序。
	dbs := []string{"db-1", "db-2", "db-3", "db-4"}
	var current atomic.Int64
	var maximum atomic.Int64

	result, err := collectSlowlogDatabaseSummaries(
		context.Background(),
		"node-1",
		dbs,
		SlowlogSortCount,
		2,
		func(_ context.Context, _, db string, _ SlowlogSort) (DatabaseSlowlogSummary, bool, error) {
			running := current.Add(1)
			defer current.Add(-1)
			updateMaximum(&maximum, running)
			time.Sleep(20 * time.Millisecond)
			return DatabaseSlowlogSummary{Database: db}, db != "db-2", nil
		},
	)
	if err != nil {
		t.Fatalf("collectSlowlogDatabaseSummaries failed: %v", err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
	if got := fmt.Sprint(databaseNames(result)); got != "[db-1 db-3 db-4]" {
		t.Fatalf("unexpected result order: %s", got)
	}
}

func TestCollectSlowlogDatabaseSummariesCancelsSiblings(t *testing.T) {
	// 测试任一 slowlog 数据库任务失败后取消仍在执行的同级任务。
	wantErr := errors.New("load failed")
	started := make(chan struct{}, 1)
	result, err := collectSlowlogDatabaseSummaries(
		context.Background(),
		"node-1",
		[]string{"wait", "fail"},
		SlowlogSortCount,
		2,
		func(ctx context.Context, _, db string, _ SlowlogSort) (DatabaseSlowlogSummary, bool, error) {
			if db == "fail" {
				<-started
				return DatabaseSlowlogSummary{}, false, wantErr
			}
			started <- struct{}{}
			<-ctx.Done()
			return DatabaseSlowlogSummary{}, false, ctx.Err()
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got result=%v err=%v", wantErr, result, err)
	}
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func databaseNames(summaries []DatabaseSlowlogSummary) []string {
	names := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		names = append(names, summary.Database)
	}
	return names
}
