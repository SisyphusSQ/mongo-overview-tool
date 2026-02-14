package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/service"
	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mongo"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
	"github.com/SisyphusSQ/mongo-overview-tool/vars"
)

var bulkDeleteCfg config.BulkConfig
var bulkUpdateCfg config.BulkConfig

var bulkDeleteCmd = &cobra.Command{
	Use:   "bulk-delete",
	Short: "Batch delete documents from a collection",
	Long:  `Batch delete documents from a MongoDB collection with controlled batch size and pause intervals to minimize impact on production traffic.`,
	Example: fmt.Sprintf(`  # JSON format
  %s bulk-delete -t 10.0.0.1 -P 27017 -d mydb -c users -f '{"status":"inactive"}' -b 500 --pause-ms 200
  # Shell syntax (unquoted keys, ISODate, ObjectId, etc.)
  %s bulk-delete --uri "mongodb://user:pass@host:27017" -d mydb -c users -f '{hitCreateTime: {$lt: ISODate("2024-01-01T00:00:00Z")}}' --dry-run`,
		vars.AppName, vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBulkDelete()
	},
}

var bulkUpdateCmd = &cobra.Command{
	Use:   "bulk-update",
	Short: "Batch update documents in a collection",
	Long:  `Batch update documents in a MongoDB collection with controlled batch size and pause intervals to minimize impact on production traffic.`,
	Example: fmt.Sprintf(`  # JSON format
  %s bulk-update -t 10.0.0.1 -P 27017 -d mydb -c orders -f '{"status":"pending"}' --update '{"$set":{"status":"archived"}}' -b 1000 --pause-ms 100
  # Shell syntax (unquoted keys, ISODate, single quotes, etc.)
  %s bulk-update --uri "mongodb://user:pass@host:27017" -d mydb -c orders -f '{status: "pending"}' --update '{$set: {status: "archived"}}' --dry-run -o bulk.log`,
		vars.AppName, vars.AppName),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBulkUpdate()
	},
}

// runBulkDelete 执行批量删除命令的核心流程。
//
// 入参: 无（使用模块级 bulkDeleteCfg）
// 出参:
// - error: 执行失败时非 nil
//
// 注意: 包含参数校验、连接建立、服务调用和资源清理的完整流程。
func runBulkDelete() error {
	start := time.Now()
	if err := config.BasePreCheck(&bulkDeleteCfg.BaseCfg); err != nil {
		return err
	}
	if err := validateBulkConfig(&bulkDeleteCfg, false); err != nil {
		return err
	}

	conn, err := mongo.NewMongoConn(bulkDeleteCfg.BuildUri)
	if err != nil {
		l.Logger.Errorf("NewMongoConn failed, err: %v", err)
		return err
	}

	srv, err := service.NewBulkSrv(context.Background(), &bulkDeleteCfg, conn)
	if err != nil {
		l.Logger.Errorf("NewBulkSrv failed, err: %v", err)
		return err
	}
	defer srv.Close()

	if err := srv.Delete(); err != nil {
		l.Logger.Errorf("bulk-delete failed, err: %v", err)
		return err
	}

	utils.PrintCost(start)
	return nil
}

// runBulkUpdate 执行批量更新命令的核心流程。
//
// 入参: 无（使用模块级 bulkUpdateCfg）
// 出参:
// - error: 执行失败时非 nil
//
// 注意: 会额外校验 --update 参数不能为空。
func runBulkUpdate() error {
	start := time.Now()
	if err := config.BasePreCheck(&bulkUpdateCfg.BaseCfg); err != nil {
		return err
	}
	if err := validateBulkConfig(&bulkUpdateCfg, true); err != nil {
		return err
	}

	conn, err := mongo.NewMongoConn(bulkUpdateCfg.BuildUri)
	if err != nil {
		l.Logger.Errorf("NewMongoConn failed, err: %v", err)
		return err
	}

	srv, err := service.NewBulkSrv(context.Background(), &bulkUpdateCfg, conn)
	if err != nil {
		l.Logger.Errorf("NewBulkSrv failed, err: %v", err)
		return err
	}
	defer srv.Close()

	if err := srv.Update(); err != nil {
		l.Logger.Errorf("bulk-update failed, err: %v", err)
		return err
	}

	utils.PrintCost(start)
	return nil
}

