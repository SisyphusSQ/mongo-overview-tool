package mot

import "testing"

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
