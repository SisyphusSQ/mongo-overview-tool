package mongo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

const mongoCleanupTimeout = 5 * time.Second

const legacySlowlogPrefix = "legacy:"

type Conn struct {
	URI    string
	Client *mongo.Client
}

// ConnOptions 控制 MongoDB 连接创建行为。
type ConnOptions struct {
	ConnectTimeout time.Duration
	Direct         *bool
}

// BulkUpdateResult 表示一次批量更新的统计结果。
type BulkUpdateResult struct {
	Matched  int64 // 命中的文档数
	Modified int64 // 实际变更的文档数
}

func NewMongoConn(uri string) (*Conn, error) {
	return NewMongoConnWithContext(context.Background(), uri, ConnOptions{
		ConnectTimeout: 10 * time.Second,
	})
}

// NewMongoConnWithContext 使用调用方传入的 context 创建 MongoDB 连接。
func NewMongoConnWithContext(ctx context.Context, uri string, connOpts ConnOptions) (*Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientOps := options.Client().ApplyURI(uri)

	if connOpts.Direct != nil {
		clientOps.SetDirect(*connOpts.Direct)
	} else {
		isMulti, err := isMultiHosts(uri)
		if err != nil {
			return nil, err
		}

		if !isMulti {
			clientOps.SetDirect(true)
		} else {
			// read pref
			clientOps.SetReadPreference(readpref.Primary())
		}
	}

	if connOpts.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, connOpts.ConnectTimeout)
		defer cancel()
	}

	// connect
	client, err := mongo.Connect(ctx, clientOps)
	if err != nil {
		return nil, fmt.Errorf("connect to %s failed: %w", redactURI(uri, "***"), err)
	}

	// ping
	if err = client.Ping(ctx, clientOps.ReadPreference); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mongoCleanupTimeout)
		cleanupErr := client.Disconnect(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			return nil, fmt.Errorf("ping to %v failed: %w; disconnect failed: %v",
				redactURI(uri, "***"), err, cleanupErr)
		}
		return nil, fmt.Errorf("ping to %v failed: %w\n"+
			"If Mongo Server is standalone(single node) Or conn address is different with mongo server address"+
			" try standalone mode by mongodb://ip:port/admin?connect=direct",
			redactURI(uri, "***"), err)
	}

	return &Conn{
		URI:    uri,
		Client: client,
	}, nil
}

func (c *Conn) Close() error {
	return c.CloseWithContext(context.Background())
}

// CloseWithContext 使用调用方传入的 context 关闭 MongoDB 连接。
func (c *Conn) CloseWithContext(ctx context.Context) error {
	if c == nil || c.Client == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.Client.Disconnect(ctx)
}

func (c *Conn) IsSharding(ctx context.Context) (isShard bool, err error) {
	master, err := c.IsMaster(ctx)
	if err != nil {
		return false, err
	}

	if master.Msg == "isdbgrid" {
		return true, nil
	}
	return false, nil
}

func (c *Conn) IsMaster(ctx context.Context) (result IsMaster, err error) {
	err = c.Client.Database("admin").RunCommand(ctx, bson.M{"isMaster": 1}).Decode(&result)
	return
}

func (c *Conn) RsStatus(ctx context.Context) (result RsStatus, err error) {
	err = c.Client.Database("admin").RunCommand(ctx, bson.M{"replSetGetStatus": 1}).Decode(&result)
	return
}

func (c *Conn) ListShards(ctx context.Context) (result ShStatus, err error) {
	err = c.Client.Database("admin").RunCommand(ctx, bson.M{"listShards": 1}).Decode(&result)
	return
}

func (c *Conn) ServerStatus(ctx context.Context) (result bson.M, err error) {
	err = c.Client.Database("admin").RunCommand(ctx, bson.M{"serverStatus": 1}).Decode(&result)
	return
}

func (c *Conn) DBStatus(ctx context.Context, db string) (result DBStats, err error) {
	err = c.Client.Database(db).RunCommand(ctx, bson.M{"dbStats": 1}).Decode(&result)
	return
}

