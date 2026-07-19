package mot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	pkgmongo "github.com/SisyphusSQ/mongo-overview-tool/v2/pkg/mongo"
)

func TestIndexCollectionRefsReturnsTypedCollectionLimitError(t *testing.T) {
	// 场景：第 501 个 collection 在 collector 执行前触发稳定的 typed scope error。
	client := newDisconnectedTestClient(t)
	session, err := client.NewCollectorSession(CollectorSessionOptions{})
	if err != nil {
		t.Fatalf("NewCollectorSession failed: %v", err)
	}
	session.databaseInventory = []string{"app"}
	metadata := make([]indexCollectionMetadata, 501)
	for i := range metadata {
		metadata[i] = indexCollectionMetadata{Name: fmt.Sprintf("collection-%03d", i), Type: "collection"}
	}
	session.collectionInventory["app"] = metadata

	_, err = session.client.indexCollectionRefs(context.Background(), IndexAuditOptions{
		AllDatabases: true, MaxCollections: 500,
	})
	if !errors.Is(err, ErrCollectionLimitExceeded) || !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("error = %v, want collection limit and invalid options compatibility", err)
	}
	var limitErr *CollectionLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != 500 || limitErr.ObservedAtLeast != 501 {
		t.Fatalf("typed error = %#v, want limit=500 observed_at_least=501", limitErr)
	}
}

func TestNextIndexAuditBatchKeepsDatabasesWholeAndSplitsOversizedDatabase(t *testing.T) {
	// 场景：小数据库整库装箱；单库超过上限时才按 collection 切分。
	refs := append(indexAuditTestRefs("db-a", 200), indexAuditTestRefs("db-b", 300)...)
	refs = append(refs, indexAuditTestRefs("db-c", 600)...)

	first := nextIndexAuditBatch(refs, 500)
	if len(first) != 500 || indexAuditBatchDatabaseCount(first) != 2 || first[len(first)-1].Database != "db-b" {
		t.Fatalf("first batch = count:%d databases:%d last:%#v", len(first), indexAuditBatchDatabaseCount(first), first[len(first)-1])
	}
	second := nextIndexAuditBatch(refs[len(first):], 500)
	if len(second) != 500 || indexAuditBatchDatabaseCount(second) != 1 || second[0].Database != "db-c" {
		t.Fatalf("second batch = count:%d databases:%d first:%#v", len(second), indexAuditBatchDatabaseCount(second), second[0])
	}
	third := nextIndexAuditBatch(refs[len(first)+len(second):], 500)
	if len(third) != 100 || indexAuditBatchDatabaseCount(third) != 1 {
		t.Fatalf("third batch = count:%d databases:%d", len(third), indexAuditBatchDatabaseCount(third))
	}
}

func TestIndexAuditBatchCursorContinuesWithoutGapWhenLimitChanges(t *testing.T) {
	// 场景：上一批成功后允许缩小本批上限，cursor 仍从上一 collection 后继续且不跳项。
	refs := append(indexAuditTestRefs("db-a", 400), indexAuditTestRefs("db-b", 300)...)
	opts := IndexAuditOptions{
		AllDatabases:   true,
		Checks:         []IndexAuditCheck{IndexCheckSpace, IndexCheckUnused},
		MinObservation: 7 * 24 * time.Hour,
		MaxCollections: 500,
		Concurrency:    4,
	}
	cursorKey := []byte("0123456789abcdef0123456789abcdef")
	scopeHash, err := indexAuditBatchScopeHash(opts)
	if err != nil {
		t.Fatalf("scope hash failed: %v", err)
	}
	catalogHash := indexAuditCatalogHash(refs)
	first := nextIndexAuditBatch(refs, 500)
	cursor, err := encodeIndexAuditBatchCursor(indexAuditBatchCursor{
		Version:     indexAuditCursorVersion,
		ScopeHash:   scopeHash,
		CatalogHash: catalogHash,
		Processed:   len(first),
		BatchIndex:  1,
	}, cursorKey)
	if err != nil {
		t.Fatalf("encode cursor failed: %v", err)
	}

	changedLimitOpts := opts
	changedLimitOpts.MaxCollections = 100
	changedLimitOpts.Concurrency = 1
	changedScopeHash, err := indexAuditBatchScopeHash(changedLimitOpts)
	if err != nil || changedScopeHash != scopeHash {
		t.Fatalf("limit/concurrency changed scope hash: got=%q want=%q err=%v", changedScopeHash, scopeHash, err)
	}
	start, batchIndex, err := indexAuditBatchStart(cursor, cursorKey, changedScopeHash, catalogHash, refs)
	if err != nil {
		t.Fatalf("continue cursor failed: %v", err)
	}
	if start != len(first) || batchIndex != 2 {
		t.Fatalf("continuation = start:%d batch:%d, want %d/2", start, batchIndex, len(first))
	}
	next := nextIndexAuditBatch(refs[start:], changedLimitOpts.MaxCollections)
	if len(next) != 100 || next[0] != refs[len(first)] {
		t.Fatalf("next batch has a gap: count=%d first=%#v want=%#v", len(next), next[0], refs[len(first)])
	}
}

