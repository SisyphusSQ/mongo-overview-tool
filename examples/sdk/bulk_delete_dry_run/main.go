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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mot.NewClient(ctx, mot.Options{URI: uri})
	if err != nil {
		log.Fatal(err)
	}
	defer closeClient(client)

	result, err := client.BulkDelete(ctx, mot.BulkOptions{
		Database:   "example",
		Collection: "events",
		Filter:     map[string]any{"status": "expired"},
		BatchSize:  1000,
		DryRun:     true,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("dryRun=%v matched=%d\n", result.DryRun, result.MatchedTotal)
}

func closeClient(client *mot.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		log.Printf("close client: %v", err)
	}
}
