package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
)

const exampleTimeout = 3 * time.Minute

type exampleConfig struct {
	ClientOptions mot.Options
	Database      string
	Collection    string
}

type diagnosticsClient interface {
	Overview(context.Context, mot.OverviewOptions) (*mot.OverviewResult, error)
	CollectionStats(context.Context, mot.CollectionStatsOptions) (*mot.CollectionStatsResult, error)
	Doctor(context.Context, mot.DoctorOptions) (*mot.DoctorResult, error)
	CurrentOperations(context.Context, mot.CurrentOperationsOptions) (*mot.CurrentOperationsResult, error)
	Hotspot(context.Context, mot.HotspotOptions) (*mot.HotspotResult, error)
	IndexAudit(context.Context, mot.IndexAuditOptions) (*mot.IndexAuditResult, error)
	Capacity(context.Context, mot.CapacityOptions) (*mot.CapacityResult, error)
	SlowlogSummary(context.Context, mot.SlowlogOptions) (*mot.SlowlogSummaryResult, error)
	SlowlogDetail(context.Context, string, string) (*mot.SlowlogDetailResult, error)
}

type diagnosticsSummary struct {
	ClusterType              mot.ClusterType     `json:"clusterType"`
	OverviewReplicaSets      int                 `json:"overviewReplicaSets"`
	CollectionStatsDatabases int                 `json:"collectionStatsDatabases"`
	DoctorFindings           int                 `json:"doctorFindings"`
	DoctorStatuses           int                 `json:"doctorStatuses"`
	OperationSource          string              `json:"operationSource"`
	OperationVisibility      string              `json:"operationVisibility"`
	Operations               int                 `json:"operations"`
	HotspotNodes             int                 `json:"hotspotNodes"`
	HotspotNamespaces        int                 `json:"hotspotNamespaces"`
	IndexCollections         int                 `json:"indexCollections"`
	CapacityDatabases        int                 `json:"capacityDatabases"`
	SlowlogReplicaSets       int                 `json:"slowlogReplicaSets"`
	SlowlogDetailLoaded      bool                `json:"slowlogDetailLoaded"`
	Session                  sessionStatsSummary `json:"session"`
	PartialCollectors        []string            `json:"partialCollectors,omitempty"`
}

