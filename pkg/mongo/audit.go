package mongo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// IndexStatSnapshot 是 $indexStats 的 presence-aware 安全投影。
type IndexStatSnapshot struct {
	Name                          string
	Host                          string
	Key                           bson.D
	Ops                           int64
	Since                         time.Time
	Unique                        bool
	Sparse                        bool
	Hidden                        bool
	Partial                       bool
	WildcardProjection            bool
	CollationFingerprint          string
	PartialFilterFingerprint      string
	WildcardProjectionFingerprint string
	ExpireAfterSeconds            *int64
	SpecialType                   string
}

// IndexStats 使用服务端投影采集索引定义和访问计数，不返回业务文档或查询文本。
func (c *Conn) IndexStats(ctx context.Context, database, collection string, maxTime time.Duration) ([]IndexStatSnapshot, error) {
	pipeline := []bson.D{
		{{Key: "$indexStats", Value: bson.D{}}},
		{{Key: "$project", Value: bson.D{
			{Key: "_id", Value: 0}, {Key: "name", Value: 1}, {Key: "host", Value: 1},
			{Key: "key", Value: "$spec.key"}, {Key: "accesses", Value: 1},
			{Key: "unique", Value: "$spec.unique"}, {Key: "sparse", Value: "$spec.sparse"},
			{Key: "hidden", Value: "$spec.hidden"}, {Key: "partialFilterExpression", Value: "$spec.partialFilterExpression"},
			{Key: "wildcardProjection", Value: "$spec.wildcardProjection"}, {Key: "collation", Value: "$spec.collation"},
			{Key: "expireAfterSeconds", Value: "$spec.expireAfterSeconds"},
		}}},
	}
	aggregateOptions := options.Aggregate()
	if maxTime > 0 {
		aggregateOptions.SetMaxTime(maxTime)
	}
	cursor, err := c.Client.Database(database).Collection(collection).Aggregate(ctx, pipeline, aggregateOptions)
	if err != nil {
		return nil, err
	}
	defer closeMongoCursor(ctx, cursor)
	var raw []struct {
		Name                    string `bson:"name"`
		Host                    string `bson:"host"`
		Key                     bson.D `bson:"key"`
		Unique                  bool   `bson:"unique"`
		Sparse                  bool   `bson:"sparse"`
		Hidden                  bool   `bson:"hidden"`
		PartialFilterExpression bson.D `bson:"partialFilterExpression"`
		WildcardProjection      bson.D `bson:"wildcardProjection"`
		Collation               bson.D `bson:"collation"`
		ExpireAfterSeconds      *int64 `bson:"expireAfterSeconds"`
		Accesses                struct {
			Ops   int64     `bson:"ops"`
			Since time.Time `bson:"since"`
		} `bson:"accesses"`
	}
	if err := cursor.All(ctx, &raw); err != nil {
		return nil, err
	}
	result := make([]IndexStatSnapshot, 0, len(raw))
	for _, item := range raw {
		result = append(result, IndexStatSnapshot{
			Name: item.Name, Host: item.Host, Key: item.Key, Ops: item.Accesses.Ops, Since: item.Accesses.Since,
			Unique: item.Unique, Sparse: item.Sparse, Hidden: item.Hidden, Partial: len(item.PartialFilterExpression) > 0,
			WildcardProjection: len(item.WildcardProjection) > 0, ExpireAfterSeconds: item.ExpireAfterSeconds,
			CollationFingerprint:          diagnosticDocumentFingerprint(item.Collation),
			PartialFilterFingerprint:      diagnosticDocumentFingerprint(item.PartialFilterExpression),
			WildcardProjectionFingerprint: diagnosticDocumentFingerprint(item.WildcardProjection),
			SpecialType:                   indexSpecialType(item.Key),
		})
	}
	return result, nil
}

