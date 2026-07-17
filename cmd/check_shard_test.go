package cmd

import (
	"testing"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/internal/config"
)

func TestCheckShardStatsOptions(t *testing.T) {
	// 测试 check-shard 默认保留全部统计供 formatter 筛选，指定集合时自动展示结果。
	tests := []struct {
		name        string
		cfg         config.StatsConfig
		wantShowAll bool
		wantColls   int
	}{
		{name: "default"},
		{name: "show all", cfg: config.StatsConfig{ShowAll: true}, wantShowAll: true},
		{name: "specific collection", cfg: config.StatsConfig{Collection: "orders"}, wantShowAll: true, wantColls: 1},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			opts, showAll := checkShardStatsOptions(&tc.cfg)
			if !opts.RequireShardedCluster {
				t.Fatalf("RequireShardedCluster must be true")
			}
			if opts.ShardedOnly {
				t.Fatalf("check-shard must not discard unsharded collections")
			}
			if showAll != tc.wantShowAll || len(opts.Collections) != tc.wantColls {
				t.Fatalf("unexpected options: %+v showAll=%v", opts, showAll)
			}
		})
	}
}
