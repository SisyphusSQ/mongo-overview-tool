package timeutil

import (
	"testing"
	"time"
)

func TestFormatLayoutString(t *testing.T) {
	ti, err := time.Parse(CSTLayout, "2024-11-20 13:55:01")
	if err != nil {
		t.Fatal(err)
	}

	result := FormatLayoutString(ti)
	t.Log(result)
}
