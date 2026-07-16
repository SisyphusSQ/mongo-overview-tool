package mongo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestCanonicalIndexDefinitionPreservesOrderAndUnknownFields(t *testing.T) {
	// 场景：key 顺序、partial/TTL 等持久化选项和未知字段都必须参与 fingerprint，但业务值不得进入安全 DTO。
	base := bson.D{
		{Key: "v", Value: int32(2)},
		{Key: "key", Value: bson.D{{Key: "tenant", Value: int32(1)}, {Key: "createdAt", Value: int32(-1)}}},
		{Key: "name", Value: "tenant_1_createdAt_-1"},
		{Key: "partialFilterExpression", Value: bson.D{{Key: "state", Value: "private-value"}}},
		{Key: "futurePersistentOption", Value: bson.D{{Key: "mode", Value: "strict"}}},
	}

	definition, err := canonicalIndexDefinition(base, "shard-a", false)
	if err != nil {
		t.Fatal(err)
	}
	wantKey := []IndexKeySnapshot{{Field: "tenant", Order: "1"}, {Field: "createdAt", Order: "-1"}}
	if !reflect.DeepEqual(definition.Key, wantKey) {
		t.Fatalf("key = %#v, want %#v", definition.Key, wantKey)
	}
	if definition.SemanticFingerprint == "" || definition.FullFingerprint == "" || definition.FieldFingerprints["futurePersistentOption"] == "" {
		t.Fatalf("fingerprints are incomplete: %#v", definition)
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(definition.SemanticFingerprint)), "private-value") {
		t.Fatal("fingerprint leaked partial filter value")
	}

	reordered := append(bson.D(nil), base...)
	reordered[1].Value = bson.D{{Key: "createdAt", Value: int32(-1)}, {Key: "tenant", Value: int32(1)}}
	reorderedDefinition, err := canonicalIndexDefinition(reordered, "shard-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if definition.SemanticFingerprint == reorderedDefinition.SemanticFingerprint {
		t.Fatal("reordered key produced the same semantic fingerprint")
	}

	topLevelReordered := bson.D{base[4], base[3], base[2], base[1], base[0]}
	topLevelDefinition, err := canonicalIndexDefinition(topLevelReordered, "shard-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if definition.SemanticFingerprint != topLevelDefinition.SemanticFingerprint || definition.FullFingerprint != topLevelDefinition.FullFingerprint {
		t.Fatal("top-level field order changed canonical fingerprints")
	}

	changedUnknown := append(bson.D(nil), base...)
	changedUnknown[4].Value = bson.D{{Key: "mode", Value: "relaxed"}}
	changedDefinition, err := canonicalIndexDefinition(changedUnknown, "shard-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if definition.SemanticFingerprint == changedDefinition.SemanticFingerprint {
		t.Fatal("unknown persistent option did not affect semantic fingerprint")
	}
}

func TestDecodeMongo34ListIndexesFixture(t *testing.T) {
	// 场景：3.4 direct listIndexes 的完整持久化 spec 通过 ordered BSON fixture 进入 canonical definition。
	definition, err := decodeListIndexDefinition(rawFixture(t, "list_indexes_3_4.json"))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Name != "tenant_1_createdAt_-1" || len(definition.Key) != 2 || definition.SemanticFingerprint == "" || definition.FieldFingerprints["expireAfterSeconds"] == "" {
		t.Fatalf("definition = %#v", definition)
	}
}

