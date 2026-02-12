package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/progress"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/utils/retry"
	"github.com/SisyphusSQ/mongo-overview-tool/utils/timeutil"
)

var _ BulkSrv = (*BulkSrvImpl)(nil)

// errCancelled 用户中断操作的哨兵错误。
var errCancelled = errors.New("operation cancelled by user")

// BulkSrv 批量操作服务接口，提供批量删除和批量更新能力。
//
// 入参: 无
// 出参: 无
// 注意: 实现方需支持优雅退出和错误即停。
type BulkSrv interface {
	Delete() error
	Update() error
	Close()
}

// BulkSrvImpl BulkSrv 的具体实现。
type BulkSrvImpl struct {
	ctx  context.Context
	cfg  *config.BulkConfig
	conn *mongo.Conn
}

// batchResult 单次批量操作的统计结果。
type batchResult struct {
	primary   int64 // 主指标: delete 为删除数, update 为命中数
	secondary int64 // 副指标: delete 未使用, update 为实际修改数
}

// batchOpts 批量操作选项，用于 processBatches 区分 delete 和 update 的差异部分。
type batchOpts struct {
	action     string // 操作名称，如 "bulk-delete"、"bulk-update"
	errorLabel string // 错误消息中的中文标签，如 "批量删除"、"批量更新"
	dryRunMsg  string // dry-run 模式下的提示信息
	filter     bson.D // 已解析的查询过滤条件

	// exec 执行一个批次的操作，返回该批次的统计结果。
	exec func(ctx context.Context, ids []any) (batchResult, error)

	// batchLog 格式化单个批次完成后写入文件日志的消息。
	batchLog func(batchNum int, processed, total int64, cum batchResult, speed float64) string

	// donePrint 格式化操作完成后输出到终端的消息（含颜色）。
	donePrint func(processed int64, cum batchResult, elapsed time.Duration, avgSpeed float64) string

	// doneLog 格式化操作完成后写入文件日志的消息。
	doneLog func(processed int64, cum batchResult, elapsedStr string, avgSpeed float64) string
}

// NewBulkSrv 创建批量操作服务实例。
//
// 入参:
// - ctx: 上下文，用于超时与取消
// - cfg: 批量操作配置，包含数据库、集合、filter、批次大小等
// - conn: MongoDB 连接实例
//
// 出参:
// - BulkSrv: 批量操作服务接口
// - error: 参数校验失败时非 nil
//
// 注意: 会校验 Database 和 Collection 不能为空。
func NewBulkSrv(ctx context.Context, cfg *config.BulkConfig, conn *mongo.Conn) (BulkSrv, error) {
	if cfg.Database == "" {
		return nil, fmt.Errorf("database is required")
	}
	if cfg.Collection == "" {
		return nil, fmt.Errorf("collection is required")
	}

	return &BulkSrvImpl{
		ctx:  ctx,
		cfg:  cfg,
		conn: conn,
	}, nil
}

// Delete 执行批量删除操作。
//
// 入参: 无（使用构造时注入的 cfg）
// 出参:
// - error: 执行失败或被中断时非 nil
//
// 注意: 支持 dry-run 模式仅统计；支持 Ctrl+C 优雅退出；某批次失败立即停止。
func (s *BulkSrvImpl) Delete() error {
	filter, err := mongo.ParseBsonDoc(s.cfg.Filter)
	if err != nil {
		return fmt.Errorf("failed to parse filter: %w", err)
	}

	return s.processBatches(&batchOpts{
		action:     "bulk-delete",
		errorLabel: "bulk-delete",
		dryRunMsg:  "[DRY-RUN] Count only, no actual delete.",
		filter:     filter,
		exec: func(ctx context.Context, ids []any) (batchResult, error) {
			deleted, err := s.conn.BulkDelete(ctx, s.cfg.Database, s.cfg.Collection, ids)
			return batchResult{primary: deleted}, err
		},
		batchLog: func(batchNum int, processed, total int64, cum batchResult, speed float64) string {
			return fmt.Sprintf("Batch #%d completed: processed=%d/%d deleted=%d speed=%.0f docs/sec",
				batchNum, processed, total, cum.primary, speed)
		},
		donePrint: func(processed int64, cum batchResult, elapsed time.Duration, avgSpeed float64) string {
			return fmt.Sprintf("Bulk delete done: processed %s docs, deleted %s docs, elapsed %s, avg speed %.0f docs/sec",
				color.GreenString("%d", processed),
				color.GreenString("%d", cum.primary),
				color.GreenString(elapsed.Round(time.Millisecond).String()),
				avgSpeed)
		},
		doneLog: func(processed int64, cum batchResult, elapsedStr string, avgSpeed float64) string {
			return fmt.Sprintf("Bulk delete done: processed=%d deleted=%d elapsed=%s avgSpeed=%.0f docs/sec",
				processed, cum.primary, elapsedStr, avgSpeed)
		},
	})
}

