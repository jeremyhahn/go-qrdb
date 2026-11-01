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

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// memoryCompactionTable is an in-memory CompactionStateStore.
type memoryCompactionTable struct {
	mu        sync.Mutex
	states    map[string]*compaction.State
	noHistory bool
	readErr   error
	cleared   []string
}

var (
	_ CompactionStateStore    = (*memoryCompactionTable)(nil)
	_ RevisionHistoryReporter = (*memoryCompactionTable)(nil)
)

func newMemoryCompactionTable() *memoryCompactionTable {
	return &memoryCompactionTable{states: make(map[string]*compaction.State)}
}

func (m *memoryCompactionTable) KeepsRevisionHistory() bool { return !m.noHistory }

func (m *memoryCompactionTable) SetCompactionPolicy(_ context.Context, scope string, b compaction.Binding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// As the state machine does: an off policy removes the record.
	if !b.Policy.Enabled() {
		delete(m.states, scope)
		return nil
	}
	st := m.states[scope]
	if st == nil {
		st = &compaction.State{}
	}
	st.Bind(b)
	m.states[scope] = st
	return nil
}

func (m *memoryCompactionTable) ClearCompactionState(_ context.Context, scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, scope)
	m.cleared = append(m.cleared, scope)
	return nil
}

func (m *memoryCompactionTable) RecordCompactionSample(_ context.Context, scope string, at time.Time, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.states[scope]; st != nil {
		st.Record(at.UnixNano(), revision)
	}
	return nil
}

func (m *memoryCompactionTable) CompactionState(_ context.Context, scope string) (*compaction.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.states[scope], nil
}

func (m *memoryCompactionTable) CompactionStates(context.Context) (map[string]*compaction.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return nil, m.readErr
	}
	out := make(map[string]*compaction.State, len(m.states))
	for k, v := range m.states {
		out[k] = v
	}
	return out, nil
}

// TestKeyspaceReclaim_DropClearsTheDatabasesCompactionRecord: dropping a
// database removes its compaction record from its store, and no other
// database's -- so a database created again under the name starts with no
// policy and no samples.
func TestKeyspaceReclaim_DropClearsTheDatabasesCompactionRecord(t *testing.T) {
	ctx := context.Background()
	table := newMemoryCompactionTable()
	reports := tenant.ComposeNamespace("acme", "reports")
	other := tenant.ComposeNamespace("acme", "other")
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 5}
	_ = table.SetCompactionPolicy(ctx, reports, compaction.Binding{Policy: policy, Tenant: "acme", Database: "reports"})
	_ = table.SetCompactionPolicy(ctx, other, compaction.Binding{Policy: policy, Tenant: "acme", Database: "other"})

	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: populatedStore(), Compaction: table})
	if err := fenceThenReclaim(ctx, svc, "acme", "reports", ""); err != nil {
		t.Fatalf("ReclaimDatabaseKeyspace: %v", err)
	}
	if st, _ := table.CompactionState(ctx, reports); st != nil {
		t.Fatalf("the dropped database's compaction record survived: %+v", st)
	}
	if st, _ := table.CompactionState(ctx, other); st == nil {
		t.Fatal("another database's compaction record was cleared")
	}
	// Idempotent: a re-issued drop finds nothing and proposes nothing.
	before := len(table.cleared)
	if err := fenceThenReclaim(ctx, svc, "acme", "reports", ""); err != nil || len(table.cleared) != before {
		t.Fatalf("a re-issued drop = %v, cleared %v", err, table.cleared)
	}
}

