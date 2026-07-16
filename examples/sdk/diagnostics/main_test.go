package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

type fakeDiagnosticsClient struct {
	doctorOptions     mot.DoctorOptions
	operationsOptions mot.CurrentOperationsOptions
	hotspotOptions    mot.HotspotOptions
	indexOptions      mot.IndexAuditOptions
	capacityOptions   mot.CapacityOptions
	slowlogOptions    mot.SlowlogOptions
}

func (f *fakeDiagnosticsClient) Doctor(_ context.Context, opts mot.DoctorOptions) (*mot.DoctorResult, error) {
	f.doctorOptions = opts
	return &mot.DoctorResult{
		ClusterType:       mot.ClusterSharded,
		Findings:          make([]mot.DiagnosticFinding, 2),
		CollectorStatuses: make([]mot.CollectorStatus, 3),
	}, errors.Join(mot.ErrPartialResult, errors.New("one collector unavailable"))
}

func (f *fakeDiagnosticsClient) CurrentOperations(_ context.Context, opts mot.CurrentOperationsOptions) (*mot.CurrentOperationsResult, error) {
	f.operationsOptions = opts
	return &mot.CurrentOperationsResult{
		ClusterType: mot.ClusterSharded,
		Source:      "aggregation",
		Visibility:  "all_users",
		Operations:  make([]mot.CurrentOperation, 4),
	}, nil
}

func (f *fakeDiagnosticsClient) Hotspot(_ context.Context, opts mot.HotspotOptions) (*mot.HotspotResult, error) {
	f.hotspotOptions = opts
	return &mot.HotspotResult{
		Nodes:      make([]mot.NodeHotspot, 5),
		Namespaces: make([]mot.NamespaceHotspot, 6),
	}, nil
}

func (f *fakeDiagnosticsClient) IndexAudit(_ context.Context, opts mot.IndexAuditOptions) (*mot.IndexAuditResult, error) {
	f.indexOptions = opts
	return &mot.IndexAuditResult{Collections: make([]mot.CollectionIndexAudit, 1)}, nil
}

func (f *fakeDiagnosticsClient) Capacity(_ context.Context, opts mot.CapacityOptions) (*mot.CapacityResult, error) {
	f.capacityOptions = opts
	return &mot.CapacityResult{Databases: make([]mot.DatabaseCapacity, 1)}, nil
}

func (f *fakeDiagnosticsClient) SlowlogSummary(_ context.Context, opts mot.SlowlogOptions) (*mot.SlowlogSummaryResult, error) {
	f.slowlogOptions = opts
	return &mot.SlowlogSummaryResult{ReplicaSets: make([]mot.ReplicaSetSlowlogSummary, 2)}, nil
}

func TestLoadConfigUsesSeparateCredentials(t *testing.T) {
	// 场景：示例从独立环境变量组装 SDK Options，不能要求调用者把密码拼进 URI。
	values := map[string]string{
		"MOT_MONGO_HOST":         "mongo.example",
		"MOT_MONGO_PORT":         "27018",
		"MOT_MONGO_AUTH_SOURCE":  "admin",
		"MONGO_USER":             "monitor",
		"MONGO_PASS":             "secret",
		"MOT_EXAMPLE_DATABASE":   "app",
		"MOT_EXAMPLE_COLLECTION": "orders",
	}
	config, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.ClientOptions.Host != "mongo.example" || config.ClientOptions.Port != 27018 {
		t.Fatalf("client options = %#v", config.ClientOptions)
	}
	if config.ClientOptions.Username != "monitor" || config.ClientOptions.Password != "secret" {
		t.Fatal("separate credentials were not preserved")
	}
	if config.Database != "app" || config.Collection != "orders" {
		t.Fatalf("scope = %s.%s", config.Database, config.Collection)
	}
}

func TestCollectDiagnosticsAcceptsPartialResults(t *testing.T) {
	// 场景：SDK 返回安全部分结果时，示例保留摘要并标明 partial collector，而不是丢弃已采集数据。
	client := &fakeDiagnosticsClient{}
	summary, err := collectDiagnostics(context.Background(), client, exampleConfig{
		Database:   "app",
		Collection: "orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ClusterType != mot.ClusterSharded || summary.OperationSource != "aggregation" {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.DoctorFindings != 2 || summary.Operations != 4 || summary.HotspotNodes != 5 || summary.HotspotNamespaces != 6 {
		t.Fatalf("diagnostic counts = %#v", summary)
	}
	if summary.IndexCollections != 1 || summary.CapacityDatabases != 1 || summary.SlowlogReplicaSets != 2 {
		t.Fatalf("audit counts = %#v", summary)
	}
	if !slices.Equal(summary.PartialCollectors, []string{"doctor"}) {
		t.Fatalf("partial collectors = %#v", summary.PartialCollectors)
	}
	if !client.operationsOptions.AllUsers || client.operationsOptions.Limit != 20 {
		t.Fatalf("operations options = %#v", client.operationsOptions)
	}
	if client.hotspotOptions.Duration != 100*time.Millisecond || !slices.Equal(client.hotspotOptions.Databases, []string{"app"}) {
		t.Fatalf("hotspot options = %#v", client.hotspotOptions)
	}
	wantChecks := []mot.IndexAuditCheck{mot.IndexCheckUnused, mot.IndexCheckRedundant, mot.IndexCheckSpace, mot.IndexCheckBuilding}
	if !slices.Equal(client.indexOptions.Checks, wantChecks) || client.indexOptions.MaxCollections != 1 {
		t.Fatalf("index options = %#v", client.indexOptions)
	}
	if !slices.Equal(client.capacityOptions.Databases, []string{"app"}) || client.capacityOptions.MaxCollections != 1 {
		t.Fatalf("capacity options = %#v", client.capacityOptions)
	}
	if !slices.Equal(client.slowlogOptions.Databases, []string{"app"}) {
		t.Fatalf("slowlog options = %#v", client.slowlogOptions)
	}
}
