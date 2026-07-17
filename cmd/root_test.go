package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/internal/config"
)

func TestRegisterBaseFlagsTargetAndLegacyHostPort(t *testing.T) {
	// 场景：-t/--target 接收 host:port，--host 与 -P/--port 继续支持拆分传参。
	tests := []struct {
		name     string
		args     []string
		wantHost string
		wantPort int
	}{
		{name: "target", args: []string{"-t", "mongo.example.com:27018"}, wantHost: "mongo.example.com", wantPort: 27018},
		{name: "target overrides split flags", args: []string{"--host", "ignored.example.com", "-P", "27019", "--target", "mongo.example.com:27018"}, wantHost: "mongo.example.com", wantPort: 27018},
		{name: "legacy split flags", args: []string{"--host", "mongo.example.com", "-P", "27019"}, wantHost: "mongo.example.com", wantPort: 27019},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.BaseCfg{}
			command := &cobra.Command{
				Use: "test",
				RunE: func(cmd *cobra.Command, args []string) error {
					return config.BasePreCheck(&cfg)
				},
			}
			registerBaseFlags(command, &cfg)
			command.SetArgs(test.args)

			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if cfg.Host != test.wantHost || cfg.Port != test.wantPort {
				t.Fatalf("resolved endpoint = %s:%d, want %s:%d", cfg.Host, cfg.Port, test.wantHost, test.wantPort)
			}
		})
	}
}

func TestRegisterBaseFlagsHelpUsesTargetSyntax(t *testing.T) {
	// 场景：帮助信息展示新的 target 默认值，并且 -t 不再是 --host 的短参数。
	cfg := config.BaseCfg{}
	command := &cobra.Command{Use: "test", Run: func(cmd *cobra.Command, args []string) {}}
	registerBaseFlags(command, &cfg)

	target := command.Flags().Lookup("target")
	host := command.Flags().Lookup("host")
	port := command.Flags().Lookup("port")
	if target == nil || target.Shorthand != "t" || target.DefValue != "127.0.0.1:27017" {
		t.Fatalf("target flag = %#v", target)
	}
	if host == nil || host.Shorthand != "" || host.DefValue != "127.0.0.1" {
		t.Fatalf("host flag = %#v", host)
	}
	if port == nil || port.Shorthand != "P" || port.DefValue != "27017" {
		t.Fatalf("port flag = %#v", port)
	}

	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Help(); err != nil {
		t.Fatal(err)
	}
	help := output.String()
	for _, want := range []string{"-t, --target string", "--host string", "-P, --port int", "127.0.0.1:27017"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help does not contain %q:\n%s", want, help)
		}
	}
}
