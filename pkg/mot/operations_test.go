package mot

import (
	"context"
	"errors"
	"testing"
	"time"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

func TestCurrentOperationsValidatesOptionsBeforeConnecting(t *testing.T) {
	// 场景：无界或非法 limit 必须在 MongoDB 调用前拒绝。
	_, err := (&Client{}).CurrentOperations(context.Background(), CurrentOperationsOptions{Limit: -1})
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("CurrentOperations error = %v, want ErrInvalidOptions", err)
	}
}

func TestCurrentOperationsDefaultsToAllUsersWithExplicitCurrentUserOptOut(t *testing.T) {
	// 场景：SDK 零值默认请求全局可见性，调用方仍可显式限制为当前用户。
	defaults, err := normalizeCurrentOperationsOptions(CurrentOperationsOptions{})
	if err != nil || !defaults.AllUsers {
		t.Fatalf("defaults = %#v, err = %v", defaults, err)
	}
	currentUser, err := normalizeCurrentOperationsOptions(CurrentOperationsOptions{CurrentUserOnly: true})
	if err != nil || currentUser.AllUsers {
		t.Fatalf("current user opts = %#v, err = %v", currentUser, err)
	}
}

func TestEvaluateCurrentOperationsFindings(t *testing.T) {
	// 场景：锁等待、长事务和维护进度分别形成可解释 finding，结果不需要 raw command。
	operations := []pkgmongo.CurrentOperationSnapshot{
		{Namespace: "db.c", Operation: "query", SecondsRunning: int64Pointer(45), WaitingForLock: true},
		{Namespace: "db.tx", Operation: "transaction", SecondsRunning: int64Pointer(70), TransactionActive: true},
		{Namespace: "db.idx", Operation: "command", Message: "Index Build", ProgressDone: int64Pointer(4), ProgressTotal: int64Pointer(10)},
	}

	findings := evaluateCurrentOperationFindings(operations, time.Now())
	assertFindingCode(t, findings, "operation.waiting_for_lock", SeverityWarning)
	assertFindingCode(t, findings, "operation.long_running", SeverityWarning)
	assertFindingCode(t, findings, "transaction.long_running", SeverityWarning)
	assertFindingCode(t, findings, "maintenance.in_progress", SeverityInfo)
}

func TestCurrentOperationsUnauthorizedAllUsersDowngradesToCurrentUser(t *testing.T) {
	// 场景：全局 inprog 无权限时必须只重试 current-user，并显式标记 visibility 降级。
	var allUsersValues []bool
	loader := func(query pkgmongo.CurrentOperationsQuery) ([]pkgmongo.CurrentOperationSnapshot, string, error) {
		allUsersValues = append(allUsersValues, query.AllUsers)
		if query.AllUsers {
			return nil, "aggregation", drivermongo.CommandError{Code: 13}
		}
		return []pkgmongo.CurrentOperationSnapshot{{Namespace: "db.c"}}, "aggregation", nil
	}
	raw, _, visibility, status, err := collectCurrentOperationsWithVisibility(pkgmongo.CurrentOperationsQuery{AllUsers: true}, loader)
	if err != nil || len(raw) != 1 || visibility != "current_user" || status.ReasonCode != "degraded_current_user" {
		t.Fatalf("raw=%#v visibility=%s status=%#v err=%v", raw, visibility, status, err)
	}
	if len(allUsersValues) != 2 || !allUsersValues[0] || allUsersValues[1] {
		t.Fatalf("allUsers calls = %v", allUsersValues)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
