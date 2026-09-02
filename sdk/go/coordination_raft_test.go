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

package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// bootRaftNode brings up a real single-replica raft group with no protocol
// servers and returns both the coordination surface and the node's KV client
// (used to wait for the group to become writable). The lease and watch families
// are raft-only, so this is the composition that exercises them end to end
// against real services — the clustered half of the mode mapping documented on
// the coordination surface.
//
// Seal is ENABLED here, which routes construction through
// completeClusteredBoot. That is one of TWO clustered composition paths; see
// bootRaftNodeOpts for the other and for why the distinction is load-bearing.
func bootRaftNode(t *testing.T) (*sdk.Coordinator, transport.KVClient) {
	t.Helper()
	return bootRaftNodeOpts(t, raftNodeOpts{sealEnabled: true})
}

// raftNodeOpts selects which clustered composition path the node is built
// through, and how its construction context behaves.
type raftNodeOpts struct {
	// sealEnabled routes construction through completeClusteredBoot (true) or
	// through the seal-disabled branch of NewClusteredNode (false).
	//
	// THESE ARE TWO SEPARATE WIRING SITES, not one path with a flag. Each has
	// its own host construction, its own BuildServices call, and its own copy
	// of every background service the node starts — which is exactly how the
	// lease-expiry sweeper came to be missing from BOTH of them while the
	// daemon had it (go-qrdb#174). A test that covers one covers one.
	sealEnabled bool

	// cancelBootCtx cancels the context passed to NewClusteredNode immediately
	// after construction returns.
	//
	// A boot request's ctx is dead as soon as New or Unseal returns, so any
	// goroutine the node starts on it stops there too. Node.background()
	// exists to give those goroutines a node-lifetime context instead, and
	// nothing asserted it: every test built its node with context.Background(),
	// which is never cancelled, so the sweeper survived by accident rather
	// than by design.
	cancelBootCtx bool
}

func bootRaftNodeOpts(t *testing.T, opts raftNodeOpts) (*sdk.Coordinator, transport.KVClient) {
	t.Helper()

	port, err := qrdbdharness.ReserveTCPPort()
	if err != nil {
		t.Fatalf("reserve raft port: %v", err)
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.RaftAddress = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.Raft.ReplicaID = 1
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Paths: []string{t.TempDir()}}}
	cfg.Seal.Enabled = opts.sealEnabled
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	bootCtx, cancelBoot := context.WithCancel(context.Background())
	// Registered UNCONDITIONALLY and before any error return: a CancelFunc is
	// safe to call more than once, and an unconditional registration is both
	// leak-free on every path and legible to go vet's lostcancel check, which
	// cannot see a cancel that only some branches reach.
	t.Cleanup(cancelBoot)

	members := map[uint64]string{1: cfg.Raft.RaftAddress}
	n, err := node.NewClusteredNode(bootCtx, cfg, members, nil)
	if err != nil {
		t.Fatalf("NewClusteredNode(seal=%v): %v", opts.sealEnabled, err)
	}
	t.Cleanup(func() { _ = n.Close() })

	if opts.cancelBootCtx {
		// The caller's boot context ends HERE, as it does for every real
		// consumer the moment its New call returns. Everything the node does
		// from now on must be running on its OWN context.
		cancelBoot()
	}

	kv := n.Client().KV()
	awaitWritable(t, kv)

	coord, err := n.Client().Coordination()
	if err != nil {
		t.Fatalf("Coordination: %v", err)
	}
	return coord, kv
}

// assertLeaseKeyReaped acquires a short lease, waits for Done, and then
// asserts the key bound to it is actually deleted.
//
// The assertion reads THE STORE, not Lessor.Leases(): that getter filters
// expired leases, so asserting on it passes whether or not anything was
// reaped — which is exactly how test/integration/kv_test.go:1050 stayed
// vacuous. It also asserts the key EXISTS while the lease is held, so the
// reap assertion cannot pass against a key that was never written.
func assertLeaseKeyReaped(t *testing.T, coord *sdk.Coordinator, key string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	l, err := coord.AcquireLease(ctx, key, 1*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease(%q): %v", key, err)
	}

	got, err := coord.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(%q) while lease held: %v", key, err)
	}
	if got == nil {
		t.Fatalf("bound key %q must exist while the lease is held; the reap "+
			"assertion below would otherwise prove nothing", key)
	}

	select {
	case <-l.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("Done did not close after the un-renewed lease should have expired")
	}

	// Done closing means the HOLDER knows. The reap is a separate event: the
	// leader proposes a revoke on its next sweep tick and every replica applies
	// the delete. Poll rather than sleep a fixed budget — the sweep interval is
	// lease.DefaultExpiryInterval and the proposal has to round-trip consensus.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := coord.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%q) after expiry: %v", key, err)
		}
		if got == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bound key %q still present 30s after the lease expired and "+
				"Done closed: nothing is reaping expired leases on this clustered "+
				"path (go-qrdb#174). A contender would receive LeaseHeldError "+
				"forever.", key)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended while waiting for the reap: %v", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// awaitWritable blocks until the single-replica raft group has completed its
