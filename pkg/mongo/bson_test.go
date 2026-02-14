package mongo

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ---------- ShellToExtJSON 单元测试 ----------

// TestShellToExtJSON_UnquotedKeys 验证无引号键名能被正确加上双引号。
//
// 入参: 无
// 出参: 无
//
// 注意: 覆盖普通键名、$ 前缀操作符、已加引号键名不受影响等场景。
func TestShellToExtJSON_UnquotedKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple unquoted key",
			input: `{status: "inactive"}`,
			want:  `{"status": "inactive"}`,
		},
		{
			name:  "dollar prefix operator",
			input: `{$lt: 100}`,
			want:  `{"$lt": 100}`,
		},
		{
			name:  "nested unquoted keys",
			input: `{hitCreateTime: {$lt: 100}}`,
			want:  `{"hitCreateTime": {"$lt": 100}}`,
		},
		{
			name:  "already quoted keys unchanged",
			input: `{"status": "inactive"}`,
			want:  `{"status": "inactive"}`,
		},
		{
			name:  "mixed quoted and unquoted",
			input: `{"status": "inactive", age: {$gt: 18}}`,
			want:  `{"status": "inactive","age": {"$gt": 18}}`,
		},
		{
			name:  "dotted key name",
			input: `{user.name: "foo"}`,
			want:  `{"user.name": "foo"}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShellToExtJSON(tc.input)
			t.Logf("input=%s, got=%s, want=%s", tc.input, got, tc.want)
			if got != tc.want {
				t.Fatalf("ShellToExtJSON mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// TestShellToExtJSON_SingleQuotes 验证单引号字符串能正确转为双引号。
//
// 入参: 无
// 出参: 无
//
// 注意: 覆盖普通单引号值、内含双引号、转义单引号等场景。
func TestShellToExtJSON_SingleQuotes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple single quoted value",
			input: `{status: 'inactive'}`,
			want:  `{"status": "inactive"}`,
		},
		{
			name:  "single quote with inner double quote",
			input: `{msg: 'say "hello"'}`,
			want:  `{"msg": "say \"hello\""}`,
		},
		{
			name:  "escaped single quote inside",
			input: `{msg: 'it\'s ok'}`,
			want:  `{"msg": "it's ok"}`,
		},
		{
			name:  "double quoted value unchanged",
			input: `{"status": "inactive"}`,
			want:  `{"status": "inactive"}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShellToExtJSON(tc.input)
			t.Logf("input=%s, got=%s, want=%s", tc.input, got, tc.want)
			if got != tc.want {
				t.Fatalf("ShellToExtJSON mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// TestShellToExtJSON_ShellFunctions 验证 Shell 函数（ISODate/ObjectId/NumberLong 等）转换正确性。
//
// 入参: 无
// 出参: 无
//
// 注意: 每个 shell 函数单独覆盖，确认转换为对应 ExtJSON 表示。
func TestShellToExtJSON_ShellFunctions(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ISODate",
			input: `{ts: ISODate("2024-01-01T00:00:00Z")}`,
			want:  `{"ts": {"$date":"2024-01-01T00:00:00Z"}}`,
		},
		{
			name:  "new Date",
			input: `{ts: new Date("2024-06-15T12:00:00Z")}`,
			want:  `{"ts": {"$date":"2024-06-15T12:00:00Z"}}`,
		},
		{
			name:  "ObjectId",
			input: `{_id: ObjectId("507f1f77bcf86cd799439011")}`,
			want:  `{"_id": {"$oid":"507f1f77bcf86cd799439011"}}`,
		},
		{
			name:  "NumberLong bare",
			input: `{count: NumberLong(9999999999)}`,
			want:  `{"count": {"$numberLong":"9999999999"}}`,
		},
		{
			name:  "NumberLong quoted",
			input: `{count: NumberLong("123")}`,
			want:  `{"count": {"$numberLong":"123"}}`,
		},
		{
			name:  "NumberInt",
			input: `{age: NumberInt(42)}`,
			want:  `{"age": {"$numberInt":"42"}}`,
		},
		{
			name:  "NumberDecimal",
			input: `{price: NumberDecimal("19.99")}`,
			want:  `{"price": {"$numberDecimal":"19.99"}}`,
		},
		{
			name:  "Timestamp",
			input: `{ts: Timestamp(1234567890, 1)}`,
			want:  `{"ts": {"$timestamp":{"t":1234567890,"i":1}}}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShellToExtJSON(tc.input)
			t.Logf("input=%s, got=%s, want=%s", tc.input, got, tc.want)
			if got != tc.want {
				t.Fatalf("ShellToExtJSON mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// TestShellToExtJSON_TrailingComma 验证尾部逗号能被正确清理。
//
// 入参: 无
// 出参: 无
//
// 注意: 覆盖对象和数组两种场景。
func TestShellToExtJSON_TrailingComma(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "trailing comma in object",
			input: `{a: 1, b: 2,}`,
			want:  `{"a": 1,"b": 2}`,
		},
		{
			name:  "trailing comma in array",
			input: `{tags: ["a", "b",]}`,
			want:  `{"tags": ["a", "b"]}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShellToExtJSON(tc.input)
			t.Logf("input=%s, got=%s, want=%s", tc.input, got, tc.want)
			if got != tc.want {
				t.Fatalf("ShellToExtJSON mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// TestShellToExtJSON_StringSafe 验证字符串内的 shell 函数名不被误替换。
//
// 入参: 无
// 出参: 无
//
// 注意: 核心幂等性和安全性测试。
func TestShellToExtJSON_StringSafe(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ISODate inside string value unchanged",
			input: `{msg: "call ISODate() here"}`,
			want:  `{"msg": "call ISODate() here"}`,
		},
		{
			name:  "ObjectId inside string value unchanged",
			input: `{"note": "ObjectId is 507f1f77bcf86cd799439011"}`,
			want:  `{"note": "ObjectId is 507f1f77bcf86cd799439011"}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShellToExtJSON(tc.input)
			t.Logf("input=%s, got=%s, want=%s", tc.input, got, tc.want)
			if got != tc.want {
				t.Fatalf("ShellToExtJSON mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// TestShellToExtJSON_Idempotent 验证对已合法的 JSON/ExtJSON 输入，转换结果不变。
//
// 入参: 无
// 出参: 无
//
// 注意: 确保对标准 JSON 和 ExtJSON 输入的幂等性。
func TestShellToExtJSON_Idempotent(t *testing.T) {
	inputs := []string{
		`{"status":"inactive"}`,
		`{"createdAt":{"$lt":{"$date":"2024-01-01T00:00:00Z"}}}`,
		`{"$set":{"status":"archived"}}`,
		`{}`,
		``,
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got := ShellToExtJSON(input)
			t.Logf("input=%s, got=%s", input, got)
			if got != input {
				t.Fatalf("expected idempotent, but got different result:\n  input: %s\n  got:   %s", input, got)
			}
		})
	}
}

// TestShellToExtJSON_ComplexShell 验证完整的复杂 shell 语法场景。
//
// 入参: 无
// 出参: 无
//
// 注意: 模拟真实用户输入场景，包含多种 shell 语法混合使用。
func TestShellToExtJSON_ComplexShell(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "date filter with ISODate",
			input: `{hitCreateTime: {$lt: ISODate("2024-01-01T00:00:00Z")}}`,
			want:  `{"hitCreateTime": {"$lt": {"$date":"2024-01-01T00:00:00Z"}}}`,
		},
		{
			name:  "mixed shell and quoted keys",
			input: `{"status": "active", hitCreateTime: {$gte: ISODate("2023-01-01T00:00:00Z"), $lt: new Date("2024-01-01T00:00:00Z")}}`,
			want:  `{"status": "active","hitCreateTime": {"$gte": {"$date":"2023-01-01T00:00:00Z"},"$lt": {"$date":"2024-01-01T00:00:00Z"}}}`,
		},
		{
			name:  "update with $set and shell types",
			input: `{$set: {status: 'archived', updatedAt: ISODate("2024-06-01T00:00:00Z"), count: NumberLong(0),}}`,
			want:  `{"$set": {"status": "archived","updatedAt": {"$date":"2024-06-01T00:00:00Z"},"count": {"$numberLong":"0"}}}`,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShellToExtJSON(tc.input)
			t.Logf("input=%s\n  got= %s\n  want=%s", tc.input, got, tc.want)
			if got != tc.want {
				t.Fatalf("ShellToExtJSON mismatch:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

// ---------- ParseBsonDoc 端到端测试 ----------

// TestParseBsonDoc_ShellSyntax 验证 ParseBsonDoc 能正确解析 shell 语法输入为 bson.D。
//
// 入参: 无
// 出参: 无
//
// 注意: 端到端测试，从 shell 字符串到 bson.D，验证类型和值都正确。
func TestParseBsonDoc_ShellSyntax(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		// 用例说明: 验证空字符串和 {} 返回空 bson.D。
		for _, input := range []string{"", "{}"} {
			got, err := ParseBsonDoc(input)
			if err != nil {
				t.Fatalf("parse %q failed: %v", input, err)
			}
			t.Logf("input=%q, result len=%d", input, len(got))
			if len(got) != 0 {
				t.Fatalf("expected empty bson.D for input %q, got: %#v", input, got)
			}
		}
	})

	t.Run("shell ISODate filter", func(t *testing.T) {
		// 用例说明: 验证 ISODate shell 语法能正确解析为 primitive.DateTime。
		input := `{hitCreateTime: {$lt: ISODate("2024-01-01T00:00:00Z")}}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		hitCreateTime := got.Map()["hitCreateTime"]
		t.Logf("hitCreateTime type=%T", hitCreateTime)
		ltDoc, ok := hitCreateTime.(bson.D)
		if !ok {
			t.Fatalf("expected hitCreateTime to be bson.D, got: %T", hitCreateTime)
		}

		ltValue := ltDoc.Map()["$lt"]
		t.Logf("$lt type=%T, value=%v", ltValue, ltValue)
		if _, ok := ltValue.(primitive.DateTime); !ok {
			t.Fatalf("expected $lt to be primitive.DateTime, got: %T", ltValue)
		}
		t.Logf("assert ok: ISODate shell syntax parsed as primitive.DateTime")
	})

	t.Run("shell new Date filter", func(t *testing.T) {
		// 用例说明: 验证 new Date shell 语法也能解析为 primitive.DateTime。
		input := `{ts: new Date("2024-06-15T12:00:00Z")}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		ts := got.Map()["ts"]
		t.Logf("ts type=%T", ts)
		if _, ok := ts.(primitive.DateTime); !ok {
			t.Fatalf("expected ts to be primitive.DateTime, got: %T", ts)
		}
		t.Logf("assert ok: new Date shell syntax parsed as primitive.DateTime")
	})

	t.Run("shell ObjectId filter", func(t *testing.T) {
		// 用例说明: 验证 ObjectId shell 语法能解析为 primitive.ObjectID。
		input := `{_id: ObjectId("507f1f77bcf86cd799439011")}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		id := got.Map()["_id"]
		t.Logf("_id type=%T, value=%v", id, id)
		if _, ok := id.(primitive.ObjectID); !ok {
			t.Fatalf("expected _id to be primitive.ObjectID, got: %T", id)
		}
		t.Logf("assert ok: ObjectId shell syntax parsed as primitive.ObjectID")
	})

	t.Run("shell single quote value", func(t *testing.T) {
		// 用例说明: 验证单引号字符串值能正确解析为 string。
		input := `{status: 'inactive'}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		status := got.Map()["status"]
		t.Logf("status type=%T, value=%v", status, status)
		if status != "inactive" {
			t.Fatalf("expected status='inactive', got: %v", status)
		}
		t.Logf("assert ok: single quote value parsed correctly")
	})

	t.Run("shell NumberLong", func(t *testing.T) {
		// 用例说明: 验证 NumberLong shell 语法能解析为 int64。
		input := `{count: NumberLong(9999999999)}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		count := got.Map()["count"]
		t.Logf("count type=%T, value=%v", count, count)
		if v, ok := count.(int64); !ok || v != 9999999999 {
			t.Fatalf("expected count=int64(9999999999), got: %T(%v)", count, count)
		}
		t.Logf("assert ok: NumberLong shell syntax parsed as int64")
	})

	t.Run("shell NumberInt", func(t *testing.T) {
		// 用例说明: 验证 NumberInt shell 语法能解析为 int32。
		input := `{age: NumberInt(42)}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		age := got.Map()["age"]
		t.Logf("age type=%T, value=%v", age, age)
		if v, ok := age.(int32); !ok || v != 42 {
			t.Fatalf("expected age=int32(42), got: %T(%v)", age, age)
		}
		t.Logf("assert ok: NumberInt shell syntax parsed as int32")
	})

	t.Run("shell NumberDecimal", func(t *testing.T) {
		// 用例说明: 验证 NumberDecimal shell 语法能解析为 primitive.Decimal128。
		input := `{price: NumberDecimal("19.99")}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		price := got.Map()["price"]
		t.Logf("price type=%T, value=%v", price, price)
		if _, ok := price.(primitive.Decimal128); !ok {
			t.Fatalf("expected price to be primitive.Decimal128, got: %T", price)
		}
		t.Logf("assert ok: NumberDecimal shell syntax parsed as Decimal128")
	})

	t.Run("trailing comma in filter", func(t *testing.T) {
		// 用例说明: 验证含尾部逗号的输入能正确解析。
		input := `{status: "inactive", age: 30,}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		t.Logf("parsed fields=%d", len(got))
		if len(got) != 2 {
			t.Fatalf("expected 2 fields, got: %d (%#v)", len(got), got)
		}
		if got.Map()["status"] != "inactive" {
			t.Fatalf("expected status=inactive, got: %v", got.Map()["status"])
		}
		t.Logf("assert ok: trailing comma handled correctly")
	})

	t.Run("standard JSON still works", func(t *testing.T) {
		// 用例说明: 验证标准 JSON 格式不受 shell 预处理影响。
		input := `{"status":"inactive","count":42}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		if got.Map()["status"] != "inactive" {
			t.Fatalf("expected status=inactive, got: %v", got.Map()["status"])
		}
		t.Logf("assert ok: standard JSON still works")
	})

	t.Run("standard ExtJSON still works", func(t *testing.T) {
		// 用例说明: 验证标准 ExtJSON 格式不受 shell 预处理影响。
		input := `{"createdAt":{"$lt":{"$date":"2024-01-01T00:00:00Z"}}}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		createdAt := got.Map()["createdAt"]
		ltDoc, ok := createdAt.(bson.D)
		if !ok {
			t.Fatalf("expected bson.D, got: %T", createdAt)
		}
		ltValue := ltDoc.Map()["$lt"]
		if _, ok := ltValue.(primitive.DateTime); !ok {
			t.Fatalf("expected primitive.DateTime, got: %T", ltValue)
		}
		t.Logf("assert ok: ExtJSON still works")
	})

	t.Run("complex mixed shell syntax", func(t *testing.T) {
		// 用例说明: 验证复杂的混合 shell 语法场景能正确解析。
		input := `{status: 'active', hitCreateTime: {$gte: ISODate("2023-01-01T00:00:00Z"), $lt: new Date("2024-01-01T00:00:00Z")},}`
		t.Logf("input=%s", input)

		got, err := ParseBsonDoc(input)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}

		if got.Map()["status"] != "active" {
			t.Fatalf("expected status=active, got: %v", got.Map()["status"])
		}

		hitCreateTime := got.Map()["hitCreateTime"]
		timeDoc, ok := hitCreateTime.(bson.D)
		if !ok {
			t.Fatalf("expected hitCreateTime to be bson.D, got: %T", hitCreateTime)
		}

		if _, ok := timeDoc.Map()["$gte"].(primitive.DateTime); !ok {
			t.Fatalf("expected $gte to be primitive.DateTime, got: %T", timeDoc.Map()["$gte"])
		}
		if _, ok := timeDoc.Map()["$lt"].(primitive.DateTime); !ok {
			t.Fatalf("expected $lt to be primitive.DateTime, got: %T", timeDoc.Map()["$lt"])
		}
		t.Logf("assert ok: complex mixed shell syntax parsed correctly")
	})
}
