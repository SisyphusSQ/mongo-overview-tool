package mot

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

type ClusterType string

const (
	ClusterReplicaSet ClusterType = "repl"
	ClusterSharded    ClusterType = "sharding"
)

type OverviewOptions struct {
	IncludeHosts    bool
	NodeConcurrency int
}

type OverviewResult struct {
	ClusterType ClusterType          `json:"clusterType"`
	Hosts       []string             `json:"hosts,omitempty"`
	ReplicaSets []ReplicaSetOverview `json:"replicaSets"`
}

type ReplicaSetOverview struct {
	Name  string         `json:"name"`
	Nodes []NodeOverview `json:"nodes"`
}

type NodeOverview struct {
	ReplicaSet string        `json:"replicaSet"`
	Address    string        `json:"address"`
	State      string        `json:"state"`
	Version    string        `json:"version,omitempty"`
	Uptime     time.Duration `json:"uptime"`

	ConnectionsCurrent int64 `json:"connectionsCurrent"`
	QueueReaders       int64 `json:"queueReaders"`
	QueueWriters       int64 `json:"queueWriters"`
	ActiveReaders      int64 `json:"activeReaders"`
	ActiveWriters      int64 `json:"activeWriters"`

	CacheSizeBytes int64         `json:"cacheSizeBytes"`
	CacheUsedBytes int64         `json:"cacheUsedBytes"`
	ReplicationLag time.Duration `json:"replicationLag"`
}

type CollectionStatsOptions struct {
	Databases             []string
	Collections           []string
	IncludeSystemDB       bool
	RequireShardedCluster bool
	ShardedOnly           bool
	Concurrency           int
}

type CollectionStatsResult struct {
	Databases []DatabaseStats `json:"databases"`
}

type DatabaseStats struct {
	Name             string            `json:"name"`
	StorageSizeBytes int64             `json:"storageSizeBytes"`
	Collections      []CollectionStats `json:"collections"`
}

type CollectionStats struct {
	Namespace        string  `json:"namespace"`
	Count            int64   `json:"count"`
	AvgObjectBytes   float64 `json:"avgObjectBytes"`
	StorageSizeBytes int64   `json:"storageSizeBytes"`
	IsSharded        bool    `json:"isSharded"`
	IndexCount       int     `json:"indexCount"`
	TotalIndexBytes  int64   `json:"totalIndexBytes"`
}

type SlowlogSort string

const (
	SlowlogSortCount     SlowlogSort = "cnt"
	SlowlogSortMaxMillis SlowlogSort = "maxMills"
	SlowlogSortMaxDocs   SlowlogSort = "maxDocs"
)

type SlowlogOptions struct {
	Databases   []string
	Sort        SlowlogSort
	QueryHash   string
	Concurrency int
}

type SlowlogSummaryResult struct {
	ClusterType ClusterType                `json:"clusterType"`
	ReplicaSets []ReplicaSetSlowlogSummary `json:"replicaSets"`
}

type ReplicaSetSlowlogSummary struct {
	Name  string               `json:"name"`
	Hosts []HostSlowlogSummary `json:"hosts"`
}

type HostSlowlogSummary struct {
	Address   string                   `json:"address"`
	State     string                   `json:"state"`
	Databases []DatabaseSlowlogSummary `json:"databases"`
}

type DatabaseSlowlogSummary struct {
	Database  string               `json:"database"`
	Total     int64                `json:"total"`
	FirstTime time.Time            `json:"firstTime"`
	LastTime  time.Time            `json:"lastTime"`
	Items     []SlowlogSummaryItem `json:"items"`
}

type SlowlogSummaryItem struct {
	Namespace string    `json:"namespace"`
	Operation string    `json:"operation"`
	QueryHash string    `json:"queryHash"`
	Count     int64     `json:"count"`
	MaxMillis int64     `json:"maxMillis"`
	MinMillis int64     `json:"minMillis"`
	MaxDocs   int64     `json:"maxDocs"`
	FirstTime time.Time `json:"firstTime"`
	LastTime  time.Time `json:"lastTime"`
}

type SlowlogDetailResult struct {
	Namespace string   `json:"namespace"`
	Slowlog   bson.M   `json:"slowlog"`
	Indexes   []bson.M `json:"indexes"`
}

type BulkOptions struct {
	Database   string
	Collection string
	Filter     any
	BatchSize  int
	Pause      time.Duration
	DryRun     bool

	AllowEmptyFilter bool
	MaxRetries       int
	Observer         BulkObserver
}

type BulkUpdateOptions struct {
	BulkOptions
	Update any
}

type BulkResult struct {
	Database   string `json:"database"`
	Collection string `json:"collection"`
	DryRun     bool   `json:"dryRun"`

	MatchedTotal int64 `json:"matchedTotal"`
	Processed    int64 `json:"processed"`
	Deleted      int64 `json:"deleted"`
	Matched      int64 `json:"matched"`
	Modified     int64 `json:"modified"`

	BatchCount int       `json:"batchCount"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

type BulkBatchResult struct {
	BatchNumber int   `json:"batchNumber"`
	Processed   int64 `json:"processed"`
	Deleted     int64 `json:"deleted"`
	Matched     int64 `json:"matched"`
	Modified    int64 `json:"modified"`
}

type BulkObserver interface {
	OnBulkStart(ctx context.Context, total int64)
	OnBulkBatch(ctx context.Context, batch BulkBatchResult)
	OnBulkRetry(ctx context.Context, err error, attempt int)
	OnBulkDone(ctx context.Context, result BulkResult)
}
