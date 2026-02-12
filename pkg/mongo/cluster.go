package mongo

import (
	"context"
	"fmt"

	l "github.com/SisyphusSQ/mongo-overview-tool/pkg/log"
)

type ClusterType string
type NodeType string

const (
	ClusterRepl  ClusterType = "repl"
	ClusterShard ClusterType = "sharding"

	NodePrimary   NodeType = "PRIMARY"
	NodeSecondary NodeType = "SECONDARY"
	NodeArbiter   NodeType = "ARBITER"
)

type ClusterInfo struct {
	Type ClusterType
	Repl map[string]string // replName -> replUri
}

func DetectCluster(ctx context.Context, conn *Conn) (*ClusterInfo, error) {
	info := &ClusterInfo{
		Repl: make(map[string]string),
	}

	isSharding, err := conn.IsSharding(ctx)
	if err != nil {
		return nil, err
	}

	if isSharding {
		info.Type = ClusterShard
		shStatus, err := conn.ListShards(ctx)
		if err != nil {
			l.Logger.Errorf("Failed to get shStatus: %v", err)
			return nil, err
		}

		for _, sh := range shStatus.Shards {
			uri := sh.GetUri()
			if uri == "" {
				return nil, fmt.Errorf("sh uri is empty, shardRs: %s", sh.Id)
			}
			info.Repl[sh.Id] = uri
		}
	} else {
		info.Type = ClusterRepl
		master, err := conn.IsMaster(ctx)
		if err != nil {
			l.Logger.Errorf("Failed to get rsStatus: %v", err)
			return nil, err
		}

		info.Repl[master.SetName] = master.Me
	}

	return info, nil
}