func TestIndexAuditBatchCursorRejectsScopeAndCatalogChanges(t *testing.T) {
	// 场景：游标不能跨目标复用，collection catalog 漂移后也不能静默续跑。
	refs := indexAuditTestRefs("db-a", 2)
	opts := IndexAuditOptions{AllDatabases: true, Checks: []IndexAuditCheck{IndexCheckSpace}, MinObservation: time.Hour, MaxCollections: 1}
	cursorKey := []byte("0123456789abcdef0123456789abcdef")
	scopeHash, _ := indexAuditBatchScopeHash(opts)
	catalogHash := indexAuditCatalogHash(refs)
	cursor, err := encodeIndexAuditBatchCursor(indexAuditBatchCursor{
		Version: indexAuditCursorVersion, ScopeHash: scopeHash, CatalogHash: catalogHash,
		Processed: 1, BatchIndex: 1,
	}, cursorKey)
	if err != nil {
		t.Fatalf("encode cursor failed: %v", err)
	}
	if _, _, err := indexAuditBatchStart(cursor, []byte("fedcba9876543210fedcba9876543210"), scopeHash, catalogHash, refs); !errors.Is(err, ErrIndexAuditCursorInvalid) {
		t.Fatalf("target mismatch error = %v, want cursor invalid", err)
	}
	changedOpts := opts
	changedOpts.Checks = []IndexAuditCheck{IndexCheckUnused}
	changedScopeHash, _ := indexAuditBatchScopeHash(changedOpts)
	if _, _, err := indexAuditBatchStart(cursor, cursorKey, changedScopeHash, catalogHash, refs); !errors.Is(err, ErrIndexAuditScopeChanged) {
		t.Fatalf("option mismatch error = %v, want scope changed", err)
	}
	changedRefs := append(append([]indexCollectionRef(nil), refs...), indexAuditTestRefs("db-b", 1)...)
	if _, _, err := indexAuditBatchStart(cursor, cursorKey, scopeHash, indexAuditCatalogHash(changedRefs), changedRefs); !errors.Is(err, ErrIndexAuditScopeChanged) {
		t.Fatalf("catalog mismatch error = %v, want scope changed", err)
	}
	if _, _, err := indexAuditBatchStart("not-a-cursor", cursorKey, scopeHash, catalogHash, refs); !errors.Is(err, ErrIndexAuditCursorInvalid) {
		t.Fatalf("invalid cursor error = %v, want cursor invalid", err)
	}
}