type sessionStatsSummary struct {
	TopologyLoads              int64            `json:"topologyLoads"`
	ShardInventoryLoads        int64            `json:"shardInventoryLoads"`
	DatabaseInventoryLoads     int64            `json:"databaseInventoryLoads"`
	CollectionInventoryLoads   int64            `json:"collectionInventoryLoads"`
	ReplicaSetInventoryLoads   int64            `json:"replicaSetInventoryLoads"`
	DerivedConnectionsOpened   int64            `json:"derivedConnectionsOpened"`
	DerivedConnectionCacheHits int64            `json:"derivedConnectionCacheHits"`
	DerivedConnectionFailures  int64            `json:"derivedConnectionFailures"`
	RemoteOperations           int64            `json:"remoteOperations"`
	PeakRemoteOperations       int64            `json:"peakRemoteOperations"`
	CapabilityCalls            map[string]int64 `json:"capabilityCalls"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), exampleTimeout)
	defer cancel()

	client, err := mot.NewClient(ctx, config.ClientOptions)
	if err != nil {
		return fmt.Errorf("create SDK client: %w", err)
	}
	defer closeClient(client)
	session, err := client.NewCollectorSession(mot.CollectorSessionOptions{MaxConcurrency: 4})
	if err != nil {
		return fmt.Errorf("create collector session: %w", err)
	}
	defer closeSession(session)

	summary, err := collectDiagnostics(ctx, session, config)
	if err != nil {
		return err
	}
	summary.Session = summarizeSessionStats(session.Stats())
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func loadConfig(getenv func(string) string) (exampleConfig, error) {
	database := strings.TrimSpace(getenv("MOT_EXAMPLE_DATABASE"))
	collection := strings.TrimSpace(getenv("MOT_EXAMPLE_COLLECTION"))
	if database == "" || collection == "" {
		return exampleConfig{}, errors.New("set MOT_EXAMPLE_DATABASE and MOT_EXAMPLE_COLLECTION")
	}

	options := mot.DefaultOptions()
	options.ConnectTimeout = 15 * time.Second
	if uri := strings.TrimSpace(getenv("MOT_MONGO_URI")); uri != "" {
		options.URI = uri
		return exampleConfig{ClientOptions: options, Database: database, Collection: collection}, nil
	}

	host := strings.TrimSpace(getenv("MOT_MONGO_HOST"))
	portText := strings.TrimSpace(getenv("MOT_MONGO_PORT"))
	if host == "" || portText == "" {
		return exampleConfig{}, errors.New("set MOT_MONGO_URI or MOT_MONGO_HOST and MOT_MONGO_PORT")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return exampleConfig{}, errors.New("MOT_MONGO_PORT must be a positive integer")
	}
	username := strings.TrimSpace(getenv("MONGO_USER"))
	password := getenv("MONGO_PASS")
	if (username == "") != (password == "") {
		return exampleConfig{}, errors.New("MONGO_USER and MONGO_PASS must be provided together")
	}
	options.Host = host
	options.Port = port
	options.Username = username
	options.Password = password
	if authSource := strings.TrimSpace(getenv("MOT_MONGO_AUTH_SOURCE")); authSource != "" {
		options.AuthSource = authSource
	}
	return exampleConfig{ClientOptions: options, Database: database, Collection: collection}, nil
}

func collectDiagnostics(ctx context.Context, client diagnosticsClient, config exampleConfig) (diagnosticsSummary, error) {
	var summary diagnosticsSummary
	overview, err := client.Overview(ctx, mot.OverviewOptions{IncludeHosts: true, NodeConcurrency: 2})
	if err := acceptPartial("overview", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if overview == nil {
		return summary, errors.New("overview returned no result")
	}
	summary.ClusterType = overview.ClusterType
	summary.OverviewReplicaSets = len(overview.ReplicaSets)

	collectionStats, err := client.CollectionStats(ctx, mot.CollectionStatsOptions{
		Databases:   []string{config.Database},
		Collections: []string{config.Collection},
		Concurrency: 2,
	})
	if err := acceptPartial("collection_stats", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if collectionStats == nil {
		return summary, errors.New("collection stats returned no result")
	}
	summary.CollectionStatsDatabases = len(collectionStats.Databases)

	doctor, err := client.Doctor(ctx, mot.DoctorOptions{NodeConcurrency: 2})
	if err := acceptPartial("doctor", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if doctor == nil {
		return summary, errors.New("doctor returned no result")
	}
	summary.DoctorFindings = len(doctor.Findings)
	summary.DoctorStatuses = len(doctor.CollectorStatuses)

	operations, err := client.CurrentOperations(ctx, mot.CurrentOperationsOptions{
		AllUsers: true,
		Limit:    20,
		MaxTime:  5 * time.Second,
	})
	if err := acceptPartial("operations", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if operations == nil {
		return summary, errors.New("current operations returned no result")
	}
	summary.OperationSource = operations.Source
	summary.OperationVisibility = operations.Visibility
	summary.Operations = len(operations.Operations)

	hotspot, err := client.Hotspot(ctx, mot.HotspotOptions{
		Duration:        100 * time.Millisecond,
		TopN:            5,
		NodeConcurrency: 2,
		Databases:       []string{config.Database},
	})
	if err := acceptPartial("hotspot", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if hotspot == nil {
		return summary, errors.New("hotspot returned no result")
	}
	summary.HotspotNodes = len(hotspot.Nodes)
	summary.HotspotNamespaces = len(hotspot.Namespaces)

	indexAudit, err := client.IndexAudit(ctx, mot.IndexAuditOptions{
		Databases:   []string{config.Database},
		Collections: []string{config.Collection},
		Checks: []mot.IndexAuditCheck{
			mot.IndexCheckUnused,
			mot.IndexCheckRedundant,
			mot.IndexCheckSpace,
			mot.IndexCheckBuilding,
		},
		MinObservation: time.Hour,
		MaxCollections: 1,
		Concurrency:    1,
	})
	if err := acceptPartial("index_audit", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if indexAudit == nil {
		return summary, errors.New("index audit returned no result")
	}
	summary.IndexCollections = len(indexAudit.Collections)

	capacity, err := client.Capacity(ctx, mot.CapacityOptions{
		Databases:      []string{config.Database},
		Collections:    []string{config.Collection},
		MaxCollections: 1,
		Concurrency:    1,
	})
	if err := acceptPartial("capacity", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if capacity == nil {
		return summary, errors.New("capacity returned no result")
	}
	summary.CapacityDatabases = len(capacity.Databases)

	slowlog, err := client.SlowlogSummary(ctx, mot.SlowlogOptions{
		Databases:   []string{config.Database},
		Sort:        mot.SlowlogSortCount,
		Concurrency: 2,
	})
	if err := acceptPartial("slowlog", err, &summary.PartialCollectors); err != nil {
		return summary, err
	}
	if slowlog == nil {
		return summary, errors.New("slowlog summary returned no result")
	}
	summary.SlowlogReplicaSets = len(slowlog.ReplicaSets)
	if database, queryHash, ok := firstSlowlogTarget(slowlog); ok {
		detail, detailErr := client.SlowlogDetail(ctx, database, queryHash)
		if detailErr != nil {
			return summary, fmt.Errorf("slowlog detail: %w", detailErr)
		}
		if detail == nil || detail.Namespace == "" {
			return summary, errors.New("slowlog detail returned no result")
		}
		summary.SlowlogDetailLoaded = true
	}
	return summary, nil
}

func firstSlowlogTarget(result *mot.SlowlogSummaryResult) (string, string, bool) {
	if result == nil {
		return "", "", false
	}
	for _, replicaSet := range result.ReplicaSets {
		for _, host := range replicaSet.Hosts {
			for _, database := range host.Databases {
				for _, item := range database.Items {
					if database.Database != "" && item.QueryHash != "" {
						return database.Database, item.QueryHash, true
					}
				}
			}
		}
	}
	return "", "", false
}

func summarizeSessionStats(stats mot.CollectorSessionStats) sessionStatsSummary {
	result := sessionStatsSummary{
		TopologyLoads:              stats.TopologyLoads,
		ShardInventoryLoads:        stats.ShardInventoryLoads,
		DatabaseInventoryLoads:     stats.DatabaseInventoryLoads,
		CollectionInventoryLoads:   stats.CollectionInventoryLoads,
		ReplicaSetInventoryLoads:   stats.ReplicaSetInventoryLoads,
		DerivedConnectionsOpened:   stats.DerivedConnectionsOpened,
		DerivedConnectionCacheHits: stats.DerivedConnectionCacheHits,
		DerivedConnectionFailures:  stats.DerivedConnectionFailures,
		RemoteOperations:           stats.RemoteOperations,
		PeakRemoteOperations:       stats.PeakRemoteOperations,
		CapabilityCalls:            make(map[string]int64, len(stats.Capabilities)),
	}
	for name, capability := range stats.Capabilities {
		result.CapabilityCalls[name] = capability.Calls
	}
	return result
}

func acceptPartial(operation string, err error, partialCollectors *[]string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mot.ErrPartialResult) {
		*partialCollectors = append(*partialCollectors, operation)
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func closeClient(client *mot.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		log.Printf("close client: %v", err)
	}
}

func closeSession(session *mot.CollectorSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Close(ctx); err != nil {
		log.Printf("close collector session: %v", err)
	}
}