func TestCanonicalIndexDefinitionPersistentOptionsAffectFingerprint(t *testing.T) {
	// 场景：常见 persistent options 与 text/geo/wildcard/storageEngine 字段变化不能被静默忽略。
	base := bson.D{
		{Key: "v", Value: int32(2)},
		{Key: "key", Value: bson.D{{Key: "tenant", Value: int32(1)}}},
		{Key: "name", Value: "tenant_1"},
	}
	baseDefinition, err := canonicalIndexDefinition(base, "shard-a", false)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "unique", field: "unique", value: true},
		{name: "sparse", field: "sparse", value: true},
		{name: "ttl", field: "expireAfterSeconds", value: int64(3600)},
		{name: "partial", field: "partialFilterExpression", value: bson.D{{Key: "state", Value: "fixture-private"}}},
		{name: "collation", field: "collation", value: bson.D{{Key: "locale", Value: "en"}, {Key: "strength", Value: int32(2)}}},
		{name: "hidden", field: "hidden", value: true},
		{name: "wildcard", field: "wildcardProjection", value: bson.D{{Key: "private.path", Value: int32(0)}}},
		{name: "text", field: "weights", value: bson.D{{Key: "title", Value: int32(10)}}},
		{name: "geo", field: "2dsphereIndexVersion", value: int32(3)},
		{name: "storage engine", field: "storageEngine", value: bson.D{{Key: "wiredTiger", Value: bson.D{{Key: "configString", Value: "block_compressor=zstd"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := append(bson.D(nil), base...)
			spec = append(spec, bson.E{Key: tt.field, Value: tt.value})
			definition, err := canonicalIndexDefinition(spec, "shard-b", false)
			if err != nil {
				t.Fatal(err)
			}
			if definition.SemanticFingerprint == baseDefinition.SemanticFingerprint || definition.FieldFingerprints[tt.field] == "" {
				t.Fatalf("option %q did not affect fingerprint: %#v", tt.field, definition)
			}
		})
	}

	for name, key := range map[string]bson.D{
		"hashed":   {{Key: "tenant", Value: "hashed"}},
		"text":     {{Key: "title", Value: "text"}},
		"geo":      {{Key: "location", Value: "2dsphere"}},
		"wildcard": {{Key: "$**", Value: int32(1)}},
	} {
		t.Run(name+" key", func(t *testing.T) {
			spec := bson.D{{Key: "v", Value: int32(2)}, {Key: "key", Value: key}, {Key: "name", Value: name + "_index"}}
			definition, err := canonicalIndexDefinition(spec, "shard-b", false)
			if err != nil || definition.SemanticFingerprint == baseDefinition.SemanticFingerprint {
				t.Fatalf("definition = %#v, err = %v", definition, err)
			}
		})
	}
}

func TestRoutingChunkFilterSupportsLegacyNamespaceAndUUID(t *testing.T) {
	// 场景：3.4 风格 routing 只按 ns 查询，带 uuid 的新 schema 同时保留 ns fallback，内部 schema 不外泄。
	legacy, err := routingChunkFilter("app.orders", bson.D{{Key: "_id", Value: "app.orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, bson.D{{Key: "ns", Value: "app.orders"}}) {
		t.Fatalf("legacy filter = %#v", legacy)
	}

	uuid := primitive.Binary{Subtype: 4, Data: []byte("0123456789abcdef")}
	modern, err := routingChunkFilter("app.orders", bson.D{{Key: "_id", Value: "app.orders"}, {Key: "uuid", Value: uuid}})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "$or", Value: bson.A{bson.D{{Key: "uuid", Value: uuid}}, bson.D{{Key: "ns", Value: "app.orders"}}}}}
	if !reflect.DeepEqual(modern, want) {
		t.Fatalf("modern filter = %#v, want %#v", modern, want)
	}
}

func TestDecodeIndexStatsDefinitionRequires42Patch4Fields(t *testing.T) {
	// 场景：4.2.4+ 的 shard/spec/building 能生成安全定义；4.2.3 风格缺少 spec 时必须触发 direct fallback。
	modern := rawFixture(t, "index_stats_4_2_4.json")
	definition, err := decodeIndexStatsDefinition(modern)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Shard != "shard-a" || definition.Name != "tenant_1" || !definition.Building {
		t.Fatalf("definition = %#v", definition)
	}

	legacy := rawFixture(t, "index_stats_4_2_3.json")
	if _, err := decodeIndexStatsDefinition(legacy); !errors.Is(err, ErrIndexConsistencyFieldsMissing) {
		t.Fatalf("legacy decode error = %v, want ErrIndexConsistencyFieldsMissing", err)
	}
	healthy := rawFixture(t, "index_stats_6_0.json")
	healthyDefinition, err := decodeIndexStatsDefinition(healthy)
	if err != nil || healthyDefinition.Building {
		t.Fatalf("healthy definition = %#v, err = %v", healthyDefinition, err)
	}
	invalidBuilding := rawDocument(t, bson.D{
		{Key: "shard", Value: "shard-a"}, {Key: "building", Value: "yes"},
		{Key: "spec", Value: bson.D{{Key: "key", Value: bson.D{{Key: "tenant", Value: 1}}}, {Key: "name", Value: "tenant_1"}}},
	})
	if _, err := decodeIndexStatsDefinition(invalidBuilding); !errors.Is(err, ErrIndexConsistencyFieldsMissing) {
		t.Fatalf("invalid building error = %v, want ErrIndexConsistencyFieldsMissing", err)
	}
}

func TestMetadataConsistencyVersionedCursorFixtures(t *testing.T) {
	// 场景：7.x 脱敏 fixture 必须保留 firstBatch/getMore 形状并安全归一化两类索引问题。
	responses := []bson.Raw{rawFixture(t, "metadata_7_first.json"), rawFixture(t, "metadata_7_next.json")}
	runner := rawCommandRunner(func(_ context.Context, _ string, _ bson.D) (bson.Raw, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	})
	issues, err := collectMetadataIndexConsistency(context.Background(), MetadataConsistencyRequest{Database: "app", BatchSize: 1}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].SourceType != "InconsistentIndex" || issues[1].SourceType != "MissingShardKeyIndex" || !issues[0].PropertiesDiffer || issues[0].Fingerprint == "" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestCollectMetadataIndexConsistencyConsumesGetMore(t *testing.T) {
	// 场景：7.x official command 的 firstBatch 和 nextBatch 必须全部消费，并仅保留索引域安全字段。
	firstIssue := bson.D{
		{Key: "type", Value: "InconsistentIndex"},
		{Key: "description", Value: "must not be exposed"},
		{Key: "details", Value: bson.D{
			{Key: "namespace", Value: "app.orders"},
			{Key: "info", Value: bson.D{
				{Key: "missingFromShards", Value: bson.A{"shard-b"}},
				{Key: "inconsistentProperties", Value: bson.A{}},
				{Key: "indexName", Value: "tenant_1"},
			}},
		}},
	}
	secondIssue := bson.D{
		{Key: "type", Value: "MissingShardKeyIndex"},
		{Key: "details", Value: bson.D{
			{Key: "namespace", Value: "app.orders"},
			{Key: "shard", Value: "shard-a"},
			{Key: "shardKey", Value: bson.D{{Key: "privateBusinessKey", Value: 1}}},
		}},
	}
	responses := []bson.Raw{
		rawDocument(t, bson.D{
			{Key: "cursor", Value: bson.D{
				{Key: "id", Value: int64(42)},
				{Key: "ns", Value: "app.$cmd.aggregate"},
				{Key: "firstBatch", Value: bson.A{firstIssue}},
			}},
			{Key: "ok", Value: 1},
		}),
		rawDocument(t, bson.D{
			{Key: "cursor", Value: bson.D{
				{Key: "id", Value: int64(0)},
				{Key: "ns", Value: "app.$cmd.aggregate"},
				{Key: "nextBatch", Value: bson.A{secondIssue}},
			}},
			{Key: "ok", Value: 1},
		}),
	}
	var commands []bson.D
	runner := rawCommandRunner(func(_ context.Context, database string, command bson.D) (bson.Raw, error) {
		if database != "app" {
			t.Fatalf("database = %q, want app", database)
		}
		commands = append(commands, command)
		if len(responses) == 0 {
			return nil, errors.New("unexpected command")
		}
		response := responses[0]
		responses = responses[1:]
		return response, nil
	})

	issues, err := collectMetadataIndexConsistency(context.Background(), MetadataConsistencyRequest{
		Database: "app", BatchSize: 1, MaxTime: time.Second,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %#v", issues)
	}
	if issues[0].SourceType != "InconsistentIndex" || !reflect.DeepEqual(issues[0].MissingFromShards, []string{"shard-b"}) || issues[0].IndexName != "tenant_1" || issues[0].Fingerprint == "" {
		t.Fatalf("first issue = %#v", issues[0])
	}
	if issues[1].SourceType != "MissingShardKeyIndex" || issues[1].Shard != "shard-a" {
		t.Fatalf("second issue = %#v", issues[1])
	}
	if len(commands) != 2 || commandValue(commands[1], "getMore") != int64(42) || commandValue(commands[1], "collection") != "$cmd.aggregate" {
		t.Fatalf("commands = %#v", commands)
	}
	for _, issue := range issues {
		if strings.Contains(strings.ToLower(strings.TrimSpace(issue.SourceType)), "privatebusinesskey") {
			t.Fatalf("issue leaked shard key: %#v", issue)
		}
	}
}

func TestCollectMetadataIndexConsistencyStopsAndKillsCursorOnCancel(t *testing.T) {
	// 场景：context 取消后不得继续 getMore；仍有 cursor 时使用独立 cleanup context 尝试关闭。
	ctx, cancel := context.WithCancel(context.Background())
	var commands []bson.D
	runner := rawCommandRunner(func(runCtx context.Context, _ string, command bson.D) (bson.Raw, error) {
		commands = append(commands, command)
		if len(commands) == 1 {
			cancel()
			return rawDocument(t, bson.D{{Key: "cursor", Value: bson.D{
				{Key: "id", Value: int64(99)},
				{Key: "ns", Value: "app.$cmd.aggregate"},
				{Key: "firstBatch", Value: bson.A{}},
			}}, {Key: "ok", Value: 1}}), nil
		}
		if runCtx.Err() != nil {
			t.Fatalf("cleanup command inherited cancelled context: %v", runCtx.Err())
		}
		return rawDocument(t, bson.D{{Key: "ok", Value: 1}}), nil
	})

	_, err := collectMetadataIndexConsistency(ctx, MetadataConsistencyRequest{Database: "app"}, runner)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(commands) != 2 || commandValue(commands[1], "killCursors") != "$cmd.aggregate" {
		t.Fatalf("commands = %#v, want initial command plus killCursors", commands)
	}
}

func TestCollectMetadataIndexConsistencyRejectsMissingCursorNamespace(t *testing.T) {
	// 场景：official command 缺少 cursor contract 时必须 fallback，不能把空响应误判为健康。
	runner := rawCommandRunner(func(context.Context, string, bson.D) (bson.Raw, error) {
		return rawDocument(t, bson.D{{Key: "cursor", Value: bson.D{{Key: "id", Value: int64(0)}, {Key: "firstBatch", Value: bson.A{}}}}, {Key: "ok", Value: 1}}), nil
	})
	if _, err := collectMetadataIndexConsistency(context.Background(), MetadataConsistencyRequest{Database: "app"}, runner); err == nil {
		t.Fatal("missing cursor namespace was accepted")
	}
}

func TestIndexRoutingSnapshotBuildsIndependentExpectedShardSet(t *testing.T) {
	// 场景：expected shards 只来自 routing chunks，必须去重稳定排序；没有 chunk 仍是已分片但 coverage 不完整。
	metadata := bson.D{{Key: "_id", Value: "app.orders"}, {Key: "key", Value: bson.D{{Key: "tenant", Value: 1}}}}
	chunks := []bson.Raw{
		rawDocument(t, bson.D{{Key: "shard", Value: "shard-b"}}),
		rawDocument(t, bson.D{{Key: "shard", Value: "shard-a"}}),
		rawDocument(t, bson.D{{Key: "shard", Value: "shard-b"}}),
	}
	snapshot, err := indexRoutingSnapshot("app.orders", metadata, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Sharded || !reflect.DeepEqual(snapshot.ExpectedShards, []string{"shard-a", "shard-b"}) {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	empty, err := indexRoutingSnapshot("app.empty", metadata, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Sharded || len(empty.ExpectedShards) != 0 {
		t.Fatalf("empty snapshot = %#v", empty)
	}

	unsharded, err := indexRoutingSnapshot("app.local", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unsharded.Sharded {
		t.Fatalf("unsharded snapshot = %#v", unsharded)
	}
}

func TestDecodeBuildInfoVersionRequiresExactVersion(t *testing.T) {
	// 场景：patch 断点不能只靠 wire version；buildInfo 缺少可解析 version 时必须失败而不是猜测。
	version, err := decodeBuildInfoVersion(rawDocument(t, bson.D{{Key: "version", Value: "4.2.4"}, {Key: "ok", Value: 1}}))
	if err != nil || version != "4.2.4" {
		t.Fatalf("version = %q, err = %v", version, err)
	}
	if _, err := decodeBuildInfoVersion(rawDocument(t, bson.D{{Key: "ok", Value: 1}})); err == nil {
		t.Fatal("missing buildInfo.version was accepted")
	}
}

func rawDocument(t *testing.T, document bson.D) bson.Raw {
	t.Helper()
	payload, err := bson.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return bson.Raw(payload)
}

func rawFixture(t *testing.T, name string) bson.Raw {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "index_consistency", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw bson.Raw
	if err := bson.UnmarshalExtJSON(payload, true, &raw); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return raw
}

func commandValue(command bson.D, key string) any {
	for _, element := range command {
		if element.Key == key {
			return element.Value
		}
	}
	return nil
}
