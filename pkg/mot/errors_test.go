package mot

import (
	"context"
	"errors"
	"testing"
)

func TestPartialErrorMatching(t *testing.T) {
	// 测试 PartialError 同时支持 ErrPartialResult 和底层错误链判断。
	err := &PartialError{
		Op:  "bulk-delete",
		Err: wrapCancelled(context.Canceled),
	}
	if !errors.Is(err, ErrPartialResult) {
		t.Fatalf("expected errors.Is ErrPartialResult")
	}
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected errors.Is ErrCancelled")
	}
}

func TestMapContextError(t *testing.T) {
	// 测试 SDK 公共边界把 context 取消和超时统一映射为 ErrCancelled。
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		mapped := mapContextError(err)
		if !errors.Is(mapped, ErrCancelled) || !errors.Is(mapped, err) {
			t.Fatalf("unexpected mapped error: %v", mapped)
		}
	}
}
