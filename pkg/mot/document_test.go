package mot

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestParseDocument(t *testing.T) {
	// 测试 ParseDocument 支持 SDK 调用方常用的字符串、BSON 和 map 输入。
	tests := []struct {
		name    string
		input   any
		wantLen int
		wantErr bool
	}{
		{name: "nil", input: nil, wantLen: 0},
		{name: "shell string", input: `{status: 'inactive'}`, wantLen: 1},
		{name: "bson d", input: bson.D{{Key: "status", Value: "active"}}, wantLen: 1},
		{name: "bson m", input: bson.M{"status": "active", "age": 10}, wantLen: 2},
		{name: "map", input: map[string]any{"status": "active"}, wantLen: 1},
		{name: "unsupported", input: 42, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDocument(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDocument failed: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len(ParseDocument()) = %d, want %d", len(got), tc.wantLen)
			}
		})
	}
}

func TestParseDocumentMapOrder(t *testing.T) {
	// 测试 map 输入会按 key 排序，便于默认单测稳定断言。
	got, err := ParseDocument(map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatalf("ParseDocument failed: %v", err)
	}
	if got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("unexpected order: %#v", got)
	}
}
