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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// fixedOwners is a static OwnerResolver for tests.
type fixedOwners struct {
	shardID uint64
	owners  []string
	err     error
}

func (f fixedOwners) Owners(_ context.Context, _, _ string) (uint64, []string, error) {
	return f.shardID, f.owners, f.err
}

// validOpts returns a passphrase-target MigrationOptions that passes Validate.
func validOpts() seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("hunter2")),
	}
}

// TestCoordinator_Request_WritesIntent proves a leader request resolves owners
// and writes a generation-1 intent record carrying the resolved owner set/shard.
func TestCoordinator_Request_WritesIntent(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return true }, func() int64 { return 1000 })

	m, err := c.Request(ctx, tid, db, validOpts())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if m.Generation != 1 || m.ShardID != 7 || len(m.Owners) != 3 {
		t.Fatalf("intent mismatch: %+v", m)
	}
	if m.Mechanism != seal.MechanismSwap || m.RequestedAtUnix != 1000 {
		t.Fatalf("intent fields mismatch: %+v", m)
	}
	got, _ := s.GetMigration(ctx, tid, db)
	if got == nil || got.Generation != 1 {
		t.Fatalf("intent not persisted: %+v", got)
	}
}

// TestCoordinator_Request_BumpsGeneration proves a re-request for the same scope
// advances the generation past the prior record (so stale acks never count).
func TestCoordinator_Request_BumpsGeneration(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}},
		func() bool { return true }, nil)

	first, err := c.Request(ctx, tid, db, validOpts())
	if err != nil || first.Generation != 1 {
		t.Fatalf("first Request = %+v, %v", first, err)
	}
	second, err := c.Request(ctx, tid, db, validOpts())
	if err != nil || second.Generation != 2 {
		t.Fatalf("second Request = %+v, %v; want gen 2", second, err)
	}
}

// shamirOpts returns a Shamir-target MigrationOptions that passes Validate.
func shamirOpts(threshold, total int) seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: threshold,
		ShamirTotal:     total,
	}
}

// TestCoordinator_Request_CarriesShamirFields proves a Shamir-target request's
// ShamirThreshold/ShamirTotal are threaded into the persisted ClusterMigration
// record (both the return value and what a fresh Store read sees) — the exact
// field that was missing, causing every owner's reconciler to rebuild
// threshold=0/total=0 and fail seal.MigrationOptions.Validate() forever.
func TestCoordinator_Request_CarriesShamirFields(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}},
		func() bool { return true }, nil)

	m, err := c.Request(ctx, tid, db, shamirOpts(2, 3))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if m.Target != seal.StrategyShamir || m.ShamirThreshold != 2 || m.ShamirTotal != 3 {
		t.Fatalf("intent shamir fields = target=%s threshold=%d total=%d, want shamir/2/3",
			m.Target, m.ShamirThreshold, m.ShamirTotal)
	}
	got, err := s.GetMigration(ctx, tid, db)
	if err != nil || got == nil {
		t.Fatalf("GetMigration = %v, %v", got, err)
	}
	if got.ShamirThreshold != 2 || got.ShamirTotal != 3 {
		t.Fatalf("persisted shamir fields = %d/%d, want 2/3", got.ShamirThreshold, got.ShamirTotal)
	}
}

// TestCoordinator_Request_RejectsInvalidShamirThreshold proves an out-of-range
// Shamir M-of-N is rejected with a typed error AT REQUEST TIME — before any
// owner is resolved or any intent record written — rather than being silently
// accepted and only failing later, invisibly, inside the reconciler.
func TestCoordinator_Request_RejectsInvalidShamirThreshold(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, func() bool { return true }, nil)

	// threshold(1) < 2.
	if _, err := c.Request(ctx, tid, db, shamirOpts(1, 3)); !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Fatalf("Request(threshold=1) = %v, want ErrShamirThresholdInvalid", err)
	}
	// threshold(4) > total(3).
	if _, err := c.Request(ctx, tid, db, shamirOpts(4, 3)); !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Fatalf("Request(threshold=4,total=3) = %v, want ErrShamirThresholdInvalid", err)
	}
	requireMigrationAbsent(t, ctx, s, "an invalid Shamir request must not write an intent record")
}

// TestCoordinator_Request_NotLeader proves a follower forwards (errors) rather
// than writing the replicated intent twice.
func TestCoordinator_Request_NotLeader(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, func() bool { return false }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts()); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("Request on follower = %v, want ErrNotLeader", err)
	}
	requireMigrationAbsent(t, ctx, s, "follower must not write an intent record")
}

// TestCoordinator_Request_NoOwners rejects a scope that resolves to no replicas.
func TestCoordinator_Request_NoOwners(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: nil}, func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts()); !errors.Is(err, ErrNoOwners) {
		t.Fatalf("Request with no owners = %v, want ErrNoOwners", err)
	}
}

// TestCoordinator_Request_InvalidOpts rejects a none/empty target before any
// owner resolution or write.
func TestCoordinator_Request_InvalidOpts(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, seal.MigrationOptions{TargetStrategy: seal.StrategyNone}); err == nil {
		t.Fatal("Request with none target must error")
	}
	requireMigrationAbsent(t, ctx, s, "invalid request must not write an intent record")
}

