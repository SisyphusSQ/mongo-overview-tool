package mot

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/utils/retry"
)

const (
	MaxBulkBatchSize      = 50000
	defaultBulkMaxRetries = 3
)

type bulkExecFunc func(ctx context.Context, ids []any) (BulkBatchResult, error)

type bulkCursor interface {
	Next(ctx context.Context) bool
	Decode(value any) error
	Err() error
	Close(ctx context.Context) error
}

type bulkOperations interface {
	CountDocuments(ctx context.Context, db, coll string, filter any) (int64, error)
	FindIDsCursor(ctx context.Context, db, coll string, filter any) (bulkCursor, error)
	BulkDelete(ctx context.Context, db, coll string, ids []any) (int64, error)
	BulkUpdate(ctx context.Context, db, coll string, ids []any, update any) (pkgmongo.BulkUpdateResult, error)
}

type connBulkOperations struct {
	conn *pkgmongo.Conn
}

func (o connBulkOperations) CountDocuments(ctx context.Context, db, coll string, filter any) (int64, error) {
	return o.conn.CountDocuments(ctx, db, coll, filter)
}

func (o connBulkOperations) FindIDsCursor(ctx context.Context, db, coll string, filter any) (bulkCursor, error) {
	return o.conn.FindIDsCursor(ctx, db, coll, filter)
}

func (o connBulkOperations) BulkDelete(ctx context.Context, db, coll string, ids []any) (int64, error) {
	return o.conn.BulkDelete(ctx, db, coll, ids)
}

func (o connBulkOperations) BulkUpdate(ctx context.Context, db, coll string, ids []any, update any) (pkgmongo.BulkUpdateResult, error) {
	return o.conn.BulkUpdate(ctx, db, coll, ids, update)
}

// BulkDelete 分批删除匹配文档。
func (c *Client) BulkDelete(ctx context.Context, opts BulkOptions) (*BulkResult, error) {
	filter, result, err := validateBulkOptions("bulk-delete", opts)
	if err != nil {
		return result, err
	}
	return c.runBulk(ctx, "bulk-delete", opts, result, filter, func(ctx context.Context, ids []any) (BulkBatchResult, error) {
		deleted, err := c.bulk.BulkDelete(ctx, opts.Database, opts.Collection, ids)
		return BulkBatchResult{
			Processed: int64(len(ids)),
			Deleted:   deleted,
		}, err
	})
}

// BulkUpdate 分批更新匹配文档。
func (c *Client) BulkUpdate(ctx context.Context, opts BulkUpdateOptions) (*BulkResult, error) {
	filter, result, err := validateBulkOptions("bulk-update", opts.BulkOptions)
	if err != nil {
		return result, err
	}
	update, err := ParseDocument(opts.Update)
	if err != nil {
		return result, err
	}
	if len(update) == 0 {
		return result, invalidOptions("update is required")
	}
	if !isOperatorUpdate(update) {
		return result, invalidOptions("update must use MongoDB update operators")
	}
	return c.runBulk(ctx, "bulk-update", opts.BulkOptions, result, filter, func(ctx context.Context, ids []any) (BulkBatchResult, error) {
		updateResult, err := c.bulk.BulkUpdate(ctx, opts.Database, opts.Collection, ids, update)
		return BulkBatchResult{
			Processed: int64(len(ids)),
			Matched:   updateResult.Matched,
			Modified:  updateResult.Modified,
		}, err
	})
}

func validateBulkOptions(op string, opts BulkOptions) (bson.D, *BulkResult, error) {
	result := &BulkResult{
		Database:   opts.Database,
		Collection: opts.Collection,
		DryRun:     opts.DryRun,
		StartedAt:  time.Now(),
	}
	if opts.Database == "" {
		return nil, result, invalidOptions("database is required")
	}
	if opts.Collection == "" {
		return nil, result, invalidOptions("collection is required")
	}
	if opts.BatchSize <= 0 {
		return nil, result, invalidOptions("batch size must be greater than 0")
	}
	if opts.BatchSize > MaxBulkBatchSize {
		return nil, result, invalidOptions("batch size must be less than or equal to %d", MaxBulkBatchSize)
	}
	if opts.Pause < 0 {
		return nil, result, invalidOptions("pause must be greater than or equal to 0")
	}
	if opts.MaxRetries < 0 {
		return nil, result, invalidOptions("max retries must be greater than or equal to 0")
	}

	filter, err := ParseDocument(opts.Filter)
	if err != nil {
		return nil, result, err
	}
	if isEmptyDocument(filter) && !opts.DryRun && !opts.AllowEmptyFilter {
		return filter, result, fmt.Errorf("%w: %s empty filter requires AllowEmptyFilter", ErrDangerousOperation, op)
	}
	return filter, result, nil
}

