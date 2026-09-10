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
		Credentials:    seal.Credentials{Secret: []byte("hunter2")},
	}
}

// TestCoordinator_Request_WritesIntent proves a leader request resolves owners
// and writes a generation-1 intent record carrying the resolved owner set/shard.
func TestCoordinator_Request_WritesIntent(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}}, nil,
		func() bool { return true }, func() int64 { return 1000 })

	m, err := c.Request(ctx, tid, db, validOpts(), nil)
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
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, nil,
		func() bool { return true }, nil)

	first, err := c.Request(ctx, tid, db, validOpts(), nil)
	if err != nil || first.Generation != 1 {
		t.Fatalf("first Request = %+v, %v", first, err)
	}
	second, err := c.Request(ctx, tid, db, validOpts(), nil)
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
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2", "n3"}}, nil,
		func() bool { return true }, nil)

	m, err := c.Request(ctx, tid, db, shamirOpts(2, 3), nil)
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
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, nil, func() bool { return true }, nil)

	// threshold(1) < 2.
	if _, err := c.Request(ctx, tid, db, shamirOpts(1, 3), nil); !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Fatalf("Request(threshold=1) = %v, want ErrShamirThresholdInvalid", err)
	}
	// threshold(4) > total(3).
	if _, err := c.Request(ctx, tid, db, shamirOpts(4, 3), nil); !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Fatalf("Request(threshold=4,total=3) = %v, want ErrShamirThresholdInvalid", err)
	}
	requireMigrationAbsent(t, ctx, s, "an invalid Shamir request must not write an intent record")
}

// TestCoordinator_Request_NotLeader proves a follower forwards (errors) rather
// than writing the replicated intent twice.
func TestCoordinator_Request_NotLeader(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, nil, func() bool { return false }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts(), nil); !errors.Is(err, ErrNotLeader) {
		t.Fatalf("Request on follower = %v, want ErrNotLeader", err)
	}
	requireMigrationAbsent(t, ctx, s, "follower must not write an intent record")
}

// TestCoordinator_Request_NoOwners rejects a scope that resolves to no replicas.
func TestCoordinator_Request_NoOwners(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: nil}, nil, func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts(), nil); !errors.Is(err, ErrNoOwners) {
		t.Fatalf("Request with no owners = %v, want ErrNoOwners", err)
	}
}

// TestCoordinator_Request_InvalidOpts rejects a none/empty target before any
// owner resolution or write.
func TestCoordinator_Request_InvalidOpts(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{owners: []string{"n1"}}, nil, func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, seal.MigrationOptions{TargetStrategy: seal.StrategyNone}, nil); err == nil {
		t.Fatal("Request with none target must error")
	}
	requireMigrationAbsent(t, ctx, s, "invalid request must not write an intent record")
}

// TestCoordinator_Request_OwnerResolveError surfaces a resolver failure wrapped.
func TestCoordinator_Request_OwnerResolveError(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	sentinel := errors.New("placement unavailable")
	c := NewCoordinator(s, fixedOwners{err: sentinel}, nil, func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts(), nil); !errors.Is(err, sentinel) {
		t.Fatalf("Request = %v, want wrapped resolver error", err)
	}
}

// recordingSealer seals each owner to a deterministic per-node blob and records
// the owners + credential it was given.
type recordingSealer struct {
	owners []string
	cred   []byte
}

func (s *recordingSealer) SealFor(_ context.Context, _ *ClusterMigration, owners []string, credential []byte) (map[string][]byte, error) {
	s.owners = append([]string(nil), owners...)
	s.cred = append([]byte(nil), credential...)
	out := make(map[string][]byte, len(owners))
	for _, o := range owners {
		out[o] = []byte("sealed-for-" + o)
	}
	return out, nil
}

// TestCoordinator_Request_SealsCredentialPerOwner proves the credential is sealed
// per owner and the per-node ciphertext blobs are stored in the record (never the
// plaintext).
func TestCoordinator_Request_SealsCredentialPerOwner(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	sealer := &recordingSealer{}
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1", "n2"}}, sealer,
		func() bool { return true }, nil)

	m, err := c.Request(ctx, tid, db, validOpts(), []byte("operator-passphrase"))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if string(sealer.cred) != "operator-passphrase" || len(sealer.owners) != 2 {
		t.Fatalf("sealer saw cred=%q owners=%v", sealer.cred, sealer.owners)
	}
	if len(m.Credentials) != 2 || string(m.Credentials["n1"]) != "sealed-for-n1" {
		t.Fatalf("record credentials = %v, want per-owner sealed blobs", m.Credentials)
	}
	// The plaintext credential must not appear anywhere in the persisted record.
	got, _ := s.GetMigration(ctx, tid, db)
	for node, blob := range got.Credentials {
		if string(blob) == "operator-passphrase" {
			t.Fatalf("plaintext credential leaked into record for %s", node)
		}
	}
}

// TestCoordinator_Request_NoSealerNoCredentials proves the record carries no
// credentials when no sealer is wired (out-of-band credential deployments).
func TestCoordinator_Request_NoSealerNoCredentials(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, nil,
		func() bool { return true }, nil)
	m, err := c.Request(ctx, tid, db, validOpts(), []byte("ignored-without-sealer"))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(m.Credentials) != 0 {
		t.Fatalf("record credentials = %v, want empty without a sealer", m.Credentials)
	}
}

// TestCoordinator_Reap deletes a completed migration and is a no-op otherwise.
func TestCoordinator_Reap(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	c := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, nil,
		func() bool { return true }, nil)

	if _, err := c.Request(ctx, tid, db, validOpts(), nil); err != nil {
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
	leader := NewCoordinator(s, fixedOwners{shardID: 7, owners: []string{"n1"}}, nil,
		func() bool { return true }, nil)
	if _, err := leader.Request(ctx, tid, db, validOpts(), nil); err != nil {
		t.Fatalf("Request: %v", err)
	}
	ack(t, s, "n1", 1, seal.PhaseComplete)

	follower := NewCoordinator(s, fixedOwners{}, nil, func() bool { return false }, nil)
	if reaped, err := follower.Reap(ctx, tid, db); reaped || err != nil {
		t.Fatalf("follower Reap = %v, %v; want false, nil", reaped, err)
	}
	if m, _ := s.GetMigration(ctx, tid, db); m == nil {
		t.Fatal("follower must not delete the intent record")
	}
}
