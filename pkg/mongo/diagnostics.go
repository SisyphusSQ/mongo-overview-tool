package mongo

import (
	"context"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CurrentOperationsQuery 只描述底层 currentOp 过滤和成本边界，不包含产品告警阈值。
type CurrentOperationsQuery struct {
	MinDuration             time.Duration
	AllUsers                bool
	IncludeIdleTransactions bool
	IncludeIdleCursors      bool
	Databases               []string
	Namespaces              []string
	Limit                   int
	MaxTime                 time.Duration
}

// OplogWindowSnapshot 是 oplog 首尾时间戳形成的有界窗口。
type OplogWindowSnapshot struct {
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
}

// OplogWindow 只按 $natural 首尾各读取一条，不扫描 oplog 内容。
func (c *Conn) OplogWindow(ctx context.Context, maxTime time.Duration) (OplogWindowSnapshot, error) {
	collection := c.Client.Database("local").Collection("oplog.rs")
	find := func(direction int) (primitive.Timestamp, error) {
		findOptions := options.FindOne().SetSort(bson.D{{Key: "$natural", Value: direction}}).SetProjection(bson.D{{Key: "_id", Value: 0}, {Key: "ts", Value: 1}})
		if maxTime > 0 {
			findOptions.SetMaxTime(maxTime)
		}
		var document struct {
			Ts primitive.Timestamp `bson:"ts"`
		}
		if err := collection.FindOne(ctx, bson.D{}, findOptions).Decode(&document); err != nil {
			return primitive.Timestamp{}, err
		}
		return document.Ts, nil
	}
	earliest, err := find(1)
	if err != nil {
		return OplogWindowSnapshot{}, err
	}
	latest, err := find(-1)
	if err != nil {
		return OplogWindowSnapshot{}, err
	}
	return OplogWindowSnapshot{Earliest: time.Unix(int64(earliest.T), 0).UTC(), Latest: time.Unix(int64(latest.T), 0).UTC()}, nil
}

// CurrentOperationSnapshot 是从 $currentOp 投影得到的安全字段集合。
type CurrentOperationSnapshot struct {
	Host                  string `bson:"host" json:"host,omitempty"`
	Shard                 string `bson:"shard" json:"shard,omitempty"`
	Namespace             string `bson:"ns" json:"namespace,omitempty"`
	Operation             string `bson:"op" json:"operation,omitempty"`
	AppName               string `bson:"appName" json:"appName,omitempty"`
	QueryHash             string `bson:"queryHash" json:"queryHash,omitempty"`
	PlanSummary           string `bson:"planSummary" json:"planSummary,omitempty"`
	SecondsRunning        *int64 `bson:"secsRunning" json:"secondsRunning,omitempty"`
	WaitingForLock        bool   `bson:"waitingForLock" json:"waitingForLock"`
	WaitingForFlowControl bool   `bson:"waitingForFlowControl" json:"waitingForFlowControl"`
	KillPending           bool   `bson:"killPending" json:"killPending"`
	TransactionActive     bool   `bson:"transactionActive" json:"transactionActive"`
	TransactionMicros     *int64 `bson:"transactionMicros" json:"transactionMicros,omitempty"`
	Message               string `bson:"message" json:"message,omitempty"`
	ProgressDone          *int64 `bson:"progressDone" json:"progressDone,omitempty"`
	ProgressTotal         *int64 `bson:"progressTotal" json:"progressTotal,omitempty"`
}

// CurrentOperations 优先使用 $currentOp aggregation 并在服务端完成过滤与投影。
func (c *Conn) CurrentOperations(ctx context.Context, query CurrentOperationsQuery) ([]CurrentOperationSnapshot, error) {
	pipeline := buildCurrentOperationsPipeline(query)
	aggregateOptions := options.Aggregate()
	if query.MaxTime > 0 {
		aggregateOptions.SetMaxTime(query.MaxTime)
	}
	cursor, err := c.Client.Database("admin").Aggregate(ctx, pipeline, aggregateOptions)
	if err != nil {
		return nil, err
	}
	defer closeMongoCursor(ctx, cursor)
	var result []CurrentOperationSnapshot
	if err := cursor.All(ctx, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CurrentOperationsCommand 是旧版本的受控 fallback；仍只解码安全字段。
func (c *Conn) CurrentOperationsCommand(ctx context.Context, query CurrentOperationsQuery) ([]CurrentOperationSnapshot, error) {
	command := bson.D{
		{Key: "currentOp", Value: 1},
		{Key: "$all", Value: query.AllUsers},
		{Key: "$ownOps", Value: !query.AllUsers},
	}
	if query.MaxTime > 0 {
		command = append(command, bson.E{Key: "maxTimeMS", Value: query.MaxTime.Milliseconds()})
	}
	var response struct {
		Operations []struct {
			Host                  string `bson:"host"`
			Shard                 string `bson:"shard"`
			Namespace             string `bson:"ns"`
			Operation             string `bson:"op"`
			AppName               string `bson:"appName"`
			QueryHash             string `bson:"queryHash"`
			PlanSummary           string `bson:"planSummary"`
			SecondsRunning        *int64 `bson:"secs_running"`
			WaitingForLock        bool   `bson:"waitingForLock"`
			WaitingForFlowControl bool   `bson:"waitingForFlowControl"`
			KillPending           bool   `bson:"killPending"`
			Transaction           bson.M `bson:"transaction"`
			Message               string `bson:"msg"`
			Progress              struct {
				Done  *int64 `bson:"done"`
				Total *int64 `bson:"total"`
			} `bson:"progress"`
		} `bson:"inprog"`
	}
	if err := c.Client.Database("admin").RunCommand(ctx, command).Decode(&response); err != nil {
		return nil, err
	}
	result := make([]CurrentOperationSnapshot, 0, len(response.Operations))
	for _, raw := range response.Operations {
		operation := CurrentOperationSnapshot{
			Host: raw.Host, Shard: raw.Shard, Namespace: raw.Namespace, Operation: raw.Operation,
			AppName: raw.AppName, QueryHash: raw.QueryHash, PlanSummary: raw.PlanSummary,
			SecondsRunning: raw.SecondsRunning, WaitingForLock: raw.WaitingForLock,
			WaitingForFlowControl: raw.WaitingForFlowControl, KillPending: raw.KillPending,
			TransactionActive: len(raw.Transaction) > 0, Message: raw.Message,
			ProgressDone: raw.Progress.Done, ProgressTotal: raw.Progress.Total,
		}
		operation.TransactionMicros = diagnosticNestedInt64(raw.Transaction, "timeOpenMicros")
		if !currentOperationNamespaceAllowed(operation.Namespace, query) {
			continue
		}
		if operation.SecondsRunning != nil && time.Duration(*operation.SecondsRunning)*time.Second < query.MinDuration &&
			!operation.WaitingForLock && !operation.WaitingForFlowControl && !operation.TransactionActive && operation.Message == "" {
			continue
		}
		result = append(result, operation)
		if query.Limit > 0 && len(result) >= query.Limit {
			break
		}
	}
	return result, nil
}

func currentOperationNamespaceAllowed(namespace string, query CurrentOperationsQuery) bool {
	if len(query.Namespaces) > 0 {
		for _, allowed := range query.Namespaces {
			if namespace == allowed {
				return true
			}
		}
		return false
	}
	if len(query.Databases) > 0 {
		for _, database := range query.Databases {
			if strings.HasPrefix(namespace, database+".") {
				return true
			}
		}
		return false
	}
	return true
}

func diagnosticNestedInt64(document bson.M, key string) *int64 {
	if len(document) == 0 {
		return nil
	}
	value, ok := document[key]
	if !ok {
		return nil
	}
	converted := diagnosticInt64(value)
	return &converted
}

func buildCurrentOperationsPipeline(query CurrentOperationsQuery) drivermongo.Pipeline {
	currentOp := bson.D{
		{Key: "allUsers", Value: query.AllUsers},
		{Key: "idleConnections", Value: query.IncludeIdleCursors},
		{Key: "idleCursors", Value: query.IncludeIdleCursors},
		{Key: "idleSessions", Value: query.IncludeIdleTransactions},
		{Key: "localOps", Value: false},
	}
	minSeconds := int64(query.MinDuration / time.Second)
	if minSeconds < 0 {
		minSeconds = 0
	}
	conditions := bson.A{
		bson.D{{Key: "secs_running", Value: bson.D{{Key: "$gte", Value: minSeconds}}}},
		bson.D{{Key: "waitingForLock", Value: true}},
		bson.D{{Key: "waitingForFlowControl", Value: true}},
		bson.D{{Key: "transaction", Value: bson.D{{Key: "$exists", Value: true}}}},
		bson.D{{Key: "msg", Value: bson.D{{Key: "$exists", Value: true}}}},
		bson.D{{Key: "progress", Value: bson.D{{Key: "$exists", Value: true}}}},
	}
	match := bson.D{{Key: "$or", Value: conditions}}
	if len(query.Namespaces) > 0 {
		match = append(match, bson.E{Key: "ns", Value: bson.D{{Key: "$in", Value: query.Namespaces}}})
	} else if len(query.Databases) > 0 {
		databasePatterns := make([]string, 0, len(query.Databases))
		for _, database := range query.Databases {
			databasePatterns = append(databasePatterns, regexp.QuoteMeta(database))
		}
		match = append(match, bson.E{Key: "ns", Value: bson.D{{Key: "$regex", Value: "^(?:" + strings.Join(databasePatterns, "|") + `)\.`}}})
	}
	project := bson.D{
		{Key: "_id", Value: 0},
		{Key: "host", Value: 1},
		{Key: "shard", Value: 1},
		{Key: "ns", Value: 1},
		{Key: "op", Value: 1},
		{Key: "appName", Value: "$appName"},
		{Key: "queryHash", Value: 1},
		{Key: "planSummary", Value: 1},
		{Key: "secsRunning", Value: "$secs_running"},
		{Key: "waitingForLock", Value: 1},
		{Key: "waitingForFlowControl", Value: 1},
		{Key: "killPending", Value: 1},
		{Key: "transactionActive", Value: bson.D{{Key: "$ne", Value: bson.A{"$transaction", nil}}}},
		{Key: "transactionMicros", Value: "$transaction.timeOpenMicros"},
		{Key: "message", Value: "$msg"},
		{Key: "progressDone", Value: "$progress.done"},
		{Key: "progressTotal", Value: "$progress.total"},
	}
	pipeline := drivermongo.Pipeline{
		bson.D{{Key: "$currentOp", Value: currentOp}},
		bson.D{{Key: "$match", Value: match}},
		bson.D{{Key: "$project", Value: project}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "waitingForLock", Value: -1}, {Key: "transactionActive", Value: -1}, {Key: "secsRunning", Value: -1}, {Key: "ns", Value: 1}}}},
	}
	if query.Limit > 0 {
		pipeline = append(pipeline, bson.D{{Key: "$limit", Value: query.Limit}})
	}
	return pipeline
}

