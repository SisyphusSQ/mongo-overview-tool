package mongo

import (
	"strings"
	"testing"
)

func TestIsMultiHosts(t *testing.T) {
	// 测试 MongoDB URI 的单节点、多节点和 SRV 拓扑判断。
	tests := []struct {
		name    string
		uri     string
		want    bool
		wantErr bool
	}{
		{name: "single", uri: "mongodb://node1:27017/admin"},
		{name: "multiple", uri: "mongodb://node1:27017,node2:27017/admin", want: true},
		{name: "srv", uri: "mongodb+srv://cluster.example/admin", want: true},
		{name: "missing host", uri: "mongodb://", wantErr: true},
		{name: "invalid scheme", uri: "http://node1:27017", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := isMultiHosts(tc.uri)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("isMultiHosts failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("isMultiHosts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRedactURI(t *testing.T) {
	// 测试连接错误中的 MongoDB URI 不泄露密码。
	got := redactURI("mongodb://root:secret@node1:27017/admin", "***")
	if strings.Contains(got, "secret") || !strings.Contains(got, "root:***@node1") {
		t.Fatalf("unexpected redacted URI: %s", got)
	}
}