// TestCoordinator_Request_OwnerResolveError surfaces a resolver failure wrapped.
func TestCoordinator_Request_OwnerResolveError(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	sentinel := errors.New("placement unavailable")
	c := NewCoordinator(s, fixedOwners{err: sentinel}, func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts()); !errors.Is(err, sentinel) {
		t.Fatalf("Request = %v, want wrapped resolver error", err)
	}
}

// TestCoordinator_Request_RecordCarriesNoCredential proves the replicated
// intent record carries no credential in any form -- not the operator's
// passphrase, and no per-node envelope of it either. Each owner supplies its
// own credential locally. The encoded field set is pinned, so a field that
// could carry one fails here rather than in review.
func TestCoordinator_Request_RecordCarriesNoCredential(t *testing.T) {
	ctx := context.Background()
	kv := newMemKV()
	s := NewStore(kv)
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2"}},
		func() bool { return true }, nil)
	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	raw, err := kv.Get(ctx, migrationKey(tid, db))
	if err != nil || len(raw) == 0 {
		t.Fatalf("record not written: %v", err)
	}
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Fatal("the operator's credential is in the replicated record")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"tenant_id": true, "database": true, "shard_id": true, "target": true,
		"mechanism": true, "shamir_threshold": true, "shamir_total": true,
		"owners": true, "generation": true, "requested_at_unix": true,
	}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("record field %q is not in the pinned set", k)
		}
	}
}

// TestCoordinator_Reap deletes a completed migration and is a no-op otherwise.
func TestCoordinator_Reap(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}},
		func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	// Incomplete -> no reap.
	if reaped, err := c.Reap(ctx, tid, db); reaped || err != nil {
		t.Fatalf("Reap(incomplete) = %v, %v; want false, nil", reaped, err)
	}
	// Complete it -> reap clears the record.
	ack(t, s, "n1", 1, seal.PhaseComplete)
	if reaped, err := c.Reap(ctx, tid, db); !reaped || err != nil {
		t.Fatalf("Reap(complete) = %v, %v; want true, nil", reaped, err)
	}
	requireMigrationAbsent(t, ctx, s, "Reap must delete the completed intent record")
}

// TestCoordinator_Reap_NotLeader never deletes from a follower.
func TestCoordinator_Reap_NotLeader(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	leader := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}},
		func() bool { return true }, nil)
	if _, err := leader.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	ack(t, s, "n1", 1, seal.PhaseComplete)

	follower := NewCoordinator(s, fixedOwners{}, func() bool { return false }, nil)
	if reaped, err := follower.Reap(ctx, tid, db); reaped || err != nil {
		t.Fatalf("follower Reap = %v, %v; want false, nil", reaped, err)
	}
	if m, _ := s.GetMigration(ctx, tid, db); m == nil {
		t.Fatal("follower must not delete the intent record")
	}
}

// TestCoordinator_IsLeader reports the configured probe.
func TestCoordinator_IsLeader(t *testing.T) {
	s := NewStore(newMemKV())
	if !NewCoordinator(s, fixedOwners{}, func() bool { return true }, nil).IsLeader() {
		t.Fatal("leader probe true, IsLeader false")
	}
	if NewCoordinator(s, fixedOwners{}, func() bool { return false }, nil).IsLeader() {
		t.Fatal("leader probe false, IsLeader true")
	}
}

// TestCoordinator_Request_PreparesBeforeTheRecordExists pins the order the
// accepting node's credential depends on: the prepare hook sees the complete
// record -- generation included -- before any owner can, because nothing is
// written until it returns. Its failure writes nothing.
func TestCoordinator_Request_PreparesBeforeTheRecordExists(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, func() bool { return true }, nil)
	if _, err := c.Request(ctx, tid, db, validOpts()); err != nil {
		t.Fatal(err)
	}
	var seen *ClusterMigration
	m, err := c.Request(ctx, tid, db, validOpts(), WithPrepare(func(m *ClusterMigration) (func(), error) {
		seen = m
		if got, _ := s.GetMigration(ctx, tid, db); got == nil || got.Generation != 1 {
			t.Errorf("record at prepare = %+v, want generation 1 still in place", got)
		}
		return func() {}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.Generation != 2 || m.Generation != 2 {
		t.Fatalf("prepare saw %+v, request returned %+v; want generation 2 for both", seen, m)
	}

	boom := errors.New("cannot hold")
	if _, err := c.Request(ctx, tid, db, validOpts(), WithPrepare(func(*ClusterMigration) (func(), error) {
		return nil, boom
	})); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the prepare failure", err)
	}
	if got, _ := s.GetMigration(ctx, tid, db); got.Generation != 2 {
		t.Fatalf("a failed prepare wrote generation %d", got.Generation)
	}
}

// TestCoordinator_Request_UndoesThePrepareWhenTheWriteFails proves a
// credential held for a record that was never written is released.
func TestCoordinator_Request_UndoesThePrepareWhenTheWriteFails(t *testing.T) {
	kv := &readOKWriteErrKV{inner: newMemKV(), putErr: errBoom}
	c := NewCoordinator(NewStore(kv), fixedOwners{shardID: 7, owners: []string{"n1"}}, func() bool { return true }, nil)
	undone := false
	_, err := c.Request(context.Background(), tid, db, validOpts(), WithPrepare(func(*ClusterMigration) (func(), error) {
		return func() { undone = true }, nil
	}))
	if !errors.Is(err, errBoom) || !undone {
		t.Fatalf("err = %v, undone = %v; want the write failure and the prepare undone", err, undone)
	}
}
