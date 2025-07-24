package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/utils"
)

type Conn struct {
	URI    string
	Client *mongo.Client
}

func NewMongoConn(uri string) (*Conn, error) {
	clientOps := options.Client().ApplyURI(uri)
	clientOps.SetDirect(true)

	// read pref
	//clientOps.SetReadPreference(readpref.Primary())

	// create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
