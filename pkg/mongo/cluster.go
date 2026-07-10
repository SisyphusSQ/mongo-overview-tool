package mongo

import (
	"context"
	"fmt"
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
			return nil, fmt.Errorf("list shards: %w", err)
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
			return nil, fmt.Errorf("get replica set primary: %w", err)
		}

		info.Repl[master.SetName] = master.Me
	}

	return info, nil
}
