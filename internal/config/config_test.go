package config

import (
	"strings"
	"testing"
)

func TestBasePreCheckUsesExplicitTarget(t *testing.T) {
	// 场景：显式传入 target 时，完整 host:port 应覆盖拆分的 host/port 默认值。
	t.Setenv(MongoUser, "")
	t.Setenv(MongoPass, "")
	cfg := BaseCfg{
		Target:        "mongo.example.com:27018",
		TargetChanged: true,
		Host:          "127.0.0.1",
		Port:          27017,
		AuthSource:    "admin",
	}

	if err := BasePreCheck(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "mongo.example.com" || cfg.Port != 27018 {
		t.Fatalf("resolved endpoint = %s:%d, want mongo.example.com:27018", cfg.Host, cfg.Port)
	}
	if cfg.BuildUri != "mongodb://mongo.example.com:27018/admin" {
		t.Fatalf("BuildUri = %q", cfg.BuildUri)
	}
}

func TestBasePreCheckTargetValidation(t *testing.T) {
	// 场景：target 必须是完整且端口合法的 host:port，同时支持带方括号的 IPv6。
	t.Setenv(MongoUser, "")
	t.Setenv(MongoPass, "")
	tests := []struct {
		name    string
		target  string
		wantErr string
	}{
		{name: "missing port", target: "mongo.example.com", wantErr: "host:port"},
		{name: "empty host", target: ":27017", wantErr: "host"},
		{name: "invalid port", target: "mongo.example.com:not-a-port", wantErr: "port"},
		{name: "port out of range", target: "mongo.example.com:65536", wantErr: "port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := BaseCfg{Target: test.target, TargetChanged: true, Host: "127.0.0.1", Port: 27017, AuthSource: "admin"}
			err := BasePreCheck(&cfg)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("BasePreCheck() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}

	secretTarget := BaseCfg{Target: "mongodb://user:secret@mongo.example.com:27017", TargetChanged: true, Host: "127.0.0.1", Port: 27017, AuthSource: "admin"}
	err := BasePreCheck(&secretTarget)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid target error leaked input: %v", err)
	}

	ipv6 := BaseCfg{Target: "[2001:db8::1]:27018", TargetChanged: true, Host: "127.0.0.1", Port: 27017, AuthSource: "admin"}
	if err := BasePreCheck(&ipv6); err != nil {
		t.Fatal(err)
	}
	if ipv6.Host != "2001:db8::1" || ipv6.Port != 27018 {
		t.Fatalf("resolved IPv6 endpoint = %s:%d", ipv6.Host, ipv6.Port)
	}
	if ipv6.BuildUri != "mongodb://[2001:db8::1]:27018/admin" {
		t.Fatalf("IPv6 BuildUri = %q", ipv6.BuildUri)
	}
}

func TestBasePreCheckURIKeepsHighestPrecedence(t *testing.T) {
	// 场景：uri 继续拥有最高优先级，即使 target 非法也不能触发 target 解析。
	t.Setenv(MongoUser, "")
	t.Setenv(MongoPass, "")
	const uri = "mongodb://user:pass@mongo.example.com:27019/admin"
	cfg := BaseCfg{
		Target:        "invalid-target",
		TargetChanged: true,
		MongoUri:      uri,
	}

	if err := BasePreCheck(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BuildUri != uri {
		t.Fatalf("BuildUri = %q, want %q", cfg.BuildUri, uri)
	}
}
