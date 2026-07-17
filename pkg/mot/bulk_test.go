package mot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

type fakeBulkCursor struct {
	ids        []any
	index      int
	finalErr   error
	runtimeErr error
	closed     bool
	closeErr   error
}

func (c *fakeBulkCursor) Next(ctx context.Context) bool {
	if err := ctx.Err(); err != nil {
		c.runtimeErr = err
		return false
	}
	return c.index < len(c.ids)
}

func (c *fakeBulkCursor) Decode(value any) error {
	doc, ok := value.(*bson.M)
	if !ok {
		return fmt.Errorf("decode target must be *bson.M")
	}
	if c.index >= len(c.ids) {
		return fmt.Errorf("cursor exhausted")
	}
	*doc = bson.M{"_id": c.ids[c.index]}
	c.index++
	return nil
}

func (c *fakeBulkCursor) Err() error {
	if c.runtimeErr != nil {
		return c.runtimeErr
	}
	return c.finalErr
}

func (c *fakeBulkCursor) Close(ctx context.Context) error {
	c.closed = true
	c.closeErr = ctx.Err()
	return nil
}

type fakeBulkOperations struct {
	count            int64
	countErr         error
	ids              []any
	cursorErr        error
	findCalls        int
	deleteCalls      [][]any
	deleteFailOnCall int
	updateCalls      [][]any
	updateFailOnCall int
	lastCursor       *fakeBulkCursor
}

func (o *fakeBulkOperations) CountDocuments(context.Context, string, string, any) (int64, error) {
	return o.count, o.countErr
}

func (o *fakeBulkOperations) FindIDsCursor(context.Context, string, string, any) (bulkCursor, error) {
	o.findCalls++
	o.lastCursor = &fakeBulkCursor{ids: append([]any(nil), o.ids...), finalErr: o.cursorErr}
	return o.lastCursor, nil
}

func (o *fakeBulkOperations) BulkDelete(_ context.Context, _, _ string, ids []any) (int64, error) {
	o.deleteCalls = append(o.deleteCalls, append([]any(nil), ids...))
	if o.deleteFailOnCall > 0 && len(o.deleteCalls) == o.deleteFailOnCall {
		return 0, errors.New("delete failed")
	}
	return int64(len(ids)), nil
}

func (o *fakeBulkOperations) BulkUpdate(_ context.Context, _, _ string, ids []any, _ any) (pkgmongo.BulkUpdateResult, error) {
	o.updateCalls = append(o.updateCalls, append([]any(nil), ids...))
	if o.updateFailOnCall > 0 && len(o.updateCalls) == o.updateFailOnCall {
		return pkgmongo.BulkUpdateResult{}, errors.New("update failed")
	}
	return pkgmongo.BulkUpdateResult{
		Matched:  int64(len(ids)),
		Modified: int64(len(ids)),
	}, nil
}

type recordingBulkObserver struct {
	events  []string
	batches []BulkBatchResult
	cancel  context.CancelFunc
}

func (o *recordingBulkObserver) OnBulkStart(context.Context, int64) {
	o.events = append(o.events, "start")
}

func (o *recordingBulkObserver) OnBulkBatch(_ context.Context, batch BulkBatchResult) {
	o.events = append(o.events, "batch")
	o.batches = append(o.batches, batch)
	if o.cancel != nil {
		o.cancel()
	}
}

func (o *recordingBulkObserver) OnBulkRetry(context.Context, error, int) {
	o.events = append(o.events, "retry")
}

func (o *recordingBulkObserver) OnBulkDone(context.Context, BulkResult) {
	o.events = append(o.events, "done")
}

