package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/SisyphusSQ/mongo-overview-tool/v2/internal/config"
	l "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/log"
	"github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mot"
)

const sdkClientCloseTimeout = 5 * time.Second
const sdkClientCloseWarning = "failed to close SDK client; detail suppressed"

func sdkOptionsFromBase(cfg *config.BaseCfg) mot.Options {
	return mot.Options{
		URI:        cfg.BuildUri,
		AuthSource: cfg.AuthSource,
		Logger:     l.Logger,
	}
}

func closeSDKClient(client *mot.Client) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sdkClientCloseTimeout)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		l.Logger.Warnf(sdkClientCloseWarning)
	}
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
