package mot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sync/semaphore"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type ScopeType string

const (
	ScopeCluster    ScopeType = "cluster"
	ScopeReplicaSet ScopeType = "replica_set"
	ScopeNode       ScopeType = "node"
	ScopeDatabase   ScopeType = "database"
	ScopeNamespace  ScopeType = "namespace"
)

type FindingScope struct {
	Type       ScopeType `json:"type"`
	Cluster    string    `json:"cluster,omitempty"`
	ReplicaSet string    `json:"replicaSet,omitempty"`
	Shard      string    `json:"shard,omitempty"`
	Node       string    `json:"node,omitempty"`
	Database   string    `json:"database,omitempty"`
	Namespace  string    `json:"namespace,omitempty"`
}

type DiagnosticFinding struct {
	Code           string         `json:"code"`
	Severity       Severity       `json:"severity"`
	Scope          FindingScope   `json:"scope"`
	Summary        string         `json:"summary"`
	Evidence       map[string]any `json:"evidence,omitempty"`
	Recommendation string         `json:"recommendation,omitempty"`
}

type CapabilityState string

const (
	CapabilitySupported    CapabilityState = "supported"
	CapabilityUnsupported  CapabilityState = "unsupported"
	CapabilityUnauthorized CapabilityState = "unauthorized"
	CapabilitySkipped      CapabilityState = "skipped"
	CapabilityFailed       CapabilityState = "failed"
)

type CapabilityCost string

const (
	CapabilityCostLow            CapabilityCost = "low"
	CapabilityCostBounded        CapabilityCost = "bounded"
	CapabilityCostExpensiveOptIn CapabilityCost = "expensive-opt-in"
)

// DiagnosticCapability 描述 collector 的稳定安全边界，不依赖用户角色名预判权限。
type DiagnosticCapability struct {
	Name               string         `json:"name"`
	MinimumVersion     string         `json:"minimumVersion,omitempty"`
	MinimumWireVersion int            `json:"minimumWireVersion,omitempty"`
	Topologies         []ClusterType  `json:"topologies"`
	Privilege          string         `json:"privilege,omitempty"`
	Cost               CapabilityCost `json:"cost"`
	SensitiveFields    []string       `json:"sensitiveFields,omitempty"`
}

// DiagnosticCapabilities 返回按名称稳定排序的共享 capability registry。
func DiagnosticCapabilities() []DiagnosticCapability {
	capabilities := []DiagnosticCapability{
		{Name: "collection_capacity", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "collStats", Cost: CapabilityCostBounded},
		{Name: "current_operations", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "inprog", Cost: CapabilityCostLow, SensitiveFields: []string{"command", "client", "user", "session"}},
		{Name: "database_capacity", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "dbStats", Cost: CapabilityCostBounded},
		{Name: "free_storage", MinimumVersion: "3.6", MinimumWireVersion: 6, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "collStats", Cost: CapabilityCostExpensiveOptIn},
		{Name: "hotspot_snapshot", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "top", Cost: CapabilityCostBounded},
		{Name: "index_consistency_direct", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterSharded}, Privilege: "listShards, find config metadata, listIndexes", Cost: CapabilityCostBounded, SensitiveFields: []string{"raw index spec", "partialFilterExpression", "derived connection"}},
		{Name: "index_consistency_index_stats", MinimumVersion: "4.2.4", MinimumWireVersion: 8, Topologies: []ClusterType{ClusterSharded}, Privilege: "indexStats", Cost: CapabilityCostBounded, SensitiveFields: []string{"raw index spec", "partialFilterExpression"}},
		{Name: "index_consistency_metadata_check", MinimumVersion: "7.0", MinimumWireVersion: 21, Topologies: []ClusterType{ClusterSharded}, Privilege: "checkMetadataConsistency", Cost: CapabilityCostBounded, SensitiveFields: []string{"raw inconsistency", "shard key values"}},
		{Name: "index_consistency_visibility", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterSharded}, Privilege: "collStats", Cost: CapabilityCostBounded},
		{Name: "index_usage", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "indexStats", Cost: CapabilityCostBounded},
		{Name: "oplog_window", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "find local.oplog.rs", Cost: CapabilityCostLow},
		{Name: "replica_status", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "replSetGetStatus", Cost: CapabilityCostLow},
		{Name: "server_status", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "serverStatus", Cost: CapabilityCostLow},
		{Name: "slowlog_insight", MinimumVersion: "3.4", MinimumWireVersion: 5, Topologies: []ClusterType{ClusterReplicaSet, ClusterSharded}, Privilege: "find system.profile", Cost: CapabilityCostBounded, SensitiveFields: []string{"command", "filter", "client", "user", "session"}},
	}
	sort.SliceStable(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	return capabilities
}

func diagnosticCapability(name string) (DiagnosticCapability, bool) {
	for _, capability := range DiagnosticCapabilities() {
		if capability.Name == name {
			return capability, true
		}
	}
	return DiagnosticCapability{}, false
}

func diagnosticCapabilitySupportsTopology(name string, topology ClusterType) bool {
	capability, ok := diagnosticCapability(name)
	if !ok {
		return false
	}
	for _, supported := range capability.Topologies {
		if supported == topology {
			return true
		}
	}
	return false
}