func TestValidateBulkOptions(t *testing.T) {
	// 测试 bulk 参数校验覆盖安全空 filter、dry-run、batch size 和 pause 边界。
	tests := []struct {
		name    string
		opts    BulkOptions
		wantErr error
	}{
		{
			name: "valid dry run empty filter",
			opts: BulkOptions{
				Database:   "db",
				Collection: "coll",
				Filter:     "{}",
				BatchSize:  100,
				DryRun:     true,
			},
		},
		{
			name: "dangerous empty filter",
			opts: BulkOptions{
				Database:   "db",
				Collection: "coll",
				Filter:     "{}",
				BatchSize:  100,
			},
			wantErr: ErrDangerousOperation,
		},
		{
			name: "allow empty filter",
			opts: BulkOptions{
				Database:         "db",
				Collection:       "coll",
				Filter:           "{}",
				BatchSize:        100,
				AllowEmptyFilter: true,
			},
		},
		{
			name: "invalid batch",
			opts: BulkOptions{
				Database:   "db",
				Collection: "coll",
				Filter:     `{"status":"inactive"}`,
				BatchSize:  0,
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "invalid pause",
			opts: BulkOptions{
				Database:   "db",
				Collection: "coll",
				Filter:     `{"status":"inactive"}`,
				BatchSize:  100,
				Pause:      -time.Millisecond,
			},
			wantErr: ErrInvalidOptions,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := validateBulkOptions("bulk-delete", tc.opts)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestBulkUpdateRequiresOperator(t *testing.T) {
	// 测试 update 文档必须使用 MongoDB 更新操作符。
	update, err := ParseDocument(`{"status":"archived"}`)
	if err != nil {
		t.Fatalf("ParseDocument failed: %v", err)
	}
	if isOperatorUpdate(update) {
		t.Fatalf("replacement-style update should not be accepted")
	}

	update, err = ParseDocument(`{"$set":{"status":"archived"}}`)
	if err != nil {
		t.Fatalf("ParseDocument failed: %v", err)
	}
	if !isOperatorUpdate(update) {
		t.Fatalf("operator update should be accepted")
	}

	update, err = ParseDocument(`{"$set":{"status":"archived"},"status":"invalid"}`)
	if err != nil {
		t.Fatalf("ParseDocument failed: %v", err)
	}
	if isOperatorUpdate(update) {
		t.Fatalf("mixed operator and replacement fields should not be accepted")
	}
}

func TestWaitWithContextCancelled(t *testing.T) {
	// 测试可取消等待不会阻塞到完整 pause。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := waitWithContext(ctx, time.Minute)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected ErrCancelled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancelled wait took too long")
	}
}

func TestShouldRetryBulkCursorStopsAfterProgress(t *testing.T) {
	// 测试 bulk 已完成至少一个批次后，不再重试原 filter，避免非幂等 update 重放。
	result := &BulkResult{Processed: 100, BatchCount: 1}
	if shouldRetryBulkCursor(true, 0, 3, result) {
		t.Fatalf("expected retry to stop after progress")
	}
}

func TestShouldRetryBulkCursorBeforeProgress(t *testing.T) {
	// 测试 bulk 尚未处理任何文档时，retryable cursor error 仍允许按上限重试。
	result := &BulkResult{}
	if !shouldRetryBulkCursor(true, 0, 3, result) {
		t.Fatalf("expected retry before progress")
	}
	if shouldRetryBulkCursor(true, 3, 3, result) {
		t.Fatalf("expected retry to stop at max retries")
	}
	if shouldRetryBulkCursor(false, 0, 3, result) {
		t.Fatalf("expected non-retryable error to stop")
	}
}

func TestBulkDeleteDryRunOnlyCounts(t *testing.T) {
	// 测试 BulkDelete dry-run 只统计数量，不打开游标或执行写操作。
	ops := &fakeBulkOperations{count: 12}
	observer := &recordingBulkObserver{}
	client := &Client{bulk: ops}

	result, err := client.BulkDelete(context.Background(), BulkOptions{
		Database:   "db",
		Collection: "coll",
		Filter:     bson.D{{Key: "status", Value: "expired"}},
		BatchSize:  2,
		DryRun:     true,
		Observer:   observer,
	})
	if err != nil {
		t.Fatalf("BulkDelete failed: %v", err)
	}
	if result.MatchedTotal != 12 || !result.DryRun || result.Processed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if ops.findCalls != 0 || len(ops.deleteCalls) != 0 {
		t.Fatalf("dry-run performed cursor or writes: find=%d delete=%d", ops.findCalls, len(ops.deleteCalls))
	}
	if fmt.Sprint(observer.events) != "[start done]" {
		t.Fatalf("unexpected observer events: %v", observer.events)
	}
}

func TestBulkDeleteBatchesAndReportsProgress(t *testing.T) {
	// 测试 BulkDelete 按批次执行，并返回累计结果和 observer 进度。
	ops := &fakeBulkOperations{
		count: 5,
		ids:   []any{1, 2, 3, 4, 5},
	}
	observer := &recordingBulkObserver{}
	client := &Client{bulk: ops}

	result, err := client.BulkDelete(context.Background(), BulkOptions{
		Database:   "db",
		Collection: "coll",
		Filter:     bson.D{{Key: "status", Value: "expired"}},
		BatchSize:  2,
		Observer:   observer,
	})
	if err != nil {
		t.Fatalf("BulkDelete failed: %v", err)
	}
	if result.Processed != 5 || result.Deleted != 5 || result.BatchCount != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(ops.deleteCalls) != 3 || len(observer.batches) != 3 {
		t.Fatalf("unexpected batches: writes=%d observer=%d", len(ops.deleteCalls), len(observer.batches))
	}
	if got := observer.batches[2].Processed; got != 5 {
		t.Fatalf("final observer progress = %d, want 5", got)
	}
	if fmt.Sprint(observer.events) != "[start batch batch batch done]" {
		t.Fatalf("unexpected observer events: %v", observer.events)
	}
}

func TestBulkUpdateReturnsPartialResult(t *testing.T) {
	// 测试 BulkUpdate 在后续批次失败时保留已完成批次的部分结果。
	ops := &fakeBulkOperations{
		count:            4,
		ids:              []any{1, 2, 3, 4},
		updateFailOnCall: 2,
	}
	client := &Client{bulk: ops}

	result, err := client.BulkUpdate(context.Background(), BulkUpdateOptions{
		BulkOptions: BulkOptions{
			Database:   "db",
			Collection: "coll",
			Filter:     bson.D{{Key: "status", Value: "pending"}},
			BatchSize:  2,
		},
		Update: bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "archived"}}}},
	})
	if err == nil {
		t.Fatalf("expected partial error")
	}
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected PartialError, got %T: %v", err, err)
	}
	if result.Processed != 2 || result.Matched != 2 || result.Modified != 2 || result.BatchCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if partial.Result != *result {
		t.Fatalf("partial result mismatch: %+v != %+v", partial.Result, *result)
	}
}

func TestBulkDeleteCancelReturnsPartialResult(t *testing.T) {
	// 测试 BulkDelete 在批次后取消时停止后续写入并返回部分结果。
	ops := &fakeBulkOperations{
		count: 4,
		ids:   []any{1, 2, 3, 4},
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer := &recordingBulkObserver{cancel: cancel}
	client := &Client{bulk: ops}

	result, err := client.BulkDelete(ctx, BulkOptions{
		Database:   "db",
		Collection: "coll",
		Filter:     bson.D{{Key: "status", Value: "expired"}},
		BatchSize:  2,
		Pause:      time.Minute,
		Observer:   observer,
	})
	if !errors.Is(err, ErrCancelled) || !errors.Is(err, ErrPartialResult) {
		t.Fatalf("expected cancelled partial error, got %v", err)
	}
	if result.Processed != 2 || result.Deleted != 2 || len(ops.deleteCalls) != 1 {
		t.Fatalf("unexpected partial result: %+v, writes=%d", result, len(ops.deleteCalls))
	}
	if ops.lastCursor == nil || !ops.lastCursor.closed || ops.lastCursor.closeErr != nil {
		t.Fatalf("cursor cleanup used cancelled context: %+v", ops.lastCursor)
	}
}
