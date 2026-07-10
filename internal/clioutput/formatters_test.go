package clioutput

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
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