func (c *Conn) GetSlowLogView(ctx context.Context, db, sort string) (result []*SlowlogView, err error) {
	errorCondition := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "$ne", Value: bson.A{"$errCode", nil}}},
		bson.D{{Key: "$ne", Value: bson.A{"$errName", nil}}},
	}}}
	errorCountExpression := bson.D{
		{Key: "$sum", Value: bson.D{
			{Key: "$cond", Value: bson.A{errorCondition, int64(1), int64(0)}},
		}},
	}
	collectionScanCondition := bson.D{{Key: "$eq", Value: bson.A{"$planSummary", "COLLSCAN"}}}
	collectionScanExpression := bson.D{
		{Key: "$sum", Value: bson.D{
			{Key: "$cond", Value: bson.A{collectionScanCondition, int64(1), int64(0)}},
		}},
	}
	agg := bson.A{
		bson.D{
			{Key: "$group",
				Value: bson.D{
					{Key: "_id",
						Value: bson.D{
							{Key: "ns", Value: "$ns"},
							{Key: "queryHash", Value: "$queryHash"},
							{Key: "op", Value: "$op"},
							{Key: "planSummary", Value: "$planSummary"},
						},
					},
					{Key: "ns", Value: bson.D{{Key: "$first", Value: "$ns"}}},
					{Key: "op", Value: bson.D{{Key: "$first", Value: "$op"}}},
					{Key: "queryHash", Value: bson.D{{Key: "$first", Value: "$queryHash"}}},
					{Key: "planSummary", Value: bson.D{{Key: "$first", Value: "$planSummary"}}},
					{Key: "cnt", Value: bson.D{{Key: "$sum", Value: 1}}},
					{Key: "maxMills", Value: bson.D{{Key: "$max", Value: "$millis"}}},
					{Key: "minMills", Value: bson.D{{Key: "$min", Value: "$millis"}}},
					{Key: "maxDocs", Value: bson.D{{Key: "$max", Value: "$docsExamined"}}},
					{Key: "maxKeysExamined", Value: bson.D{{Key: "$max", Value: "$keysExamined"}}},
					{Key: "maxDocsExamined", Value: bson.D{{Key: "$max", Value: "$docsExamined"}}},
					{Key: "maxDocsReturned", Value: bson.D{{Key: "$max", Value: "$nreturned"}}},
					{Key: "maxPlanningMicros", Value: bson.D{{Key: "$max", Value: "$planningTimeMicros"}}},
					{Key: "maxCpuNanos", Value: bson.D{{Key: "$max", Value: "$cpuNanos"}}},
					{Key: "appNames", Value: bson.D{{Key: "$addToSet", Value: "$appName"}}},
					{Key: "errorCount", Value: errorCountExpression},
					{Key: "collectionScanCount", Value: collectionScanExpression},
					{Key: "maxTs", Value: bson.D{{Key: "$max", Value: "$ts"}}},
					{Key: "minTs", Value: bson.D{{Key: "$min", Value: "$ts"}}},
				},
			},
		},
		bson.D{{Key: "$sort", Value: bson.D{{Key: sort, Value: -1}}}},
		bson.D{
			{Key: "$project",
				Value: bson.D{
					{Key: "_id", Value: 0},
					{Key: "ns", Value: 1},
					{Key: "op", Value: 1},
					{Key: "queryHash", Value: 1},
					{Key: "planSummary", Value: 1},
					{Key: "cnt", Value: 1},
					{Key: "maxMills", Value: 1},
					{Key: "minMills", Value: 1},
					{Key: "maxDocs", Value: 1},
					{Key: "maxKeysExamined", Value: 1},
					{Key: "maxDocsExamined", Value: 1},
					{Key: "maxDocsReturned", Value: 1},
					{Key: "maxPlanningMicros", Value: 1},
					{Key: "maxCpuNanos", Value: 1},
					{Key: "appNames", Value: 1},
					{Key: "errorCount", Value: 1},
					{Key: "collectionScanCount", Value: 1},
					{Key: "maxTs", Value: 1},
					{Key: "minTs", Value: 1},
				},
			},
		},
	}

	cur, err := c.Client.Database(db).Collection("system.profile").
		Aggregate(ctx, agg)
	if err != nil {
		return nil, err
	}
	defer closeMongoCursor(ctx, cur)
	if err = cur.All(ctx, &result); err != nil {
		return nil, err
	}
	for _, r := range result {
		r.DB = db
		if r.QueryHash == "" {
			r.QueryHash = legacySlowlogID(r.Ns, r.Op, r.PlanSummary)
		}
	}
	return
}

