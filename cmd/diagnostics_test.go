package cmd

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

func TestParseIndexChecksRejectsUnknownBeforeConnection(t *testing.T) {
	// 场景：未知 check 必须在建立 MongoDB 连接前失败。
	if _, err := parseIndexChecks("unused,drop-index"); err == nil {
		t.Fatal("parseIndexChecks() error = nil, want validation error")
	}
	checks, err := parseIndexChecks("unused,space")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || checks[0] != mot.IndexCheckUnused || checks[1] != mot.IndexCheckSpace {
		t.Fatalf("checks = %#v", checks)
	}
}

func TestReadCapacitySnapshotIgnoresCompatibleUnknownFields(t *testing.T) {
	// 场景：快照 schema 允许兼容新增字段，旧 CLI 读取时不能失败。
	path := t.TempDir() + "/snapshot.json"
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"clusterIdentity":{"topologyType":"repl","digest":"x"},"futureField":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := readCapacitySnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.ClusterIdentity.Digest != "x" {
		t.Fatalf("snapshot = %#v", result)
	}
}

func TestDiagnosticCLIValidationRunsBeforeConnection(t *testing.T) {
	// 场景：format、timeout、severity 和并发非法时，CLI 可在建立 MongoDB 连接前拒绝。
	if err := validateDiagnosticBase(diagnosticBaseConfig{Format: "yaml"}); err == nil {
		t.Fatal("invalid format was accepted")
	}
	if err := validateDiagnosticBase(diagnosticBaseConfig{Format: "json", Timeout: -1}); err == nil {
		t.Fatal("negative timeout was accepted")
	}
	if err := validateDoctorCLI(diagnosticBaseConfig{Format: "table"}, mot.Severity("fatal"), 1); err == nil {
		t.Fatal("invalid severity was accepted")
	}
}

func TestDiagnosticCLIErrorDoesNotExposeServerDetail(t *testing.T) {
	// 场景：部分结果退出语义保留 ErrPartialResult，但 stderr 错误不得包含原始 command/业务值。
	command := &cobra.Command{}
	result := &mot.DoctorResult{}
	err := printDiagnosticAndError(command, result, "json", &mot.DiagnosticPartialError{Op: "doctor", Result: result, Err: errors.New("command={find:'secret'} host=internal")})
	if !errors.Is(err, mot.ErrPartialResult) {
		t.Fatalf("error = %v, want ErrPartialResult", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "internal") {
		t.Fatalf("error leaked server detail: %v", err)
	}
}

func TestDiagnosticCommandFlagDefaultsAndIndexMutualExclusion(t *testing.T) {
	// 场景：五个命令的关键成本/数量/格式默认值稳定，index database 选择严格互斥。
	initDiagnostics()
	tests := []struct {
		command *cobra.Command
		flags   map[string]string
	}{
		{doctorCmd, map[string]string{"format": "table", "timeout": "30s", "concurrency": "10", "oplog-window": "false"}},
		{opsCmd, map[string]string{"format": "table", "min-duration": "2s", "limit": "100", "all-users": "true"}},
		{hotspotCmd, map[string]string{"duration": "10s", "top": "10", "concurrency": "10"}},
		{indexAuditCmd, map[string]string{"max-collections": "500", "concurrency": "10", "all-databases": "false"}},
		{capacityCmd, map[string]string{"max-collections": "500", "concurrency": "10", "free-storage": "false"}},
	}
	for _, test := range tests {
		for name, want := range test.flags {
			flag := test.command.Flags().Lookup(name)
			if flag == nil || flag.DefValue != want {
				t.Fatalf("%s --%s default = %v, want %s", test.command.Name(), name, flag, want)
			}
		}
	}
	valid := []struct {
		all bool
		dbs string
	}{{true, ""}, {false, "app"}}
	for _, input := range valid {
		if err := validateIndexAuditSelection(input.all, input.dbs); err != nil {
			t.Fatalf("valid selection %#v: %v", input, err)
		}
	}
	for _, input := range []struct {
		all bool
		dbs string
	}{{false, ""}, {true, "app"}} {
		if err := validateIndexAuditSelection(input.all, input.dbs); err == nil {
			t.Fatalf("invalid selection accepted: %#v", input)
		}
	}
	if got := []string{doctorCmd.Name(), opsCmd.Name(), hotspotCmd.Name(), indexAuditCmd.Name(), capacityCmd.Name()}; !reflect.DeepEqual(got, []string{"doctor", "ops", "hotspot", "index-audit", "capacity"}) {
		t.Fatalf("commands = %v", got)
	}
}

func TestSafeDiagnosticCommandErrorHidesCompleteFailure(t *testing.T) {
	// 场景：无部分结果的完整 server failure 也不能进入 Cobra/root 输出。
	err := safeDiagnosticCommandError(errors.New("host=internal command={find:'secret'}"))
	if strings.Contains(err.Error(), "internal") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("safe error leaked: %v", err)
	}
}

func TestDiagnosticClientAndTopLevelCleanupUseSuppressedErrors(t *testing.T) {
	// 场景：五个命令共享的 client 初始化入口和顶层 cleanup 文案都不能包含 URI、host 或底层错误占位符。
	_, err := diagnosticClient(context.Background(), &config.BaseCfg{})
	if err == nil || err.Error() != safeDiagnosticConnectionError().Error() {
		t.Fatalf("diagnosticClient error = %v", err)
	}
	for _, forbidden := range []string{"mongodb://", "password", "host=", "%v"} {
		if strings.Contains(err.Error(), forbidden) || strings.Contains(sdkClientCloseWarning, forbidden) {
			t.Fatalf("suppressed boundary contains %q: err=%q close=%q", forbidden, err, sdkClientCloseWarning)
		}
	}
}
