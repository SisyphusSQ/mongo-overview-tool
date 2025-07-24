package timeutil

import (
	"math"
	"net/http"
	"time"
)

var (
	cst *time.Location
)

// CSTLayout China Standard Time Layout
const (
	DateLayout   = "2006-01-02"
	CSTLayout    = "2006-01-02 15:04:05"
	outputLayout = "2006-01-02 15:04:00"
)

func init() {
	var err error
	if cst, err = time.LoadLocation("Asia/Shanghai"); err != nil {
		panic(err)
	}

	// 默认设置为中国时区
	time.Local = cst
}

// RFC3339ToCSTLayout convert rfc3339 value to china standard time layout
// 2020-11-08T08:18:46+08:00 => 2020-11-08 08:18:46
func RFC3339ToCSTLayout(value string) (string, error) {
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", err
	}

	return ts.In(cst).Format(CSTLayout), nil
}

// CSTLayoutString 格式化时间
// 返回 "2006-01-02 15:04:05" 格式的时间
func CSTLayoutString() string {
	ts := time.Now()
	return ts.In(cst).Format(CSTLayout)
}

func FormatLayoutString(t time.Time) string {
	return t.In(cst).Format(CSTLayout)
}

// ParseCSTInLocation 格式化时间
func ParseCSTInLocation(date string) (time.Time, error) {
	return time.ParseInLocation(CSTLayout, date, cst)
}

// CSTLayoutStringToUnix 返回 unix 时间戳
// 2020-01-24 21:11:11 => 1579871471
func CSTLayoutStringToUnix(cstLayoutString string) (int64, error) {
	stamp, err := time.ParseInLocation(CSTLayout, cstLayoutString, cst)
	if err != nil {
		return 0, err
	}
	return stamp.Unix(), nil
}

// GMTLayoutString 格式化时间
// 返回 "Mon, 02 Jan 2006 15:04:05 GMT" 格式的时间
func GMTLayoutString() string {
	return time.Now().In(cst).Format(http.TimeFormat)
}

// ParseGMTInLocation 格式化时间
func ParseGMTInLocation(date string) (time.Time, error) {
	return time.ParseInLocation(http.TimeFormat, date, cst)
}

// SubInLocation 计算时间差
func SubInLocation(ts time.Time) float64 {
	return math.Abs(time.Now().In(cst).Sub(ts).Seconds())
}

func SubLayoutString(date string, sub time.Duration) (string, error) {
	loc, err := ParseCSTInLocation(date)
	if err != nil {
		return "", err
	}

	t := loc.Add(sub)
	return FormatLayoutString(t), nil
}

func CompareTimes(t1, t2 string) bool {
	c1, err := CSTLayoutStringToUnix(t1)
	if err != nil {
		return true
	}

	c2, err := CSTLayoutStringToUnix(t2)
	if err != nil {
		return true
	}

	return c1 >= c2
}

func Yesterday() string {
	var (
		now                = time.Now()
		yesterdayBeginning = time.Date(
			now.Year(),
			now.Month(),
			now.Day()-1,
			0,
			0,
			0,
			0,
			now.Location(),
		)
	)

	return yesterdayBeginning.Format(CSTLayout)
}

func NearestTenMinute(t time.Time) time.Time {
	minutes := t.Minute()
	roundedMinutes := (minutes / 30) * 30
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), roundedMinutes, 0, 0, cst)
}

func NearestTenMinuteStr(timeStr string) (string, error) {
	t, err := time.Parse(CSTLayout, timeStr)
	if err != nil {
		return "", err
	}

	minutes := t.Minute()
	roundedMinutes := (minutes / 30) * 30
	roundedTime := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), roundedMinutes, 0, 0, t.Location())
	return roundedTime.Format(outputLayout), nil
}

func GetDateFromTimeStr(str string) (string, error) {
	t, err := ParseCSTInLocation(str)
	if err != nil {
		return "", err
	}

	return t.Format(DateLayout), nil
}
