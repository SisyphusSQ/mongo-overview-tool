package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/SisyphusSQ/mongo-overview-tool/internal/clioutput"
	"github.com/SisyphusSQ/mongo-overview-tool/internal/config"
	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

type diagnosticBaseConfig struct {
	config.BaseCfg
	Format  string
	Timeout time.Duration
}

const maxCapacitySnapshotBytes = 32 << 20

var doctorConfig struct {
	diagnosticBaseConfig
	MinimumSeverity string
	Concurrency     int
	IncludeSystemDB bool
	OplogWindow     bool
}

var opsConfig struct {
	diagnosticBaseConfig
	MinDuration             time.Duration
	AllUsers                bool
	IncludeIdleTransactions bool
	IncludeIdleCursors      bool
	Databases               string
	Namespaces              string
	Limit                   int
}

var hotspotConfig struct {
	diagnosticBaseConfig
	Duration        time.Duration
	TopN            int
	Concurrency     int
	Databases       string
	IncludeSystemDB bool
}

var indexAuditConfig struct {
	diagnosticBaseConfig
	Databases       string
	AllDatabases    bool
	Collections     string
	Checks          string
	IncludeSystemDB bool
	MinObservation  time.Duration
	MaxCollections  int
	Concurrency     int
}

var capacityConfig struct {
	diagnosticBaseConfig
	Databases       string
	Collections     string
	IncludeSystemDB bool
	FreeStorage     bool
	Snapshot        string
	MaxCollections  int
	Concurrency     int
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run a read-only MongoDB health check",
	RunE: func(cmd *cobra.Command, _ []string) error {
		severity := mot.Severity(doctorConfig.MinimumSeverity)
		if err := validateDoctorCLI(doctorConfig.diagnosticBaseConfig, severity, doctorConfig.Concurrency); err != nil {
			return err
		}
		ctx, cancel := diagnosticContext(cmd.Context(), doctorConfig.Timeout)
		defer cancel()
		client, err := diagnosticClient(ctx, &doctorConfig.BaseCfg)
		if err != nil {
			return err
		}
		defer closeSDKClient(client)
		result, operationErr := client.Doctor(ctx, mot.DoctorOptions{MinimumSeverity: severity, NodeConcurrency: doctorConfig.Concurrency, IncludeSystemDB: doctorConfig.IncludeSystemDB, IncludeOplogWindow: doctorConfig.OplogWindow})
		return printDiagnosticAndError(cmd, result, doctorConfig.Format, operationErr)
	},
}

var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "View active operations with server-side filtering and redaction",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateDiagnosticBase(opsConfig.diagnosticBaseConfig); err != nil {
			return err
		}
		if opsConfig.MinDuration < 0 || opsConfig.Limit < 0 {
			return fmt.Errorf("min-duration and limit must not be negative")
		}
		if err := clioutput.ValidateFormat(opsConfig.Format); err != nil {
			return err
		}
		ctx, cancel := diagnosticContext(cmd.Context(), opsConfig.Timeout)
		defer cancel()
		client, err := diagnosticClient(ctx, &opsConfig.BaseCfg)
		if err != nil {
			return err
		}
		defer closeSDKClient(client)
		result, operationErr := client.CurrentOperations(ctx, mot.CurrentOperationsOptions{MinDuration: opsConfig.MinDuration, AllUsers: opsConfig.AllUsers, CurrentUserOnly: !opsConfig.AllUsers, IncludeIdleTransactions: opsConfig.IncludeIdleTransactions, IncludeIdleCursors: opsConfig.IncludeIdleCursors, Databases: splitCSV(opsConfig.Databases), Namespaces: splitCSV(opsConfig.Namespaces), Limit: opsConfig.Limit, MaxTime: opsConfig.Timeout})
		return printDiagnosticAndError(cmd, result, opsConfig.Format, operationErr)
	},
}

var hotspotCmd = &cobra.Command{
	Use:   "hotspot",
	Short: "Identify node and namespace hotspots using two snapshots",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateDiagnosticBase(hotspotConfig.diagnosticBaseConfig); err != nil {
			return err
		}
		if hotspotConfig.Duration < 0 || hotspotConfig.TopN < 0 || hotspotConfig.Concurrency < 0 {
			return fmt.Errorf("duration, top and concurrency must not be negative")
		}
		if err := clioutput.ValidateFormat(hotspotConfig.Format); err != nil {
			return err
		}
		ctx, cancel := diagnosticContext(cmd.Context(), hotspotConfig.Timeout)
		defer cancel()
		client, err := diagnosticClient(ctx, &hotspotConfig.BaseCfg)
		if err != nil {
			return err
		}
		defer closeSDKClient(client)
		result, operationErr := client.Hotspot(ctx, mot.HotspotOptions{Duration: hotspotConfig.Duration, TopN: hotspotConfig.TopN, NodeConcurrency: hotspotConfig.Concurrency, Databases: splitCSV(hotspotConfig.Databases), IncludeSystemDB: hotspotConfig.IncludeSystemDB})
		return printDiagnosticAndError(cmd, result, hotspotConfig.Format, operationErr)
	},
}

