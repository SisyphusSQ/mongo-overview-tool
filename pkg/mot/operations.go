package mot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

const (
	defaultCurrentOperationsMinDuration = 2 * time.Second
	defaultCurrentOperationsLimit       = 100
)

type currentOperationsLoader func(pkgmongo.CurrentOperationsQuery) ([]pkgmongo.CurrentOperationSnapshot, string, error)

type CurrentOperationsOptions struct {
	MinDuration             time.Duration
	AllUsers                bool
	CurrentUserOnly         bool
	IncludeIdleTransactions bool
	IncludeIdleCursors      bool
	Databases               []string
	Namespaces              []string
	Limit                   int
	MaxTime                 time.Duration
}

type CurrentOperation struct {
	Host                  string        `json:"host,omitempty"`
	Shard                 string        `json:"shard,omitempty"`
	Namespace             string        `json:"namespace,omitempty"`
	Operation             string        `json:"operation,omitempty"`
	AppName               string        `json:"appName,omitempty"`
	QueryHash             string        `json:"queryHash,omitempty"`
	PlanSummary           string        `json:"planSummary,omitempty"`
	RunningDuration       time.Duration `json:"runningDuration"`
	WaitingForLock        bool          `json:"waitingForLock"`
	WaitingForFlowControl bool          `json:"waitingForFlowControl"`
	KillPending           bool          `json:"killPending"`
	TransactionActive     bool          `json:"transactionActive"`
	TransactionDuration   time.Duration `json:"transactionDuration,omitempty"`
	Message               string        `json:"message,omitempty"`
	ProgressDone          *int64        `json:"progressDone,omitempty"`
	ProgressTotal         *int64        `json:"progressTotal,omitempty"`
}

type CurrentOperationsResult struct {
	ClusterType       ClusterType         `json:"clusterType"`
	CollectedAt       time.Time           `json:"collectedAt"`
	Visibility        string              `json:"visibility"`
	Source            string              `json:"source"`
	Operations        []CurrentOperation  `json:"operations"`
	Findings          []DiagnosticFinding `json:"findings"`
	CollectorStatuses []CollectorStatus   `json:"collectorStatuses"`
}

// CurrentOperations 返回经过服务端投影和 SDK 脱敏的活跃操作。
func (c *Client) CurrentOperations(ctx context.Context, opts CurrentOperationsOptions) (*CurrentOperationsResult, error) {
	normalized, err := normalizeCurrentOperationsOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := c.requireConn(); err != nil {
		return nil, err
	}
	cluster, err := pkgmongo.DetectCluster(ctx, c.conn)
	if err != nil {
		return nil, err
	}
	clusterType := convertClusterType(cluster.Type)
	if gate, allowed := diagnosticCapabilityGate("current_operations", clusterType, cluster.MaxWireVersion, true); !allowed {
		return &CurrentOperationsResult{ClusterType: clusterType, CollectedAt: time.Now().UTC(), Visibility: "unavailable", CollectorStatuses: []CollectorStatus{gate}}, nil
	}
	query := pkgmongo.CurrentOperationsQuery{
		MinDuration: normalized.MinDuration, AllUsers: normalized.AllUsers,
		IncludeIdleTransactions: normalized.IncludeIdleTransactions,
		IncludeIdleCursors:      normalized.IncludeIdleCursors,
		Databases:               normalized.Databases, Namespaces: normalized.Namespaces,
		Limit: normalized.Limit, MaxTime: normalized.MaxTime,
	}
	raw, source, visibility, status, collectErr := collectCurrentOperationsWithVisibility(query, func(query pkgmongo.CurrentOperationsQuery) ([]pkgmongo.CurrentOperationSnapshot, string, error) {
		return c.collectCurrentOperations(ctx, query)
	})
	result := &CurrentOperationsResult{
		ClusterType: clusterType, CollectedAt: time.Now().UTC(),
		Visibility: visibility, Source: source,
	}
	if collectErr != nil {
		result.CollectorStatuses = []CollectorStatus{failedCollectorStatus("current_operations", FindingScope{Type: ScopeCluster}, collectErr)}
		if isUnauthorizedError(collectErr) {
			return result, nil
		}
		return result, collectErr
	}
	result.CollectorStatuses = []CollectorStatus{status}
	result.Operations = convertCurrentOperations(raw)
	result.Findings = evaluateCurrentOperationFindings(raw, result.CollectedAt)
	sanitizeAndSortFindings(result.Findings)
	sort.SliceStable(result.Operations, func(i, j int) bool {
		left, right := result.Operations[i], result.Operations[j]
		if left.WaitingForLock != right.WaitingForLock {
			return left.WaitingForLock
		}
		if left.TransactionActive != right.TransactionActive {
			return left.TransactionActive
		}
		if left.RunningDuration != right.RunningDuration {
			return left.RunningDuration > right.RunningDuration
		}
		return left.Namespace < right.Namespace
	})
	return result, nil
}

func collectCurrentOperationsWithVisibility(query pkgmongo.CurrentOperationsQuery, loader currentOperationsLoader) ([]pkgmongo.CurrentOperationSnapshot, string, string, CollectorStatus, error) {
	visibility := "current_user"
	if query.AllUsers {
		visibility = "all_users"
	}
	raw, source, collectErr := loader(query)
	status := CollectorStatus{Name: "current_operations", State: CapabilitySupported, Scope: FindingScope{Type: ScopeCluster}}
	if isUnauthorizedError(collectErr) && query.AllUsers {
		query.AllUsers = false
		visibility = "current_user"
		raw, source, collectErr = loader(query)
		if collectErr == nil {
			status.ReasonCode = "degraded_current_user"
			status.Message = "缺少全局 inprog 权限，已降级为当前用户可见范围"
		}
	}
	return raw, source, visibility, status, collectErr
}

