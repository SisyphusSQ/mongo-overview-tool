package timeutil

import (
	"testing"
	"time"
)

func TestRFC3339ToCSTLayout(t *testing.T) {
	t.Log(RFC3339ToCSTLayout("2020-11-08T08:18:46+08:00"))
}

func TestCSTLayoutString(t *testing.T) {
	t.Log(CSTLayoutString())
}

func TestCSTLayoutStringToUnix(t *testing.T) {
	t.Log(CSTLayoutStringToUnix("2020-01-24 21:11:11"))
}

func TestGMTLayoutString(t *testing.T) {
	t.Log(GMTLayoutString())
}

func TestYesterday(t *testing.T) {
	t.Log(Yesterday())
}

func TestNearestTenMinuteStr(t *testing.T) {
	timeStr := "2024-11-20 13:55:01"
	ti, err := NearestTenMinuteStr(timeStr)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(ti)
}

func TestNearestTenMinute(t *testing.T) {
	timeStr := "2024-11-20 13:55:01"
	ti, err := time.Parse(CSTLayout, timeStr)
	if err != nil {
		t.Fatal(err)
	}

	tim := NearestTenMinute(ti)
	t.Log(tim)
}

func TestGetDateFromTimeStr(t *testing.T) {
	timeStr := "2024-11-20 13:55:01"
	date, err := GetDateFromTimeStr(timeStr)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(date)
}