// ServerStatusSnapshot 只保留诊断所需数值，并用指针区分字段缺失和真实零值。
type ServerStatusSnapshot struct {
	Version string `bson:"version" json:"version"`
	Uptime  *int64 `bson:"uptime" json:"uptime,omitempty"`

	Connections struct {
		Current      *int64 `bson:"current" json:"current,omitempty"`
		Available    *int64 `bson:"available" json:"available,omitempty"`
		TotalCreated *int64 `bson:"totalCreated" json:"totalCreated,omitempty"`
		Rejected     *int64 `bson:"rejected" json:"rejected,omitempty"`
	} `bson:"connections" json:"connections"`

	Global struct {
		CurrentQueue struct {
			Readers *int64 `bson:"readers" json:"readers,omitempty"`
			Writers *int64 `bson:"writers" json:"writers,omitempty"`
			Total   *int64 `bson:"total" json:"total,omitempty"`
		} `bson:"currentQueue" json:"currentQueue"`
	} `bson:"globalLock" json:"globalLock"`

	WiredTiger struct {
		Cache struct {
			MaximumBytesConfigured *int64 `bson:"maximum bytes configured" json:"maximumBytesConfigured,omitempty"`
			BytesInCache           *int64 `bson:"bytes currently in the cache" json:"bytesInCache,omitempty"`
			ApplicationEviction    *int64 `bson:"application threads page read from disk to cache count" json:"applicationEviction,omitempty"`
			PagesReadIntoCache     *int64 `bson:"pages read into cache" json:"pagesReadIntoCache,omitempty"`
			PagesWrittenFromCache  *int64 `bson:"pages written from cache" json:"pagesWrittenFromCache,omitempty"`
		} `bson:"cache" json:"cache"`
		ConcurrentTransactions struct {
			Read struct {
				Available *int64 `bson:"available" json:"available,omitempty"`
				Out       *int64 `bson:"out" json:"out,omitempty"`
			} `bson:"read" json:"read"`
			Write struct {
				Available *int64 `bson:"available" json:"available,omitempty"`
				Out       *int64 `bson:"out" json:"out,omitempty"`
			} `bson:"write" json:"write"`
		} `bson:"concurrentTransactions" json:"concurrentTransactions"`
	} `bson:"wiredTiger" json:"wiredTiger"`

	Queues struct {
		Execution struct {
			Reads struct {
				TotalTimeQueuedMicros *int64 `bson:"totalTimeQueuedMicros" json:"totalTimeQueuedMicros,omitempty"`
			} `bson:"reads" json:"reads"`
			Writes struct {
				TotalTimeQueuedMicros *int64 `bson:"totalTimeQueuedMicros" json:"totalTimeQueuedMicros,omitempty"`
			} `bson:"writes" json:"writes"`
		} `bson:"execution" json:"execution"`
	} `bson:"queues" json:"queues"`

	OpCounters struct {
		Insert  *int64 `bson:"insert" json:"insert,omitempty"`
		Query   *int64 `bson:"query" json:"query,omitempty"`
		Update  *int64 `bson:"update" json:"update,omitempty"`
		Delete  *int64 `bson:"delete" json:"delete,omitempty"`
		GetMore *int64 `bson:"getmore" json:"getMore,omitempty"`
		Command *int64 `bson:"command" json:"command,omitempty"`
	} `bson:"opcounters" json:"opcounters"`

	Network struct {
		BytesIn  *int64 `bson:"bytesIn" json:"bytesIn,omitempty"`
		BytesOut *int64 `bson:"bytesOut" json:"bytesOut,omitempty"`
	} `bson:"network" json:"network"`

	Metrics struct {
		Document struct {
			Deleted  *int64 `bson:"deleted" json:"deleted,omitempty"`
			Inserted *int64 `bson:"inserted" json:"inserted,omitempty"`
			Returned *int64 `bson:"returned" json:"returned,omitempty"`
			Updated  *int64 `bson:"updated" json:"updated,omitempty"`
		} `bson:"document" json:"document"`
	} `bson:"metrics" json:"metrics"`

	OpLatencies struct {
		Reads struct {
			Latency *int64 `bson:"latency" json:"latency,omitempty"`
			Ops     *int64 `bson:"ops" json:"ops,omitempty"`
		} `bson:"reads" json:"reads"`
		Writes struct {
			Latency *int64 `bson:"latency" json:"latency,omitempty"`
			Ops     *int64 `bson:"ops" json:"ops,omitempty"`
		} `bson:"writes" json:"writes"`
		Commands struct {
			Latency *int64 `bson:"latency" json:"latency,omitempty"`
			Ops     *int64 `bson:"ops" json:"ops,omitempty"`
		} `bson:"commands" json:"commands"`
	} `bson:"opLatencies" json:"opLatencies"`
}