func normalizeCurrentOperationsOptions(opts CurrentOperationsOptions) (CurrentOperationsOptions, error) {
	if opts.AllUsers && opts.CurrentUserOnly {
		return CurrentOperationsOptions{}, invalidOptions("all users and current user only are mutually exclusive")
	}
	if opts.MinDuration < 0 {
		return CurrentOperationsOptions{}, invalidOptions("min duration must not be negative")
	}
	if opts.Limit < 0 {
		return CurrentOperationsOptions{}, invalidOptions("limit must not be negative")
	}
	if opts.MaxTime < 0 {
		return CurrentOperationsOptions{}, invalidOptions("max time must not be negative")
	}
	if opts.MinDuration == 0 {
		opts.MinDuration = defaultCurrentOperationsMinDuration
	}
	if opts.Limit == 0 {
		opts.Limit = defaultCurrentOperationsLimit
	}
	if !opts.CurrentUserOnly {
		opts.AllUsers = true
	}
	return opts, nil
}

func (c *Client) collectCurrentOperations(ctx context.Context, query pkgmongo.CurrentOperationsQuery) ([]pkgmongo.CurrentOperationSnapshot, string, error) {
	operations, err := c.conn.CurrentOperations(ctx, query)
	if err == nil {
		return operations, "aggregation", nil
	}
	if !isUnsupportedCurrentOpAggregation(err) {
		return nil, "aggregation", err
	}
	operations, fallbackErr := c.conn.CurrentOperationsCommand(ctx, query)
	return operations, "command_fallback", fallbackErr
}

func isUnsupportedCurrentOpAggregation(err error) bool {
	var commandError drivermongo.CommandError
	if errors.As(err, &commandError) {
		switch commandError.Code {
		case 9, 16436, 168, 40324:
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "unrecognized pipeline stage")
}

func convertCurrentOperations(raw []pkgmongo.CurrentOperationSnapshot) []CurrentOperation {
	result := make([]CurrentOperation, 0, len(raw))
	for _, operation := range raw {
		item := CurrentOperation{
			Host: operation.Host, Shard: operation.Shard, Namespace: operation.Namespace,
			Operation: operation.Operation, AppName: anonymizeAppName(operation.AppName),
			QueryHash: operation.QueryHash, PlanSummary: operation.PlanSummary,
			WaitingForLock: operation.WaitingForLock, WaitingForFlowControl: operation.WaitingForFlowControl,
			KillPending: operation.KillPending, TransactionActive: operation.TransactionActive,
			Message: safeOperationMessage(operation.Message), ProgressDone: operation.ProgressDone, ProgressTotal: operation.ProgressTotal,
		}
		if operation.SecondsRunning != nil {
			item.RunningDuration = time.Duration(*operation.SecondsRunning) * time.Second
		}
		if operation.TransactionMicros != nil {
			item.TransactionDuration = time.Duration(*operation.TransactionMicros) * time.Microsecond
		}
		result = append(result, item)
	}
	return result
}

func evaluateCurrentOperationFindings(operations []pkgmongo.CurrentOperationSnapshot, _ time.Time) []DiagnosticFinding {
	findings := make([]DiagnosticFinding, 0)
	for _, operation := range operations {
		scope := FindingScope{Type: ScopeNamespace, Shard: operation.Shard, Node: operation.Host, Namespace: operation.Namespace}
		duration := time.Duration(0)
		if operation.SecondsRunning != nil {
			duration = time.Duration(*operation.SecondsRunning) * time.Second
		}
		if operation.WaitingForLock {
			findings = append(findings, DiagnosticFinding{Code: "operation.waiting_for_lock", Severity: SeverityWarning, Scope: scope, Summary: "操作正在等待锁", Evidence: map[string]any{"runningSeconds": duration.Seconds()}})
		}
		if duration >= 30*time.Second && operation.Message == "" {
			findings = append(findings, DiagnosticFinding{Code: "operation.long_running", Severity: SeverityWarning, Scope: scope, Summary: "操作运行时间超过 30 秒", Evidence: map[string]any{"runningSeconds": duration.Seconds()}})
		}
		transactionDuration := duration
		if operation.TransactionMicros != nil {
			transactionDuration = time.Duration(*operation.TransactionMicros) * time.Microsecond
		}
		if operation.TransactionActive && transactionDuration >= time.Minute {
			findings = append(findings, DiagnosticFinding{Code: "transaction.long_running", Severity: SeverityWarning, Scope: scope, Summary: "事务运行时间超过 60 秒", Evidence: map[string]any{"runningSeconds": transactionDuration.Seconds()}})
		}
		if operation.WaitingForFlowControl {
			findings = append(findings, DiagnosticFinding{Code: "operation.waiting_for_flow_control", Severity: SeverityWarning, Scope: scope, Summary: "操作正在等待 flow control"})
		}
		if operation.KillPending {
			findings = append(findings, DiagnosticFinding{Code: "operation.kill_pending", Severity: SeverityInfo, Scope: scope, Summary: "操作已标记终止但尚未退出"})
		}
		if operation.Message != "" || operation.ProgressDone != nil || operation.ProgressTotal != nil {
			findings = append(findings, DiagnosticFinding{Code: "maintenance.in_progress", Severity: SeverityInfo, Scope: scope, Summary: "检测到正在进行的维护操作"})
		}
	}
	return findings
}

func anonymizeAppName(appName string) string {
	if appName == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(appName))
	return "app:" + hex.EncodeToString(digest[:4])
}

func safeOperationMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 160 {
		message = message[:160]
	}
	return message
}