var indexAuditCmd = &cobra.Command{
	Use:   "index-audit",
	Short: "Audit index usage, definitions, and storage candidates",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateDiagnosticBase(indexAuditConfig.diagnosticBaseConfig); err != nil {
			return err
		}
		if indexAuditConfig.MinObservation < 0 || indexAuditConfig.MaxCollections < 0 || indexAuditConfig.Concurrency < 0 {
			return fmt.Errorf("min-observation, max-collections and concurrency must not be negative")
		}
		if err := validateIndexAuditSelection(indexAuditConfig.AllDatabases, indexAuditConfig.Databases); err != nil {
			return err
		}
		if err := clioutput.ValidateFormat(indexAuditConfig.Format); err != nil {
			return err
		}
		checks, err := parseIndexChecks(indexAuditConfig.Checks)
		if err != nil {
			return err
		}
		ctx, cancel := diagnosticContext(cmd.Context(), indexAuditConfig.Timeout)
		defer cancel()
		client, err := diagnosticClient(ctx, &indexAuditConfig.BaseCfg)
		if err != nil {
			return err
		}
		defer closeSDKClient(client)
		result, operationErr := client.IndexAudit(ctx, mot.IndexAuditOptions{Databases: splitCSV(indexAuditConfig.Databases), AllDatabases: indexAuditConfig.AllDatabases, Collections: splitCSV(indexAuditConfig.Collections), Checks: checks, IncludeSystemDB: indexAuditConfig.IncludeSystemDB, MinObservation: indexAuditConfig.MinObservation, MaxCollections: indexAuditConfig.MaxCollections, Concurrency: indexAuditConfig.Concurrency})
		return printDiagnosticAndError(cmd, result, indexAuditConfig.Format, operationErr)
	},
}

var capacityCmd = &cobra.Command{
	Use:   "capacity",
	Short: "Collect a stable, redacted MongoDB capacity snapshot",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateDiagnosticBase(capacityConfig.diagnosticBaseConfig); err != nil {
			return err
		}
		if capacityConfig.MaxCollections < 0 || capacityConfig.Concurrency < 0 {
			return fmt.Errorf("max-collections and concurrency must not be negative")
		}
		if err := clioutput.ValidateFormat(capacityConfig.Format); err != nil {
			return err
		}
		ctx, cancel := diagnosticContext(cmd.Context(), capacityConfig.Timeout)
		defer cancel()
		client, err := diagnosticClient(ctx, &capacityConfig.BaseCfg)
		if err != nil {
			return err
		}
		defer closeSDKClient(client)
		result, operationErr := client.Capacity(ctx, mot.CapacityOptions{Databases: splitCSV(capacityConfig.Databases), Collections: splitCSV(capacityConfig.Collections), IncludeSystemDB: capacityConfig.IncludeSystemDB, IncludeFreeStorage: capacityConfig.FreeStorage, MaxCollections: capacityConfig.MaxCollections, Concurrency: capacityConfig.Concurrency})
		if result != nil && capacityConfig.Snapshot != "" {
			if writeErr := writeCapacitySnapshot(capacityConfig.Snapshot, result); writeErr != nil {
				return writeErr
			}
		}
		return printDiagnosticAndError(cmd, result, capacityConfig.Format, operationErr)
	},
}

var capacityDiffCmd = &cobra.Command{
	Use:   "diff <before.json> <after.json>",
	Short: "Compare two capacity snapshots offline",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		if err := clioutput.ValidateFormat(format); err != nil {
			return err
		}
		before, err := readCapacitySnapshot(args[0])
		if err != nil {
			return err
		}
		after, err := readCapacitySnapshot(args[1])
		if err != nil {
			return err
		}
		result, err := mot.DiffCapacity(before, after)
		if err != nil {
			return err
		}
		return clioutput.PrintDiagnosticResult(cmd.OutOrStdout(), result, format)
	},
}

