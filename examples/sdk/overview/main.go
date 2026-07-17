package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
)

func main() {
	uri := os.Getenv("MOT_MONGO_URI")
	if uri == "" {
		log.Fatal("set MOT_MONGO_URI before running this example")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := mot.NewClient(ctx, mot.Options{URI: uri})
	if err != nil {
		log.Fatal(err)
	}
	defer closeClient(client)

	result, err := client.Overview(ctx, mot.OverviewOptions{IncludeHosts: true})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("cluster=%s replicaSets=%d hosts=%d\n", result.ClusterType, len(result.ReplicaSets), len(result.Hosts))
}

func closeClient(client *mot.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		log.Printf("close client: %v", err)
	}
}