// TopNamespaceCounter 是 top 命令中 namespace 级的累计逻辑读写计数。
type TopNamespaceCounter struct {
	ReadCount       int64 `json:"readCount"`
	WriteCount      int64 `json:"writeCount"`
	ReadTimeMicros  int64 `json:"readTimeMicros"`
	WriteTimeMicros int64 `json:"writeTimeMicros"`
}

// TopSnapshot 保留热点差分所需的 namespace 累计计数。
type TopSnapshot struct {
	Namespaces map[string]TopNamespaceCounter `json:"namespaces"`
}

// Top 执行只读 top 命令；调用方必须保证目标是 mongod 数据节点而不是 mongos。
func (c *Conn) Top(ctx context.Context, maxTime time.Duration) (TopSnapshot, error) {
	command := bson.D{{Key: "top", Value: 1}}
	if maxTime > 0 {
		millis := maxTime.Milliseconds()
		if millis == 0 {
			millis = 1
		}
		command = append(command, bson.E{Key: "maxTimeMS", Value: millis})
	}
	var raw bson.Raw
	if err := c.Client.Database("admin").RunCommand(ctx, command).Decode(&raw); err != nil {
		return TopSnapshot{}, err
	}
	return decodeTopSnapshot(raw)
}

func decodeTopSnapshot(raw bson.Raw) (TopSnapshot, error) {
	var response struct {
		Totals bson.M `bson:"totals"`
	}
	if err := bson.Unmarshal(raw, &response); err != nil {
		return TopSnapshot{}, err
	}
	result := TopSnapshot{Namespaces: make(map[string]TopNamespaceCounter, len(response.Totals))}
	for namespace, value := range response.Totals {
		metrics, ok := value.(bson.M)
		if !ok {
			continue
		}
		counter := TopNamespaceCounter{}
		for _, category := range []string{"queries", "getmore"} {
			count, elapsed := topMetric(metrics[category])
			counter.ReadCount += count
			counter.ReadTimeMicros += elapsed
		}
		for _, category := range []string{"insert", "update", "remove"} {
			count, elapsed := topMetric(metrics[category])
			counter.WriteCount += count
			counter.WriteTimeMicros += elapsed
		}
		result.Namespaces[namespace] = counter
	}
	return result, nil
}

