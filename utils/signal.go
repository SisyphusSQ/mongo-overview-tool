package utils

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	l "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/log"
)

// SetupSignalHandler 注册 SIGINT/SIGTERM 信号监听，返回取消标志和清理函数。
//
// 入参: 无
//
// 出参:
// - *atomic.Bool: 取消标志，收到信号后置为 true
// - func(): 清理函数，调用后停止信号监听并释放 goroutine
//
// 注意: 启动单独的 goroutine 监听信号，调用方需在业务循环中检查取消标志，并在退出时调用清理函数。
//
//	使用 done channel 确保 goroutine 在正常结束时不会泄漏。
func SetupSignalHandler() (*atomic.Bool, func()) {
	cancelled := &atomic.Bool{}
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			cancelled.Store(true)
			fmt.Println()
			l.Logger.Warnf("Interrupt received, will exit safely after current batch completes...")
		case <-done:
		}
	}()

	stop := func() {
		signal.Stop(sigCh)
		close(done)
	}
	return cancelled, stop
}

// SetupSignalCancel 注册 SIGINT/SIGTERM 信号监听，并在收到信号时调用 cancel。
func SetupSignalCancel(cancel context.CancelFunc) func() {
	sigCh := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			fmt.Println()
			l.Logger.Warnf("Interrupt received, cancelling context...")
			if cancel != nil {
				cancel()
			}
		case <-done:
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}
