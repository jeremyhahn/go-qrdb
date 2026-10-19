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
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

// PutAllIfUnchanged applies writes only while guardKey holds guardValue (nil:
// absent), as kvstore.GuardedBatchKVStore does.
func (k *memKV) PutAllIfUnchanged(_ context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	cur, ok := k.m[guardKey]
	if guardValue == nil && ok || guardValue != nil && (!ok || !bytes.Equal(cur, guardValue)) {
		return false, nil
	}
	for _, w := range writes {
		if w.Delete {
			delete(k.m, w.Key)
			continue
		}
		k.m[w.Key] = bytes.Clone(w.Value)
	}
	return true, nil
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
	ctx := context.Background()
	_, prev, err := s.readMigration(ctx, tid, db)
	if err != nil {
		t.Fatalf("readMigration: %v", err)
	}
	if err := s.swapMigration(ctx, prev, &ClusterMigration{
		TenantID: tid, Database: db, ShardID: 7,
		Target: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Owners: owners, Generation: gen,
	}); err != nil {
		t.Fatalf("swapMigration: %v", err)
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
func TestStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	ack(t, s, "n1", 1, seal.PhaseComplete)
	ack(t, s, "n2", 1, seal.PhaseComplete)

	if err := s.Delete(ctx, tid, db); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	requireMigrationAbsent(t, ctx, s, "after Delete")
	st, err := s.ListNodeStatuses(ctx, tid, db)
	if err != nil {
		t.Fatalf("ListNodeStatuses after Delete: %v -- the scan failed, so it never established that the statuses were gone", err)
	}
	if len(st) != 0 {
		t.Fatalf("statuses after Delete = %d, want 0", len(st))
	}
}

// TestStore_Validation covers the input guards.
func TestStore_Validation(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if err := s.swapMigration(ctx, nil, nil); !errors.Is(err, ErrNilMigration) {
		t.Fatalf("swapMigration(nil) = %v, want ErrNilMigration", err)
	}
	if err := s.swapMigration(ctx, nil, &ClusterMigration{TenantID: tid}); !errors.Is(err, ErrEmptyTarget) {
		t.Fatalf("swapMigration(no target) = %v, want ErrEmptyTarget", err)
	}
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{}); !errors.Is(err, ErrNilStatus) {
		t.Fatalf("PutNodeStatus(no node) = %v, want ErrNilStatus", err)
	}
	// Absent migration: not complete, no error.
	if ok, err := s.IsComplete(ctx, "nope", ""); ok || err != nil {
		t.Fatalf("IsComplete(absent) = %v, %v; want false, nil", ok, err)
	}
}

// TestStore_ShamirFieldsRoundTrip proves ShamirThreshold/ShamirTotal survive a
// PutMigration/GetMigration round trip through the replicated KV (JSON
// marshal/unmarshal), which is what every owner's reconciler relies on to
// rebuild seal.MigrationOptions for a Shamir-target migration. Before this
// field existed on ClusterMigration, every reconciler tick reconstructed
// threshold=0/total=0 regardless of the operator's request, failing
// seal.MigrationOptions.Validate() forever.
func TestStore_ShamirFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	if err := s.swapMigration(ctx, nil, &ClusterMigration{
		TenantID: "", Database: "",
		Target: seal.StrategyShamir, Mechanism: seal.MechanismSwap,
		ShamirThreshold: 2, ShamirTotal: 3,
		Owners: []string{"n1"}, Generation: 1,
	}); err != nil {
		t.Fatalf("swapMigration: %v", err)
	}
	got, err := s.GetMigration(ctx, "", "")
	if err != nil || got == nil {
		t.Fatalf("GetMigration = %v, %v", got, err)
	}
	if got.ShamirThreshold != 2 || got.ShamirTotal != 3 {
		t.Fatalf("ShamirThreshold/ShamirTotal = %d/%d, want 2/3", got.ShamirThreshold, got.ShamirTotal)
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
