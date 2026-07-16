package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

const exampleTimeout = 3 * time.Minute

type exampleConfig struct {
	ClientOptions mot.Options
	Database      string
	Collection    string
}

type indexAuditClient interface {
	IndexAudit(context.Context, mot.IndexAuditOptions) (*mot.IndexAuditResult, error)
}

type consistencySummary struct {
	State             mot.IndexConsistencyState    `json:"state"`
	Strategy          mot.IndexConsistencyStrategy `json:"strategy"`
	Coverage          mot.IndexConsistencyCoverage `json:"coverage"`
	ExpectedShards    int                          `json:"expectedShards"`
	ObservedShards    int                          `json:"observedShards"`
	Differences       int                          `json:"differences"`
	CollectorStatuses int                          `json:"collectorStatuses"`
	Fallback          bool                         `json:"fallback"`
	Partial           bool                         `json:"partial"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), exampleTimeout)
	defer cancel()

	client, err := mot.NewClient(ctx, config.ClientOptions)
	if err != nil {
		return fmt.Errorf("create SDK client: %w", err)
	}
	defer closeClient(client)

	summary, err := collectIndexConsistency(ctx, client, config)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func loadConfig(getenv func(string) string) (exampleConfig, error) {
	database := strings.TrimSpace(getenv("MOT_EXAMPLE_DATABASE"))
	collection := strings.TrimSpace(getenv("MOT_EXAMPLE_COLLECTION"))
	if database == "" || collection == "" {
		return exampleConfig{}, errors.New("set MOT_EXAMPLE_DATABASE and MOT_EXAMPLE_COLLECTION")
	}

	options := mot.DefaultOptions()
	options.ConnectTimeout = 15 * time.Second
	options.Direct = boolPointer(false)
	if uri := strings.TrimSpace(getenv("MOT_MONGO_URI")); uri != "" {
		options.URI = uri
		return exampleConfig{ClientOptions: options, Database: database, Collection: collection}, nil
	}

	host := strings.TrimSpace(getenv("MOT_MONGO_HOST"))
	portText := strings.TrimSpace(getenv("MOT_MONGO_PORT"))
	if host == "" || portText == "" {
		return exampleConfig{}, errors.New("set MOT_MONGO_URI or MOT_MONGO_HOST and MOT_MONGO_PORT")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return exampleConfig{}, errors.New("MOT_MONGO_PORT must be a positive integer")
	}
	username := strings.TrimSpace(getenv("MONGO_USER"))
	password := getenv("MONGO_PASS")
	if (username == "") != (password == "") {
		return exampleConfig{}, errors.New("MONGO_USER and MONGO_PASS must be provided together")
	}
	options.Host = host
	options.Port = port
	options.Username = username
	options.Password = password
	if authSource := strings.TrimSpace(getenv("MOT_MONGO_AUTH_SOURCE")); authSource != "" {
		options.AuthSource = authSource
	}
	return exampleConfig{ClientOptions: options, Database: database, Collection: collection}, nil
}

func collectIndexConsistency(ctx context.Context, client indexAuditClient, config exampleConfig) (consistencySummary, error) {
	result, err := client.IndexAudit(ctx, mot.IndexAuditOptions{
		Databases:      []string{config.Database},
		Collections:    []string{config.Collection},
		Checks:         []mot.IndexAuditCheck{mot.IndexCheckConsistency},
		MaxCollections: 1,
		Concurrency:    1,
	})
	partial := errors.Is(err, mot.ErrPartialResult)
	if err != nil && !partial {
		return consistencySummary{}, fmt.Errorf("index consistency: %w", err)
	}
	if result == nil || len(result.Collections) != 1 {
		return consistencySummary{}, errors.New("index consistency returned no selected collection")
	}
	collection := result.Collections[0]
	return consistencySummary{
		State:             collection.State,
		Strategy:          collection.Strategy,
		Coverage:          collection.Coverage,
		ExpectedShards:    len(collection.ExpectedShards),
		ObservedShards:    len(collection.ObservedShards),
		Differences:       len(collection.Differences),
		CollectorStatuses: len(result.CollectorStatuses),
		Fallback:          collection.Fallback != nil,
		Partial:           partial,
	}, nil
}

func boolPointer(value bool) *bool {
	return &value
}

func closeClient(client *mot.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		log.Printf("close client: %v", err)
	}
}