func (c *Client) runBulk(ctx context.Context, op string, opts BulkOptions, result *BulkResult, filter bson.D, exec bulkExecFunc) (*BulkResult, error) {
	if c == nil || c.bulk == nil {
		return result, invalidOptions("bulk operations are not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if result.FinishedAt.IsZero() {
			result.FinishedAt = time.Now()
		}
	}()

	if err := contextError(ctx); err != nil {
		return result, partialBulkError(op, result, err)
	}
	total, err := c.bulk.CountDocuments(ctx, opts.Database, opts.Collection, filter)
	if err != nil {
		return result, partialBulkError(op, result, mapContextError(err))
	}
	result.MatchedTotal = total
	if opts.Observer != nil {
		opts.Observer.OnBulkStart(ctx, total)
	}
	if total == 0 || opts.DryRun {
		finishBulkResult(result)
		if opts.Observer != nil {
			opts.Observer.OnBulkDone(ctx, *result)
		}
		return result, nil
	}

	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultBulkMaxRetries
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		retryable, err := c.runBulkOnce(ctx, opts, result, filter, exec)
		if err == nil {
			finishBulkResult(result)
			if opts.Observer != nil {
				opts.Observer.OnBulkDone(ctx, *result)
			}
			return result, nil
		}
		lastErr = err
		if !shouldRetryBulkCursor(retryable, attempt, maxRetries, result) {
			break
		}
		nextAttempt := attempt + 1
		if opts.Observer != nil {
			opts.Observer.OnBulkRetry(ctx, err, nextAttempt)
		}
		if waitErr := waitWithContext(ctx, retry.DefaultSleep); waitErr != nil {
			return result, partialBulkError(op, result, waitErr)
		}
	}
	return result, partialBulkError(op, result, lastErr)
}

func shouldRetryBulkCursor(retryable bool, attempt, maxRetries int, result *BulkResult) bool {
	if !retryable || attempt == maxRetries {
		return false
	}
	if result != nil && result.Processed > 0 {
		return false
	}
	return true
}

func (c *Client) runBulkOnce(ctx context.Context, opts BulkOptions, result *BulkResult, filter bson.D, exec bulkExecFunc) (bool, error) {
	cur, err := c.bulk.FindIDsCursor(ctx, opts.Database, opts.Collection, filter)
	if err != nil {
		err = mapContextError(err)
		return pkgmongo.IsRetryableCursorError(err), fmt.Errorf("failed to open cursor: %w", err)
	}
	defer func() {
		closeCtx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = cur.Close(closeCtx)
	}()

	ids := make([]any, 0, opts.BatchSize)
	for cur.Next(ctx) {
		if err := contextError(ctx); err != nil {
			return false, err
		}
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return false, fmt.Errorf("failed to decode document: %w", err)
		}
		ids = append(ids, doc["_id"])
		if len(ids) >= opts.BatchSize {
			if err := c.executeBulkBatch(ctx, opts, result, ids, exec); err != nil {
				return false, err
			}
			ids = ids[:0]
		}
	}
	if err := cur.Err(); err != nil {
		err = mapContextError(err)
		return pkgmongo.IsRetryableCursorError(err), fmt.Errorf("cursor iteration error: %w", err)
	}
	if len(ids) > 0 {
		if err := c.executeBulkBatch(ctx, opts, result, ids, exec); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (c *Client) executeBulkBatch(ctx context.Context, opts BulkOptions, result *BulkResult, ids []any, exec bulkExecFunc) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	batch, err := exec(ctx, ids)
	if err != nil {
		return mapContextError(err)
	}
	result.BatchCount++
	batch.BatchNumber = result.BatchCount
	result.Processed += batch.Processed
	result.Deleted += batch.Deleted
	result.Matched += batch.Matched
	result.Modified += batch.Modified
	batch.Processed = result.Processed
	if opts.Observer != nil {
		opts.Observer.OnBulkBatch(ctx, batch)
	}
	if opts.Pause > 0 {
		return waitWithContext(ctx, opts.Pause)
	}
	return nil
}

func isOperatorUpdate(update bson.D) bool {
	if len(update) == 0 {
		return false
	}
	for _, item := range update {
		if len(item.Key) == 0 || item.Key[0] != '$' {
			return false
		}
	}
	return true
}

func waitWithContext(ctx context.Context, pause time.Duration) error {
	if pause <= 0 {
		return contextError(ctx)
	}
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return wrapCancelled(ctx.Err())
	case <-timer.C:
		return nil
	}
}

func partialBulkError(op string, result *BulkResult, err error) error {
	if err == nil {
		return nil
	}
	finishBulkResult(result)
	partial := BulkResult{}
	if result != nil {
		partial = *result
	}
	return &PartialError{
		Op:     op,
		Result: partial,
		Err:    err,
	}
}

func finishBulkResult(result *BulkResult) {
	if result != nil && result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
}
