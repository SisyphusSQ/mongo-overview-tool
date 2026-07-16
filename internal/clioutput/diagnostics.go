package clioutput

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

const (
	FormatTable = "table"
	FormatJSON  = "json"
)

func ValidateFormat(format string) error {
	if format != FormatTable && format != FormatJSON {
		return fmt.Errorf("format must be table or json")
	}
	return nil
}

// PrintDiagnosticResult 只渲染 SDK 已脱敏结果，不重新判断 severity。
func PrintDiagnosticResult(w io.Writer, result any, format string) error {
	if err := ValidateFormat(format); err != nil {
		return err
	}
	if format == FormatJSON {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	switch value := result.(type) {
	case *mot.DoctorResult:
		fmt.Fprintf(w, "MongoDB Doctor (%s)\n", value.ClusterType)
		printFindings(w, value.Findings)
		printStatuses(w, value.CollectorStatuses)
	case *mot.CurrentOperationsResult:
		fmt.Fprintf(w, "Current Operations (%s, visibility=%s, source=%s)\n", value.ClusterType, value.Visibility, value.Source)
		fmt.Fprintln(w, "HOST\tNAMESPACE\tOP\tDURATION\tLOCK\tFLOW")
		for _, item := range value.Operations {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%t\n", item.Host, item.Namespace, item.Operation, durationText(item.RunningDuration), item.WaitingForLock, item.WaitingForFlowControl)
		}
		printFindings(w, value.Findings)
		printStatuses(w, value.CollectorStatuses)
	case *mot.HotspotResult:
		fmt.Fprintf(w, "MongoDB Hotspot (%s, duration=%s)\n", value.ClusterType, durationText(value.EffectiveDuration))
		fmt.Fprintln(w, "SHARD\tHOST\tNAMESPACE\tREAD/S\tWRITE/S\tTIME_US")
		for _, item := range value.Namespaces {
			fmt.Fprintf(w, "%s\t%s\t%s\t%.2f\t%.2f\t%d\n", item.Shard, item.Host, item.Namespace, item.ReadPerSecond, item.WritePerSecond, item.TotalTimeMicros)
		}
		printFindings(w, value.Findings)
		printStatuses(w, value.CollectorStatuses)
	case *mot.IndexAuditResult:
		fmt.Fprintln(w, "MongoDB Index Audit")
		printIndexConsistency(w, value)
		fmt.Fprintln(w, "NAMESPACE\tINDEX\tSHARD\tHOST\tOPS\tSINCE\tSIZE")
		for _, collection := range value.Collections {
			for _, index := range collection.Indexes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", collection.Namespace, index.Name, index.Shard, index.Host, index.Ops, index.Since.UTC().Format("2006-01-02T15:04:05Z"), optionalBytes(index.SizeBytes))
			}
		}
		printFindings(w, value.Findings)
		printStatuses(w, value.CollectorStatuses)
	case *mot.CapacityResult:
		fmt.Fprintf(w, "MongoDB Capacity (schema=%d, topology=%s)\n", value.SchemaVersion, value.ClusterIdentity.TopologyType)
		fmt.Fprintln(w, "NAMESPACE\tCOUNT\tDATA\tSTORAGE\tINDEX\tFREE")
		for _, database := range value.Databases {
			for _, collection := range database.Collections {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", collection.Namespace, optionalInt(collection.Count), optionalBytes(collection.DataSizeBytes), optionalBytes(collection.StorageSizeBytes), optionalBytes(collection.IndexSizeBytes), optionalBytes(collection.FreeStorageSizeBytes))
				for _, shard := range collection.Shards {
					label := collection.Namespace + " [" + shard.Shard
					if shard.Host != "" {
						label += "/" + shard.Host
					}
					label += "]"
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", label, optionalInt(shard.Count), optionalBytes(shard.DataSizeBytes), optionalBytes(shard.StorageSizeBytes), optionalBytes(shard.IndexSizeBytes), optionalBytes(shard.FreeStorageSizeBytes))
				}
			}
		}
		printFindings(w, value.Findings)
		printStatuses(w, value.CollectorStatuses)
	case *mot.CapacityDiffResult:
		fmt.Fprintf(w, "MongoDB Capacity Diff (duration=%s)\n", durationText(value.Duration))
		fmt.Fprintln(w, "NAMESPACE\tSTATE\tCOUNT_DELTA\tDATA_DELTA\tSTORAGE_DELTA\tINDEX_DELTA")
		for _, item := range value.Collections {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", item.Namespace, item.State, optionalInt(item.Count.Delta), optionalBytes(item.Data.Delta), optionalBytes(item.Storage.Delta), optionalBytes(item.Index.Delta))
		}
	default:
		return fmt.Errorf("unsupported diagnostic result %T", result)
	}
	return nil
}