func diagnosticDocumentFingerprint(document bson.D) string {
	if len(document) == 0 {
		return ""
	}
	payload, err := bson.Marshal(document)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func indexSpecialType(key bson.D) string {
	for _, element := range key {
		if element.Key == "$**" || strings.HasSuffix(element.Key, ".$**") {
			return "wildcard"
		}
		value, ok := element.Value.(string)
		if !ok {
			continue
		}
		switch value {
		case "text", "2d", "2dsphere", "geoHaystack", "hashed", "wildcard":
			return value
		}
	}
	return ""
}

// CollectionCapacitySnapshot 是 collStats 的容量字段安全投影。
type CollectionCapacitySnapshot struct {
	Namespace            string
	Count                *int64
	AverageObjectBytes   *float64
	DataSizeBytes        *int64
	StorageSizeBytes     *int64
	TotalIndexSizeBytes  *int64
	FreeStorageSizeBytes *int64
	IndexSizes           map[string]int64
	IndexBuilds          []string
	Sharded              bool
	Shards               []CollectionCapacityShardSnapshot
}

type CollectionCapacityShardSnapshot struct {
	Shard                string
	Host                 string
	Count                *int64
	AverageObjectBytes   *float64
	DataSizeBytes        *int64
	StorageSizeBytes     *int64
	TotalIndexSizeBytes  *int64
	FreeStorageSizeBytes *int64
}

// DatabaseCapacitySnapshot 是 dbStats 中可区分缺失和零值的容量字段。
type DatabaseCapacitySnapshot struct {
	Objects                   *int64
	DataSizeBytes             *int64
	StorageSizeBytes          *int64
	IndexSizeBytes            *int64
	TotalSizeBytes            *int64
	FSUsedSizeBytes           *int64
	FSTotalSizeBytes          *int64
	FreeStorageSizeBytes      *int64
	IndexFreeStorageSizeBytes *int64
	TotalFreeStorageSizeBytes *int64
}

// DatabaseCapacity 执行只读 dbStats；free storage 仅在调用方显式启用时进入结果。
func (c *Conn) DatabaseCapacity(ctx context.Context, database string, includeFreeStorage bool, maxTime time.Duration) (DatabaseCapacitySnapshot, error) {
	command := bson.D{{Key: "dbStats", Value: 1}, {Key: "scale", Value: 1}}
	if includeFreeStorage {
		command = append(command, bson.E{Key: "freeStorage", Value: 1})
	}
	if maxTime > 0 {
		command = append(command, bson.E{Key: "maxTimeMS", Value: maxTime.Milliseconds()})
	}
	var raw struct {
		Objects                   *int64 `bson:"objects"`
		DataSizeBytes             *int64 `bson:"dataSize"`
		StorageSizeBytes          *int64 `bson:"storageSize"`
		IndexSizeBytes            *int64 `bson:"indexSize"`
		TotalSizeBytes            *int64 `bson:"totalSize"`
		FSUsedSizeBytes           *int64 `bson:"fsUsedSize"`
		FSTotalSizeBytes          *int64 `bson:"fsTotalSize"`
		FreeStorageSizeBytes      *int64 `bson:"freeStorageSize"`
		IndexFreeStorageSizeBytes *int64 `bson:"indexFreeStorageSize"`
		TotalFreeStorageSizeBytes *int64 `bson:"totalFreeStorageSize"`
	}
	if err := c.Client.Database(database).RunCommand(ctx, command).Decode(&raw); err != nil {
		return DatabaseCapacitySnapshot{}, err
	}
	result := DatabaseCapacitySnapshot(raw)
	if !includeFreeStorage {
		result.FreeStorageSizeBytes = nil
		result.IndexFreeStorageSizeBytes = nil
		result.TotalFreeStorageSizeBytes = nil
	}
	return result, nil
}

// CollectionCapacity 执行一次有 maxTimeMS 的只读 collStats。
func (c *Conn) CollectionCapacity(ctx context.Context, database, collection string, includeFreeStorage bool, maxTime time.Duration) (CollectionCapacitySnapshot, error) {
	if includeFreeStorage {
		aggregateOptions := options.Aggregate()
		if maxTime > 0 {
			aggregateOptions.SetMaxTime(maxTime)
		}
		cursor, err := c.Client.Database(database).Collection(collection).Aggregate(ctx, []bson.D{{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{{Key: "scale", Value: 1}}}}}}}, aggregateOptions)
		if err != nil {
			return CollectionCapacitySnapshot{}, err
		}
		defer closeMongoCursor(ctx, cursor)
		var documents []struct {
			Namespace    string `bson:"ns"`
			Shard        string `bson:"shard"`
			Host         string `bson:"host"`
			StorageStats struct {
				Count                *int64           `bson:"count"`
				AverageObjectBytes   *float64         `bson:"avgObjSize"`
				DataSizeBytes        *int64           `bson:"size"`
				StorageSizeBytes     *int64           `bson:"storageSize"`
				TotalIndexSizeBytes  *int64           `bson:"totalIndexSize"`
				FreeStorageSizeBytes *int64           `bson:"freeStorageSize"`
				IndexSizes           map[string]int64 `bson:"indexSizes"`
				IndexBuilds          []string         `bson:"indexBuilds"`
			} `bson:"storageStats"`
		}
		if err := cursor.All(ctx, &documents); err != nil {
			return CollectionCapacitySnapshot{}, err
		}
		result := CollectionCapacitySnapshot{Namespace: database + "." + collection, IndexSizes: make(map[string]int64)}
		for _, document := range documents {
			if document.Namespace != "" {
				result.Namespace = document.Namespace
			}
			addOptionalInt64(&result.Count, document.StorageStats.Count)
			addOptionalInt64(&result.DataSizeBytes, document.StorageStats.DataSizeBytes)
			addOptionalInt64(&result.StorageSizeBytes, document.StorageStats.StorageSizeBytes)
			addOptionalInt64(&result.TotalIndexSizeBytes, document.StorageStats.TotalIndexSizeBytes)
			addOptionalInt64(&result.FreeStorageSizeBytes, document.StorageStats.FreeStorageSizeBytes)
			if len(documents) == 1 {
				result.AverageObjectBytes = document.StorageStats.AverageObjectBytes
			}
			for name, size := range document.StorageStats.IndexSizes {
				result.IndexSizes[name] += size
			}
			result.IndexBuilds = append(result.IndexBuilds, document.StorageStats.IndexBuilds...)
			result.Shards = append(result.Shards, CollectionCapacityShardSnapshot{Shard: document.Shard, Host: document.Host, Count: document.StorageStats.Count, AverageObjectBytes: document.StorageStats.AverageObjectBytes, DataSizeBytes: document.StorageStats.DataSizeBytes, StorageSizeBytes: document.StorageStats.StorageSizeBytes, TotalIndexSizeBytes: document.StorageStats.TotalIndexSizeBytes, FreeStorageSizeBytes: document.StorageStats.FreeStorageSizeBytes})
		}
		result.Sharded = len(result.Shards) > 1 || len(result.Shards) == 1 && result.Shards[0].Shard != ""
		return result, nil
	}
	command := bson.D{{Key: "collStats", Value: collection}, {Key: "scale", Value: 1}}
	if maxTime > 0 {
		command = append(command, bson.E{Key: "maxTimeMS", Value: maxTime.Milliseconds()})
	}
	var raw struct {
		Namespace            string           `bson:"ns"`
		Count                *int64           `bson:"count"`
		AverageObjectBytes   *float64         `bson:"avgObjSize"`
		DataSizeBytes        *int64           `bson:"size"`
		StorageSizeBytes     *int64           `bson:"storageSize"`
		TotalIndexSizeBytes  *int64           `bson:"totalIndexSize"`
		FreeStorageSizeBytes *int64           `bson:"freeStorageSize"`
		IndexSizes           map[string]int64 `bson:"indexSizes"`
		IndexBuilds          []string         `bson:"indexBuilds"`
		Sharded              bool             `bson:"sharded"`
		Shards               map[string]struct {
			Count               *int64   `bson:"count"`
			AverageObjectBytes  *float64 `bson:"avgObjSize"`
			DataSizeBytes       *int64   `bson:"size"`
			StorageSizeBytes    *int64   `bson:"storageSize"`
			TotalIndexSizeBytes *int64   `bson:"totalIndexSize"`
		} `bson:"shards"`
	}
	if err := c.Client.Database(database).RunCommand(ctx, command).Decode(&raw); err != nil {
		return CollectionCapacitySnapshot{}, err
	}
	result := CollectionCapacitySnapshot{Namespace: raw.Namespace, Count: raw.Count, AverageObjectBytes: raw.AverageObjectBytes, DataSizeBytes: raw.DataSizeBytes, StorageSizeBytes: raw.StorageSizeBytes, TotalIndexSizeBytes: raw.TotalIndexSizeBytes, IndexSizes: raw.IndexSizes, IndexBuilds: raw.IndexBuilds, Sharded: raw.Sharded}
	for shard, stats := range raw.Shards {
		result.Shards = append(result.Shards, CollectionCapacityShardSnapshot{Shard: shard, Count: stats.Count, AverageObjectBytes: stats.AverageObjectBytes, DataSizeBytes: stats.DataSizeBytes, StorageSizeBytes: stats.StorageSizeBytes, TotalIndexSizeBytes: stats.TotalIndexSizeBytes})
	}
	return result, nil
}

func addOptionalInt64(target **int64, value *int64) {
	if value == nil {
		return
	}
	if *target == nil {
		copyOfValue := *value
		*target = &copyOfValue
		return
	}
	**target += *value
}