// TestKeyspaceReclaim_DropClearsOnlyItsOwnIncarnationsRecord: the record a
// drop clears is the one the dropped incarnation set. A record under the
// same name bound to another incarnation -- one a database created again
// under the name wrote, which a re-issued drop of the old incarnation must
// not take from it -- survives.
func TestKeyspaceReclaim_DropClearsOnlyItsOwnIncarnationsRecord(t *testing.T) {
	ctx := context.Background()
	table := newMemoryCompactionTable()
	reports := tenant.ComposeNamespace("acme", "reports")
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 5}
	_ = table.SetCompactionPolicy(ctx, reports, compaction.Binding{Policy: policy, Tenant: "acme", Database: "reports", Incarnation: "new"})

	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: populatedStore(), Compaction: table})
	// The name is fenced, so the store admits both reclamations; the
	// record's own incarnation is what keeps the old one off the new record.
	if err := svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "", ""); err != nil {
		t.Fatalf("FenceDatabaseKeyspace: %v", err)
	}
	if err := svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", "old", ""); err != nil {
		t.Fatalf("ReclaimDatabaseKeyspace(old): %v", err)
	}
	if st, _ := table.CompactionState(ctx, reports); st == nil || st.Incarnation != "new" {
		t.Fatalf("a drop of the old incarnation cleared the new incarnation's record: %+v", st)
	}

	if err := svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", "new", ""); err != nil {
		t.Fatalf("ReclaimDatabaseKeyspace(new): %v", err)
	}
	if st, _ := table.CompactionState(ctx, reports); st != nil {
		t.Fatalf("a drop of the record's own incarnation left it: %+v", st)
	}
}

// TestKeyspaceReclaim_TenantTeardownClearsItsCompactionRecords: removing a
// tenant clears every compaction record its databases own, and no other
// tenant's.
func TestKeyspaceReclaim_TenantTeardownClearsItsCompactionRecords(t *testing.T) {
	ctx := context.Background()
	table := newMemoryCompactionTable()
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 5}
	for _, db := range []string{"a", "b"} {
		_ = table.SetCompactionPolicy(ctx, tenant.ComposeNamespace("acme", db), compaction.Binding{Policy: policy, Tenant: "acme", Database: db})
	}
	_ = table.SetCompactionPolicy(ctx, tenant.ComposeNamespace("beta", "a"), compaction.Binding{Policy: policy, Tenant: "beta", Database: "a"})
	_ = table.SetCompactionPolicy(ctx, compaction.NodeScope, compaction.Binding{Policy: policy})

	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: populatedStore(), Compaction: table})
	if err := svc.ReclaimTenantKeyspace(ctx, "acme"); err != nil {
		t.Fatalf("ReclaimTenantKeyspace: %v", err)
	}
	states, _ := table.CompactionStates(ctx)
	if len(states) != 2 || states[tenant.ComposeNamespace("beta", "a")] == nil || states[compaction.NodeScope] == nil {
		t.Fatalf("records after removing acme = %v, want beta's and the node keyspace's", states)
	}
}

// TestKeyspaceReclaim_CompactionRecordSkipsAStoreWithoutHistory: a store that
// keeps no MVCC history holds no compaction table, so the drop does not read
// or write one; a store that cannot be read fails the drop, typed.
func TestKeyspaceReclaim_CompactionRecordSkipsAStoreWithoutHistory(t *testing.T) {
	ctx := context.Background()
	table := newMemoryCompactionTable()
	table.noHistory = true
	table.readErr = errors.New("would fail if read")
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: populatedStore(), Compaction: table})
	if err := fenceThenReclaim(ctx, svc, "acme", "reports", ""); err != nil {
		t.Fatalf("a store without history = %v", err)
	}

	broken := newMemoryCompactionTable()
	broken.readErr = errors.New("unavailable")
	svc = NewKeyspaceReclaimService(ReclaimTarget{KV: populatedStore(), Compaction: broken})
	var rce *ReclaimCompactionError
	if err := fenceThenReclaim(ctx, svc, "acme", "reports", ""); !errors.As(err, &rce) || rce.Error() == "" || rce.Unwrap() == nil {
		t.Fatalf("an unreadable store = %v, want *ReclaimCompactionError", err)
	}
	if err := svc.ReclaimTenantKeyspace(ctx, "acme"); !errors.As(err, &rce) {
		t.Fatalf("tenant teardown over an unreadable store = %v", err)
	}
}