// election and accepts writes, or the deadline passes. A write attempted before
// the election completes fails with a transient not-leader / read-index-not-
// ready error; those are retried, anything else is returned to the caller's
// assertion. Mirrors the conformance harness helper of the same name.
func awaitWritable(t *testing.T, kv transport.KVClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := kv.Put(ctx, "coordination/readiness", []byte("1"))
		if err == nil {
			return
		}
		var notLeader *transport.NotLeaderError
		if !errors.As(err, &notLeader) && !errors.Is(err, quicraft.ErrReadIndexNotReady) {
			t.Fatalf("raft node not writable (non-transient): %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("raft node never became writable: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRaft_LeaseLifecycle exercises acquire → key present → renew → release →
// Done fires and key gone, against real lease and MVCC services.
func TestRaft_LeaseLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	coord, _ := bootRaftNode(t)

	l, err := coord.AcquireLease(ctx, "leader/lock", 10*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if l.Key() != "leader/lock" {
		t.Fatalf("Key()=%q, want leader/lock", l.Key())
	}

	// The bound key is present while the lease is held.
	got, err := coord.Get(ctx, "leader/lock")
	if err != nil || got == nil {
		t.Fatalf("bound key must be present: got (%v, %v)", got, err)
	}

	// Renew keeps it alive.
	if err := l.Renew(ctx); err != nil {
		t.Fatalf("Renew: %v", err)
	}

	// Release revokes it, closes Done, and deletes the bound key.
	if err := l.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after Release")
	}
	got, err = coord.Get(ctx, "leader/lock")
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if got != nil {
		t.Fatalf("bound key must be deleted after release, got %q", got)
	}
}

// TestRaft_LeaseTTLExpiry acquires a lease and never renews it: the holder must
// learn it lost the lease when Done closes, AND the key bound to that lease
// must actually be reaped — on BOTH clustered composition paths, and with the
// caller's boot context already cancelled.
//
// This test used to assert only the first half, on the reasoning that reaping
// is "the leader sweeper's job, whose cadence and shard placement are the
// server's concern, not this thin surfacing layer's". That reasoning was wrong
// about WHICH layer composes the sweeper. pkg/lease.RunExpiry was started from
// cmd/qrdbd and pkg/server only -- never from sdk/go/node -- so an embedded
// clustered node closed Done on time and then left the bound key in place
// forever (go-qrdb#174: measured present 60s after a 1s lease whose Done closed
// at ~996ms).
//
// The distinction matters because a consumer's HA design can make the bound
// key's existence the ownership signal. Done closing tells the DEAD leader it
// lost; only the reap lets a REPLACEMENT take over. With the key never deleted,
// every contender receives LeaseHeldError forever -- an embedded clustered
// control plane that can fence a dead leader but can never elect a successor.
//
// THE SUBTESTS ARE THE POINT, and the first version of this test had only the
// middle one. The fix wires the sweeper in two separate places and introduces a
// node-lifetime context so it survives boot; a single case over the sealed path
// with an uncancelled context leaves two thirds of that unverified, and a
// regression in either would have kept every assertion green.
func TestRaft_LeaseTTLExpiry(t *testing.T) {
	cases := []struct {
		name string
		opts raftNodeOpts
		// what a failure of THIS case, and only this case, would mean
		proves string
	}{
		{
			name:   "SealEnabled",
			opts:   raftNodeOpts{sealEnabled: true},
			proves: "completeClusteredBoot starts the sweeper",
		},
		{
			name:   "SealDisabled",
			opts:   raftNodeOpts{sealEnabled: false},
			proves: "the seal-disabled branch of NewClusteredNode starts it too",
		},
		{
			name:   "SealEnabled_BootContextCancelled",
			opts:   raftNodeOpts{sealEnabled: true, cancelBootCtx: true},
			proves: "the sweeper runs on the node's own context, not the boot request's",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coord, _ := bootRaftNodeOpts(t, tc.opts)
			assertLeaseKeyReaped(t, coord, "ephemeral/"+tc.name)
		})
	}
}

// TestRaft_LeaseRenewedOnSubTTLCadenceKeepsDoneOpen is the counterpart to
// TestRaft_LeaseTTLExpiry, and covers what waiting for Done cannot: that Done
// does NOT close while the holder is doing everything right.
//
// A two-second lease renewed every 1.5s is a correct cadence — the contract asks
// only for a renew shorter than the TTL. But the server truncates RemainingTTL
// to whole seconds (pkg/lease.remainingTTL), so the monitor's one-second poll
// lands on a lease with ~0.999s left and reads 0. Treating that as expiry told
// the holder it had lost a lease the server still held, before its first renewal
// even came due. This test holds across several renewal cycles and fails the
// moment Done closes early; the expiry half then proves the confirming recheck
// did not cost the loss signal itself.
func TestRaft_LeaseRenewedOnSubTTLCadenceKeepsDoneOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	coord, _ := bootRaftNode(t)

	l, err := coord.AcquireLease(ctx, "renewed/lock", 2*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	// Four cycles at 1.5s: each check happens at the moment of maximum time
	// since the last renewal, which is when an early close is most likely.
	//
	// The 500ms of slack between the 1.5s cadence and the 2s TTL is NOT a
	// safety margin, and go-qrdb#174 is why it now matters. Until the
	// lease-expiry sweeper was wired into sdk/go/node, nothing ever revoked an
	// expired lease on this path -- so a cycle that overslept its TTL on a
	// loaded box found the lease still sitting in the Lessor and renewed it
	// anyway. The absence of the sweeper was supplying this test's tolerance.
	// With expiry real, an overslept cycle is a genuinely lost lease.
	//
	// The timing is deliberately NOT widened. A 2s TTL renewed at 1.5s is the
	// exact cadence that produces the ~0.999s remainder the whole-second
	// RemainingTTL truncation rounds to zero -- which is the defect this test
	// exists to catch. A longer TTL would make it pass more often and stop
	// testing anything. Widening the margin here would be shortening the
	// verification.
	//
	// What IS fixed is that the failure was undiagnosable: "Renew on cycle 4:
	// lease lost" reads identically whether the monitor closed Done early (the
	// bug) or the scheduler starved us past the TTL (the environment). The
	// elapsed measurement below names which one happened. The durable fix is
	// the injectable clock seam (go-qrdb#172), for which this is now a second
	// concrete consumer alongside the MCP poll watcher.
	const (
		renewEvery = 1500 * time.Millisecond
		leaseTTL   = 2 * time.Second
	)
	for cycle := 1; cycle <= 4; cycle++ {
		start := time.Now()
		time.Sleep(renewEvery)
		select {
		case <-l.Done():
			t.Fatalf("Done closed during renewal cycle %d after %v: the holder was "+
				"told it lost a lease it was renewing correctly", cycle, time.Since(start))
		default:
		}
		if err := l.Renew(ctx); err != nil {
			if elapsed := time.Since(start); elapsed >= leaseTTL {
				t.Fatalf("Renew on cycle %d: %v -- but the cycle took %v against a %v "+
					"TTL, so the lease had genuinely expired before the renewal was "+
					"attempted. This is host starvation, not a lease defect: the "+
					"scheduler did not return within the cadence. Re-run on a quiet "+
					"machine (see go-qrdb#139, #171) and see go-qrdb#172 for the "+
					"clock seam that removes the wall-clock dependency.",
					cycle, err, elapsed, leaseTTL)
			}
			t.Fatalf("Renew on cycle %d: %v (cycle took %v, within the %v TTL, so the "+
				"lease should still have been held)", cycle, err, time.Since(start), leaseTTL)
		}
	}

	// Renewal stops here. The lease must now actually expire and be reported.
	select {
	case <-l.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("Done did not close once renewal stopped: confirming an uncertain " +
			"reading must not cost the loss signal")
	}
}

// TestRaft_LeaseExclusive proves acquisition is exclusive: while one holder
// owns a key, a second AcquireLease on the same key is refused with
// LeaseHeldError rather than silently displacing the holder. After the holder
// releases, the key is acquirable again.
func TestRaft_LeaseExclusive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	coord, _ := bootRaftNode(t)

	first, err := coord.AcquireLease(ctx, "singleton/leader", 30*time.Second)
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}

	// A contender must be refused while the first holder owns the key.
	_, err = coord.AcquireLease(ctx, "singleton/leader", 30*time.Second)
	var held *sdk.LeaseHeldError
	if !errors.As(err, &held) {
		t.Fatalf("second AcquireLease must fail with LeaseHeldError, got %v", err)
	}

	// The first holder still owns the key and its Done is still open.
	select {
	case <-first.Done():
		t.Fatal("first holder's Done closed while it still held the lease")
	default:
	}

	// Once released, the key is acquirable again.
	if err := first.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := coord.AcquireLease(ctx, "singleton/leader", 30*time.Second)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = second.Release(ctx)
}

// TestRaft_WatchDelivery subscribes to a prefix and asserts that a subsequent
// Put is delivered as a translated put Event.
func TestRaft_WatchDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	coord, _ := bootRaftNode(t)

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	w, err := coord.Watch(watchCtx, "fleet/")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := coord.Put(ctx, "fleet/node-1", []byte("online")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	select {
	case ev, ok := <-w.Events():
		if !ok {
			t.Fatalf("watch channel closed before delivering the event: %v", w.Err())
		}
		if ev.Type != transport.EventPut || ev.Key != "fleet/node-1" || string(ev.Value) != "online" {
			t.Fatalf("watch delivered a mistranslated event: %+v", ev)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watch delivered nothing for a matching Put")
	}

	// Against a real server, the caller's own cancellation must read as exactly
	// that, and not as a lost watch that needs reconciling.
	watchCancel()
	for range w.Events() { //nolint:revive // drain until the stream ends
	}
	if !errors.Is(w.Err(), context.Canceled) {
		t.Fatalf("after caller cancellation Err()=%v, want context.Canceled", w.Err())
	}
}