// maxBatchSize 批量操作每批文档数的上限，防止内存溢出或 $in 查询超出 BSON 16MB 限制。
const maxBatchSize = 50000

// validateBulkConfig 校验批量命令配置项是否合法。
//
// 入参:
// - cfg: 批量命令配置
// - requireUpdate: 是否要求校验 update 字段（bulk-update 为 true）
//
// 出参:
// - error: 配置非法时返回具体错误
//
// 注意: 该校验仅做参数边界约束，不涉及数据库连接可达性。BatchSize 上限为 50000。
func validateBulkConfig(cfg *config.BulkConfig, requireUpdate bool) error {
	if cfg.BatchSize <= 0 {
		return fmt.Errorf("--batch-size must be greater than 0, got %d", cfg.BatchSize)
	}
	if cfg.BatchSize > maxBatchSize {
		return fmt.Errorf("--batch-size must be less than or equal to %d, got %d", maxBatchSize, cfg.BatchSize)
	}
	if cfg.PauseMS < 0 {
		return fmt.Errorf("--pause-ms must be greater than or equal to 0, got %d", cfg.PauseMS)
	}
	if requireUpdate && cfg.Update == "" {
		return fmt.Errorf("--update is required for bulk-update command")
	}
	return nil
}

// registerBulkFlags 为批量操作命令注册通用的命令行参数。
//
// 入参:
// - cmd: cobra 命令实例
// - cfg: 批量操作配置指针
//
// 出参: 无
//
// 注意: 包含基础连接参数和批量操作专用参数；database 和 collection 标记为必填。
func registerBulkFlags(cmd *cobra.Command, cfg *config.BulkConfig) {
	registerBaseFlags(cmd, &cfg.BaseCfg)

	cmd.Flags().StringVarP(&cfg.Database, "database", "d", "", "Target database (required)")
	cmd.Flags().StringVarP(&cfg.Collection, "collection", "c", "", "Target collection (required)")
	cmd.Flags().StringVarP(&cfg.Filter, "filter", "f", "{}", `Query filter in JSON or Shell format (e.g. '{"status":"inactive"}' or '{status: "inactive", ts: {$lt: ISODate("2024-01-01T00:00:00Z")}}')`)
	cmd.Flags().IntVarP(&cfg.BatchSize, "batch-size", "b", 1000, "Number of documents per batch")
	cmd.Flags().IntVar(&cfg.PauseMS, "pause-ms", 100, "Pause duration in milliseconds between batches")
	cmd.Flags().BoolVar(&cfg.DryRun, "dry-run", false, "Only count matched documents, do not execute")
	cmd.Flags().StringVarP(&cfg.Output, "output", "o", "", "Optional log output file path")

	_ = cmd.MarkFlagRequired("database")
	_ = cmd.MarkFlagRequired("collection")
}

// initBulkDelete 初始化并注册 bulk-delete 命令。
//
// 入参: 无
// 出参: 无
//
// 注意: 在 initAll() 中调用。
func initBulkDelete() {
	registerBulkFlags(bulkDeleteCmd, &bulkDeleteCfg)
	rootCmd.AddCommand(bulkDeleteCmd)
}

// initBulkUpdate 初始化并注册 bulk-update 命令。
//
// 入参: 无
// 出参: 无
//
// 注意: 在 initAll() 中调用，额外注册 --update 参数。
func initBulkUpdate() {
	registerBulkFlags(bulkUpdateCmd, &bulkUpdateCfg)
	bulkUpdateCmd.Flags().StringVar(&bulkUpdateCfg.Update, "update", "", `Update operation in JSON or Shell format (e.g. '{"$set":{"status":"archived"}}' or '{$set: {status: "archived"}}') (required)`)
	rootCmd.AddCommand(bulkUpdateCmd)
}
