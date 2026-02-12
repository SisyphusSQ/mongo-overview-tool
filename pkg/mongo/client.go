package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
)

type Conn struct {
	URI    string
	Client *mongo.Client
}

// BulkUpdateResult 表示一次批量更新的统计结果。
type BulkUpdateResult struct {
	Matched  int64 // 命中的文档数
	Modified int64 // 实际变更的文档数
}

func NewMongoConn(uri string) (*Conn, error) {
	clientOps := options.Client().ApplyURI(uri)

	isMulti, err := utils.IsMultiHosts(uri)
	if err != nil {
		return nil, err
	}

	if !isMulti {
		clientOps.SetDirect(true)
	} else {
		// read pref
		clientOps.SetReadPreference(readpref.Primary())
	}

	// create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// connect
	client, err := mongo.Connect(ctx, clientOps)
	if err != nil {
		return nil, fmt.Errorf("connect to %s failed: %v", utils.BlockPassword(uri, "***"), err)
	}

	// ping
	if err = client.Ping(ctx, clientOps.ReadPreference); err != nil {
		return nil, fmt.Errorf("ping to %v failed: %v\n"+
			"If Mongo Server is standalone(single node) Or conn address is different with mongo server address"+
			" try atandalone mode by mongodb://ip:port/admin?connect=direct",
			utils.BlockPassword(uri, "***"), err)
	}

	l.Logger.Debugf("New session to %s successfully", utils.BlockPassword(uri, "***"))
	return &Conn{
		URI:    uri,
		Client: client,
	}, nil
}

func (c *Conn) Close() error {
	l.Logger.Debugf("Close client with %s", utils.BlockPassword(c.URI, "***"))
	return c.Client.Disconnect(context.Background())
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
	agg := bson.A{
		bson.D{{"$match", bson.D{{"queryHash", bson.D{{"$ne", primitive.Null{}}}}}}},
		bson.D{
			{"$group",
				bson.D{
					{"_id",
						bson.D{
							{"ns", "$ns"},
							{"queryHash", "$queryHash"},
						},
					},
					{"ns", bson.D{{"$first", "$ns"}}},
					{"op", bson.D{{"$first", "$op"}}},
					{"queryHash", bson.D{{"$first", "$queryHash"}}},
					{"cmd", bson.D{{"$first", "$command"}}},
					{"cnt", bson.D{{"$sum", 1}}},
					{"maxMills", bson.D{{"$max", "$millis"}}},
					{"minMills", bson.D{{"$min", "$millis"}}},
					{"maxDocs", bson.D{{"$max", "$docsExamined"}}},
					{"maxTs", bson.D{{"$max", "$ts"}}},
					{"minTs", bson.D{{"$min", "$ts"}}},
				},
			},
		},
		bson.D{{"$sort", bson.D{{sort, -1}}}},
		bson.D{
			{"$project",
				bson.D{
					{"_id", 0},
					{"ns", 1},
					{"op", 1},
					{"queryHash", 1},
					//{"cmd", 0},
					{"cnt", 1},
					{"maxMills", 1},
					{"minMills", 1},
					{"maxDocs", 1},
					{"maxTs", 1},
					{"minTs", 1},
				},
			},
		},
	}

	cur, err := c.Client.Database(db).Collection("system.profile").
		Aggregate(ctx, agg)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	if err = cur.All(ctx, &result); err != nil {
		return nil, err
	}
	for _, r := range result {
		r.DB = db
	}
	return
}

func (c *Conn) GetSlowDetail(ctx context.Context, db, hash string) (result bson.M, err error) {
	err = c.Client.Database(db).Collection("system.profile").
		FindOne(ctx, bson.M{"queryHash": hash}, options.FindOne().SetSort(bson.M{"ts": -1})).
		Decode(&result)
	return
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
		SetProjection(bson.D{{"_id", 1}}).
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
	filter := bson.D{{"_id", bson.D{{"$in", ids}}}}
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
	filter := bson.D{{"_id", bson.D{{"$in", ids}}}}
	result, err := c.Client.Database(db).Collection(coll).UpdateMany(ctx, filter, update)
	if err != nil {
		return BulkUpdateResult{}, err
	}
	return BulkUpdateResult{
		Matched:  result.MatchedCount,
		Modified: result.ModifiedCount,
	}, nil
}