func initDiagnostics() {
	registerDiagnosticFlags(doctorCmd, &doctorConfig.diagnosticBaseConfig)
	doctorCmd.Flags().StringVar(&doctorConfig.MinimumSeverity, "minimum-severity", "info", "Minimum finding severity: info|warning|critical")
	doctorCmd.Flags().IntVar(&doctorConfig.Concurrency, "concurrency", 10, "Maximum number of concurrent node collectors")
	doctorCmd.Flags().BoolVar(&doctorConfig.IncludeSystemDB, "include-system-db", false, "Include system databases")
	doctorCmd.Flags().BoolVar(&doctorConfig.OplogWindow, "oplog-window", false, "Collect optional oplog window metrics")

	registerDiagnosticFlags(opsCmd, &opsConfig.diagnosticBaseConfig)
	opsCmd.Flags().DurationVar(&opsConfig.MinDuration, "min-duration", 2*time.Second, "Minimum operation duration")
	opsCmd.Flags().BoolVar(&opsConfig.AllUsers, "all-users", true, "Request operations for all users; fall back to the current user if unauthorized")
	opsCmd.Flags().BoolVar(&opsConfig.IncludeIdleTransactions, "include-idle-transactions", false, "Include idle transactions")
	opsCmd.Flags().BoolVar(&opsConfig.IncludeIdleCursors, "include-idle-cursors", false, "Include idle cursors")
	opsCmd.Flags().StringVar(&opsConfig.Databases, "database", "", "Filter by database names (CSV)")
	opsCmd.Flags().StringVar(&opsConfig.Namespaces, "namespace", "", "Filter by namespaces (CSV)")
	opsCmd.Flags().IntVar(&opsConfig.Limit, "limit", 100, "Maximum number of results")

	registerDiagnosticFlags(hotspotCmd, &hotspotConfig.diagnosticBaseConfig)
	hotspotCmd.Flags().DurationVar(&hotspotConfig.Duration, "duration", 10*time.Second, "Interval between the two snapshots")
	hotspotCmd.Flags().IntVar(&hotspotConfig.TopN, "top", 10, "Maximum number of namespace hotspots")
	hotspotCmd.Flags().IntVar(&hotspotConfig.Concurrency, "concurrency", 10, "Maximum number of concurrent node collectors")
	hotspotCmd.Flags().StringVar(&hotspotConfig.Databases, "database", "", "Filter by database names (CSV)")
	hotspotCmd.Flags().BoolVar(&hotspotConfig.IncludeSystemDB, "include-system-db", false, "Include system databases")

	registerDiagnosticFlags(indexAuditCmd, &indexAuditConfig.diagnosticBaseConfig)
	indexAuditCmd.Flags().StringVar(&indexAuditConfig.Databases, "database", "", "Select databases (CSV); mutually exclusive with --all-databases")
	indexAuditCmd.Flags().BoolVar(&indexAuditConfig.AllDatabases, "all-databases", false, "Audit all non-system databases")
	indexAuditCmd.Flags().StringVar(&indexAuditConfig.Collections, "collection", "", "Filter by collection names (CSV)")
	indexAuditCmd.Flags().StringVar(&indexAuditConfig.Checks, "checks", "", "Checks to run (CSV): unused,redundant,space,building,consistency")
	indexAuditCmd.Flags().BoolVar(&indexAuditConfig.IncludeSystemDB, "include-system-db", false, "Include system databases")
	indexAuditCmd.Flags().DurationVar(&indexAuditConfig.MinObservation, "min-observation", 7*24*time.Hour, "Minimum observation window for zero usage")
	indexAuditCmd.Flags().IntVar(&indexAuditConfig.MaxCollections, "max-collections", 500, "Maximum number of collections")
	indexAuditCmd.Flags().IntVar(&indexAuditConfig.Concurrency, "concurrency", 10, "Maximum number of concurrent collection collectors")

	registerDiagnosticFlags(capacityCmd, &capacityConfig.diagnosticBaseConfig)
	capacityCmd.Flags().StringVar(&capacityConfig.Databases, "database", "", "Filter by database names (CSV); empty selects all non-system databases")
	capacityCmd.Flags().StringVar(&capacityConfig.Collections, "collection", "", "Filter by collection names (CSV)")
	capacityCmd.Flags().BoolVar(&capacityConfig.IncludeSystemDB, "include-system-db", false, "Include system databases")
	capacityCmd.Flags().BoolVar(&capacityConfig.FreeStorage, "free-storage", false, "Explicitly enable high-cost free storage collection")
	capacityCmd.Flags().StringVar(&capacityConfig.Snapshot, "snapshot", "", "Write the redacted JSON snapshot to a local path")
	capacityCmd.Flags().IntVar(&capacityConfig.MaxCollections, "max-collections", 500, "Maximum number of collections")
	capacityCmd.Flags().IntVar(&capacityConfig.Concurrency, "concurrency", 10, "Maximum number of concurrent collection collectors")
	capacityDiffCmd.Flags().String("format", "table", "Output format: table|json")
	capacityCmd.AddCommand(capacityDiffCmd)
	rootCmd.AddCommand(doctorCmd, opsCmd, hotspotCmd, indexAuditCmd, capacityCmd)
}

