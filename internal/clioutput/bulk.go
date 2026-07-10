package clioutput

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fatih/color"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/progress"
	"github.com/SisyphusSQ/mongo-overview-tool/utils/timeutil"
)

type BulkObserverOptions struct {
	Action     string
	Database   string
	Collection string
	Filter     string
	Update     string
	BatchSize  int
	Pause      time.Duration
	DryRun     bool
}

type BulkObserver struct {
	w      io.Writer
	opts   BulkObserverOptions
	file   *os.File
	log    *bufio.Writer
	bar    *progress.ProgressBar
	total  int64
	closed bool
}

func NewBulkObserver(w io.Writer, output string, opts BulkObserverOptions) (*BulkObserver, error) {
	observer := &BulkObserver{w: w, opts: opts}
	if observer.w == nil {
		observer.w = io.Discard
	}
	if output == "" {
		return observer, nil
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	observer.file = file
	observer.log = bufio.NewWriter(file)
	return observer, nil
}

func (o *BulkObserver) Close() error {
	if o == nil || o.closed {
		return nil
	}
	o.closed = true
	if o.log != nil {
		if err := o.log.Flush(); err != nil {
			return err
		}
	}
	if o.file != nil {
		return o.file.Close()
	}
	return nil
}

func (o *BulkObserver) OnBulkStart(ctx context.Context, total int64) {
	o.total = total
	fmt.Fprintf(o.w, "%s\n", color.CyanString("========== %s Summary ==========", o.opts.Action))
	fmt.Fprintf(o.w, "  Database:   %s\n", color.GreenString(o.opts.Database))
	fmt.Fprintf(o.w, "  Collection: %s\n", color.GreenString(o.opts.Collection))
	fmt.Fprintf(o.w, "  Filter:     %s\n", color.GreenString(o.opts.Filter))
	if o.opts.Action == "bulk-update" {
		fmt.Fprintf(o.w, "  Update:     %s\n", color.GreenString(o.opts.Update))
	}
	fmt.Fprintf(o.w, "  Matched:    %s\n", color.HiRedString("%d", total))
	fmt.Fprintf(o.w, "  Batch size: %s\n", color.GreenString("%d", o.opts.BatchSize))
	fmt.Fprintf(o.w, "  Pause:      %s\n", color.GreenString(o.opts.Pause.String()))
	if o.opts.DryRun {
		fmt.Fprintf(o.w, "  Mode:       %s\n", color.YellowString("DRY-RUN"))
	}
	fmt.Fprintf(o.w, "%s\n", color.CyanString("======================================"))
	if total > 0 && !o.opts.DryRun {
		o.bar = progress.NewProgressBar(total)
	}
}

func (o *BulkObserver) OnBulkBatch(ctx context.Context, batch mot.BulkBatchResult) {
	if o.bar != nil {
		o.bar.Update(batch.Processed)
	}
	switch o.opts.Action {
	case "bulk-update":
		o.writeLog("Batch #%d completed: processed=%d/%d matched=%d modified=%d",
			batch.BatchNumber, batch.Processed, o.total, batch.Matched, batch.Modified)
	default:
		o.writeLog("Batch #%d completed: processed=%d/%d deleted=%d",
			batch.BatchNumber, batch.Processed, o.total, batch.Deleted)
	}
}

func (o *BulkObserver) OnBulkRetry(ctx context.Context, err error, attempt int) {
	msg := fmt.Sprintf("retryable cursor error, retrying attempt=%d: %v", attempt, err)
	fmt.Fprintln(o.w, color.YellowString(msg))
	o.writeLog("%s", msg)
}

func (o *BulkObserver) OnBulkDone(ctx context.Context, result mot.BulkResult) {
	if result.MatchedTotal == 0 {
		fmt.Fprintln(o.w, color.YellowString("No matching documents, nothing to do."))
		return
	}
	if result.DryRun {
		fmt.Fprintf(o.w, "%s\n", color.YellowString("[DRY-RUN] Count only, no actual write."))
		return
	}
	if o.bar != nil {
		o.bar.Finish()
	}
	elapsed := result.FinishedAt.Sub(result.StartedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	switch o.opts.Action {
	case "bulk-update":
		fmt.Fprintf(o.w, "Bulk update done: processed %s docs, matched %s, modified %s, elapsed %s\n",
			color.GreenString("%d", result.Processed),
			color.GreenString("%d", result.Matched),
			color.GreenString("%d", result.Modified),
			color.GreenString(elapsed.Round(time.Millisecond).String()))
		o.writeLog("Bulk update done: processed=%d matched=%d modified=%d elapsed=%s",
			result.Processed, result.Matched, result.Modified, elapsed.Round(time.Millisecond).String())
	default:
		fmt.Fprintf(o.w, "Bulk delete done: processed %s docs, deleted %s docs, elapsed %s\n",
			color.GreenString("%d", result.Processed),
			color.GreenString("%d", result.Deleted),
			color.GreenString(elapsed.Round(time.Millisecond).String()))
		o.writeLog("Bulk delete done: processed=%d deleted=%d elapsed=%s",
			result.Processed, result.Deleted, elapsed.Round(time.Millisecond).String())
	}
}

func (o *BulkObserver) writeLog(format string, args ...any) {
	if o == nil || o.log == nil {
		return
	}
	line := fmt.Sprintf("[%s] %s\n", timeutil.FormatLayoutString(time.Now()), fmt.Sprintf(format, args...))
	_, _ = o.log.WriteString(line)
}
