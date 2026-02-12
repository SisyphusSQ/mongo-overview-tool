package retry

import (
	"context"
	"math/rand"
	"net/http"
	"time"
)

var (
	DefaultSleep         = 1 * time.Second
	ReadableDefaultSleep = "1s"
)

// Func is the function to be executed and eventually retried.
type Func func() error

type Condition bool

const (
	Break    Condition = true
	Continue Condition = false
)

// ConditionFunc returns additional flag determine whether to break retry or not.
type ConditionFunc func() (Condition, error)

// HTTPFunc is the function to be executed and eventually retried.
// The only difference from Func is that it expects an *http.Response on the first returning argument.
type HTTPFunc func() (*http.Response, error)

// Do runs the passed function until the number of retries is reached.
// The sleep value is slightly modified on every retry (exponential backoff) to prevent the thundering herd problem (https://en.wikipedia.org/wiki/Thundering_herd_problem).
// If no value is given to sleep it will defaults to 1s.
func Do(fn Func, retries int, sleep time.Duration) error {
	if sleep == 0 {
		sleep = DefaultSleep
	}

	var err error
	for i := 0; i < retries; i++ {
		if err = fn(); err == nil {
			return nil
		}

		// 最后一次失败不再等待
		if i == retries-1 {
			break
		}

		// preventing thundering herd problem (https://en.wikipedia.org/wiki/Thundering_herd_problem)
		sleep += (time.Duration(rand.Int63n(int64(sleep)))) / 2
		if sleep > maxBackoff {
			sleep = maxBackoff
		}
		time.Sleep(sleep)
		sleep *= 2
	}

	return err
}

// maxBackoff 指数退避上限，防止无限增长
const maxBackoff = 30 * time.Second

// DoWithContext 带 context 的重试函数，支持 ctx 取消时立即返回
//
// 使用循环 + 指数退避 + 随机抖动，与 Do 行为一致但可被 ctx 取消。
// retries 为总尝试次数（含首次），sleep 为首次重试等待时间（0 默认 1s）。
func DoWithContext(ctx context.Context, fn Func, retries int, sleep time.Duration) error {
	if sleep == 0 {
		sleep = DefaultSleep
	}

	var err error
	for i := 0; i < retries; i++ {
		if err = fn(); err == nil {
			return nil
		}

		// 最后一次失败不再等待
		if i == retries-1 {
			break
		}

		// 指数退避 + 随机抖动
		sleep += time.Duration(rand.Int63n(int64(sleep))) / 2
		if sleep > maxBackoff {
			sleep = maxBackoff
		}

		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		sleep *= 2
	}
	return err
}

func DoCondition(fn ConditionFunc, retries int, sleep time.Duration) error {
	if sleep == 0 {
		sleep = DefaultSleep
	}

	var cond Condition
	var err error
	for i := 0; i < retries; i++ {
		cond, err = fn()

		if cond == Break {
			return err
		}

		// 最后一次失败不再等待
		if i == retries-1 {
			break
		}

		// preventing thundering herd problem (https://en.wikipedia.org/wiki/Thundering_herd_problem)
		sleep += (time.Duration(rand.Int63n(int64(sleep)))) / 2
		if sleep > maxBackoff {
			sleep = maxBackoff
		}
		time.Sleep(sleep)
	}

	return err
}
