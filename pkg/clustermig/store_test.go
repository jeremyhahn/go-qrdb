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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// memKV is the package KV over kvstore.MemoryKVStore, whose PutIfAbsent,
// CompareAndSwap and CompareAndDelete are the real compare-then-write a
// replicated store evaluates in its state machine, so a test of a lost race
// sees the store's own verdict rather than a double's opinion of it.
type memKV struct {
	*kvstore.MemoryKVStore
}

func newMemKV() *memKV { return &memKV{kvstore.NewMemoryKVStore()} }

// putMigration writes m over whatever record its scope holds, through the
// conditional write the store offers, for tests that seed a record.
func putMigration(t *testing.T, s *Store, m *ClusterMigration) {
	t.Helper()
	ctx := context.Background()
	_, ver, err := s.GetMigrationVersion(ctx, m.TenantID, m.Database)
	if err != nil {
		t.Fatalf("GetMigrationVersion: %v", err)
	}
	if ok, err := s.CompareAndSwapMigration(ctx, ver, m); err != nil || !ok {
		t.Fatalf("CompareAndSwapMigration = %v, %v; want true, nil", ok, err)
	}
}

const (
	tid = "acme"
	db  = "appdata"
)

// requireComplete asserts that IsComplete reports want for the fixture scope,
// and fails DISTINCTLY when the store call itself failed.
//
// The distinction is the whole point (go-qrdb#236). IsComplete returns
// (false, err) when it cannot read the store and (false, nil) when the scope is
// genuinely incomplete, so the previous form here --
//
//	if ok, _ := s.IsComplete(ctx, tid, db); ok { t.Fatal(...) }
//
// scored a BROKEN STORE as "not complete yet", which is the answer every one of
// these call sites was hoping for. The discarded error was the only thing that
// separated the two, so the assertion could not fail for the reason it existed
// to catch.
//
// Reporting them separately rather than as one combined condition is
// deliberate: "the store call failed" and "the answer was wrong" need different
// fixes, and a message that cannot tell you which you have is the defect one
// rung up (go-qrdb#185, #229).
func requireComplete(t *testing.T, ctx context.Context, s *Store, want bool, why string) {
	t.Helper()
	ok, err := s.IsComplete(ctx, tid, db)
	if err != nil {
		t.Fatalf("IsComplete (%s): %v -- the store call failed, so completion was never determined", why, err)
	}
	if ok != want {
		t.Fatalf("IsComplete (%s) = %v, want %v", why, ok, want)
	}
}

// requireMigrationAbsent asserts GetMigration reports no record for the
// fixture scope, and fails DISTINCTLY when the store read itself failed
// (go-qrdb#255).
//
// GetMigration returns (nil, nil) when the record is absent and (nil, err)
// when the read failed -- see the GetMigration doc comment in store.go -- so
//
//	if m, _ := s.GetMigration(ctx, tid, db); m != nil { t.Fatal(...) }
//
// scores a failed read as "the record is gone", which is the exact defect
// #236 fixed 15 instances of elsewhere in this package. #236 itself excluded
// this shape of assertion on the premise that for a Get-style API absence
// always arrives through the error; that premise is false for this store,
// which separates the two channels explicitly (issue #255).
func requireMigrationAbsent(t *testing.T, ctx context.Context, s *Store, why string) {
	t.Helper()
	m, err := s.GetMigration(ctx, tid, db)
	if err != nil {
		t.Fatalf("GetMigration (%s): %v -- the store read failed, so absence was never established", why, err)
	}
	if m != nil {
		t.Fatalf("GetMigration (%s) = %+v, want absent", why, m)
	}
}

// requireNodeStatusAbsent asserts GetNodeStatus reports no status for nodeID
// in the fixture scope, and fails DISTINCTLY when the store read itself
// failed. Same rationale as requireMigrationAbsent: GetNodeStatus returns
// (nil, nil) for "no status yet" and (nil, err) for "the read failed", and
// only checking the value conflates the two (go-qrdb#255).
func requireNodeStatusAbsent(t *testing.T, ctx context.Context, s *Store, nodeID, why string) {
	t.Helper()
	st, err := s.GetNodeStatus(ctx, tid, db, nodeID)
	if err != nil {
		t.Fatalf("GetNodeStatus(%s) (%s): %v -- the store read failed, so absence was never established", nodeID, why, err)
	}
	if st != nil {
		t.Fatalf("GetNodeStatus(%s) (%s) = %+v, want absent", nodeID, why, st)
	}
}

