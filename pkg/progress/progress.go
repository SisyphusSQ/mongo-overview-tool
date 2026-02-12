package progress

import (
	"fmt"
	"strings"
	"time"
)

const defaultBarWidth = 40

// ProgressBar 终端进度条组件，支持百分比、处理速度和预估剩余时间。
//
// 入参: 无
// 出参: 无
// 注意: 使用 \r 覆盖同一行实现实时刷新，Finish() 后输出换行。
type ProgressBar struct {
	total     int64     // 总文档数
	current   int64     // 已处理数
	startTime time.Time // 开始时间
	barWidth  int       // 进度条宽度（字符数）
}

// NewProgressBar 创建一个新的进度条实例。
//
// 入参:
// - total: 需要处理的文档总数，必须为正数
//
// 出参:
// - *ProgressBar: 进度条实例
//
// 注意: 创建后自动记录开始时间，用于后续速度和 ETA 计算。
func NewProgressBar(total int64) *ProgressBar {
	return &ProgressBar{
		total:     total,
		startTime: time.Now(),
		barWidth:  defaultBarWidth,
	}
}

// Elapsed 返回从创建进度条到现在经过的时间。
//
// 入参: 无
// 出参:
// - time.Duration: 已流逝的时间
//
// 注意: 基于创建时记录的 startTime 计算。
func (p *ProgressBar) Elapsed() time.Duration {
	return time.Since(p.startTime)
}

// Speed 返回当前的文档处理速度（docs/sec）。
//
// 入参: 无
// 出参:
// - float64: 处理速度，elapsed 为 0 时返回 0 避免除零
//
// 注意: 基于已处理文档数和已流逝时间计算。
func (p *ProgressBar) Speed() float64 {
	elapsed := p.Elapsed().Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(p.current) / elapsed
}

// Update 更新进度条的当前进度并刷新终端显示。
//
// 入参:
// - current: 当前已处理的文档数
//
// 出参: 无
//
// 注意: 使用 \r 覆盖当前行，不会产生新行。速度和 ETA 基于已流逝时间计算。
//
//	当 current 超过 total 时（并发写入场景），自动调整 total 避免显示异常。
func (p *ProgressBar) Update(current int64) {
	p.current = current
	if current > p.total {
		p.total = current
	}

	percent := float64(current) / float64(p.total) * 100
	if p.total == 0 {
		percent = 100
	}

	// 计算已填充的格数
	filled := int(percent / 100 * float64(p.barWidth))
	if filled > p.barWidth {
		filled = p.barWidth
	}
	empty := p.barWidth - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)

	// 计算速度
	speed := p.Speed()

	// 计算 ETA
	eta := "N/A"
	if speed > 0 {
		remaining := float64(p.total-current) / speed
		eta = formatDuration(time.Duration(remaining * float64(time.Second)))
	}

	fmt.Printf("\r[%s]  %.1f%%  %d/%d  |  %.0f docs/sec  |  ETA: %s    ",
		bar, percent, current, p.total, speed, eta)
}

// Finish 完成进度条，输出最终的 100% 状态并换行。
//
// 入参: 无
// 出参: 无
//
// 注意: 调用后会输出换行符，后续打印不会覆盖进度条。
func (p *ProgressBar) Finish() {
	p.Update(p.total)
	fmt.Println()
}

// formatDuration 将 time.Duration 格式化为人类可读的简短字符串。
//
// 入参:
// - d: 时间间隔
//
// 出参:
// - string: 格式化后的字符串，如 "1m30s"、"45s"、"<1s"
//
// 注意: 不显示毫秒精度，小于 1 秒统一显示 "<1s"。
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}

	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60

	if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