func TestIndexAuditBatchCursorRejectsTamperingAndDoesNotExposeKeyMaterial(t *testing.T) {
	// 场景：调用方不能修改 processed 跳过集合，cursor payload 也不包含 URI、凭据或 namespace。
	client := newDisconnectedTestClient(t)
	client.uri = "mongodb://readonly:sensitive-password@private-target.invalid/admin"
	client.opts.AuthSource = "admin"
	cursorKey := client.indexAuditCursorKey()
	refs := indexAuditTestRefs("private-database", 3)
	opts := IndexAuditOptions{AllDatabases: true, Checks: []IndexAuditCheck{IndexCheckSpace}, MinObservation: time.Hour, MaxCollections: 1}
	scopeHash, _ := indexAuditBatchScopeHash(opts)
	catalogHash := indexAuditCatalogHash(refs)
	cursor, err := encodeIndexAuditBatchCursor(indexAuditBatchCursor{
		Version: indexAuditCursorVersion, ScopeHash: scopeHash, CatalogHash: catalogHash, Processed: 1, BatchIndex: 1,
	}, cursorKey)
	if err != nil {
		t.Fatalf("encode cursor failed: %v", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode cursor failed: %v", err)
	}
	for _, forbidden := range []string{"readonly", "sensitive-password", "private-target.invalid", "private-database", "collection-000"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("cursor payload exposed forbidden value %q: %s", forbidden, payload)
		}
	}
	var tampered indexAuditBatchCursor
	if err := json.Unmarshal(payload, &tampered); err != nil {
		t.Fatalf("decode cursor JSON failed: %v", err)
	}
	tampered.Processed = 2
	tamperedPayload, _ := json.Marshal(tampered)
	tamperedCursor := base64.RawURLEncoding.EncodeToString(tamperedPayload)
	if _, _, err := indexAuditBatchStart(tamperedCursor, cursorKey, scopeHash, catalogHash, refs); !errors.Is(err, ErrIndexAuditCursorInvalid) {
		t.Fatalf("tampered cursor error = %v, want cursor invalid", err)
	}
}

func TestIndexAuditBatchRejectsMoreThanFiveHundredCollectionsPerExecution(t *testing.T) {
	// 场景：batch API 自身强制每批不超过 500，不依赖上层 schema 约束。
	client := newDisconnectedTestClient(t)
	_, err := client.IndexAuditBatch(context.Background(), IndexAuditBatchOptions{
		Audit: IndexAuditOptions{AllDatabases: true, MaxCollections: 501},
	})
	if !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("error = %v, want ErrInvalidOptions", err)
	}
}

func TestIndexAuditBatchPlanCoversCompleteScopeWithoutDuplicates(t *testing.T) {
	// 场景：完整 cursor loop 的 union 与发现 scope 完全一致，每批都不超过 500。
	refs := append(indexAuditTestRefs("db-a", 200), indexAuditTestRefs("db-b", 300)...)
	refs = append(refs, indexAuditTestRefs("db-c", 600)...)
	processed := 0
	batches := 0
	seen := make(map[string]struct{}, len(refs))
	for processed < len(refs) {
		batch := nextIndexAuditBatch(refs[processed:], 500)
		if len(batch) == 0 || len(batch) > 500 {
			t.Fatalf("batch %d count = %d", batches+1, len(batch))
		}
		for _, ref := range batch {
			key := ref.Database + "\x00" + ref.Collection + "\x00" + ref.Type
			if _, exists := seen[key]; exists {
				t.Fatalf("duplicate ref in batch loop: %#v", ref)
			}
			seen[key] = struct{}{}
		}
		processed += len(batch)
		batches++
	}
	if processed != len(refs) || len(seen) != len(refs) || batches != 3 {
		t.Fatalf("coverage = processed:%d seen:%d batches:%d want:%d/%d/3", processed, len(seen), batches, len(refs), len(refs))
	}
}

func TestIndexAuditBatchResultPreservesLegacyJSONShapeAndStructCompatibility(t *testing.T) {
	// 场景：batch result 追加 metadata，但既有 IndexAuditResult 字段数与顶层 JSON 字段保持兼容。
	legacy := IndexAuditResult{time.Time{}, IndexConsistencySummary{}, nil, nil, nil}
	result := IndexAuditBatchResult{
		IndexAuditResult: legacy,
		Batch:            IndexAuditBatchMetadata{Mode: indexAuditBatchModeCursor, Index: 1, CollectionCount: 2},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal batch result failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode batch result failed: %v", err)
	}
	if _, ok := payload["collections"]; !ok {
		t.Fatalf("legacy collections field missing: %s", encoded)
	}
	if _, ok := payload["batch"]; !ok {
		t.Fatalf("batch field missing: %s", encoded)
	}
	if _, leakedWrapper := payload["IndexAuditResult"]; leakedWrapper {
		t.Fatalf("embedded wrapper leaked into JSON: %s", encoded)
	}
}