func (c *Conn) GetSlowDetail(ctx context.Context, db, hash string) (result bson.M, err error) {
	profile := c.Client.Database(db).Collection("system.profile")
	err = profile.
		FindOne(ctx, bson.M{"queryHash": hash}, options.FindOne().SetSort(bson.M{"ts": -1})).
		Decode(&result)
	if err == nil || !errors.Is(err, mongo.ErrNoDocuments) || !strings.HasPrefix(hash, legacySlowlogPrefix) {
		return result, err
	}

	cur, err := profile.Find(ctx, bson.D{{Key: "queryHash", Value: nil}}, options.Find().SetSort(bson.D{{Key: "ts", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer closeMongoCursor(ctx, cur)
	for cur.Next(ctx) {
		var candidate bson.M
		if err := cur.Decode(&candidate); err != nil {
			return nil, err
		}
		if legacySlowlogDocumentID(candidate) == hash {
			return candidate, nil
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return nil, mongo.ErrNoDocuments
}

func legacySlowlogDocumentID(document bson.M) string {
	return legacySlowlogID(
		stringValue(document["ns"]),
		stringValue(document["op"]),
		stringValue(document["planSummary"]),
	)
}

func legacySlowlogID(namespace, operation, planSummary string) string {
	digest := sha256.Sum256([]byte(namespace + "\x00" + operation + "\x00" + planSummary))
	return legacySlowlogPrefix + strings.ToUpper(hex.EncodeToString(digest[:8]))
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func closeMongoCursor(ctx context.Context, cursor *mongo.Cursor) {
	if cursor == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mongoCleanupTimeout)
	defer cancel()
	_ = cursor.Close(cleanupCtx)
}

// CountDocuments 统计指定集合中匹配 filter 的文档数量。
//
// 入参:
// - ctx: 上下文，用于超时与取消
// - db: 目标数据库名
// - coll: 目标集合名
// - filter: 查询过滤条件（bson.D 格式）
//
// 出参:
// - int64: 匹配的文档数量
// - error: 查询失败时非 nil
//
// 注意: 使用 mongo-driver 的 CountDocuments 方法，filter 为空时统计全部文档。
func (c *Conn) CountDocuments(ctx context.Context, db, coll string, filter any) (int64, error) {
	return c.Client.Database(db).Collection(coll).CountDocuments(ctx, filter)
}

// FindIDsCursor 打开一个只返回 _id 字段的游标，用于批量操作时收集文档 ID。
//
// 入参:
// - ctx: 上下文，用于超时与取消
// - db: 目标数据库名
// - coll: 目标集合名
// - filter: 查询过滤条件（bson.D 格式）
//
// 出参:
// - *mongo.Cursor: 游标对象，调用方需负责关闭
// - error: 查询失败时非 nil
//
// 注意: 设置 NoCursorTimeout 防止长时间操作游标超时；仅投影 _id 字段以减少网络开销。
func (c *Conn) FindIDsCursor(ctx context.Context, db, coll string, filter any) (*mongo.Cursor, error) {
	opts := options.Find().
		SetProjection(bson.D{{Key: "_id", Value: 1}}).
		SetNoCursorTimeout(true)
	return c.Client.Database(db).Collection(coll).Find(ctx, filter, opts)
}

// BulkDelete 根据一组 _id 批量删除文档。
//
// 入参:
// - ctx: 上下文，用于超时与取消
// - db: 目标数据库名
// - coll: 目标集合名
// - ids: 待删除文档的 _id 列表
//
// 出参:
// - int64: 实际删除的文档数量
// - error: 删除失败时非 nil
//
// 注意: 使用 deleteMany + $in 操作，单次调用删除一个批次的文档。
func (c *Conn) BulkDelete(ctx context.Context, db, coll string, ids []any) (int64, error) {
	filter := bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}
	result, err := c.Client.Database(db).Collection(coll).DeleteMany(ctx, filter)
	if err != nil {
		return 0, err
	}
	return result.DeletedCount, nil
}

// BulkUpdate 根据一组 _id 批量更新文档。
//
// 入参:
// - ctx: 上下文，用于超时与取消
// - db: 目标数据库名
// - coll: 目标集合名
// - ids: 待更新文档的 _id 列表
// - update: 更新操作（bson.D 格式），如 bson.D{{"$set", bson.D{{"status", "archived"}}}}
//
// 出参:
// - BulkUpdateResult: 批量更新统计结果（命中数与修改数）
// - error: 更新失败时非 nil
//
// 注意: 使用 UpdateMany + $in 操作批量更新同一批 _id 对应的文档，所有文档应用相同的 update 操作。
func (c *Conn) BulkUpdate(ctx context.Context, db, coll string, ids []any, update any) (BulkUpdateResult, error) {
	filter := bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}
	result, err := c.Client.Database(db).Collection(coll).UpdateMany(ctx, filter, update)
	if err != nil {
		return BulkUpdateResult{}, err
	}
	return BulkUpdateResult{
		Matched:  result.MatchedCount,
		Modified: result.ModifiedCount,
	}, nil
}
