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
	client, err := mongo.NewClient(clientOps)
	if err != nil {
		return nil, fmt.Errorf("new client failed: %v", err)
	}
	if err = client.Connect(ctx); err != nil {
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

func (c *Conn) IsGood() bool {
	if err := c.Client.Ping(nil, nil); err != nil {
		return false
	}

	return true
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
