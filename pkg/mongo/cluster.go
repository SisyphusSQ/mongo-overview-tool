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
	Type           ClusterType
	Repl           map[string]string // replName -> replUri
	MaxWireVersion int
}

func DetectCluster(ctx context.Context, conn *Conn) (*ClusterInfo, error) {
	info, err := DetectClusterTopology(ctx, conn)
	if err != nil {
		return nil, err
	}
	if info.Type != ClusterShard {
		return info, nil
	}
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
	return info, nil
}

// DetectClusterTopology 只通过 hello/isMaster 识别入口拓扑，不要求 listShards 权限。
func DetectClusterTopology(ctx context.Context, conn *Conn) (*ClusterInfo, error) {
	info := &ClusterInfo{
		Repl: make(map[string]string),
	}

	master, err := conn.IsMaster(ctx)
	if err != nil {
		return nil, err
	}
	info.MaxWireVersion = master.MaxWireVersion

	if master.Msg == "isdbgrid" {
		info.Type = ClusterShard
	} else {
		info.Type = ClusterRepl
		info.Repl[master.SetName] = master.Me
	}

	return info, nil
}
