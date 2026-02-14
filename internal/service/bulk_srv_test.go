package service

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
)

// TestParseBsonDoc 解析 JSON 与 ExtJSON 字符串为 bson.D。
//
// 入参: 无（通过测试用例构造不同输入）
// 出参: 无（断言失败时由 testing 框架报错）
//
// 注意: 主要验证 parseBsonDoc 在空过滤条件、普通 JSON 与 ExtJSON($date) 场景下的解析行为。
func TestParseBsonDoc(t *testing.T) {
	t.Logf("testing parseBsonDoc: empty/normal/extjson_date cases")

	t.Run("empty filter", func(t *testing.T) {
		// 用例说明: 验证 parseBsonDoc 在 "{}" 输入下返回空 bson.D（匹配全部）。
		input := "{}"
		t.Logf("case=empty filter, input=%s", input)
		got, err := mongo.ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse empty filter failed: %v", err)
		}
		t.Logf("parsed result len=%d", len(got))
		if len(got) != 0 {
			t.Fatalf("expected empty bson.D, got: %#v", got)
		}
		t.Logf("assert ok: empty filter returns empty bson.D")
	})

	t.Run("normal json filter", func(t *testing.T) {
		// 用例说明: 验证 parseBsonDoc 能正确解析普通 JSON 键值过滤条件。
		input := `{"status":"inactive"}`
		t.Logf("case=normal json filter, input=%s", input)
		got, err := mongo.ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse normal filter failed: %v", err)
		}
		t.Logf("parsed result=%#v", got)
		if len(got) != 1 || got[0].Key != "status" || got[0].Value != "inactive" {
			t.Fatalf("unexpected filter result: %#v", got)
		}
		t.Logf("assert ok: normal json filter key/value as expected")
	})

	t.Run("extjson date filter", func(t *testing.T) {
		// 用例说明: 验证 parseBsonDoc 对 ExtJSON $date 的解析结果为 primitive.DateTime。
		input := `{"createdAt":{"$lt":{"$date":"2024-01-01T00:00:00Z"}}}`
		t.Logf("case=extjson date filter, input=%s", input)
		got, err := mongo.ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse extjson filter failed: %v", err)
		}

		createdAt := got.Map()["createdAt"]
		t.Logf("createdAt field type=%T", createdAt)
		ltDoc, ok := createdAt.(bson.D)
		if !ok {
			t.Fatalf("expected createdAt to be bson.D, got: %T", createdAt)
		}

		ltValue := ltDoc.Map()["$lt"]
		t.Logf("$lt field type=%T", ltValue)
		if _, ok := ltValue.(primitive.DateTime); !ok {
			t.Fatalf("expected $lt to be primitive.DateTime, got: %T", ltValue)
		}
		t.Logf("assert ok: extjson $date parsed as primitive.DateTime")
	})
}
