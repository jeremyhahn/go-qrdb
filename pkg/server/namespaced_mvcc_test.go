// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// compactRecordingMVCC is an inner MVCCKVServicer shaped like the raw shard
// service: Compact is its store-global form, which the real service refuses
// and which a namespaced view must never reach, and CompactPrefix is the
// per-prefix compaction the view is meant to use. It embeds the interface so
// any other method call panics, proving Compact touches nothing else.
type compactRecordingMVCC struct {
	service.MVCCKVServicer
	globalCalls   int
	prefixes      []string
	revisions     []int64
	compactReturn error
}

var _ service.PrefixCompactor = (*compactRecordingMVCC)(nil)

func (m *compactRecordingMVCC) Compact(context.Context, int64) error {
	m.globalCalls++
	return &service.CompactScopeRequiredError{}
}

func (m *compactRecordingMVCC) CompactPrefix(_ context.Context, prefix string, revision int64) error {
	m.prefixes = append(m.prefixes, prefix)
	m.revisions = append(m.revisions, revision)
	return m.compactReturn
}

// globalOnlyMVCC is an inner service with no per-prefix compaction at all.
type globalOnlyMVCC struct {
	service.MVCCKVServicer
	globalCalls int
}

func (m *globalOnlyMVCC) Compact(context.Context, int64) error {
	m.globalCalls++
	return nil
}

// TestNamespacedMVCC_Compact_CompactsOnlyItsOwnPrefix is the data-plane half
// of the per-tenant compaction rule: every tenant -- the system tenant
// included -- compacts exactly its own (tenant, database) namespace through
// the view, and the store-global compaction is never reached.
func TestNamespacedMVCC_Compact_CompactsOnlyItsOwnPrefix(t *testing.T) {
	for _, tenantID := range []string{"tenantA", "acme", tenant.SystemTenantID} {
		t.Run(tenantID, func(t *testing.T) {
			inner := &compactRecordingMVCC{}
			prefix := tenant.ComposeNamespace(tenantID, "db")
			w := newNamespacedMVCCService(inner, prefix, nil)
			ctx := tenant.WithTenantID(context.Background(), tenantID)

			if err := w.Compact(ctx, 42); err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if inner.globalCalls != 0 {
				t.Fatalf("CROSS-TENANT COMPACTION: the store-global Compact was reached %d time(s)", inner.globalCalls)
			}
			if len(inner.prefixes) != 1 || inner.prefixes[0] != prefix || inner.revisions[0] != 42 {
				t.Fatalf("CompactPrefix calls = %q at %v, want exactly [%q] at 42", inner.prefixes, inner.revisions, prefix)
			}
		})
	}
}

// TestNamespacedMVCC_Compact_DedicatedShardCompactsItsWholeKeyspace pins the
// raft-isolated case: the view's prefix is empty because the shard IS the
// database's keyspace, and it compacts that shard -- through the per-prefix
// path, still never the store-global one.
func TestNamespacedMVCC_Compact_DedicatedShardCompactsItsWholeKeyspace(t *testing.T) {
	inner := &compactRecordingMVCC{}
	w := newNamespacedMVCCService(inner, "", nil)

	if err := w.Compact(tenant.WithTenantID(context.Background(), "acme"), 9); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if inner.globalCalls != 0 || len(inner.prefixes) != 1 || inner.prefixes[0] != "" {
		t.Fatalf("global=%d prefixes=%q, want one CompactPrefix of the empty prefix", inner.globalCalls, inner.prefixes)
	}
}

// TestNamespacedMVCC_Compact_RefusesAnInnerWithoutPrefixCompaction proves the
// view never falls back to compacting its inner service whole: with no
// per-prefix compaction available it refuses.
func TestNamespacedMVCC_Compact_RefusesAnInnerWithoutPrefixCompaction(t *testing.T) {
	inner := &globalOnlyMVCC{}
	w := newNamespacedMVCCService(inner, "acme/db/", nil)

	err := w.Compact(context.Background(), 5)
	var scope *service.CompactScopeRequiredError
	if !errors.As(err, &scope) {
		t.Fatalf("Compact = %v (%T), want *service.CompactScopeRequiredError", err, err)
	}
	if inner.globalCalls != 0 {
		t.Fatalf("the view fell back to the store-global Compact (%d call(s))", inner.globalCalls)
	}
}

// TestNamespacedMVCC_Compact_ErrorPropagates proves the inner refusal reaches
// the caller unchanged, typed.
func TestNamespacedMVCC_Compact_ErrorPropagates(t *testing.T) {
	refusal := &service.RevisionCompactedError{Requested: 3, Compacted: 5}
	inner := &compactRecordingMVCC{compactReturn: refusal}
	w := newNamespacedMVCCService(inner, "acme/db/", nil)

	var got *service.RevisionCompactedError
	if err := w.Compact(context.Background(), 3); !errors.As(err, &got) || got != refusal {
		t.Fatalf("Compact = %v, want the inner *service.RevisionCompactedError", err)
	}
}
