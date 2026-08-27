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
func bootRaftNode(t *testing.T) (*sdk.Coordinator, transport.KVClient) {
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
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	members := map[uint64]string{1: cfg.Raft.RaftAddress}
	n, err := node.NewClusteredNode(context.Background(), cfg, members, nil)
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	kv := n.Client().KV()
	awaitWritable(t, kv)

	coord, err := n.Client().Coordination()
	if err != nil {
		t.Fatalf("Coordination: %v", err)
	}
	return coord, kv
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
	if testing.Short() {
		t.Skip("raft lifecycle test skipped in -short mode")
	}
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
// learn it lost the lease when Done closes.
//
// The assertion is deliberately scoped to the wrapper's guarantee — Done fires
// when the lease is no longer held — rather than to server-side reaping of the
// bound key. Deleting the attached key on expiry is the leader sweeper's job
// (pkg/lease.RunExpiry), whose cadence and shard placement are the server's
// concern, not this thin surfacing layer's; the delete-attached-keys path
// itself is exercised through Release in TestRaft_LeaseLifecycle.
func TestRaft_LeaseTTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("raft ttl-expiry test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	coord, _ := bootRaftNode(t)

	l, err := coord.AcquireLease(ctx, "ephemeral/k", 1*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	// Without a renew, the lease expires and the monitor closes Done.
	select {
	case <-l.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("Done did not close after the un-renewed lease should have expired")
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
	for cycle := 1; cycle <= 4; cycle++ {
		time.Sleep(1500 * time.Millisecond)
		select {
		case <-l.Done():
			t.Fatalf("Done closed during renewal cycle %d: the holder was told it "+
				"lost a lease it was renewing correctly", cycle)
		default:
		}
		if err := l.Renew(ctx); err != nil {
			t.Fatalf("Renew on cycle %d: %v", cycle, err)
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
	if testing.Short() {
		t.Skip("raft exclusivity test skipped in -short mode")
	}
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
	if testing.Short() {
		t.Skip("raft watch test skipped in -short mode")
	}
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
