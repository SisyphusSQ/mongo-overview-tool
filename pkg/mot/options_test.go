package mot

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestBuildURI(t *testing.T) {
	// 测试 BuildURI 在默认值、认证拼接、URI 优先级和非法输入场景下的行为。
	tests := []struct {
		name    string
		opts    Options
		want    string
		wantErr bool
	}{
		{
			name: "default options",
			opts: Options{},
			want: "mongodb://127.0.0.1:27017/admin",
		},
		{
			name: "username password",
			opts: Options{
				Host:       "mongo.local",
				Port:       27018,
				Username:   "root",
				Password:   "secret",
				AuthSource: "admin",
			},
			want: "mongodb://root:secret@mongo.local:27018/admin",
		},
		{
			name: "uri priority",
			opts: Options{
				URI:      "mongodb://user:pass@db.example:27017/admin",
				Host:     "ignored",
				Port:     1,
				Password: "ignored",
			},
			want: "mongodb://user:pass@db.example:27017/admin",
		},
		{
			name:    "invalid scheme",
			opts:    Options{URI: "http://db.example:27017"},
			wantErr: true,
		},
		{
			name:    "missing uri host",
			opts:    Options{URI: "mongodb://"},
			wantErr: true,
		},
		{
			name:    "half auth",
			opts:    Options{Username: "root"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildURI(tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildURI failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("BuildURI() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactURI(t *testing.T) {
	// 测试 RedactURI 会脱敏密码且保留其它连接信息。
	got := RedactURI("mongodb://root:secret@127.0.0.1:27017/admin")
	if strings.Contains(got, "secret") {
		t.Fatalf("redacted uri still contains password: %s", got)
	}
	if !strings.Contains(got, "root:***@127.0.0.1:27017") {
		t.Fatalf("unexpected redacted uri: %s", got)
	}
}

func TestDeriveConnectionURI(t *testing.T) {
	// 测试派生节点 URI 时保留认证和 TLS 参数，并重算拓扑参数。（风险复现用例）
	tests := []struct {
		name       string
		baseURI    string
		address    string
		target     derivedConnectionOptions
		wantScheme string
		wantPath   string
		wantPass   string
		wantQuery  map[string]string
		absent     []string
		wantErr    bool
	}{
		{
			name:       "direct node preserves auth and transport options",
			baseURI:    "mongodb://root:p%40ss@router:27017/admin?tls=true&authMechanism=SCRAM-SHA-256&replicaSet=routerRS&directConnection=true",
			address:    "node1:27017",
			target:     derivedConnectionOptions{Database: "app", Direct: boolPointer(true)},
			wantScheme: "mongodb",
			wantPath:   "/app",
			wantPass:   "p@ss",
			wantQuery: map[string]string{
				"authMechanism": "SCRAM-SHA-256",
				"authSource":    "admin",
				"tls":           "true",
			},
			absent: []string{"directConnection", "replicaSet"},
		},
		{
			name:       "shard seed sets host replica set instead of shard id",
			baseURI:    "mongodb://root:secret@router:27017/admin?tls=true&replicaSet=routerRS",
			address:    "shard1-a:27017,shard1-b:27017",
			target:     derivedConnectionOptions{ReplicaSet: "shard01", Direct: boolPointer(false)},
			wantScheme: "mongodb",
			wantPath:   "/admin",
			wantPass:   "secret",
			wantQuery: map[string]string{
				"replicaSet": "shard01",
				"tls":        "true",
			},
		},
		{
			name:       "srv target keeps implicit tls",
			baseURI:    "mongodb+srv://root:secret@cluster.example/admin?authSource=admin",
			address:    "node1:27017",
			target:     derivedConnectionOptions{Direct: boolPointer(true)},
			wantScheme: "mongodb",
			wantPath:   "/admin",
			wantPass:   "secret",
			wantQuery: map[string]string{
				"authSource": "admin",
				"tls":        "true",
			},
		},
		{
			name:       "missing base database keeps admin auth source",
			baseURI:    "mongodb://root:secret@router:27017",
			address:    "node1:27017",
			target:     derivedConnectionOptions{Database: "app", Direct: boolPointer(true)},
			wantScheme: "mongodb",
			wantPath:   "/app",
			wantPass:   "secret",
			wantQuery: map[string]string{
				"authSource": "admin",
			},
		},
		{
			name:       "injected client auth source is used as fallback",
			baseURI:    "mongodb://root:secret@router:27017",
			address:    "node1:27017",
			target:     derivedConnectionOptions{Database: "app", FallbackAuthDB: "users", Direct: boolPointer(true)},
			wantScheme: "mongodb",
			wantPath:   "/app",
			wantPass:   "secret",
			wantQuery: map[string]string{
				"authSource": "users",
			},
		},
		{
			name:       "overview member keeps injected auth source",
			baseURI:    "mongodb://root:secret@router:27017",
			address:    "node1:27017",
			target:     derivedConnectionOptions{FallbackAuthDB: "users", Direct: boolPointer(true)},
			wantScheme: "mongodb",
			wantPath:   "",
			wantPass:   "secret",
			wantQuery: map[string]string{
				"authSource": "users",
			},
		},
		{
			name:       "query option names are handled case insensitively",
			baseURI:    "mongodb://root:secret@router:27017?AUTHSOURCE=users&TLS=true&REPLICASET=routerRS&DIRECTCONNECTION=true",
			address:    "node1:27017",
			target:     derivedConnectionOptions{Database: "app", Direct: boolPointer(true)},
			wantScheme: "mongodb",
			wantPath:   "/app",
			wantPass:   "secret",
			wantQuery: map[string]string{
				"AUTHSOURCE": "users",
				"TLS":        "true",
			},
			absent: []string{"DIRECTCONNECTION", "REPLICASET", "authSource", "replicaSet"},
		},
		{
			name:    "missing base uri",
			address: "node1:27017",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveConnectionURI(tc.baseURI, tc.address, tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("deriveConnectionURI failed: %v", err)
			}
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("url.Parse failed: %v", err)
			}
			if parsed.Scheme != tc.wantScheme || parsed.Host != tc.address || parsed.Path != tc.wantPath {
				t.Fatalf("unexpected derived uri: %s", got)
			}
			if tc.wantPass != "" {
				password, ok := parsed.User.Password()
				if !ok || password != tc.wantPass {
					t.Fatalf("password was not preserved: %s", got)
				}
			}
			query := parsed.Query()
			for key, want := range tc.wantQuery {
				if query.Get(key) != want {
					t.Fatalf("query[%s] = %q, want %q in %s", key, query.Get(key), want, got)
				}
			}
			for _, key := range tc.absent {
				if query.Has(key) {
					t.Fatalf("query[%s] should be absent in %s", key, got)
				}
			}
		})
	}
}

func TestParseShardHost(t *testing.T) {
	// 测试派生连接使用 host 中的 replica set 名，而不是可自定义的 shard id。（风险复现用例）
	replicaSet, addresses, err := parseShardHost("rs-orders/node-a:27017,node-b:27017")
	if err != nil {
		t.Fatalf("parseShardHost failed: %v", err)
	}
	if replicaSet != "rs-orders" || addresses != "node-a:27017,node-b:27017" {
		t.Fatalf("unexpected shard host parse: replicaSet=%q addresses=%q", replicaSet, addresses)
	}
	if _, _, err := parseShardHost("custom-shard-name"); !errors.Is(err, ErrUnsupportedTopology) {
		t.Fatalf("expected ErrUnsupportedTopology, got %v", err)
	}
}
