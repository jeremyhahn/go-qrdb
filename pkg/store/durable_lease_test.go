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

// A checkpointed lease restarts from what it had left, not from a full TTL:
// that is what stops a replica that restarts -- or becomes leader -- from
// handing a dead holder's lease more time than it was renewed for. A replicated
// keep-alive deletes the record, so the next restart is back to a full TTL.
func TestDurableLease_RestartCountsFromTheCheckpoint(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()

			n := openNode(t, kind, dir)
			l, err := n.lease.Grant(ctx, 60)
			if err != nil {
				t.Fatalf("grant: %v", err)
			}
			// Entry 1 granted the lease. The first checkpoint, entry 2, starts
			// the group recording renewals and is itself rejected; the second,
			// sampled once entry 2 applied, records the 20s.
			cp := service.NewLeaseCheckpointer(n.host, 1)
			for _, sampled := range []uint64{1, 2} {
				if err := cp.Checkpoint(ctx, 0, sampled, []lease.Checkpoint{{ID: l.ID, RemainingTTL: 20}}); err != nil {
					t.Fatalf("checkpoint sampled at %d: %v", sampled, err)
				}
			}
			n.stop(t)

			n = openNode(t, kind, dir)
			got, err := n.lease.TimeToLive(ctx, l.ID)
			if err != nil {
				t.Fatalf("lease %d did not survive the restart: %v", l.ID, err)
			}
			if got.GrantedTTL != 60 {
				t.Fatalf("granted TTL after restart = %d, want 60", got.GrantedTTL)
			}
			if got.RemainingTTL > 20 || got.RemainingTTL < 18 {
				t.Fatalf("remaining TTL after restart = %d, want the checkpointed 20", got.RemainingTTL)
			}

			if _, err := n.lease.KeepAlive(ctx, l.ID); err != nil {
				t.Fatalf("keep-alive: %v", err)
			}
			n.stop(t)

			n = openNode(t, kind, dir)
			defer n.stop(t)
			got, err = n.lease.TimeToLive(ctx, l.ID)
			if err != nil {
				t.Fatalf("lease %d did not survive the second restart: %v", l.ID, err)
			}
			if got.RemainingTTL < 58 {
				t.Fatalf("remaining TTL after a keep-alive and restart = %d, want a full TTL (~60): the keep-alive left the checkpoint behind", got.RemainingTTL)
			}
		})
	}
}

// leaseEntries encodes cmds as consecutive Raft log entries from index first,
// the way a group's log delivers them, so a test sets every applied index the
// checkpoint rule judges by.
func leaseEntries(t *testing.T, first uint64, cmds ...func() ([]byte, error)) []sm.Entry {
	t.Helper()
	entries := make([]sm.Entry, len(cmds))
	for i, encode := range cmds {
		cmd, err := encode()
		if err != nil {
			t.Fatalf("encode entry %d: %v", first+uint64(i), err)
		}
		entries[i] = sm.Entry{Index: first + uint64(i), Cmd: cmd}
	}
	return entries
}

// supersededLog is the log of the #681 finding: a 600s grant (lease 1), the
// first checkpoint, which starts the group recording renewals, a replicated
// keep-alive, and a 100s checkpoint the leader sampled before that keep-alive.
func supersededLog(t *testing.T) []sm.Entry {
	t.Helper()
	return leaseEntries(t, 1,
		func() ([]byte, error) { return protocol.EncodeLeaseGrant(600) },
		func() ([]byte, error) {
			return protocol.EncodeLeaseCheckpoint(0, 1, []lease.Checkpoint{{ID: 1, RemainingTTL: 600}})
		},
		func() ([]byte, error) { return protocol.EncodeLeaseKeepAlive(1) },
		func() ([]byte, error) {
			return protocol.EncodeLeaseCheckpoint(0, 2, []lease.Checkpoint{{ID: 1, RemainingTTL: 100}})
		},
	)
}

// apply applies entries to the node's state machine in one batch.
func (n *node) apply(t *testing.T, entries []sm.Entry) {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := n.sm.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("update: %v", err)
	}
}

// remaining is the TTL lease id has left on the node.
func (n *node) remaining(t *testing.T, id int64) int64 {
	t.Helper()
	got, err := n.lease.TimeToLive(context.Background(), id)
	if err != nil {
		t.Fatalf("lease %d: %v", id, err)
	}
	return got.RemainingTTL
}