func diagnosticCapabilityGate(name string, topology ClusterType, maxWireVersion int, requested bool) (CollectorStatus, bool) {
	scope := FindingScope{Type: ScopeCluster}
	capability, ok := diagnosticCapability(name)
	if !ok {
		return CollectorStatus{Name: name, State: CapabilityFailed, Scope: scope, ReasonCode: "capability_unregistered", Message: "collector 未登记统一能力边界"}, false
	}
	if !diagnosticCapabilitySupportsTopology(name, topology) {
		return CollectorStatus{Name: name, State: CapabilityUnsupported, Scope: scope, ReasonCode: "unsupported_topology", Message: "当前拓扑不支持该采集项"}, false
	}
	if capability.MinimumWireVersion > 0 && maxWireVersion < capability.MinimumWireVersion {
		return CollectorStatus{Name: name, State: CapabilityUnsupported, Scope: scope, ReasonCode: "unsupported_version", Message: "当前服务器不满足 collector 最低能力版本 " + capability.MinimumVersion}, false
	}
	if capability.Cost == CapabilityCostExpensiveOptIn && !requested {
		return CollectorStatus{Name: name, State: CapabilitySkipped, Scope: scope, ReasonCode: "not_requested", Message: "高成本采集项需要显式 opt-in"}, false
	}
	return CollectorStatus{Name: name, State: CapabilitySupported, Scope: scope, ReasonCode: "capability_gate_passed"}, true
}

func acquireDiagnosticSlot(ctx context.Context, limit *semaphore.Weighted) error {
	if err := limit.Acquire(ctx, 1); err != nil {
		return mapContextError(err)
	}
	if err := contextError(ctx); err != nil {
		limit.Release(1)
		return err
	}
	return nil
}

type CollectorStatus struct {
	Name       string          `json:"name"`
	State      CapabilityState `json:"state"`
	Scope      FindingScope    `json:"scope,omitempty"`
	ReasonCode string          `json:"reasonCode,omitempty"`
	Message    string          `json:"message,omitempty"`
}

type FindingSummary struct {
	Info       int      `json:"info"`
	Warning    int      `json:"warning"`
	Critical   int      `json:"critical"`
	MostSevere Severity `json:"mostSevere,omitempty"`
}

func validateSeverity(severity Severity) error {
	switch severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return nil
	default:
		return invalidOptions("invalid severity %q", severity)
	}
}

func sanitizeAndSortFindings(findings []DiagnosticFinding) {
	for i := range findings {
		findings[i].Evidence = sanitizeEvidence(findings[i].Evidence)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		if severityRank(left.Severity) != severityRank(right.Severity) {
			return severityRank(left.Severity) < severityRank(right.Severity)
		}
		if scopeSortKey(left.Scope) != scopeSortKey(right.Scope) {
			return scopeSortKey(left.Scope) < scopeSortKey(right.Scope)
		}
		return left.Code < right.Code
	})
}

func sortCollectorStatuses(statuses []CollectorStatus) {
	sort.SliceStable(statuses, func(i, j int) bool {
		if statuses[i].Name != statuses[j].Name {
			return statuses[i].Name < statuses[j].Name
		}
		return scopeSortKey(statuses[i].Scope) < scopeSortKey(statuses[j].Scope)
	})
}

func summarizeFindings(findings []DiagnosticFinding) FindingSummary {
	var summary FindingSummary
	for _, finding := range findings {
		switch finding.Severity {
		case SeverityCritical:
			summary.Critical++
		case SeverityWarning:
			summary.Warning++
		case SeverityInfo:
			summary.Info++
		}
	}
	switch {
	case summary.Critical > 0:
		summary.MostSevere = SeverityCritical
	case summary.Warning > 0:
		summary.MostSevere = SeverityWarning
	case summary.Info > 0:
		summary.MostSevere = SeverityInfo
	}
	return summary
}

func severityRank(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

func scopeSortKey(scope FindingScope) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		scope.Type,
		scope.Cluster,
		scope.ReplicaSet,
		scope.Shard,
		scope.Node,
		scope.Database,
		scope.Namespace,
	)
}

func sanitizeEvidence(evidence map[string]any) map[string]any {
	if len(evidence) == 0 {
		return evidence
	}
	result := make(map[string]any, len(evidence))
	for key, value := range evidence {
		if isSensitiveDiagnosticKey(key) {
			continue
		}
		result[key] = value
	}
	return result
}

func isSensitiveDiagnosticKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
	for _, token := range []string{
		"command", "filter", "pipeline", "password", "credential", "uri",
		"client", "username", "user", "session", "lsid", "transactionid", "servererror",
	} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func isUnauthorizedError(err error) bool {
	var commandError drivermongo.CommandError
	return errors.As(err, &commandError) && (commandError.Code == 13 || commandError.Code == 18)
}

func isUnsupportedDiagnosticError(err error) bool {
	var commandError drivermongo.CommandError
	if errors.As(err, &commandError) {
		switch commandError.Code {
		case 9, 59, 115, 16436, 168, 40324:
			return true
		}
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unrecognized pipeline stage") || strings.Contains(message, "no such command")
}
