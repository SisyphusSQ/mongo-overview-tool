package mongo

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestDecodeServerStatusSnapshotPreservesMissingAndZero(t *testing.T) {
	// 场景：MongoDB 旧版本字段缺失与服务器真实返回 0 必须保持不同 presence。
	payload, err := bson.Marshal(bson.D{
		{Key: "version", Value: "4.4.0"},
		{Key: "uptime", Value: int64(0)},
		{Key: "connections", Value: bson.D{{Key: "current", Value: int64(0)}}},
		{Key: "wiredTiger", Value: bson.D{{Key: "cache", Value: bson.D{
			{Key: "maximum bytes configured", Value: int64(100)},
			{Key: "bytes currently in the cache", Value: int64(0)},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := decodeServerStatusSnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Uptime == nil || *snapshot.Uptime != 0 {
		t.Fatalf("uptime = %#v, want present zero", snapshot.Uptime)
	}
	if snapshot.Connections.Current == nil || *snapshot.Connections.Current != 0 {
		t.Fatalf("connections.current = %#v, want present zero", snapshot.Connections.Current)
	}
	if snapshot.Connections.Available != nil {
		t.Fatalf("connections.available = %#v, want missing", snapshot.Connections.Available)
	}
	if snapshot.WiredTiger.Cache.BytesInCache == nil || *snapshot.WiredTiger.Cache.BytesInCache != 0 {
		t.Fatalf("cache bytes = %#v, want present zero", snapshot.WiredTiger.Cache.BytesInCache)
	}
}

func TestBuildCurrentOperationsPipelineProjectsOnlySafeFields(t *testing.T) {
	// 场景：currentOp 必须在服务端过滤/投影，默认 pipeline 不传输 command、client、user 或 session。
	pipeline := buildCurrentOperationsPipeline(CurrentOperationsQuery{
		MinDuration: 2 * time.Second,
		AllUsers:    true,
		Databases:   []string{"db1"},
		Namespaces:  []string{"db1.c1"},
		Limit:       100,
	})
	payload, err := bson.MarshalExtJSON(bson.D{{Key: "pipeline", Value: pipeline}}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"command", "client", "effectiveUsers", "lsid", "transaction.parameters"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("pipeline contains forbidden field %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"$currentOp", "$match", "$project", "$limit", "secs_running", "waitingForLock"} {
		if !strings.Contains(text, required) {
			t.Fatalf("pipeline missing %q: %s", required, text)
		}
	}
}

func TestCurrentOperationLegacyFixtureOmitsUnavailableAndRawFields(t *testing.T) {
	// 场景：MongoDB 3.4 currentOp 没有 queryHash/planSummary，raw command 即使存在也不能进入安全 DTO。
	payload, err := bson.Marshal(bson.D{
		{Key: "host", Value: "node"},
		{Key: "ns", Value: "app.orders"},
		{Key: "op", Value: "query"},
		{Key: "secsRunning", Value: int64(0)},
		{Key: "command", Value: bson.D{{Key: "find", Value: "secret"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot CurrentOperationSnapshot
	if err := bson.Unmarshal(payload, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.QueryHash != "" || snapshot.PlanSummary != "" || snapshot.SecondsRunning == nil || *snapshot.SecondsRunning != 0 {
		t.Fatalf("legacy snapshot = %#v", snapshot)
	}
}

func TestDecodeTopSnapshotPreservesNamespaceCounters(t *testing.T) {
	// 场景：top 的 namespace read/write count 与 time 必须保留，未知分类不影响解码。
	metrics := bson.D{
		{Key: "queries", Value: bson.D{{Key: "time", Value: int64(20)}, {Key: "count", Value: int64(2)}}},
		{Key: "getmore", Value: bson.D{{Key: "time", Value: int64(10)}, {Key: "count", Value: int64(1)}}},
		{Key: "insert", Value: bson.D{{Key: "time", Value: int64(30)}, {Key: "count", Value: int64(3)}}},
	}
	payload, err := bson.Marshal(bson.D{{Key: "totals", Value: bson.D{{Key: "db.c", Value: metrics}}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeTopSnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := result.Namespaces["db.c"]
	if got.ReadCount != 3 || got.ReadTimeMicros != 30 || got.WriteCount != 3 || got.WriteTimeMicros != 30 {
		t.Fatalf("top counters = %#v", got)
	}
}

func TestCurrentOperationFallbackAppliesNamespaceBoundary(t *testing.T) {
	// 场景：旧 currentOp command fallback 也必须遵守 namespace/database 过滤，不能扩大可见范围。
	query := CurrentOperationsQuery{Databases: []string{"app"}}
	if !currentOperationNamespaceAllowed("app.orders", query) {
		t.Fatal("app.orders should be allowed")
	}
	if currentOperationNamespaceAllowed("other.orders", query) {
		t.Fatal("other.orders should be rejected")
	}
	query.Namespaces = []string{"app.users"}
	if currentOperationNamespaceAllowed("app.orders", query) || !currentOperationNamespaceAllowed("app.users", query) {
		t.Fatal("explicit namespace filter must take precedence")
	}
}

func TestDecodeServerStatusVersionFixturesPreserveOptionalMetrics(t *testing.T) {
	// 场景：3.4/4.4/6.x/7.x/8.x 风格 fixture 的缺失字段不能解码为伪造零值，新字段出现时需保留 presence。
	fixtures := []struct {
		version       string
		includeModern bool
		includeQueues bool
	}{{"3.4.24", false, false}, {"4.4.29", false, false}, {"6.0.20", true, false}, {"7.0.18", true, false}, {"8.0.8", true, true}}
	for _, fixture := range fixtures {
		t.Run(fixture.version, func(t *testing.T) {
			document := bson.D{{Key: "version", Value: fixture.version}, {Key: "opcounters", Value: bson.D{{Key: "query", Value: int64(0)}}}}
			if fixture.includeModern {
				document = append(document, bson.E{Key: "connections", Value: bson.D{{Key: "rejected", Value: int64(0)}}}, bson.E{Key: "opLatencies", Value: bson.D{{Key: "reads", Value: bson.D{{Key: "latency", Value: int64(0)}, {Key: "ops", Value: int64(0)}}}}})
			}
			if fixture.includeQueues {
				document = append(document,
					bson.E{Key: "queues", Value: bson.D{{Key: "execution", Value: bson.D{{Key: "reads", Value: bson.D{{Key: "totalTimeQueuedMicros", Value: int64(0)}}}}}}},
					bson.E{Key: "futureMongoDBField", Value: bson.D{{Key: "unknownMetric", Value: int64(1)}}},
				)
			}
			payload, err := bson.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := decodeServerStatusSnapshot(payload)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.OpCounters.Query == nil || *snapshot.OpCounters.Query != 0 {
				t.Fatalf("query presence = %#v", snapshot.OpCounters.Query)
			}
			if fixture.includeModern && (snapshot.Connections.Rejected == nil || snapshot.OpLatencies.Reads.Ops == nil) {
				t.Fatalf("modern metrics missing: %#v", snapshot)
			}
			if !fixture.includeModern && snapshot.Connections.Rejected != nil {
				t.Fatalf("legacy rejected = %#v, want missing", snapshot.Connections.Rejected)
			}
			if fixture.includeQueues && (snapshot.Queues.Execution.Reads.TotalTimeQueuedMicros == nil || *snapshot.Queues.Execution.Reads.TotalTimeQueuedMicros != 0) {
				t.Fatalf("8.x queue presence = %#v", snapshot.Queues.Execution.Reads.TotalTimeQueuedMicros)
			}
		})
	}
}