func topMetric(value any) (int64, int64) {
	metric, ok := value.(bson.M)
	if !ok {
		return 0, 0
	}
	return diagnosticInt64(metric["count"]), diagnosticInt64(metric["time"])
}

func diagnosticInt64(value any) int64 {
	switch number := value.(type) {
	case int:
		return int64(number)
	case int32:
		return int64(number)
	case int64:
		return number
	case float32:
		return int64(number)
	case float64:
		return int64(number)
	default:
		return 0
	}
}

// DiagnosticServerStatus 执行只读 serverStatus，并在可用时下推 maxTimeMS。
func (c *Conn) DiagnosticServerStatus(ctx context.Context, maxTime time.Duration) (ServerStatusSnapshot, error) {
	command := bson.D{{Key: "serverStatus", Value: 1}}
	if maxTime > 0 {
		millis := maxTime.Milliseconds()
		if millis == 0 {
			millis = 1
		}
		command = append(command, bson.E{Key: "maxTimeMS", Value: millis})
	}
	var raw bson.Raw
	if err := c.Client.Database("admin").RunCommand(ctx, command).Decode(&raw); err != nil {
		return ServerStatusSnapshot{}, err
	}
	return decodeServerStatusSnapshot(raw)
}

func decodeServerStatusSnapshot(raw bson.Raw) (ServerStatusSnapshot, error) {
	var snapshot ServerStatusSnapshot
	if err := bson.Unmarshal(raw, &snapshot); err != nil {
		return ServerStatusSnapshot{}, err
	}
	return snapshot, nil
}
