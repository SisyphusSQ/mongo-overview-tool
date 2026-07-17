package clioutput

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
)

var updateGolden = flag.Bool("update", false, "update golden files")

func TestPrintOverviewGolden(t *testing.T) {
	// 测试 overview formatter 的关键表格输出，避免 SDK 化后 CLI 字段丢失。
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
	}()

	result := &mot.OverviewResult{
		ClusterType: mot.ClusterReplicaSet,
		Hosts:       []string{"127.0.0.1"},
		ReplicaSets: []mot.ReplicaSetOverview{
			{
				Name: "rs0",
				Nodes: []mot.NodeOverview{
					{
						ReplicaSet:         "rs0",
						Address:            "127.0.0.1:27017",
						State:              "PRIMARY",
						Version:            "6.0",
						Uptime:             2 * time.Minute,
						ConnectionsCurrent: 10,
						QueueReaders:       1,
						QueueWriters:       2,
						ActiveReaders:      3,
						ActiveWriters:      4,
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := PrintOverview(&buf, result, OverviewPrintOptions{}); err != nil {
		t.Fatalf("PrintOverview failed: %v", err)
	}

	path := filepath.Join("testdata", "overview_repl.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("MkdirAll failed: %v", err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if got := buf.String(); got != string(want) {
		t.Fatalf("overview output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, string(want))
	}
}
