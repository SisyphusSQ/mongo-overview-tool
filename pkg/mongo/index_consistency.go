package mongo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/bson/primitive"
	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var ErrIndexConsistencyFieldsMissing = errors.New("index consistency fields missing")

// IndexKeySnapshot 是可安全公开的有序索引 key 形状，不包含业务文档值。
type IndexKeySnapshot struct {
	Field string
	Order string
}

// CanonicalIndexDefinition 只保留安全 fingerprint、key 顺序和顶层字段 hash。
// 原始 BSON 与 canonical bytes 始终留在本包调用栈内。
type CanonicalIndexDefinition struct {
	Name                string
	Key                 []IndexKeySnapshot
	SemanticFingerprint string
	FullFingerprint     string
	FieldFingerprints   map[string]string
	Building            bool
	Shard               string
}

// IndexRoutingSnapshot 是从 config routing metadata 独立建立的 expected-shard 基线。
type IndexRoutingSnapshot struct {
	Namespace      string
	Sharded        bool
	ExpectedShards []string
}

// MetadataConsistencyRequest 限定 official command 的 database/collection scope 和成本边界。
type MetadataConsistencyRequest struct {
	Database   string
	Collection string
	BatchSize  int32
	MaxTime    time.Duration
}

// MetadataIndexInconsistency 是 7.x official response 的安全索引域投影。
type MetadataIndexInconsistency struct {
	SourceType         string
	Namespace          string
	IndexName          string
	MissingFromShards  []string
	InconsistentFields []string
	PropertiesDiffer   bool
	Shard              string
	Fingerprint        string
}

type rawCommandRunner func(context.Context, string, bson.D) (bson.Raw, error)

type metadataCursorResponse struct {
	Cursor struct {
		ID         int64      `bson:"id"`
		Namespace  string     `bson:"ns"`
		FirstBatch []bson.Raw `bson:"firstBatch"`
		NextBatch  []bson.Raw `bson:"nextBatch"`
	} `bson:"cursor"`
}

func decodeIndexStatsDefinition(raw bson.Raw) (CanonicalIndexDefinition, error) {
	shardValue := raw.Lookup("shard")
	specValue := raw.Lookup("spec")
	if shardValue.Type != bsontype.String || strings.TrimSpace(shardValue.StringValue()) == "" || specValue.Type != bsontype.EmbeddedDocument {
		return CanonicalIndexDefinition{}, ErrIndexConsistencyFieldsMissing
	}
	var spec bson.D
	if err := bson.Unmarshal(specValue.Document(), &spec); err != nil {
		return CanonicalIndexDefinition{}, fmt.Errorf("decode index stats spec: %w", err)
	}
	building := false
	buildingValue := raw.Lookup("building")
	if buildingValue.Type == bsontype.Boolean {
		building = buildingValue.Boolean()
	} else if buildingValue.Type != bsontype.Type(0) {
		return CanonicalIndexDefinition{}, ErrIndexConsistencyFieldsMissing
	}
	definition, err := canonicalIndexDefinition(spec, shardValue.StringValue(), building)
	if err != nil {
		return CanonicalIndexDefinition{}, err
	}
	return definition, nil
}

// ServerVersion 返回 buildInfo 的精确版本字符串，用于 4.2.4 patch 断点。
func (c *Conn) ServerVersion(ctx context.Context) (string, error) {
	if c == nil || c.Client == nil {
		return "", fmt.Errorf("MongoDB connection is required")
	}
	raw, err := c.Client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).DecodeBytes()
	if err != nil {
		return "", err
	}
	return decodeBuildInfoVersion(raw)
}

func decodeBuildInfoVersion(raw bson.Raw) (string, error) {
	version := raw.Lookup("version")
	if version.Type != bsontype.String || strings.TrimSpace(version.StringValue()) == "" {
		return "", fmt.Errorf("buildInfo version is missing")
	}
	return version.StringValue(), nil
}

