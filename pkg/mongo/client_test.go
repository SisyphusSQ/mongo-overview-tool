package mongo

import (
	"context"
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
)

func getClient() (*Conn, error) {
	log.New(true)

	// repl
	cli, err := NewMongoConn("mongodb://user:pwd@mongod:27017/admin")

	// sharding
	//cli, err := NewMongoConn("mongodb://user:pwd@mongos:27017/admin")

	if err != nil {
		return nil, err
	}
	return cli, nil
}

func TestIsSharding(t *testing.T) {
	cli, err := getClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	info, err := cli.IsSharding(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Log(info)
}

func TestIsMaster(t *testing.T) {
	cli, err := getClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	info, err := cli.IsMaster(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	j, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(j))
}

func TestRsStatus(t *testing.T) {
	cli, err := getClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	info, err := cli.RsStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	j, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(j))
}

func TestListShards(t *testing.T) {
	cli, err := getClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	info, err := cli.ListShards(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	j, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(j))
}

func TestServerStatus(t *testing.T) {
	cli, err := getClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	info, err := cli.ServerStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	j, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(j))
}

func TestDBStatus(t *testing.T) {
	cli, err := getClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ctx := context.Background()
	dbs, err := cli.Client.ListDatabaseNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}

	for _, db := range dbs {
		t.Log("db:", db)
		info, err := cli.DBStatus(ctx, db)
		if err != nil {
			t.Fatal(err)
		}

		j, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		t.Log(string(j))
		println()
	}
}