// A checkpoint sampled before a replicated keep-alive that applied ahead of it
// is rejected, so the keep-alive's TTL stands -- on the replica that applied
// it, after that replica restarts, on a replica recovered from a snapshot
// taken before the checkpoint that then applies it, and on one that replays
// the whole log (#681).
func TestDurableLease_ARenewalSupersedesAnEarlierCheckpoint(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			log := supersededLog(t)

			dir := t.TempDir()
			n := openNode(t, kind, dir)
			n.apply(t, log)
			if got := n.remaining(t, 1); got != 600 {
				t.Fatalf("remaining after the superseded checkpoint = %d, want 600", got)
			}
			n.stop(t)
			n = openNode(t, kind, dir)
			if got := n.remaining(t, 1); got < 598 {
				t.Fatalf("restarted remaining = %d, want the renewal's 600", got)
			}
			n.stop(t)

			leader := openNode(t, kind, t.TempDir())
			defer leader.stop(t)
			leader.apply(t, log[:3])
			var buf bytes.Buffer
			snap, err := leader.sm.PrepareSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := leader.sm.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
				t.Fatal(err)
			}
			recoveredDir := t.TempDir()
			recovered := openNode(t, kind, recoveredDir)
			if err := recovered.sm.RecoverFromSnapshot(ctx, &buf, make(chan struct{})); err != nil {
				t.Fatalf("recover: %v", err)
			}
			recovered.apply(t, log[3:])
			recovered.stop(t)
			recovered = openNode(t, kind, recoveredDir)
			defer recovered.stop(t)
			if got := recovered.remaining(t, 1); got < 598 {
				t.Fatalf("snapshot-recovered remaining = %d, want the renewal's 600", got)
			}

			replayed := openNode(t, kind, t.TempDir())
			defer replayed.stop(t)
			for _, e := range log {
				replayed.apply(t, []sm.Entry{e})
			}
			if got := replayed.remaining(t, 1); got != 600 {
				t.Fatalf("replayed remaining = %d, want the renewal's 600", got)
			}
		})
	}
}

// A checkpoint no keep-alive superseded applies, and a restart counts from it.
func TestDurableLease_AnUnsupersededCheckpointApplies(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			log := supersededLog(t)
			unsuperseded := append(log[:2:2], leaseEntries(t, 3, func() ([]byte, error) {
				return protocol.EncodeLeaseCheckpoint(0, 2, []lease.Checkpoint{{ID: 1, RemainingTTL: 100}})
			})...)

			dir := t.TempDir()
			n := openNode(t, kind, dir)
			n.apply(t, unsuperseded)
			n.stop(t)
			n = openNode(t, kind, dir)
			defer n.stop(t)
			if got := n.remaining(t, 1); got > 100 || got < 98 {
				t.Fatalf("restarted remaining = %d, want the checkpointed 100", got)
			}
		})
	}
}

// With the lease-checkpoint gate off no checkpoint is ever proposed, so the
// group never starts recording renewals: a keep-alive writes nothing beyond
// what it wrote before the rule, and a restart is back to a full TTL.
func TestDurableLease_WithoutACheckpointRenewalsAreNotRecorded(t *testing.T) {
	for _, kind := range leaseSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			n := openNode(t, kind, dir)
			n.apply(t, supersededLog(t)[:1])
			n.apply(t, leaseEntries(t, 2, func() ([]byte, error) { return protocol.EncodeLeaseKeepAlive(1) }))

			var buf bytes.Buffer
			snap, err := n.sm.PrepareSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := n.sm.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
				t.Fatal(err)
			}
			// The control: a group that did apply a checkpoint carries both
			// records in its snapshot, so their absence above is a finding.
			control := openNode(t, kind, t.TempDir())
			defer control.stop(t)
			control.apply(t, supersededLog(t)[:3])
			var controlBuf bytes.Buffer
			controlSnap, err := control.sm.PrepareSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := control.sm.SaveSnapshot(ctx, controlSnap, &controlBuf, make(chan struct{})); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{lease.TablePrefix + "r/", lease.TablePrefix + "rfrom"} {
				if bytes.Contains(buf.Bytes(), []byte(k)) {
					t.Fatalf("snapshot carries %q with no checkpoint applied", k)
				}
				if !bytes.Contains(controlBuf.Bytes(), []byte(k)) {
					t.Fatalf("control snapshot lacks %q: the absence check cannot fail", k)
				}
			}
			n.stop(t)
			n = openNode(t, kind, dir)
			defer n.stop(t)
			if got := n.remaining(t, 1); got < 598 {
				t.Fatalf("restarted remaining = %d, want a full 600", got)
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