// IndexRouting 读取版本适配后的 config metadata；内部 schema 不进入返回 contract。
func (c *Conn) IndexRouting(ctx context.Context, database, collection string, maxTime time.Duration) (IndexRoutingSnapshot, error) {
	if c == nil || c.Client == nil {
		return IndexRoutingSnapshot{}, fmt.Errorf("MongoDB connection is required")
	}
	namespace := database + "." + collection
	findOneOptions := options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}, {Key: "uuid", Value: 1}, {Key: "dropped", Value: 1}})
	if maxTime > 0 {
		findOneOptions.SetMaxTime(maxTime)
	}
	var metadata bson.D
	err := c.Client.Database("config").Collection("collections").FindOne(
		ctx,
		bson.D{{Key: "_id", Value: namespace}},
		findOneOptions,
	).Decode(&metadata)
	if errors.Is(err, drivermongo.ErrNoDocuments) {
		return IndexRoutingSnapshot{Namespace: namespace}, nil
	}
	if err != nil {
		return IndexRoutingSnapshot{}, err
	}
	filter, err := routingChunkFilter(namespace, metadata)
	if err != nil {
		return IndexRoutingSnapshot{}, err
	}
	findOptions := options.Find().SetProjection(bson.D{{Key: "_id", Value: 0}, {Key: "shard", Value: 1}})
	if maxTime > 0 {
		findOptions.SetMaxTime(maxTime)
	}
	cursor, err := c.Client.Database("config").Collection("chunks").Find(ctx, filter, findOptions)
	if err != nil {
		return IndexRoutingSnapshot{}, err
	}
	defer closeMongoCursor(ctx, cursor)
	var chunks []bson.Raw
	for cursor.Next(ctx) {
		chunks = append(chunks, append(bson.Raw(nil), cursor.Current...))
	}
	if err := cursor.Err(); err != nil {
		return IndexRoutingSnapshot{}, err
	}
	return indexRoutingSnapshot(namespace, metadata, chunks)
}

func indexRoutingSnapshot(namespace string, metadata bson.D, chunks []bson.Raw) (IndexRoutingSnapshot, error) {
	result := IndexRoutingSnapshot{Namespace: namespace}
	if len(metadata) == 0 {
		return result, nil
	}
	for _, element := range metadata {
		if element.Key == "dropped" {
			if dropped, ok := element.Value.(bool); ok && dropped {
				return result, nil
			}
		}
	}
	result.Sharded = true
	shards := make(map[string]struct{})
	for _, chunk := range chunks {
		value := chunk.Lookup("shard")
		if value.Type != bsontype.String || strings.TrimSpace(value.StringValue()) == "" {
			return IndexRoutingSnapshot{}, fmt.Errorf("routing chunk shard is missing")
		}
		shards[value.StringValue()] = struct{}{}
	}
	for shard := range shards {
		result.ExpectedShards = append(result.ExpectedShards, shard)
	}
	sort.Strings(result.ExpectedShards)
	return result, nil
}

