package mongo

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/mongo"
)

// retryableKeywords 网络/IO 类可重试错误的关键字列表。
var retryableKeywords = []string{
	"connection reset",
	"connection refused",
	"i/o timeout",
	"broken pipe",
	"no reachable servers",
	"EOF",
	"socket was unexpectedly closed",
	"server selection timeout",
}

// IsRetryableCursorError 判断游标错误是否为可重试的瞬态错误。
//
// 入参:
// - err: 游标遍历返回的错误
//
// 出参:
// - bool: true 表示可重试（如游标超时、网络中断、节点切换），false 表示不可重试
//
// 注意: context 取消/超时一律不重试；通过 MongoDB 错误码和错误消息关键字综合判断。
func IsRetryableCursorError(err error) bool {
	if err == nil {
		return false
	}

	// context 取消或超时不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// MongoDB CommandError 按错误码判断
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		switch cmdErr.Code {
		case 43: // CursorNotFound: 游标在服务端已过期或被清理
			return true
		case 11600, 11602: // InterruptedAtShutdown, InterruptedDueToReplStateChange
			return true
		case 189, 91: // PrimarySteppedDown, ShutdownInProgress
			return true
		case 262: // ExceededTimeLimit: 游标 maxTimeMS 超时
			return true
		}
	}

	// 网络/IO 类错误按关键字判断
	errMsg := err.Error()
	for _, kw := range retryableKeywords {
		if strings.Contains(errMsg, kw) {
			return true
		}
	}

	return false
}
