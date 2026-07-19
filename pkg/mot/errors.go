package mot

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidOptions          = errors.New("invalid options")
	ErrCollectionLimitExceeded = errors.New("collection limit exceeded")
	ErrIndexAuditCursorInvalid = errors.New("index audit cursor invalid")
	ErrIndexAuditScopeChanged  = errors.New("index audit scope changed")
	ErrUnsupportedTopology     = errors.New("unsupported topology")
	ErrNotSharded              = errors.New("not sharded")
	ErrDangerousOperation      = errors.New("dangerous operation")
	ErrCancelled               = errors.New("operation cancelled")
	ErrPartialResult           = errors.New("partial result")
	ErrCollectorSessionClosed  = errors.New("collector session is closed")
)

// CollectionLimitError 表示只读 collection scope 超过单次执行上限。
// 错误只携带数量边界，不包含 database、namespace 或连接信息。
type CollectionLimitError struct {
	Limit           int
	ObservedAtLeast int
}

func (e *CollectionLimitError) Error() string {
	if e == nil {
		return "collection limit exceeded"
	}
	return fmt.Sprintf("selected at least %d collections, exceeds max %d", e.ObservedAtLeast, e.Limit)
}

func (e *CollectionLimitError) Is(target error) bool {
	return target == ErrCollectionLimitExceeded || target == ErrInvalidOptions
}

// PartialError 表示操作失败时已经产生了可读取的部分结果。
type PartialError struct {
	Op     string
	Result BulkResult
	Err    error
}

func (e *PartialError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s partial result", e.Op)
	}
	return fmt.Sprintf("%s partial result: %v", e.Op, e.Err)
}

func (e *PartialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *PartialError) Is(target error) bool {
	return target == ErrPartialResult
}

// DiagnosticPartialError 表示诊断操作已产生安全的结构化部分结果。
// 它与既有 bulk PartialError 分离，避免破坏外部未命名复合字面量兼容性。
type DiagnosticPartialError struct {
	Op     string
	Result any
	Err    error
}

func (e *DiagnosticPartialError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s partial result", e.Op)
}

func (e *DiagnosticPartialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *DiagnosticPartialError) Is(target error) bool { return target == ErrPartialResult }

func (e *DiagnosticPartialError) DiagnosticResult() any {
	if e == nil {
		return nil
	}
	return e.Result
}

func newDiagnosticPartialError(op string, result any, err error) *DiagnosticPartialError {
	return &DiagnosticPartialError{Op: op, Result: result, Err: err}
}

func invalidOptions(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOptions, fmt.Sprintf(format, args...))
}

func collectionLimitExceeded(limit, observedAtLeast int) error {
	return &CollectionLimitError{Limit: limit, ObservedAtLeast: observedAtLeast}
}

func wrapCancelled(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrCancelled, err)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return wrapCancelled(err)
	}
	return nil
}

func mapContextError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return wrapCancelled(err)
	}
	return err
}
