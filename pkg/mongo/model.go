package mongo

import (
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RsStatus struct {
	ClusterTime                ClusterTime         `json:"$clusterTime" bson:"$clusterTime"`
	Ok                         int                 `json:"ok" bson:"ok"`
	MyState                    int                 `json:"myState" bson:"myState"`
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
	State                int                 `json:"state" bson:"state"`
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