func registerDiagnosticFlags(command *cobra.Command, cfg *diagnosticBaseConfig) {
	registerBaseFlags(command, &cfg.BaseCfg)
	command.Flags().StringVar(&cfg.Format, "format", "table", "Output format: table|json")
	command.Flags().DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "Overall command timeout")
}

func validateDiagnosticBase(cfg diagnosticBaseConfig) error {
	if err := clioutput.ValidateFormat(cfg.Format); err != nil {
		return err
	}
	if cfg.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	return nil
}

func validateDoctorCLI(cfg diagnosticBaseConfig, severity mot.Severity, concurrency int) error {
	if err := validateDiagnosticBase(cfg); err != nil {
		return err
	}
	if severity != mot.SeverityInfo && severity != mot.SeverityWarning && severity != mot.SeverityCritical {
		return fmt.Errorf("minimum-severity must be info, warning or critical")
	}
	if concurrency < 0 {
		return fmt.Errorf("concurrency must not be negative")
	}
	return nil
}

func validateIndexAuditSelection(allDatabases bool, databases string) error {
	if allDatabases == (len(splitCSV(databases)) > 0) {
		return fmt.Errorf("database and all-databases must be specified exclusively")
	}
	return nil
}

func diagnosticClient(ctx context.Context, cfg *config.BaseCfg) (*mot.Client, error) {
	if err := config.BasePreCheck(cfg); err != nil {
		return nil, safeDiagnosticConnectionError()
	}
	client, err := mot.NewClient(ctx, sdkOptionsFromBase(cfg))
	if err != nil {
		return nil, safeDiagnosticConnectionError()
	}
	return client, nil
}

func safeDiagnosticConnectionError() error {
	return errors.New("unable to initialize MongoDB diagnostic client; connection detail suppressed")
}

func diagnosticContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	signalCtx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	if timeout <= 0 {
		return signalCtx, stop
	}
	ctx, cancel := context.WithTimeout(signalCtx, timeout)
	return ctx, func() { cancel(); stop() }
}

func printDiagnosticAndError(cmd *cobra.Command, result any, format string, operationErr error) error {
	if result != nil {
		if err := clioutput.PrintDiagnosticResult(cmd.OutOrStdout(), result, format); err != nil {
			return err
		}
	}
	if operationErr != nil {
		return safeDiagnosticCommandError(operationErr)
	}
	return nil
}

func safeDiagnosticCommandError(operationErr error) error {
	switch {
	case errors.Is(operationErr, mot.ErrPartialResult):
		return fmt.Errorf("%w: 部分 collector 未完成，已输出可用结果", mot.ErrPartialResult)
	case errors.Is(operationErr, mot.ErrCancelled):
		return fmt.Errorf("%w", mot.ErrCancelled)
	case errors.Is(operationErr, mot.ErrUnsupportedTopology):
		return fmt.Errorf("%w", mot.ErrUnsupportedTopology)
	default:
		return errors.New("diagnostic command failed; 原始服务器错误已隐藏")
	}
}

func parseIndexChecks(value string) ([]mot.IndexAuditCheck, error) {
	parts := splitCSV(value)
	result := make([]mot.IndexAuditCheck, 0, len(parts))
	for _, part := range parts {
		check := mot.IndexAuditCheck(strings.ToLower(part))
		switch check {
		case mot.IndexCheckUnused, mot.IndexCheckRedundant, mot.IndexCheckSpace, mot.IndexCheckBuilding, mot.IndexCheckConsistency:
			result = append(result, check)
		default:
			return nil, fmt.Errorf("unknown index audit check %q", part)
		}
	}
	return result, nil
}

func writeCapacitySnapshot(path string, result *mot.CapacityResult) error {
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".mot-capacity-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readCapacitySnapshot(path string) (mot.CapacityResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return mot.CapacityResult{}, err
	}
	if info.Size() > maxCapacitySnapshotBytes {
		return mot.CapacityResult{}, fmt.Errorf("capacity snapshot exceeds %d bytes", maxCapacitySnapshotBytes)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return mot.CapacityResult{}, err
	}
	var result mot.CapacityResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return mot.CapacityResult{}, err
	}
	return result, nil
}
