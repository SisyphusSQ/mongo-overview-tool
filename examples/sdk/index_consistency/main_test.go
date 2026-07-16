package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/SisyphusSQ/mongo-overview-tool/pkg/mot"
)

type fakeIndexAuditClient struct {
	options mot.IndexAuditOptions
}

func (f *fakeIndexAuditClient) IndexAudit(_ context.Context, opts mot.IndexAuditOptions) (*mot.IndexAuditResult, error) {
	f.options = opts
	return &mot.IndexAuditResult{
		Collections: []mot.CollectionIndexAudit{{
			State:          mot.IndexConsistencyConsistent,
			Strategy:       mot.IndexConsistencyMetadataCheck,
			Coverage:       mot.IndexConsistencyCoverageComplete,
			ExpectedShards: []string{"s1", "s2"},
			ObservedShards: []string{"s1", "s2"},
		}},
		CollectorStatuses: make([]mot.CollectorStatus, 3),
	}, errors.Join(mot.ErrPartialResult, errors.New("optional status unavailable"))
}

func TestCollectIndexConsistencyUsesConsistencyOnly(t *testing.T) {
	// 场景：示例必须只执行 consistency check，并将 coverage/strategy/partial 暴露为结构化摘要。
	client := &fakeIndexAuditClient{}
	summary, err := collectIndexConsistency(context.Background(), client, exampleConfig{
		Database:   "app",
		Collection: "orders",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.options.Databases, []string{"app"}) || !slices.Equal(client.options.Collections, []string{"orders"}) {
		t.Fatalf("scope options = %#v", client.options)
	}
	if !slices.Equal(client.options.Checks, []mot.IndexAuditCheck{mot.IndexCheckConsistency}) {
		t.Fatalf("checks = %#v", client.options.Checks)
	}
	if client.options.MaxCollections != 1 || client.options.Concurrency != 1 {
		t.Fatalf("bounds = %#v", client.options)
	}
	if summary.State != mot.IndexConsistencyConsistent || summary.Strategy != mot.IndexConsistencyMetadataCheck {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Coverage != mot.IndexConsistencyCoverageComplete || summary.ExpectedShards != 2 || summary.ObservedShards != 2 {
		t.Fatalf("coverage = %#v", summary)
	}
	if !summary.Partial || summary.CollectorStatuses != 3 {
		t.Fatalf("partial summary = %#v", summary)
	}
}

func TestCollectIndexConsistencyRejectsEmptyResult(t *testing.T) {
	// 场景：SDK 未返回目标 collection 时，示例不能输出伪成功摘要。
	client := &emptyIndexAuditClient{}
	if _, err := collectIndexConsistency(context.Background(), client, exampleConfig{Database: "app", Collection: "orders"}); err == nil {
		t.Fatal("empty SDK result must fail")
	}
}

type emptyIndexAuditClient struct{}

func (*emptyIndexAuditClient) IndexAudit(context.Context, mot.IndexAuditOptions) (*mot.IndexAuditResult, error) {
	return &mot.IndexAuditResult{}, nil
}
