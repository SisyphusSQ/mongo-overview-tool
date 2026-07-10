package mongo

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestLegacySlowlogID(t *testing.T) {
	// 测试低版本 profiler 缺少 queryHash 时生成稳定且可区分的标识。
	first := legacySlowlogID("app.orders", "query", "COLLSCAN")
	second := legacySlowlogID("app.orders", "query", "IXSCAN { status: 1 }")
	if !strings.HasPrefix(first, legacySlowlogPrefix) {
		t.Fatalf("legacy id = %q, want prefix %q", first, legacySlowlogPrefix)
	}
	if first != legacySlowlogID("app.orders", "query", "COLLSCAN") {
		t.Fatalf("legacy id is not deterministic")
	}
	if first == second {
		t.Fatalf("different plan summaries produced the same legacy id")
	}
}

func TestLegacySlowlogDocumentID(t *testing.T) {
	// 测试 detail 扫描和 summary 使用相同字段生成 legacy 标识。
	document := bson.M{
		"ns":          "app.orders",
		"op":          "query",
		"planSummary": "COLLSCAN",
	}
	want := legacySlowlogID("app.orders", "query", "COLLSCAN")
	if got := legacySlowlogDocumentID(document); got != want {
		t.Fatalf("legacySlowlogDocumentID() = %q, want %q", got, want)
	}
}
