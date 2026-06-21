// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clustermig

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// memKV is an in-memory KV implementing the package KV interface for tests.
type memKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) Put(_ context.Context, key string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cp := make([]byte, len(v))
	copy(cp, v)
	k.m[key] = cp
	return nil
}

func (k *memKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}

func (k *memKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

func (k *memKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	k.mu.Lock()
	keys := make([]string, 0, len(k.m))
	for key := range k.m {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	snapshot := make(map[string][]byte, len(keys))
	for _, key := range keys {
		snapshot[key] = k.m[key]
	}
	k.mu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		if err := fn(key, snapshot[key]); err != nil {
			return err
		}
	}
	return nil
}

const (
	tid = "acme"
	db  = "appdata"
)

func seedMigration(t *testing.T, s *Store, gen uint64, owners []string) {
	t.Helper()
	if err := s.PutMigration(context.Background(), &ClusterMigration{
		TenantID: tid, Database: db, ShardID: 7,
		Target: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Owners: owners, Generation: gen,
	}); err != nil {
		t.Fatalf("PutMigration: %v", err)
	}
}

func ack(t *testing.T, s *Store, node string, gen uint64, phase seal.MigrationPhase) {
	t.Helper()
	if err := s.PutNodeStatus(context.Background(), tid, db, &NodeMigrationStatus{
		NodeID: node, Generation: gen, Phase: phase,
	}); err != nil {
		t.Fatalf("PutNodeStatus(%s): %v", node, err)
	}
}

// TestStore_RoundTrip covers put/get of the intent record + per-node statuses.
func TestStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2", "n3"})

	got, err := s.GetMigration(ctx, tid, db)
	if err != nil || got == nil {
		t.Fatalf("GetMigration = %v, %v", got, err)
	}
	if got.Target != seal.StrategyPassphrase || got.Generation != 1 || len(got.Owners) != 3 {
		t.Fatalf("migration round-trip mismatch: %+v", got)
	}

	ack(t, s, "n1", 1, seal.PhaseCopying)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	statuses, err := s.ListNodeStatuses(ctx, tid, db)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("ListNodeStatuses = %d, %v; want 2", len(statuses), err)
	}
}

// TestStore_Completion proves IsComplete is true ONLY when every owner acked the
// current generation with PhaseComplete; partial state is incomplete (fail-safe).
func TestStore_Completion(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2", "n3"})

	// No acks -> incomplete.
	if ok, _ := s.IsComplete(ctx, tid, db); ok {
		t.Fatal("IsComplete with no acks must be false")
	}
	// Partial (2/3) -> incomplete; the cluster never blocks but isn't done.
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	if ok, _ := s.IsComplete(ctx, tid, db); ok {
		t.Fatal("IsComplete with 2/3 owners must be false")
	}
	if a, total, _ := s.Progress(ctx, tid, db); a != 2 || total != 3 {
		t.Fatalf("Progress = %d/%d, want 2/3", a, total)
	}
	// A laggard reporting a non-terminal phase doesn't count.
	ack(t, s, "n3", 1, seal.PhaseVerifying)
	if ok, _ := s.IsComplete(ctx, tid, db); ok {
		t.Fatal("IsComplete with n3 still verifying must be false")
	}
	// Final ack -> complete.
	ack(t, s, "n3", 1, seal.PhaseComplete)
	if ok, _ := s.IsComplete(ctx, tid, db); !ok {
		t.Fatal("IsComplete with all owners complete must be true")
	}
}

// TestStore_GenerationGuard proves a stale-generation ack does not count toward a
// newer migration request (guards against an aborted request racing a re-issue).
func TestStore_GenerationGuard(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	if ok, _ := s.IsComplete(ctx, tid, db); !ok {
		t.Fatal("gen 1 should be complete")
	}

	// Re-issue as generation 2: the gen-1 ack must NOT satisfy gen 2.
	seedMigration(t, s, 2, []string{"n1"})
	if ok, _ := s.IsComplete(ctx, tid, db); ok {
		t.Fatal("stale gen-1 ack must not complete gen-2 migration")
	}
	ack(t, s, "n1", 2, seal.PhaseComplete)
	if ok, _ := s.IsComplete(ctx, tid, db); !ok {
		t.Fatal("gen-2 ack should complete gen-2 migration")
	}
}

// TestStore_Delete clears the intent + all node statuses.
func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	if err := s.Delete(ctx, tid, db); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m, _ := s.GetMigration(ctx, tid, db); m != nil {
		t.Fatal("migration record should be gone after Delete")
	}
	if st, _ := s.ListNodeStatuses(ctx, tid, db); len(st) != 0 {
		t.Fatalf("statuses should be gone after Delete, got %d", len(st))
	}
}

// TestStore_Validation covers the input guards.
func TestStore_Validation(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if err := s.PutMigration(ctx, nil); !errors.Is(err, ErrNilMigration) {
		t.Fatalf("PutMigration(nil) = %v, want ErrNilMigration", err)
	}
	if err := s.PutMigration(ctx, &ClusterMigration{TenantID: tid}); !errors.Is(err, ErrEmptyTarget) {
		t.Fatalf("PutMigration(no target) = %v, want ErrEmptyTarget", err)
	}
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{}); !errors.Is(err, ErrNilStatus) {
		t.Fatalf("PutNodeStatus(no node) = %v, want ErrNilStatus", err)
	}
	// Absent migration: not complete, no error.
	if ok, err := s.IsComplete(ctx, "nope", ""); ok || err != nil {
		t.Fatalf("IsComplete(absent) = %v, %v; want false, nil", ok, err)
	}
}

// TestStore_StatusListExcludesIntentRecord proves the status scan returns only
// per-node children, not the intent record sharing the prefix path.
func TestStore_StatusListExcludesIntentRecord(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	statuses, err := s.ListNodeStatuses(ctx, tid, db)
	if err != nil {
		t.Fatalf("ListNodeStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].NodeID != "n1" {
		t.Fatalf("statuses = %+v, want exactly [n1]", statuses)
	}
}