func seedMigration(t *testing.T, s *Store, gen uint64, owners []string) {
	t.Helper()
	putMigration(t, s, &ClusterMigration{
		TenantID: tid, Database: db, ShardID: 7,
		Target: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Owners: owners, Generation: gen,
	})
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
	requireComplete(t, ctx, s, false, "no acks")
	// Partial (2/3) -> incomplete; the cluster never blocks but isn't done.
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 1, seal.PhaseComplete)
	requireComplete(t, ctx, s, false, "2/3 owners acked")
	if a, total, _ := s.Progress(ctx, tid, db); a != 2 || total != 3 {
		t.Fatalf("Progress = %d/%d, want 2/3", a, total)
	}
	// A laggard reporting a non-terminal phase doesn't count.
	ack(t, s, "n3", 1, seal.PhaseVerifying)
	requireComplete(t, ctx, s, false, "n3 still verifying")
	// Final ack -> complete.
	ack(t, s, "n3", 1, seal.PhaseComplete)
	requireComplete(t, ctx, s, true, "all owners complete")
}

// TestStore_GenerationGuard proves a stale-generation ack does not count toward a
// newer migration request (guards against an aborted request racing a re-issue).
func TestStore_GenerationGuard(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	requireComplete(t, ctx, s, true, "gen-1 ack for a gen-1 migration")

	// Re-issue as generation 2: the gen-1 ack must NOT satisfy gen 2.
	seedMigration(t, s, 2, []string{"n1"})
	requireComplete(t, ctx, s, false, "stale gen-1 ack against a gen-2 migration")
	ack(t, s, "n1", 2, seal.PhaseComplete)
	requireComplete(t, ctx, s, true, "gen-2 ack for a gen-2 migration")
}

// TestStore_Delete clears the intent + all node statuses.
func TestStore_CompareAndDeleteMigration(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	_, ver, err := s.GetMigrationVersion(ctx, tid, db)
	if err != nil {
		t.Fatalf("GetMigrationVersion: %v", err)
	}
	deleted, err := s.CompareAndDeleteMigration(ctx, ver)
	if err != nil || !deleted {
		t.Fatalf("CompareAndDeleteMigration = %v, %v; want true, nil", deleted, err)
	}
	requireMigrationAbsent(t, ctx, s, "after CompareAndDeleteMigration")
	st, err := s.ListNodeStatuses(ctx, tid, db)
	if err != nil {
		t.Fatalf("ListNodeStatuses after CompareAndDeleteMigration: %v -- the scan failed, so it never established that the statuses were gone", err)
	}
	if len(st) != 0 {
		t.Fatalf("statuses after CompareAndDeleteMigration = %d, want 0", len(st))
	}
}

// TestStore_CompareAndDeleteMigration_KeepsANewerRecord deletes against a
// version that a newer generation has replaced: neither the newer intent nor
// a status reported for it may go, and the stale status does.
func TestStore_CompareAndDeleteMigration_KeepsANewerRecord(t *testing.T) {
	ctx := context.Background()
	kv := newMemKV()
	s := NewStore(kv)
	seedMigration(t, s, 1, []string{"n1", "n2"})
	_, stale, err := s.GetMigrationVersion(ctx, tid, db)
	if err != nil {
		t.Fatalf("GetMigrationVersion: %v", err)
	}
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 2, seal.PhaseComplete)
	seedMigration(t, s, 2, []string{"n1", "n2"})
	before, err := kv.Get(ctx, migrationKey(tid, db))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	deleted, err := s.CompareAndDeleteMigration(ctx, stale)
	if err != nil || deleted {
		t.Fatalf("CompareAndDeleteMigration(stale) = %v, %v; want false, nil", deleted, err)
	}
	after, err := kv.Get(ctx, migrationKey(tid, db))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("newer intent changed:\n got %s\nwant %s", after, before)
	}
	st, err := s.ListNodeStatuses(ctx, tid, db)
	if err != nil {
		t.Fatalf("ListNodeStatuses: %v", err)
	}
	if len(st) != 1 || st[0].NodeID != "n2" || st[0].Generation != 2 {
		t.Fatalf("statuses = %+v, want only n2 at generation 2", st)
	}
}

