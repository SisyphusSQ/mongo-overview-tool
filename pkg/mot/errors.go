package mot

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrInvalidOptions      = errors.New("invalid options")
	ErrUnsupportedTopology = errors.New("unsupported topology")
	ErrNotSharded          = errors.New("not sharded")
	ErrDangerousOperation  = errors.New("dangerous operation")
	ErrCancelled           = errors.New("operation cancelled")
	ErrPartialResult       = errors.New("partial result")
)

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

func invalidOptions(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOptions, fmt.Sprintf(format, args...))
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
