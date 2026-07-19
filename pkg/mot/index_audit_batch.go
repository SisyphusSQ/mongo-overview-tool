package mot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	indexAuditBatchModeCursor = "cursor"
	indexAuditCursorVersion   = 1
	maxIndexAuditCursorBytes  = 8192
	maxIndexAuditBatchSize    = 500
)

// IndexAuditBatchOptions 控制一次有界索引审计批次。
type IndexAuditBatchOptions struct {
	Audit  IndexAuditOptions
	Cursor string
}

// IndexAuditBatchMetadata 描述当前批次在完整 collection scope 中的位置。
type IndexAuditBatchMetadata struct {
	Mode                 string `json:"mode"`
	Index                int    `json:"index"`
	DatabaseCount        int    `json:"databaseCount"`
	CollectionCount      int    `json:"collectionCount"`
	ProcessedCollections int    `json:"processedCollections"`
	TotalCollections     int    `json:"totalCollections"`
	RemainingCollections int    `json:"remainingCollections"`
	HasMore              bool   `json:"hasMore"`
	NextCursor           string `json:"nextCursor,omitempty"`
}

// IndexAuditBatchResult 保持 IndexAuditResult 的 JSON 字段形态，并追加批次元数据。
// 使用独立结果类型可避免给既有公开 struct 增字段而破坏 unkeyed literal 兼容性。
type IndexAuditBatchResult struct {
	IndexAuditResult
	Batch IndexAuditBatchMetadata `json:"batch"`
}

type indexAuditBatchCursor struct {
	Version     int    `json:"version"`
	ScopeHash   string `json:"scope_hash"`
	CatalogHash string `json:"catalog_hash"`
	Processed   int    `json:"processed"`
	BatchIndex  int    `json:"batch_index"`
	Signature   string `json:"signature"`
}

type indexAuditBatchScope struct {
	Databases       []string          `json:"databases"`
	AllDatabases    bool              `json:"all_databases"`
	Collections     []string          `json:"collections"`
	Checks          []IndexAuditCheck `json:"checks"`
	IncludeSystemDB bool              `json:"include_system_db"`
	MinObservation  int64             `json:"min_observation_ns"`
}

// IndexAuditBatch 执行一批最多 MaxCollections 个集合的索引审计。
// 返回的 cursor 只用于继续同一目标、同一审计语义和同一 collection catalog。
// 每次公开调用都创建 fresh request session，确保续跑时重新读取 collection catalog。
func (c *Client) IndexAuditBatch(ctx context.Context, batchOpts IndexAuditBatchOptions) (*IndexAuditBatchResult, error) {
	return withEphemeralCollectorSession(ctx, c, func(session *CollectorSession) (*IndexAuditBatchResult, error) {
		return session.client.indexAuditBatch(ctx, batchOpts)
	})
}

func (c *Client) indexAuditBatch(ctx context.Context, batchOpts IndexAuditBatchOptions) (batchResult *IndexAuditBatchResult, err error) {
	opts, err := normalizeIndexAuditOptions(batchOpts.Audit)
	if err != nil {
		return nil, err
	}
	if opts.MaxCollections > maxIndexAuditBatchSize {
		return nil, invalidOptions("index audit batch max collections must not exceed %d", maxIndexAuditBatchSize)
	}
	if len(batchOpts.Cursor) > maxIndexAuditCursorBytes {
		return nil, indexAuditCursorInvalid("cursor exceeds the size limit")
	}
	if err := c.requireMemberConnectionURI(); err != nil {
		return nil, err
	}
	defer func() { err = mapContextError(err) }()
	cluster, err := c.detectClusterTopology(ctx)
	if err != nil {
		return nil, err
	}
	result := &IndexAuditResult{CollectedAt: time.Now().UTC()}
	consistencyRequested := includesIndexCheck(opts.Checks, IndexCheckConsistency)
	generalRequested := includesGeneralIndexCheck(opts.Checks)
	if err := validateIndexConsistencyTopology(cluster.Type, consistencyRequested); err != nil {
		return nil, err
	}
	if generalRequested {
		if gate, allowed := diagnosticCapabilityGate("index_usage", convertClusterType(cluster.Type), cluster.MaxWireVersion, true); !allowed {
			result.CollectorStatuses = []CollectorStatus{gate}
			return &IndexAuditBatchResult{
				IndexAuditResult: *result,
				Batch:            IndexAuditBatchMetadata{Mode: indexAuditBatchModeCursor},
			}, nil
		}
	}

	discoveryOpts := opts
	discoveryOpts.MaxCollections = 0
	refs, err := c.indexCollectionRefs(ctx, discoveryOpts)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, invalidOptions("no collections selected")
	}
	scopeHash, err := indexAuditBatchScopeHash(opts)
	if err != nil {
		return nil, err
	}
	catalogHash := indexAuditCatalogHash(refs)
	cursorKey := c.indexAuditCursorKey()
	start, batchIndex, err := indexAuditBatchStart(batchOpts.Cursor, cursorKey, scopeHash, catalogHash, refs)
	if err != nil {
		return nil, err
	}
	batchRefs := nextIndexAuditBatch(refs[start:], opts.MaxCollections)
	if len(batchRefs) == 0 {
		return nil, indexAuditCursorInvalid("cursor does not select a remaining batch")
	}
	processed := start + len(batchRefs)
	metadata := IndexAuditBatchMetadata{
		Mode:                 indexAuditBatchModeCursor,
		Index:                batchIndex,
		DatabaseCount:        indexAuditBatchDatabaseCount(batchRefs),
		CollectionCount:      len(batchRefs),
		ProcessedCollections: processed,
		TotalCollections:     len(refs),
		RemainingCollections: len(refs) - processed,
		HasMore:              processed < len(refs),
	}
	if metadata.HasMore {
		metadata.NextCursor, err = encodeIndexAuditBatchCursor(indexAuditBatchCursor{
			Version:     indexAuditCursorVersion,
			ScopeHash:   scopeHash,
			CatalogHash: catalogHash,
			Processed:   processed,
			BatchIndex:  batchIndex,
		}, cursorKey)
		if err != nil {
			return nil, err
		}
	}
	collected, collectErr := c.collectIndexAuditRefs(ctx, opts, cluster.Type, result, batchRefs, consistencyRequested, generalRequested)
	if collected != nil {
		result = collected
	}
	batchResult = &IndexAuditBatchResult{IndexAuditResult: *result, Batch: metadata}
	if collectErr == nil {
		return batchResult, nil
	}
	if errors.Is(collectErr, ErrPartialResult) {
		return batchResult, newDiagnosticPartialError("index-audit-batch", batchResult, collectErr)
	}
	return nil, collectErr
}

