package mot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sync/semaphore"
)

func TestDiagnosticContractsSortAndSanitize(t *testing.T) {
	// 场景：finding 与 collector status 必须稳定排序，且 evidence 中的敏感键不得进入公共结果。
	findings := []DiagnosticFinding{
		{
			Code:     "query.collection_scan",
			Severity: SeverityWarning,
			Scope:    FindingScope{Type: ScopeNamespace, Namespace: "db.b"},
			Evidence: map[string]any{"ratio": 12.5, "command": "secret"},
		},
		{
			Code:     "replica.primary_missing",
			Severity: SeverityCritical,
			Scope:    FindingScope{Type: ScopeReplicaSet, ReplicaSet: "rs0"},
			Evidence: map[string]any{"required": int64(2)},
		},
		{
			Code:     "capacity.index_ratio",
			Severity: SeverityInfo,
			Scope:    FindingScope{Type: ScopeNamespace, Namespace: "db.a"},
		},
	}

	sanitizeAndSortFindings(findings)
	gotCodes := []string{findings[0].Code, findings[1].Code, findings[2].Code}
	wantCodes := []string{"replica.primary_missing", "query.collection_scan", "capacity.index_ratio"}
	if !reflect.DeepEqual(gotCodes, wantCodes) {
		t.Fatalf("finding order = %v, want %v", gotCodes, wantCodes)
	}
	if _, ok := findings[1].Evidence["command"]; ok {
		t.Fatal("sensitive evidence key command must be removed")
	}

	statuses := []CollectorStatus{
		{Name: "top", Scope: FindingScope{Type: ScopeNode, Node: "b"}},
		{Name: "server_status", Scope: FindingScope{Type: ScopeNode, Node: "a"}},
		{Name: "server_status", Scope: FindingScope{Type: ScopeNode, Node: "b"}},
	}
	sortCollectorStatuses(statuses)
	gotStatuses := []string{
		statuses[0].Name + ":" + statuses[0].Scope.Node,
		statuses[1].Name + ":" + statuses[1].Scope.Node,
		statuses[2].Name + ":" + statuses[2].Scope.Node,
	}
	wantStatuses := []string{"server_status:a", "server_status:b", "top:b"}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("status order = %v, want %v", gotStatuses, wantStatuses)
	}
}

func TestUnsupportedCollectorErrorMapsToUnsupportedState(t *testing.T) {
	// 场景：已知命令/版本不支持必须是 unsupported，而不是 failed。
	status := failedCollectorStatus("collection_capacity", FindingScope{Type: ScopeCluster}, drivermongo.CommandError{Code: 59})
	if status.State != CapabilityUnsupported || status.ReasonCode != "unsupported_version" {
		t.Fatalf("status = %#v", status)
	}
}

func TestDiagnosticContractsValidateAndPartialErrorCompatibility(t *testing.T) {
	// 场景：新增诊断部分结果不能破坏既有 BulkResult 字段或 errors.Is/errors.As 行为。
	if err := validateSeverity(Severity("fatal")); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("validateSeverity error = %v, want ErrInvalidOptions", err)
	}

	diagnosticResult := &DoctorResult{}
	err := newDiagnosticPartialError("doctor", diagnosticResult, errors.New("collector failed"))
	if !errors.Is(err, ErrPartialResult) {
		t.Fatalf("errors.Is = false, want ErrPartialResult: %v", err)
	}
	var partial *DiagnosticPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("errors.As = false: %v", err)
	}
	if partial.DiagnosticResult() != diagnosticResult {
		t.Fatalf("DiagnosticResult = %#v, want same pointer", partial.DiagnosticResult())
	}
	if partial.Result != diagnosticResult {
		t.Fatalf("diagnostic Result = %#v, want same pointer", partial.Result)
	}
}

func TestDiagnosticCapabilitiesAreStableAndMarkExpensiveOptIn(t *testing.T) {
	// 场景：共享 registry 必须稳定排序，并把 free storage 明确标为显式高成本能力。
	capabilities := DiagnosticCapabilities()
	if len(capabilities) == 0 {
		t.Fatal("DiagnosticCapabilities() returned empty registry")
	}
	for index := 1; index < len(capabilities); index++ {
		if capabilities[index-1].Name > capabilities[index].Name {
			t.Fatalf("registry is not sorted: %#v", capabilities)
		}
	}
	found := false
	for _, capability := range capabilities {
		if capability.Name == "free_storage" {
			found = true
			if capability.Cost != CapabilityCostExpensiveOptIn {
				t.Fatalf("free storage cost = %q", capability.Cost)
			}
		}
	}
	if !found {
		t.Fatal("free_storage capability is missing")
	}
}

func TestDiagnosticCapabilityGateUsesWireVersionTopologyAndCost(t *testing.T) {
	// 场景：运行时 gate 在 collector 执行前统一区分旧版本、不支持拓扑和未 opt-in 的高成本能力。
	if status, allowed := diagnosticCapabilityGate("current_operations", ClusterReplicaSet, 4, true); allowed || status.State != CapabilityUnsupported || status.ReasonCode != "unsupported_version" {
		t.Fatalf("old-version gate = %#v allowed=%t", status, allowed)
	}
	if status, allowed := diagnosticCapabilityGate("free_storage", ClusterReplicaSet, 25, false); allowed || status.State != CapabilitySkipped || status.ReasonCode != "not_requested" {
		t.Fatalf("cost gate = %#v allowed=%t", status, allowed)
	}
	if status, allowed := diagnosticCapabilityGate("hotspot_snapshot", ClusterType("standalone"), 25, true); allowed || status.State != CapabilityUnsupported || status.ReasonCode != "unsupported_topology" {
		t.Fatalf("topology gate = %#v allowed=%t", status, allowed)
	}
	if status, allowed := diagnosticCapabilityGate("current_operations", ClusterSharded, 30, true); !allowed || status.State != CapabilitySupported {
		t.Fatalf("forward-compatible gate = %#v allowed=%t", status, allowed)
	}
}

func TestAcquireDiagnosticSlotRejectsAlreadyCancelledContext(t *testing.T) {
	// 场景：即使并发槽位空闲，context 已取消后也不能派发新的 collector goroutine。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	limit := semaphore.NewWeighted(1)
	if err := acquireDiagnosticSlot(ctx, limit); !errors.Is(err, ErrCancelled) {
		t.Fatalf("acquire error = %v, want ErrCancelled", err)
	}
	if !limit.TryAcquire(1) {
		t.Fatal("cancelled acquire leaked semaphore slot")
	}
	limit.Release(1)
}