// ListIndexDefinitions 通过一个 shard replica set 入口完整读取 collection index definitions。
func (c *Conn) ListIndexDefinitions(ctx context.Context, database, collection string, maxTime time.Duration) ([]CanonicalIndexDefinition, error) {
	listOptions := options.ListIndexes()
	if maxTime > 0 {
		listOptions.SetMaxTime(maxTime)
	}
	cursor, err := c.Client.Database(database).Collection(collection).Indexes().List(ctx, listOptions)
	if err != nil {
		return nil, err
	}
	defer closeMongoCursor(ctx, cursor)
	var result []CanonicalIndexDefinition
	for cursor.Next(ctx) {
		definition, err := decodeListIndexDefinition(cursor.Current)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeListIndexDefinition(raw bson.Raw) (CanonicalIndexDefinition, error) {
	var spec bson.D
	if err := bson.Unmarshal(raw, &spec); err != nil {
		return CanonicalIndexDefinition{}, fmt.Errorf("decode listIndexes definition: %w", err)
	}
	return canonicalIndexDefinition(spec, "", false)
}

// IndexConsistencyStats 通过 mongos 执行首阶段 $indexStats 并要求 4.2.4+ consistency 字段。
func (c *Conn) IndexConsistencyStats(ctx context.Context, database, collection string, maxTime time.Duration) ([]CanonicalIndexDefinition, error) {
	aggregateOptions := options.Aggregate()
	if maxTime > 0 {
		aggregateOptions.SetMaxTime(maxTime)
	}
	cursor, err := c.Client.Database(database).Collection(collection).Aggregate(
		ctx,
		[]bson.D{{{Key: "$indexStats", Value: bson.D{}}}},
		aggregateOptions,
	)
	if err != nil {
		return nil, err
	}
	defer closeMongoCursor(ctx, cursor)
	var result []CanonicalIndexDefinition
	for cursor.Next(ctx) {
		definition, err := decodeIndexStatsDefinition(cursor.Current)
		if err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// CheckMetadataIndexConsistency 执行 7.x official command 并完整消费 command cursor。
func (c *Conn) CheckMetadataIndexConsistency(ctx context.Context, request MetadataConsistencyRequest) ([]MetadataIndexInconsistency, error) {
	if c == nil || c.Client == nil {
		return nil, fmt.Errorf("MongoDB connection is required")
	}
	return collectMetadataIndexConsistency(ctx, request, func(runCtx context.Context, database string, command bson.D) (bson.Raw, error) {
		return c.Client.Database(database).RunCommand(runCtx, command).DecodeBytes()
	})
}

func collectMetadataIndexConsistency(ctx context.Context, request MetadataConsistencyRequest, runner rawCommandRunner) (_ []MetadataIndexInconsistency, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(request.Database) == "" {
		return nil, fmt.Errorf("metadata consistency database is required")
	}
	if request.BatchSize <= 0 {
		request.BatchSize = 100
	}
	scope := any(int32(1))
	if request.Collection != "" {
		scope = request.Collection
	}
	command := bson.D{
		{Key: "checkMetadataConsistency", Value: scope},
		{Key: "checkIndexes", Value: true},
		{Key: "cursor", Value: bson.D{{Key: "batchSize", Value: request.BatchSize}}},
	}
	if request.MaxTime > 0 {
		command = append(command, bson.E{Key: "maxTimeMS", Value: request.MaxTime.Milliseconds()})
	}

	var cursorID int64
	var cursorCollection string
	defer func() {
		if cursorID == 0 || cursorCollection == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mongoCleanupTimeout)
		defer cancel()
		_, _ = runner(cleanupCtx, request.Database, bson.D{
			{Key: "killCursors", Value: cursorCollection},
			{Key: "cursors", Value: bson.A{cursorID}},
		})
	}()

	raw, err := runner(ctx, request.Database, command)
	if err != nil {
		return nil, err
	}
	response, err := decodeMetadataCursorResponse(raw)
	if err != nil {
		return nil, err
	}
	cursorID = response.Cursor.ID
	cursorCollection, err = metadataCursorCollection(request.Database, response.Cursor.Namespace)
	if err != nil {
		return nil, err
	}
	result, err := decodeMetadataIndexIssues(response.Cursor.FirstBatch)
	if err != nil {
		return nil, err
	}

	for cursorID != 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		getMore := bson.D{
			{Key: "getMore", Value: cursorID},
			{Key: "collection", Value: cursorCollection},
			{Key: "batchSize", Value: request.BatchSize},
		}
		raw, err = runner(ctx, request.Database, getMore)
		if err != nil {
			return result, err
		}
		response, err = decodeMetadataCursorResponse(raw)
		if err != nil {
			return result, err
		}
		if _, namespaceErr := metadataCursorCollection(request.Database, response.Cursor.Namespace); namespaceErr != nil {
			return result, namespaceErr
		}
		cursorID = response.Cursor.ID
		batch, decodeErr := decodeMetadataIndexIssues(response.Cursor.NextBatch)
		if decodeErr != nil {
			return result, decodeErr
		}
		result = append(result, batch...)
	}
	return result, nil
}

func decodeMetadataCursorResponse(raw bson.Raw) (metadataCursorResponse, error) {
	var response metadataCursorResponse
	if err := bson.Unmarshal(raw, &response); err != nil {
		return metadataCursorResponse{}, fmt.Errorf("decode metadata consistency cursor: %w", err)
	}
	return response, nil
}

func decodeMetadataIndexIssues(rawIssues []bson.Raw) ([]MetadataIndexInconsistency, error) {
	result := make([]MetadataIndexInconsistency, 0, len(rawIssues))
	for _, rawIssue := range rawIssues {
		var raw struct {
			SourceType string `bson:"type"`
			Details    struct {
				Namespace string   `bson:"namespace"`
				Shard     string   `bson:"shard"`
				Info      bson.Raw `bson:"info"`
			} `bson:"details"`
		}
		if err := bson.Unmarshal(rawIssue, &raw); err != nil {
			return nil, fmt.Errorf("decode metadata consistency issue: %w", err)
		}
		if strings.TrimSpace(raw.Details.Namespace) == "" {
			return nil, ErrIndexConsistencyFieldsMissing
		}
		var info struct {
			IndexName              string   `bson:"indexName"`
			MissingFromShards      []string `bson:"missingFromShards"`
			InconsistentProperties []struct {
				Key string `bson:"k"`
			} `bson:"inconsistentProperties"`
		}
		if len(raw.Details.Info) > 0 {
			if err := bson.Unmarshal(raw.Details.Info, &info); err != nil {
				return nil, fmt.Errorf("decode metadata consistency index info: %w", err)
			}
		}
		issue := MetadataIndexInconsistency{
			SourceType:        sanitizeMetadataSourceType(raw.SourceType),
			Namespace:         raw.Details.Namespace,
			IndexName:         info.IndexName,
			MissingFromShards: append([]string(nil), info.MissingFromShards...),
			Shard:             raw.Details.Shard,
			PropertiesDiffer:  len(info.InconsistentProperties) > 0,
		}
		if len(raw.Details.Info) > 0 {
			digest := sha256.Sum256(raw.Details.Info)
			issue.Fingerprint = hex.EncodeToString(digest[:])
		}
		for _, property := range info.InconsistentProperties {
			if property.Key != "" {
				issue.InconsistentFields = append(issue.InconsistentFields, property.Key)
			}
		}
		sort.Strings(issue.MissingFromShards)
		issue.MissingFromShards = deduplicateStrings(issue.MissingFromShards)
		sort.Strings(issue.InconsistentFields)
		issue.InconsistentFields = deduplicateStrings(issue.InconsistentFields)
		result = append(result, issue)
	}
	return result, nil
}

func sanitizeMetadataSourceType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return "unknown"
		}
	}
	return value
}