// Update 执行批量更新操作。
//
// 入参: 无（使用构造时注入的 cfg）
// 出参:
// - error: 执行失败或被中断时非 nil
//
// 注意: 支持 dry-run 模式仅统计；支持 Ctrl+C 优雅退出；某批次失败立即停止；
//
//	update 字段必须为合法的 MongoDB 更新操作符（如 $set、$unset）。
func (s *BulkSrvImpl) Update() error {
	filter, err := mongo.ParseBsonDoc(s.cfg.Filter)
	if err != nil {
		return fmt.Errorf("failed to parse filter: %w", err)
	}

	update, err := mongo.ParseBsonDoc(s.cfg.Update)
	if err != nil {
		return fmt.Errorf("failed to parse update: %w", err)
	}
	if len(update) == 0 {
		return fmt.Errorf("update cannot be empty")
	}

	return s.processBatches(&batchOpts{
		action:     "bulk-update",
		errorLabel: "bulk-update",
		dryRunMsg:  "[DRY-RUN] Count only, no actual update.",
		filter:     filter,
		exec: func(ctx context.Context, ids []any) (batchResult, error) {
			result, err := s.conn.BulkUpdate(ctx, s.cfg.Database, s.cfg.Collection, ids, update)
			return batchResult{primary: result.Matched, secondary: result.Modified}, err
		},
		batchLog: func(batchNum int, processed, total int64, cum batchResult, speed float64) string {
			return fmt.Sprintf("Batch #%d completed: processed=%d/%d matched=%d modified=%d speed=%.0f docs/sec",
				batchNum, processed, total, cum.primary, cum.secondary, speed)
		},
		donePrint: func(processed int64, cum batchResult, elapsed time.Duration, avgSpeed float64) string {
			return fmt.Sprintf("Bulk update done: processed %s docs, matched %s, modified %s, elapsed %s, avg speed %.0f docs/sec",
				color.GreenString("%d", processed),
				color.GreenString("%d", cum.primary),
				color.GreenString("%d", cum.secondary),
				color.GreenString(elapsed.Round(time.Millisecond).String()),
				avgSpeed)
		},
		doneLog: func(processed int64, cum batchResult, elapsedStr string, avgSpeed float64) string {
			return fmt.Sprintf("Bulk update done: processed=%d matched=%d modified=%d elapsed=%s avgSpeed=%.0f docs/sec",
				processed, cum.primary, cum.secondary, elapsedStr, avgSpeed)
		},
	})
}