func TestStore_CompareAndDeleteMigration_ZeroVersion(t *testing.T) {
	s := NewStore(newMemKV())
	if _, err := s.CompareAndDeleteMigration(context.Background(), MigrationVersion{}); !errors.Is(err, ErrNilMigration) {
		t.Fatalf("CompareAndDeleteMigration(zero) = %v, want ErrNilMigration", err)
	}
	if _, err := s.CompareAndDeleteMigration(context.Background(), MigrationVersion{raw: []byte("{")}); err == nil {
		t.Fatal("CompareAndDeleteMigration(corrupt) = nil, want an unmarshal error")
	}
}

// TestStore_Validation covers the input guards.
func TestStore_Validation(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if _, err := s.CompareAndSwapMigration(ctx, MigrationVersion{}, nil); !errors.Is(err, ErrNilMigration) {
		t.Fatalf("CompareAndSwapMigration(nil) = %v, want ErrNilMigration", err)
	}
	if _, err := s.CompareAndSwapMigration(ctx, MigrationVersion{}, &ClusterMigration{TenantID: tid}); !errors.Is(err, ErrEmptyTarget) {
		t.Fatalf("CompareAndSwapMigration(no target) = %v, want ErrEmptyTarget", err)
	}
	// A create against an existing record stores nothing.
	seedMigration(t, s, 1, []string{"n1"})
	created, err := s.CompareAndSwapMigration(ctx, MigrationVersion{}, &ClusterMigration{
		TenantID: tid, Database: db, Target: seal.StrategyPassphrase, Generation: 9,
	})
	if err != nil || created {
		t.Fatalf("CompareAndSwapMigration(create over existing) = %v, %v; want false, nil", created, err)
	}
	if m, err := s.GetMigration(ctx, tid, db); err != nil || m.Generation != 1 {
		t.Fatalf("GetMigration after refused create = %+v, %v; want generation 1", m, err)
	}
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{}); !errors.Is(err, ErrNilStatus) {
		t.Fatalf("PutNodeStatus(no node) = %v, want ErrNilStatus", err)
	}
	// Absent migration: not complete, no error.
	if ok, err := s.IsComplete(ctx, "nope", ""); ok || err != nil {
		t.Fatalf("IsComplete(absent) = %v, %v; want false, nil", ok, err)
	}
}

// TestStore_ShamirFieldsZeroForPassphrase proves a passphrase-target record
// round-trips with zero Shamir fields (omitempty), matching
// seal.MigrationOptions.Validate's contract that they are ignored for a
// non-Shamir target.
func TestStore_ShamirFieldsZeroForPassphrase(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	got, err := s.GetMigration(ctx, tid, db)
	if err != nil || got == nil {
		t.Fatalf("GetMigration = %v, %v", got, err)
	}
	if got.ShamirThreshold != 0 || got.ShamirTotal != 0 {
		t.Fatalf("passphrase-target record ShamirThreshold/ShamirTotal = %d/%d, want 0/0", got.ShamirThreshold, got.ShamirTotal)
	}
}

// TestStore_NodeStatusAttemptsRoundTrip proves NodeMigrationStatus.Attempts
// survives a PutNodeStatus/GetNodeStatus round trip, which the Reconciler's
// bounded-retry logic depends on to persist (and later read back) the retry
// budget across reconcile passes / process restarts.
func TestStore_NodeStatusAttemptsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{
		NodeID: "n1", Generation: 1, Phase: seal.PhaseFailed,
		Detail: "boom", Attempts: 3,
	}); err != nil {
		t.Fatalf("PutNodeStatus: %v", err)
	}
	got, err := s.GetNodeStatus(ctx, tid, db, "n1")
	if err != nil || got == nil {
		t.Fatalf("GetNodeStatus = %v, %v", got, err)
	}
	if got.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", got.Attempts)
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