func indexAuditBatchScopeHash(opts IndexAuditOptions) (string, error) {
	scope := indexAuditBatchScope{
		Databases:       append([]string(nil), opts.Databases...),
		AllDatabases:    opts.AllDatabases,
		Collections:     append([]string(nil), opts.Collections...),
		Checks:          append([]IndexAuditCheck(nil), opts.Checks...),
		IncludeSystemDB: opts.IncludeSystemDB,
		MinObservation:  int64(opts.MinObservation),
	}
	sort.Strings(scope.Databases)
	sort.Strings(scope.Collections)
	sort.SliceStable(scope.Checks, func(i, j int) bool { return scope.Checks[i] < scope.Checks[j] })
	encoded, err := json.Marshal(scope)
	if err != nil {
		return "", fmt.Errorf("encode index audit batch scope: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func indexAuditCatalogHash(refs []indexCollectionRef) string {
	hasher := sha256.New()
	for _, ref := range refs {
		_, _ = hasher.Write([]byte(ref.Database))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(ref.Collection))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(ref.Type))
		_, _ = hasher.Write([]byte{0xff})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func indexAuditBatchStart(
	rawCursor string,
	cursorKey []byte,
	scopeHash string,
	catalogHash string,
	refs []indexCollectionRef,
) (int, int, error) {
	if strings.TrimSpace(rawCursor) == "" {
		return 0, 1, nil
	}
	cursor, err := decodeIndexAuditBatchCursor(rawCursor)
	if err != nil {
		return 0, 0, err
	}
	if !validIndexAuditBatchCursorSignature(cursor, cursorKey) {
		return 0, 0, indexAuditCursorInvalid("cursor signature is invalid")
	}
	if cursor.ScopeHash != scopeHash || cursor.CatalogHash != catalogHash {
		return 0, 0, fmt.Errorf("%w: cursor scope no longer matches", ErrIndexAuditScopeChanged)
	}
	if cursor.Processed <= 0 || cursor.Processed >= len(refs) {
		return 0, 0, indexAuditCursorInvalid("cursor position is outside the remaining scope")
	}
	return cursor.Processed, cursor.BatchIndex + 1, nil
}

func nextIndexAuditBatch(refs []indexCollectionRef, limit int) []indexCollectionRef {
	if len(refs) == 0 || limit <= 0 {
		return nil
	}
	selected := 0
	for selected < len(refs) {
		database := refs[selected].Database
		end := selected + 1
		for end < len(refs) && refs[end].Database == database {
			end++
		}
		databaseCount := end - selected
		if selected == 0 && databaseCount > limit {
			return refs[:limit]
		}
		if selected+databaseCount > limit {
			break
		}
		selected = end
	}
	return refs[:selected]
}

func indexAuditBatchDatabaseCount(refs []indexCollectionRef) int {
	count := 0
	last := ""
	for _, ref := range refs {
		if count == 0 || ref.Database != last {
			count++
			last = ref.Database
		}
	}
	return count
}

func encodeIndexAuditBatchCursor(cursor indexAuditBatchCursor, cursorKey []byte) (string, error) {
	cursor.Signature = indexAuditBatchCursorSignature(cursor, cursorKey)
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode index audit cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeIndexAuditBatchCursor(raw string) (indexAuditBatchCursor, error) {
	var cursor indexAuditBatchCursor
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) == 0 || len(decoded) > maxIndexAuditCursorBytes {
		return cursor, indexAuditCursorInvalid("cursor encoding is invalid")
	}
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return cursor, indexAuditCursorInvalid("cursor payload is invalid")
	}
	if cursor.Version != indexAuditCursorVersion || cursor.ScopeHash == "" || cursor.CatalogHash == "" ||
		cursor.Processed <= 0 || cursor.BatchIndex <= 0 || cursor.Signature == "" {
		return cursor, indexAuditCursorInvalid("cursor fields are invalid")
	}
	return cursor, nil
}

func indexAuditBatchCursorSignature(cursor indexAuditBatchCursor, cursorKey []byte) string {
	cursor.Signature = ""
	encoded, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, cursorKey)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil))
}

func validIndexAuditBatchCursorSignature(cursor indexAuditBatchCursor, cursorKey []byte) bool {
	want, err := hex.DecodeString(indexAuditBatchCursorSignature(cursor, cursorKey))
	if err != nil {
		return false
	}
	got, err := hex.DecodeString(cursor.Signature)
	return err == nil && hmac.Equal(got, want)
}

func (c *Client) indexAuditCursorKey() []byte {
	digest := sha256.Sum256([]byte(c.uri + "\x00" + c.opts.AuthSource))
	return digest[:]
}

func indexAuditCursorInvalid(message string) error {
	return fmt.Errorf("%w: %s", ErrIndexAuditCursorInvalid, message)
}
