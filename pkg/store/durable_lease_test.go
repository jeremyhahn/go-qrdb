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

package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
)

// These tests drive the two lease-serving raft state machines through the
// SAME service calls a node makes -- service.LeaseService over a host, and
// the OpMVCCPut command every lease-attaching write proposes -- and restart the state machine the way
// a process restart does: a new state machine and a new Lessor over the same
// directory, with no log replay (both persist their last applied index, so
// quicraft replays nothing that was already applied).
//
// Before durable leases every one of them failed: the Lessor lived only in
// memory, so the restarted replica came back with no leases, the key bound to
// the lease stayed in the store with nothing left to expire it -- a lock held
// at shutdown stayed held forever -- and the next grant reused ID 1.

// smKind builds an unopened state machine with lr attached.
type smKind struct {
	name string
	new  func(lr *lease.Lessor) sm.DiskStateMachine
}

var leaseSMKinds = []smKind{
	{name: "pebble", new: func(lr *lease.Lessor) sm.DiskStateMachine {
		s := store.NewPebbleStateMachine(nil)
		s.SetLessor(lr)
		return s
	}},
	{name: "mvcc", new: func(lr *lease.Lessor) sm.DiskStateMachine {
		s := store.NewMVCCStateMachine(nil)
		s.SetLessor(lr)
		return s
	}},
}

// node is one incarnation of a replica: a state machine opened over dir with
// a fresh Lessor, and the services a node composes over it.
type node struct {
	sm    sm.DiskStateMachine
	lr    *lease.Lessor
	host  service.Host
	lease service.LeaseServicer
	kv    service.KVServicer
}

// openNode starts an incarnation of the replica whose state lives in dir.
func openNode(t *testing.T, kind smKind, dir string) *node {
	t.Helper()
	return openNodeAt(t, kind, dir, clocktest.New())
}

// openNodeAt is openNode with the Lessor on clk, so a test that restarts a
// node can carry one fake clock across the restart and assert expiry exactly.
func openNodeAt(t *testing.T, kind smKind, dir string, clk clock.Clock) *node {
	t.Helper()
	lr := lease.NewLessor(lease.WithClock(clk))
	s := kind.new(lr)
	if _, err := s.Open(context.Background(), dir, make(chan struct{})); err != nil {
		t.Fatalf("%s: open: %v", kind.name, err)
	}
	host := backend.NewLocalBackend(s)
	return &node{
		sm:    s,
		lr:    lr,
		host:  host,
		lease: service.NewLeaseService(host, 1),
		kv:    service.NewKVService(host, 1),
	}
}

// putWithLease proposes the write that binds key to lease id: the command
// MVCCKVService.PutWithLease and the lock service's transaction both carry.
func (n *node) putWithLease(t *testing.T, key, value string, id int64) {
	t.Helper()
	cmd, err := protocol.EncodeMVCCPut([]byte(key), []byte(value), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.host.SyncPropose(context.Background(), 1, cmd); err != nil {
		t.Fatalf("put %s with lease %d: %v", key, id, err)
	}
}

// exists reports whether key holds a value.
func (n *node) exists(t *testing.T, key string) bool {
	t.Helper()
	_, err := n.kv.Get(context.Background(), key)
	return err == nil
}

// stop ends the incarnation, as a process exit does.
func (n *node) stop(t *testing.T) {
	t.Helper()
	if err := n.sm.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	n.lr.Close()
}

// hasKey reports whether l lists key among its attached keys.
func hasKey(l *lease.Lease, key string) bool {
	for _, k := range l.Keys {
		if bytes.Equal(k, []byte(key)) {
			return true
		}
	}
	return false
}

func TestDurableLease_SurvivesRestartAndStillExpiresItsKeys(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()

			clk := clocktest.New()
			n := openNodeAt(t, kind, dir, clk)
			l, err := n.lease.Grant(ctx, 60)
			if err != nil {
				t.Fatalf("grant: %v", err)
			}
			n.putWithLease(t, "lock/scheduler", "holder-a", l.ID)
			n.stop(t)

			// Most of the TTL elapses while the node is down.
			clk.Advance(45 * time.Second)
			n = openNodeAt(t, kind, dir, clk)
			got, err := n.lease.TimeToLive(ctx, l.ID)
			if err != nil {
				t.Fatalf("lease %d did not survive the restart: %v", l.ID, err)
			}
			if got.GrantedTTL != 60 {
				t.Fatalf("granted TTL after restart = %d, want 60", got.GrantedTTL)
			}
			// A recovered lease gets one full granted TTL from recovery.
			if got.RemainingTTL != 60 {
				t.Fatalf("remaining TTL after restart = %d, want a full TTL of 60", got.RemainingTTL)
			}
			if !hasKey(got, "lock/scheduler") {
				t.Fatalf("attachment of lock/scheduler lost across the restart: keys=%q", got.Keys)
			}

			// The recovered lease still deletes its key, which is what frees
			// a dead holder's lock.
			if err := n.lease.Revoke(ctx, l.ID); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			if n.exists(t, "lock/scheduler") {
				t.Fatal("key bound to the recovered lease survived its revoke")
			}
			n.stop(t)

			// A revoked lease stays revoked across the next restart.
			n = openNode(t, kind, dir)
			defer n.stop(t)
			if _, err := n.lease.TimeToLive(ctx, l.ID); !isLeaseNotFound(err) {
				t.Fatalf("revoked lease reappeared after restart: err=%v", err)
			}
		})
	}
}

