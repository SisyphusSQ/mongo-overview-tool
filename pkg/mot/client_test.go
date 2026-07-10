package mot

import (
	"context"
	"errors"
	"testing"

	drivermongo "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestNewClientFromMongoClientDoesNotOwnConnectionByDefault(t *testing.T) {
	// 测试注入已有 mongo.Client 时，默认 Close 不会断开调用方连接池。
	client, err := drivermongo.NewClient(options.Client().ApplyURI("mongodb://127.0.0.1:27017/admin"))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	motClient, err := NewClientFromMongoClient(context.Background(), client, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClientFromMongoClient failed: %v", err)
	}
	if err := motClient.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestInjectedClientRequiresURIForMemberConnections(t *testing.T) {
	// 测试注入 client 未提供 URI 时，成员级能力返回明确配置错误。
	client, err := drivermongo.NewClient(options.Client().ApplyURI("mongodb://127.0.0.1:27017/admin"))
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	motClient, err := NewClientFromMongoClient(context.Background(), client, ClientOptions{})
	if err != nil {
		t.Fatalf("NewClientFromMongoClient failed: %v", err)
	}

	if _, err := motClient.Overview(context.Background(), OverviewOptions{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("Overview error = %v, want ErrInvalidOptions", err)
	}
	if _, err := motClient.SlowlogSummary(context.Background(), SlowlogOptions{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("SlowlogSummary error = %v, want ErrInvalidOptions", err)
	}
}