func printIndexConsistency(w io.Writer, result *mot.IndexAuditResult) {
	hasConsistency := false
	for _, collection := range result.Collections {
		if collection.State != "" {
			hasConsistency = true
			break
		}
	}
	if !hasConsistency {
		return
	}
	fmt.Fprintf(w, "Consistency Summary: consistent=%d inconsistent=%d inconclusive=%d skipped=%d\n",
		result.ConsistencySummary.Consistent, result.ConsistencySummary.Inconsistent,
		result.ConsistencySummary.Inconclusive, result.ConsistencySummary.Skipped)
	fmt.Fprintln(w, "NAMESPACE\tSHARDED\tSTATE\tSTRATEGY\tCOVERAGE\tEXPECTED\tOBSERVED\tFALLBACK")
	for _, collection := range result.Collections {
		if collection.State == "" {
			continue
		}
		fallback := "-"
		if collection.Fallback != nil {
			fallback = fmt.Sprintf("%s->%s(%s)", collection.Fallback.From, collection.Fallback.To, collection.Fallback.ReasonCode)
		}
		fmt.Fprintf(w, "%s\t%t\t%s\t%s\t%s\t%s\t%s\t%s\n",
			collection.Namespace, collection.Sharded, collection.State, collection.Strategy, collection.Coverage,
			strings.Join(collection.ExpectedShards, ","), strings.Join(collection.ObservedShards, ","), fallback)
	}
	fmt.Fprintln(w, "Index Differences:")
	fmt.Fprintln(w, "NAMESPACE\tCODE\tINDEX\tSHARDS\tKEY\tFINGERPRINT\tFIELDS\tSOURCE")
	for _, collection := range result.Collections {
		for _, difference := range collection.Differences {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				collection.Namespace, difference.Code, difference.IndexName, strings.Join(difference.Shards, ","),
				indexKeyText(difference.Key), difference.Fingerprint, strings.Join(difference.DifferingFields, ","), difference.SourceType)
		}
	}
}

func indexKeyText(key []mot.IndexKeyField) string {
	parts := make([]string, 0, len(key))
	for _, field := range key {
		parts = append(parts, field.Field+":"+field.Order)
	}
	return strings.Join(parts, ",")
}

func printFindings(w io.Writer, findings []mot.DiagnosticFinding) {
	if len(findings) == 0 {
		fmt.Fprintln(w, "Findings: none")
		return
	}
	fmt.Fprintln(w, "Findings:")
	for _, finding := range findings {
		fmt.Fprintf(w, "- %s\t%s\t%s\t%s\n", strings.ToUpper(string(finding.Severity)), finding.Code, diagnosticScopeText(finding.Scope), finding.Summary)
	}
}

func printStatuses(w io.Writer, statuses []mot.CollectorStatus) {
	if len(statuses) == 0 {
		return
	}
	copyOfStatuses := append([]mot.CollectorStatus(nil), statuses...)
	sort.SliceStable(copyOfStatuses, func(i, j int) bool { return copyOfStatuses[i].Name < copyOfStatuses[j].Name })
	fmt.Fprintln(w, "Collector Status:")
	for _, status := range copyOfStatuses {
		fmt.Fprintf(w, "- %s\t%s\t%s\t%s\n", status.Name, status.State, diagnosticScopeText(status.Scope), status.ReasonCode)
	}
}

func diagnosticScopeText(scope mot.FindingScope) string {
	parts := make([]string, 0, 6)
	for _, value := range []string{scope.Cluster, scope.Shard, scope.ReplicaSet, scope.Node, scope.Database, scope.Namespace} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return string(scope.Type)
	}
	return strings.Join(parts, "/")
}

func optionalBytes(value *int64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", *value)
}

func optionalInt(value *int64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", *value)
}