// processBatches 批量操作的通用流程：统计 -> dry-run -> 游标遍历 -> 分批执行 -> 进度汇报。
//
// 入参:
// - opts: 批量操作选项，包含执行回调和格式化回调
//
// 出参:
// - error: 执行失败时非 nil，用户中断时返回 nil
//
// 注意: 支持信号中断（SIGINT/SIGTERM）优雅退出，错误即停；使用 ProgressBar 实时刷新进度。
func (s *BulkSrvImpl) processBatches(opts *batchOpts) error {
	// 统计匹配文档数
	total, err := s.conn.CountDocuments(s.ctx, s.cfg.Database, s.cfg.Collection, opts.filter)
	if err != nil {
		return fmt.Errorf("failed to count documents: %w", err)
	}

	// 打印操作摘要
	s.printSummary(opts.action, total)

	if total == 0 {
		fmt.Println(color.YellowString("No matching documents, nothing to do."))
		return nil
	}

	// dry-run 模式
	if s.cfg.DryRun {
		fmt.Println(color.YellowString(opts.dryRunMsg))
		return nil
	}

	// 初始化文件日志（带缓冲）
	fileLogger, cleanup, err := s.initFileLogger()
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	// 设置信号监听
	cancelled, stopSignal := utils.SetupSignalHandler()
	defer stopSignal()

	// 初始化进度条
	bar := progress.NewProgressBar(total)
	var processed int64
	var cumResult batchResult
	batchNum := 0

	ids := make([]any, 0, s.cfg.BatchSize)

	// executeBatch 执行一个批次并更新进度。
	executeBatch := func() error {
		// 检查中断信号
		if cancelled.Load() {
			bar.Update(processed)
			fmt.Println()
			fmt.Println(color.YellowString("Interrupt received, exited safely. Processed: %d/%d", processed, total))
			s.writeLog(fileLogger, "%s", fmt.Sprintf("User interrupted, exited safely. Processed: %d/%d", processed, total))
			return errCancelled
		}

		batchNum++
		result, err := opts.exec(s.ctx, ids)
		if err != nil {
			bar.Update(processed)
			fmt.Println()
			l.Logger.Errorf("batch #%d execution failed: %v (completed: %d/%d)", batchNum, err, processed, total)
			s.writeLog(fileLogger, "batch #%d execution failed: %v (completed: %d/%d)", batchNum, err, processed, total)
			return fmt.Errorf("%s failed at %d/%d: %w", opts.errorLabel, processed, total, err)
		}

		processed += int64(len(ids))
		cumResult.primary += result.primary
		cumResult.secondary += result.secondary
		bar.Update(processed)

		speed := bar.Speed()
		s.writeLog(fileLogger, "%s", opts.batchLog(batchNum, processed, total, cumResult, speed))

		ids = ids[:0]
		time.Sleep(time.Duration(s.cfg.PauseMS) * time.Millisecond)
		return nil
	}

	// 游标遍历，支持可重试错误（游标超时、网络中断等）的自动重试。
	// 重试时重新打开游标：delete 场景已删除的文档不再匹配，update 场景 $set 等操作幂等，均可安全重试。
	const cursorRetries = 3
	iterErr := retry.DoCondition(func() (retry.Condition, error) {
		cur, openErr := s.conn.FindIDsCursor(s.ctx, s.cfg.Database, s.cfg.Collection, opts.filter)
		if openErr != nil {
			return retry.Break, fmt.Errorf("failed to open cursor: %w", openErr)
		}
		defer cur.Close(s.ctx)

		// 重试时清空上次未完成的部分批次
		ids = ids[:0]

		for cur.Next(s.ctx) {
			var doc bson.M
			if decErr := cur.Decode(&doc); decErr != nil {
				return retry.Break, fmt.Errorf("failed to decode document: %w", decErr)
			}
			ids = append(ids, doc["_id"])

			if len(ids) >= s.cfg.BatchSize {
				if batchErr := executeBatch(); batchErr != nil {
					return retry.Break, batchErr
				}
			}
		}

		// 检查游标错误，判断是否为可重试的瞬态错误
		if curErr := cur.Err(); curErr != nil {
			if mongo.IsRetryableCursorError(curErr) {
				l.Logger.Warnf("retryable cursor error encountered, will retry: %v", curErr)
				s.writeLog(fileLogger, "retryable cursor error, retrying: %v (processed so far: %d/%d)", curErr, processed, total)
				return retry.Continue, curErr
			}
			return retry.Break, fmt.Errorf("cursor iteration error: %w", curErr)
		}

		// 处理剩余不满一批的文档
		if len(ids) > 0 {
			if batchErr := executeBatch(); batchErr != nil {
				return retry.Break, batchErr
			}
		}

		return retry.Break, nil
	}, cursorRetries, retry.DefaultSleep)

	if iterErr != nil {
		if errors.Is(iterErr, errCancelled) {
			return nil
		}
		return iterErr
	}

	bar.Finish()
	elapsed := bar.Elapsed()
	avgSpeed := bar.Speed()

	fmt.Println(opts.donePrint(processed, cumResult, elapsed, avgSpeed))
	s.writeLog(fileLogger, "%s", opts.doneLog(processed, cumResult, elapsed.Round(time.Millisecond).String(), avgSpeed))

	return nil
}