func metadataCursorCollection(database, namespace string) (string, error) {
	prefix := database + "."
	if !strings.HasPrefix(namespace, prefix) || len(namespace) == len(prefix) {
		return "", fmt.Errorf("metadata consistency cursor namespace is invalid")
	}
	return strings.TrimPrefix(namespace, prefix), nil
}

func deduplicateStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func canonicalIndexDefinition(spec bson.D, shard string, building bool) (CanonicalIndexDefinition, error) {
	result := CanonicalIndexDefinition{
		FieldFingerprints: make(map[string]string),
		Building:          building,
		Shard:             shard,
	}
	semantic := make(bson.D, 0, len(spec))
	full := make(bson.D, 0, len(spec))
	for _, element := range spec {
		if element.Key == "ns" {
			continue
		}
		fieldFingerprint, err := fingerprintBSON(bson.D{{Key: element.Key, Value: element.Value}})
		if err != nil {
			return CanonicalIndexDefinition{}, fmt.Errorf("fingerprint index field %q: %w", element.Key, err)
		}
		result.FieldFingerprints[element.Key] = fieldFingerprint
		full = append(full, element)
		switch element.Key {
		case "name":
			name, ok := element.Value.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return CanonicalIndexDefinition{}, fmt.Errorf("index name is missing")
			}
			result.Name = name
		case "key":
			key, err := indexKeyDocument(element.Value)
			if err != nil {
				return CanonicalIndexDefinition{}, err
			}
			for _, keyElement := range key {
				result.Key = append(result.Key, IndexKeySnapshot{Field: keyElement.Key, Order: fmt.Sprint(keyElement.Value)})
			}
			semantic = append(semantic, element)
		default:
			semantic = append(semantic, element)
		}
	}
	if result.Name == "" {
		return CanonicalIndexDefinition{}, fmt.Errorf("index name is missing")
	}
	if len(result.Key) == 0 {
		return CanonicalIndexDefinition{}, fmt.Errorf("index key is missing")
	}
	sort.SliceStable(semantic, func(i, j int) bool { return semantic[i].Key < semantic[j].Key })
	sort.SliceStable(full, func(i, j int) bool { return full[i].Key < full[j].Key })
	var err error
	result.SemanticFingerprint, err = fingerprintBSON(semantic)
	if err != nil {
		return CanonicalIndexDefinition{}, fmt.Errorf("fingerprint semantic index definition: %w", err)
	}
	result.FullFingerprint, err = fingerprintBSON(full)
	if err != nil {
		return CanonicalIndexDefinition{}, fmt.Errorf("fingerprint full index definition: %w", err)
	}
	return result, nil
}

func fingerprintBSON(document bson.D) (string, error) {
	payload, err := bson.MarshalExtJSON(document, true, false)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func indexKeyDocument(value any) (bson.D, error) {
	switch typed := value.(type) {
	case bson.D:
		return typed, nil
	case bson.Raw:
		var result bson.D
		if err := bson.Unmarshal(typed, &result); err != nil {
			return nil, fmt.Errorf("decode index key: %w", err)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("index key has unsupported BSON type %T", value)
	}
}

func routingChunkFilter(namespace string, collectionMetadata bson.D) (bson.D, error) {
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("routing namespace is required")
	}
	for _, element := range collectionMetadata {
		if element.Key != "uuid" {
			continue
		}
		uuid, ok := element.Value.(primitive.Binary)
		if !ok || len(uuid.Data) == 0 {
			break
		}
		return bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "uuid", Value: uuid}},
			bson.D{{Key: "ns", Value: namespace}},
		}}}, nil
	}
	return bson.D{{Key: "ns", Value: namespace}}, nil
}
