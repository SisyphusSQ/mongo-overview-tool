package cmd

import (
	"testing"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
)

// TestValidateBulkConfig 校验批量命令参数边界。
//
// 入参: 无（测试函数通过构造不同配置覆盖场景）
// 出参: 无（断言失败时由 testing 框架报错）
//
// 注意: 覆盖 batch-size（含上限）、pause-ms 与 update 必填校验。
func TestValidateBulkConfig(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *config.BulkConfig
		requireUpdate bool
		wantErr       bool
	}{
		{
			name: "valid delete config",
			cfg: &config.BulkConfig{
				BatchSize: 100,
				PauseMS:   0,
			},
			requireUpdate: false,
			wantErr:       false,
		},
		{
			name: "invalid batch size zero",
			cfg: &config.BulkConfig{
				BatchSize: 0,
				PauseMS:   100,
			},
			requireUpdate: false,
			wantErr:       true,
		},
		{
			name: "batch size exceeds upper limit",
			cfg: &config.BulkConfig{
				BatchSize: maxBatchSize + 1,
				PauseMS:   100,
			},
			requireUpdate: false,
			wantErr:       true,
		},
		{
			name: "batch size at upper limit",
			cfg: &config.BulkConfig{
				BatchSize: maxBatchSize,
				PauseMS:   100,
			},
			requireUpdate: false,
			wantErr:       false,
		},
		{
			name: "invalid pause",
			cfg: &config.BulkConfig{
				BatchSize: 100,
				PauseMS:   -1,
			},
			requireUpdate: false,
			wantErr:       true,
		},
		{
			name: "require update but empty",
			cfg: &config.BulkConfig{
				BatchSize: 100,
				PauseMS:   100,
				Update:    "",
			},
			requireUpdate: true,
			wantErr:       true,
		},
		{
			name: "require update and provided",
			cfg: &config.BulkConfig{
				BatchSize: 100,
				PauseMS:   100,
				Update:    `{"$set":{"status":"archived"}}`,
			},
			requireUpdate: true,
			wantErr:       false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateBulkConfig(tc.cfg, tc.requireUpdate)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil error but got: %v", err)
			}
		})
	}
}