// Close 关闭 MongoDB 连接。
//
// 入参: 无
// 出参: 无
//
// 注意: 幂等操作，conn 为 nil 时不会 panic；关闭失败时打印警告日志。
func (s *BulkSrvImpl) Close() {
	if s.conn != nil {
		if err := s.conn.Close(); err != nil {
			l.Logger.Warnf("failed to close MongoDB connection: %v", err)
		}
	}
}

// printSummary 打印操作摘要信息。
//
// 入参:
// - action: 操作类型名称，如 "bulk-delete"、"bulk-update"
// - total: 匹配的文档总数
//
// 出参: 无
//
// 注意: 在实际操作前调用，帮助用户确认操作范围。
func (s *BulkSrvImpl) printSummary(action string, total int64) {
	fmt.Println(color.CyanString("========== %s Summary ==========", action))
	fmt.Printf("  Database:   %s\n", color.GreenString(s.cfg.Database))
	fmt.Printf("  Collection: %s\n", color.GreenString(s.cfg.Collection))
	fmt.Printf("  Filter:     %s\n", color.GreenString(s.cfg.Filter))
	if action == "bulk-update" {
		fmt.Printf("  Update:     %s\n", color.GreenString(s.cfg.Update))
	}
	fmt.Printf("  Matched:    %s\n", color.HiRedString("%d", total))
	fmt.Printf("  Batch size: %s\n", color.GreenString("%d", s.cfg.BatchSize))
	fmt.Printf("  Pause:      %s\n", color.GreenString("%dms", s.cfg.PauseMS))
	if s.cfg.DryRun {
		fmt.Printf("  Mode:       %s\n", color.YellowString("DRY-RUN"))
	}
	fmt.Println(color.CyanString("======================================"))
}

// initFileLogger 初始化带缓冲的文件日志 writer。
//
// 入参: 无（使用 cfg.Output 路径）
// 出参:
// - *bufio.Writer: 缓冲日志 writer，未指定 Output 时返回 nil
// - func(): 清理函数（flush + close），未指定 Output 时返回 nil
// - error: 文件创建失败时非 nil
//
// 注意: 以追加模式打开文件，包裹 bufio.Writer 减少 syscall 次数；调用方需负责调用清理函数。
func (s *BulkSrvImpl) initFileLogger() (*bufio.Writer, func(), error) {
	if s.cfg.Output == "" {
		return nil, nil, nil
	}

	f, err := os.OpenFile(s.cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open log file: %w", err)
	}

	w := bufio.NewWriter(f)
	cleanup := func() {
		if err := w.Flush(); err != nil {
			l.Logger.Warnf("failed to flush log buffer: %v", err)
		}
		if err := f.Close(); err != nil {
			l.Logger.Warnf("failed to close log file: %v", err)
		}
	}

	l.Logger.Infof("Log output to: %s", s.cfg.Output)
	return w, cleanup, nil
}

// writeLog 向带缓冲的日志文件写入一条记录。
//
// 入参:
// - w: 缓冲 writer，为 nil 时不写入
// - format: 格式化字符串
// - args: 格式化参数
//
// 出参: 无
//
// 注意: 自动添加时间戳前缀，写入失败仅打印警告不中断主流程。
func (s *BulkSrvImpl) writeLog(w *bufio.Writer, format string, args ...any) {
	if w == nil {
		return
	}

	msg := fmt.Sprintf(format, args...)
	ts := timeutil.FormatLayoutString(time.Now())
	line := fmt.Sprintf("[%s] %s\n", ts, msg)

	if _, err := w.WriteString(line); err != nil {
		l.Logger.Warnf("failed to write to log file: %v", err)
	}
}

