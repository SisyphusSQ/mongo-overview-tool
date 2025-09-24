package mongo

import (
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RsStatus struct {
	ClusterTime                ClusterTime         `json:"$clusterTime" bson:"$clusterTime"`
	Ok                         int                 `json:"ok" bson:"ok"`
	MyState                    ReplicaState        `json:"myState" bson:"myState"`
	Date                       time.Time           `json:"date" bson:"date"`
	OT                         primitive.Timestamp `json:"operationTime" bson:"operationTime"`
	Optimes                    Optimes             `json:"optimes" bson:"optimes"`
	ElMetrics                  ElMetrics           `json:"electionCandidateMetrics" bson:"electionCandidateMetrics"`
	HBInterval                 int                 `json:"heartbeatIntervalMillis" bson:"heartbeatIntervalMillis"`
	LastStableRecovery         primitive.Timestamp `json:"lastStableRecoveryTimestamp" bson:"lastStableRecoveryTimestamp"`
	MajVoteCount               int                 `json:"majorityVoteCount" bson:"majorityVoteCount"`
	Members                    []RsMember          `json:"members" bson:"members"`
	Set                        string              `json:"set"`
	SyncSourceHost             string              `json:"syncSourceHost"`
	SyncSourceId               int                 `json:"syncSourceId"`
	Term                       int                 `json:"term"`
	VotingMembersCount         int                 `json:"votingMembersCount"`
	WritableVotingMembersCount int                 `json:"writableVotingMembersCount"`
	WriteMajorityCount         int                 `json:"writeMajorityCount"`
}

type ReplicaState int

const (
	StateStartup    ReplicaState = iota // 0: 启动中，尚未加入副本集
	StatePrimary                        // 1: 主节点，负责所有写操作
	StateSecondary                      // 2: 从节点，复制主节点数据，提供读操作
	StateRecovering                     // 3: 恢复中，通常是刚启动或正在同步大量数据
	StateStartup2                       // 4: 启动2阶段，用于版本兼容
	StateUnknown                        // 5: 未知状态
	StateArbiter                        // 6: 仲裁节点，不存储数据，只参与选举
	StateDown                           // 7: 节点不可用
	StateRollback                       // 8: 回滚中，正在撤销不符合主节点的操作
	StateShun                           // 9: 节点被排斥，不参与副本集操作
)

// String 方法实现，返回状态的描述信息
func (s ReplicaState) String() string {
	switch s {
	case StateStartup:
		return "Startup (0) - 节点启动中，尚未加入副本集"
	case StatePrimary:
		return "Primary (1) - 主节点，处理所有写操作"
	case StateSecondary:
		return "Secondary (2) - 从节点，复制主节点数据，可处理读操作"
	case StateRecovering:
		return "Recovering (3) - 恢复中，可能正在同步数据"
	case StateStartup2:
		return "Startup2 (4) - 启动第二阶段，用于版本兼容性"
	case StateUnknown:
		return "Unknown (5) - 未知状态"
	case StateArbiter:
		return "Arbiter (6) - 仲裁节点，仅参与选举，不存储数据"
	case StateDown:
		return "Down (7) - 节点不可用"
	case StateRollback:
		return "Rollback (8) - 正在执行回滚操作"
	case StateShun:
		return "Shun (9) - 节点被排斥，不参与副本集活动"
	default:
		return fmt.Sprintf("Unknown state (%d) - 未定义的副本集状态", s)
	}
}

type ShStatus struct {
	ClusterTime ClusterTime         `json:"$clusterTime" bson:"$clusterTime"`
	Ok          int                 `json:"ok" bson:"ok"`
	OT          primitive.Timestamp `json:"operationTime" bson:"operationTime"`
	Shards      []Shard             `json:"shards" bson:"shards"`
}

type IsMaster struct {
	Hosts      []string           `json:"hosts" bson:"hosts"`
	Arbiters   []string           `json:"arbiters" bson:"arbiters"`
	SetName    string             `json:"setName" bson:"setName"`
	SetVersion int                `json:"setVersion" bson:"setVersion"`
	IsMaster   bool               `json:"isMaster" bson:"isMaster"`
	Secondary  bool               `json:"secondary" bson:"secondary"`
	Primary    string             `json:"primary" bson:"primary"`
	Me         string             `json:"me" bson:"me"`
	ElectionId primitive.ObjectID `json:"electionId" bson:"electionId"`
	LastWrite  struct {
		Optime            Optime    `json:"optime" bson:"optime"`
		LastWriteDate     time.Time `json:"lastWriteDate" bson:"lastWriteDate"`
		MajorityOpTime    Optime    `json:"majorityOpTime,omitempty" bson:"majorityOpTime"`
		MajorityWriteDate time.Time `json:"majorityWriteDate,omitempty" bson:"majorityWriteDate"`
	} `json:"lastWrite,omitempty" bson:"lastWrite"`
	MaxBsonObjectSize   int64     `json:"maxBsonObjectSize" bson:"maxBsonObjectSize"`
	MaxMessageSizeBytes int64     `json:"maxMessageSizeBytes" bson:"maxMessageSizeBytes"`
	MaxWriteBatchSize   int64     `json:"maxWriteBatchSize" bson:"maxWriteBatchSize"`
	LocalTime           time.Time `json:"localTime" bson:"localTime"`
	MaxWireVersion      int       `json:"maxWireVersion" bson:"maxWireVersion"`
	MinWireVersion      int       `json:"minWireVersion" bson:"minWireVersion"`
	ReadOnly            bool      `json:"readOnly" bson:"readOnly"`
	Ok                  int       `json:"ok" bson:"ok"`

	// in mongos
	Msg string `json:"msg" bson:"msg"`

	// in high version
	ClusterTime       ClusterTime         `json:"$clusterTime,omitempty" bson:"$clusterTime"`
	OT                primitive.Timestamp `json:"operationTime,omitempty" bson:"operationTime"`
	SessionTimeout    int                 `json:"logicalSessionTimeoutMinutes,omitempty" bson:"logicalSessionTimeoutMinutes"`
	ConnectionId      int                 `json:"connectionId,omitempty" bson:"connectionId"`
	IsWritablePrimary bool                `json:"IsWritablePrimary,omitempty" bson:"IsWritablePrimary"`
	TopologyVersion   struct {
		ProcessId primitive.ObjectID `json:"processId,omitempty" bson:"processId"`
		Counter   int64              `json:"counter,omitempty" bson:"counter"`
	}
}

type DBStats struct {
	ClusterTime ClusterTime         `json:"$clusterTime" bson:"$clusterTime"`
	Ok          int                 `json:"ok" bson:"ok"`
	AvgObjSize  float64             `json:"avgObjSize" bson:"avgObjSize"`
	Collections int                 `json:"collections" bson:"collections"`
	DataSize    int64               `json:"dataSize" bson:"dataSize"`
	DB          string              `json:"db" bson:"db"`
	FsTotalSize int64               `json:"fsTotalSize" bson:"fsTotalSize"`
	FsUsedSize  int64               `json:"fsUsedSize" bson:"fsUsedSize"`
	Indexes     int                 `json:"indexes" bson:"indexes"`
	NumExtents  int                 `json:"numExtents" bson:"numExtents"`
	Objects     int                 `json:"objects" bson:"objects"`
	OT          primitive.Timestamp `json:"operationTime" bson:"operationTime"`
	ScaleFactor int                 `json:"scaleFactor" bson:"scaleFactor"`
	StorageSize int64               `json:"storageSize" bson:"storageSize"`
	Views       int                 `json:"views" bson:"views"`
}

type CollStats struct {
	AvgObjSize     float64              `json:"avgObjSize" bson:"avgObjSize"`
	Capped         bool                 `json:"capped" bson:"capped"`
	Count          int64                `json:"count" bson:"count"`
	IndexSize      map[string]int64     `json:"indexSize" bson:"indexSize"`
	NIndexes       int                  `json:"nIndexes" bson:"nIndexes"`
	Ns             string               `json:"ns" bson:"ns"`
	Ok             int                  `json:"ok" bson:"ok"`
	ScaleFactor    int                  `json:"scaleFactor" bson:"scaleFactor"`
	Sharded        bool                 `json:"sharded" bson:"sharded"`
	Shards         map[string]CollStats `json:"shards" bson:"shards"`
	Size           int64                `json:"size" bson:"size"`
	StorageSize    int64                `json:"storageSize" bson:"storageSize"`
	TotalIndexSize int64                `json:"totalIndexSize" bson:"totalIndexSize"`
	WiredTiger     bson.M               `json:"wiredTiger" bson:"wiredTiger"`

	IndexBuilds bson.A `json:"indexBuilds,omitempty" bson:"indexBuilds"`
}

type ClusterTime struct {
	ClusterTime primitive.Timestamp `json:"clusterTime" bson:"clusterTime"`
	Hash        struct {
		Subtype int    `json:"Subtype" bson:"Subtype"`
		Data    string `json:"Data" bson:"Data"`
	} `json:"hash" bson:"hash"`
	KeyId int64 `json:"keyId" bson:"keyId"`
}

type Shard struct {
	Id           string              `json:"_id" bson:"_id"`
	Host         string              `json:"host" bson:"host"`
	State        int                 `json:"state" bson:"state"`
	TopologyTime primitive.Timestamp `json:"topologyTime" bson:"topologyTime"`
}

func (sh Shard) GetUri() string {
	ss := strings.Split(sh.Host, "/")
	if len(ss) != 2 {
		return ""
	}
	return ss[1]
}

type RsMember struct {
	Id                   int                 `json:"_id" bson:"_id"`
	ConfigTerm           int                 `json:"configTerm" bson:"configTerm"`
	ConfigVersion        int                 `json:"configVersion" bson:"configVersion"`
	ElectionDate         time.Time           `json:"electionDate" bson:"electionDate"`
	ElectionTime         primitive.Timestamp `json:"electionTime" bson:"electionTime"`
	Health               int                 `json:"health" bson:"health"`
	InfoMessage          string              `json:"infoMessage" bson:"infoMessage"`
	LastAppliedWallTime  time.Time           `json:"lastAppliedWallTime" bson:"lastAppliedWallTime"`
	LastDurableWallTime  time.Time           `json:"lastDurableWallTime" bson:"lastDurableWallTime"`
	LastHeartbeatMessage string              `json:"lastHeartbeatMessage" bson:"lastHeartbeatMessage"`
	Name                 string              `json:"name" bson:"name"`
	Optime               Optime              `json:"optime" bson:"optime"`
	OptimeDate           time.Time           `json:"optimeDate" bson:"optimeDate"`
	Self                 bool                `json:"self" bson:"self"`
	State                ReplicaState        `json:"state" bson:"state"`
	StateStr             string              `json:"stateStr" bson:"stateStr"`
	SyncSourceHost       string              `json:"syncSourceHost" bson:"syncSourceHost"`
	SyncSourceId         int                 `json:"syncSourceId" bson:"syncSourceId"`
	Uptime               int                 `json:"uptime" bson:"uptime"`
}

type Optimes struct {
	AppliedOpTime             Optime    `json:"appliedOpTime" bson:"appliedOpTime"`
	DurableOpTime             Optime    `json:"durableOpTime" bson:"durableOpTime"`
	LastAppliedWallTime       time.Time `json:"lastAppliedWallTime" bson:"lastAppliedWallTime"`
	LastCommittedOpTime       Optime    `json:"lastCommittedOpTime" bson:"lastCommittedOpTime"`
	LastCommittedWallTime     time.Time `json:"lastCommittedWallTime" bson:"lastCommittedWallTime"`
	LastDurableWallTime       time.Time `json:"lastDurableWallTime" bson:"lastDurableWallTime"`
	ReadConcernMajorityOpTime Optime    `json:"readConcernMajorityOpTime" bson:"readConcernMajorityOpTime"`
}

type ElMetrics struct {
	ElectionTerm                   int       `json:"electionTerm" bson:"electionTerm"`
	ElectionTimeoutMillis          int       `json:"electionTimeoutMillis" bson:"electionTimeoutMillis"`
	LastCommittedOpTimeAtElection  Optime    `json:"lastCommittedOpTimeAtElection" bson:"lastCommittedOpTimeAtElection"`
	LastElectionDate               time.Time `json:"lastElectionDate" bson:"lastElectionDate"`
	LastElectionReason             string    `json:"lastElectionReason" bson:"lastElectionReason"`
	LastSeenOpTimeAtElection       Optime    `json:"lastSeenOpTimeAtElection" bson:"lastSeenOpTimeAtElection"`
	NewTermStartDate               time.Time `json:"newTermStartDate" bson:"newTermStartDate"`
	NumCatchUpOps                  int       `json:"numCatchUpOps" bson:"numCatchUpOps"`
	NumVotesNeeded                 int       `json:"numVotesNeeded" bson:"numVotesNeeded"`
	PriorityAtElection             int       `json:"priorityAtElection" bson:"priorityAtElection"`
	WMajorityWriteAvailabilityDate time.Time `json:"wMajorityWriteAvailabilityDate" bson:"wMajorityWriteAvailabilityDate"`
}

type Optime struct {
	T  int                 `json:"t" bson:"t"`
	Ts primitive.Timestamp `json:"ts" bson:"ts"`
}

type SlowlogView struct {
	Ns        string    `json:"ns" bson:"ns"`
	Op        string    `json:"op" bson:"op"`
	QueryHash string    `json:"queryHash" bson:"queryHash"`
	Cnt       int64     `json:"cnt" bson:"cnt"`
	MaxMills  int64     `json:"maxMills" bson:"maxMills"`
	MinMills  int64     `json:"minMills" bson:"minMills"`
	MaxDocs   int64     `json:"maxDocs" bson:"maxDocs"`
	MaxTs     time.Time `json:"maxTs" bson:"maxTs"`
	MinTs     time.Time `json:"minTs" bson:"minTs"`

	DB string `json:"-" bson:"-"`
}