func TestIndexAuditBatchFreshSessionRejectsCatalogDrift(t *testing.T) {
	// 场景：两次公开 batch 调用使用的 ephemeral session 不共享 catalog；目录漂移后旧 cursor fail closed。
	client := newDisconnectedTestClient(t)
	source := &mutableIndexAuditCatalogSource{
		collections: []indexCollectionMetadata{{Name: "orders", Type: "collection"}, {Name: "payments", Type: "collection"}},
	}
	discover := func(session *CollectorSession) ([]indexCollectionRef, error) {
		session.catalogSource = source
		return session.client.indexCollectionRefs(context.Background(), IndexAuditOptions{AllDatabases: true})
	}
	firstRefs, err := withEphemeralCollectorSession(context.Background(), client, discover)
	if err != nil {
		t.Fatalf("first catalog discovery failed: %v", err)
	}
	opts := IndexAuditOptions{AllDatabases: true, Checks: []IndexAuditCheck{IndexCheckSpace}, MaxCollections: 1}
	scopeHash, _ := indexAuditBatchScopeHash(opts)
	cursor, err := encodeIndexAuditBatchCursor(indexAuditBatchCursor{
		Version: indexAuditCursorVersion, ScopeHash: scopeHash, CatalogHash: indexAuditCatalogHash(firstRefs),
		Processed: 1, BatchIndex: 1,
	}, client.indexAuditCursorKey())
	if err != nil {
		t.Fatalf("encode cursor failed: %v", err)
	}

	source.collections = append(source.collections, indexCollectionMetadata{Name: "shipments", Type: "collection"})
	secondRefs, err := withEphemeralCollectorSession(context.Background(), client, discover)
	if err != nil {
		t.Fatalf("second catalog discovery failed: %v", err)
	}
	if source.collectionLoads != 2 || len(firstRefs) != 2 || len(secondRefs) != 3 {
		t.Fatalf("fresh catalog loads=%d first=%d second=%d, want 2/2/3", source.collectionLoads, len(firstRefs), len(secondRefs))
	}
	if _, _, err := indexAuditBatchStart(cursor, client.indexAuditCursorKey(), scopeHash, indexAuditCatalogHash(secondRefs), secondRefs); !errors.Is(err, ErrIndexAuditScopeChanged) {
		t.Fatalf("catalog drift error = %v, want ErrIndexAuditScopeChanged", err)
	}
}

func TestIndexAuditBatchPartialErrorPreservesBatchResultAndCursor(t *testing.T) {
	// 场景：collector partial 仍向调用方返回可续跑的 batch result，错误文本不暴露 cursor。
	result := &IndexAuditBatchResult{
		Batch: IndexAuditBatchMetadata{Mode: indexAuditBatchModeCursor, Index: 1, HasMore: true, NextCursor: "opaque-cursor"},
	}
	err := newDiagnosticPartialError("index-audit-batch", result, errors.New("collector unavailable"))
	if !errors.Is(err, ErrPartialResult) {
		t.Fatalf("error = %v, want ErrPartialResult", err)
	}
	diagnosticResult, ok := err.DiagnosticResult().(*IndexAuditBatchResult)
	if !ok || diagnosticResult != result || diagnosticResult.Batch.NextCursor != "opaque-cursor" {
		t.Fatalf("diagnostic result = %#v, want original batch result and cursor", err.DiagnosticResult())
	}
	if strings.Contains(err.Error(), result.Batch.NextCursor) {
		t.Fatalf("partial error exposed cursor: %q", err.Error())
	}
}

func indexAuditTestRefs(database string, count int) []indexCollectionRef {
	refs := make([]indexCollectionRef, count)
	for i := range refs {
		refs[i] = indexCollectionRef{Database: database, Collection: fmt.Sprintf("collection-%03d", i), Type: "collection"}
	}
	return refs
}

type mutableIndexAuditCatalogSource struct {
	collections     []indexCollectionMetadata
	collectionLoads int
}

func (s *mutableIndexAuditCatalogSource) ListDatabaseNames(context.Context, *pkgmongo.Conn) ([]string, error) {
	return []string{"app"}, nil
}

func (s *mutableIndexAuditCatalogSource) ListCollections(context.Context, *pkgmongo.Conn, string) ([]indexCollectionMetadata, error) {
	s.collectionLoads++
	return append([]indexCollectionMetadata(nil), s.collections...), nil
}