func TestDurableLease_IDsAreNotReusedAfterRestart(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()

			n := openNode(t, kind, dir)
			first, err := n.lease.Grant(ctx, 30)
			if err != nil {
				t.Fatalf("grant: %v", err)
			}
			if err := n.lease.Revoke(ctx, first.ID); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			n.stop(t)

			n = openNode(t, kind, dir)
			defer n.stop(t)
			second, err := n.lease.Grant(ctx, 30)
			if err != nil {
				t.Fatalf("grant after restart: %v", err)
			}
			if second.ID <= first.ID {
				t.Fatalf("lease ID after restart = %d, want greater than %d (IDs must never be reused)", second.ID, first.ID)
			}
		})
	}
}

// TestDurableLease_RestartedReplicaAssignsTheSameIDs is the divergence the
// in-memory sequence caused: two replicas apply the same log, one of them
// restarts half way, and both must assign the same ID to the same grant.
func TestDurableLease_RestartedReplicaAssignsTheSameIDs(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			steady := openNode(t, kind, t.TempDir())
			defer steady.stop(t)
			restartedDir := t.TempDir()
			restarted := openNode(t, kind, restartedDir)

			a1, err := steady.lease.Grant(ctx, 30)
			if err != nil {
				t.Fatal(err)
			}
			b1, err := restarted.lease.Grant(ctx, 30)
			if err != nil {
				t.Fatal(err)
			}
			restarted.stop(t)
			restarted = openNode(t, kind, restartedDir)
			defer restarted.stop(t)

			a2, err := steady.lease.Grant(ctx, 30)
			if err != nil {
				t.Fatal(err)
			}
			b2, err := restarted.lease.Grant(ctx, 30)
			if err != nil {
				t.Fatal(err)
			}
			if a1.ID != b1.ID || a2.ID != b2.ID {
				t.Fatalf("replicas diverged: steady assigned %d,%d; restarted assigned %d,%d", a1.ID, a2.ID, b1.ID, b2.ID)
			}
		})
	}
}

func TestDurableLease_TravelsInASnapshot(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			leader := openNode(t, kind, t.TempDir())
			defer leader.stop(t)
			l, err := leader.lease.Grant(ctx, 45)
			if err != nil {
				t.Fatal(err)
			}
			leader.putWithLease(t, "svc/registration", "a", l.ID)

			var buf bytes.Buffer
			snap, err := leader.sm.PrepareSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := leader.sm.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
				t.Fatal(err)
			}

			// The follower holds leases of its own that the snapshot does
			// not. It grants TWO, so the second has an ID the snapshot never
			// issued: the first shares the leader's ID and is replaced by it,
			// which on its own would let a recovery that kept stale leases
			// pass. A key is bound to the stale lease too.
			follower := openNode(t, kind, t.TempDir())
			defer follower.stop(t)
			if _, err := follower.lease.Grant(ctx, 10); err != nil {
				t.Fatal(err)
			}
			stale, err := follower.lease.Grant(ctx, 10)
			if err != nil {
				t.Fatal(err)
			}
			if stale.ID == l.ID {
				t.Fatalf("the follower's second lease reused the leader's ID %d: the stale-lease check below would be vacuous", l.ID)
			}
			follower.putWithLease(t, "stale/bound", "x", stale.ID)
			if err := follower.sm.RecoverFromSnapshot(ctx, &buf, make(chan struct{})); err != nil {
				t.Fatalf("recover: %v", err)
			}

			got, err := follower.lease.TimeToLive(ctx, l.ID)
			if err != nil {
				t.Fatalf("lease %d not restored from the snapshot: %v", l.ID, err)
			}
			if got.GrantedTTL != 45 || !hasKey(got, "svc/registration") {
				t.Fatalf("restored lease = %+v, want granted TTL 45 with svc/registration attached", got)
			}
			if _, err := follower.lease.TimeToLive(ctx, stale.ID); !isLeaseNotFound(err) {
				t.Fatalf("lease %d absent from the snapshot survived recovery: err=%v", stale.ID, err)
			}
			if follower.exists(t, "stale/bound") {
				t.Fatal("a key bound to a lease absent from the snapshot survived recovery")
			}
			if err := follower.lease.Revoke(ctx, l.ID); err != nil {
				t.Fatal(err)
			}
			if follower.exists(t, "svc/registration") {
				t.Fatal("key bound to the snapshot-restored lease survived its revoke")
			}
		})
	}
}

// isLeaseNotFound reports whether err is the typed missing-lease answer.
func isLeaseNotFound(err error) bool {
	var nf *service.LeaseNotFoundError
	return errors.As(err, &nf)
}
