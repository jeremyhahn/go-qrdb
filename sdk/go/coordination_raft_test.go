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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	coord, kv, _ := bootRaftNodeOpts(t, raftNodeOpts{sealEnabled: true})
	return coord, kv
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

	// serverOpts are passed to NewClusteredNode as the caller's own server
	// options.
	serverOpts []server.ServerOption
}

// raftCallBudget bounds the coordination calls one test makes against its
// single-replica raft node. It is a FAILURE bound, not a pacing interval: every
// call returns as soon as consensus answers, so an idle machine never spends
// it. Each test takes it only AFTER bootRaftNode returns. Taken before, as it
// used to be, the election and first commit on a loaded host were charged to
// the calls under test: a docs-only pull request timed out in AcquireLease with
// `quicraft: timeout` after boot had consumed the budget (go-qrdb#418).
const raftCallBudget = 60 * time.Second

// raftWritableBound bounds how long a fresh single-replica group may take to
// elect itself and commit its first write. A failure bound, as above; the time
// actually taken is logged on every boot.
const raftWritableBound = 30 * time.Second

func bootRaftNodeOpts(t *testing.T, opts raftNodeOpts) (*sdk.Coordinator, transport.KVClient, *sdk.Client) {
	t.Helper()
	n, kv := bootRaftNodeFull(t, opts)
	coord, err := n.Client().Coordination()
	if err != nil {
		t.Fatalf("Coordination: %v", err)
	}
	return coord, kv, n.Client()
}

// bootRaftNodeFull boots a single-member clustered node and returns it once
// writable.
func bootRaftNodeFull(t *testing.T, opts raftNodeOpts) (*node.Node, transport.KVClient) {
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
	n, err := node.NewClusteredNode(bootCtx, cfg, members, opts.serverOpts)
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
	return n, kv
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

	ctx, cancel := context.WithTimeout(context.Background(), raftCallBudget)
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
	ctx, cancel := context.WithTimeout(context.Background(), raftWritableBound)
	defer cancel()
	start := time.Now()
	for {
		err := kv.Put(ctx, "coordination/readiness", []byte("1"))
		if err == nil {
			// Reported so a wait that has quietly grown into most of its bound
			// is visible before it becomes a failure.
			t.Logf("raft node writable after %s", time.Since(start).Round(time.Millisecond))
			return
		}
		if errors.Is(err, quicraft.ErrTimeout) {
			// SLOW, not broken: a leader accepted the proposal (a group with
			// no leader refuses it at once with NotLeader) and the commit did
			// not finish inside the bound. That is the host starving the
			// commit path — CPU or fsync — and says nothing about leases.
			t.Fatalf("raft node SLOW, not broken: a leader accepted the first write but it "+
				"did not commit within %s (host starvation of the commit path): %v",
				raftWritableBound, err)
		}
		var notLeader *transport.NotLeaderError
		if !errors.As(err, &notLeader) && !errors.Is(err, quicraft.ErrReadIndexNotReady) {
			t.Fatalf("raft node not writable (non-transient) after %s: %v", time.Since(start).Round(time.Millisecond), err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("raft node elected no leader within %s: every write was refused as not-leader: %v",
				raftWritableBound, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestRaft_LeaseLifecycle exercises acquire → key present → renew → release →
// Done fires and key gone, against real lease and MVCC services.
func TestRaft_LeaseLifecycle(t *testing.T) {
	coord, _ := bootRaftNode(t)

	ctx, cancel := context.WithTimeout(context.Background(), raftCallBudget)
	defer cancel()

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
			coord, _, _ := bootRaftNodeOpts(t, tc.opts)
			assertLeaseKeyReaped(t, coord, "ephemeral/"+tc.name)
		})
	}
}

// TestRaft_TruncateReclaimSweeperRuns is TestRaft_LeaseTTLExpiry's
// go-qrdb#547 counterpart: it proves the reclaim-job sweeper
// (lifecycle.Node.ReclaimSweeper) is actually RUNNING on both clustered
// composition paths, with the resolver the server installs, named after the
// go-qrdb#174 lesson -- constructing a sweeper once, centrally, does not by
// itself prove every composer remembered to START it.
//
// The job is not hand-written: a real TRUNCATE TABLE is published through a
// registry over the node's own services, so the row, the job and the index
// entry land exactly where production puts them (the job and row
// composite-namespaced, the entry un-namespaced in the system store). The
// node's server is given the same DatabaseServiceRegistry, which is what
// makes it install a resolver on the node's sweeper at all.
func TestRaft_TruncateReclaimSweeperRuns(t *testing.T) {
	cases := []struct {
		name string
		opts raftNodeOpts
		// what a failure of THIS case, and only this case, would mean
		proves string
	}{
		{
			name:   "SealEnabled",
			opts:   raftNodeOpts{sealEnabled: true},
			proves: "completeClusteredBoot starts the reclaim sweeper",
		},
		{
			name:   "SealDisabled",
			opts:   raftNodeOpts{sealEnabled: false},
			proves: "the seal-disabled branch of NewClusteredNode starts it too",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbReg := server.NewDatabaseServiceRegistry("system")
			tc.opts.serverOpts = []server.ServerOption{server.WithDatabaseServiceRegistry(dbReg)}
			n, _ := bootRaftNodeFull(t, tc.opts)
			svc := n.Services()
			dbReg.Register("system", &server.DatabaseServices{
				Name: "system", KV: svc.KV, MVCC: svc.MVCC, Transaction: svc.Transaction,
				Batch: svc.Batch, Index: svc.Index,
			})
			assertTruncateReclaimed(t, dbReg, svc.MVCC)
		})
	}
}

// assertTruncateReclaimed truncates a one-row table through a registry over
// dbReg and polls the raw store until the old generation's row, the job
// record and its index entry are all gone.
func assertTruncateReclaimed(t *testing.T, dbReg *server.DatabaseServiceRegistry, raw service.MVCCKVServicer) {
	t.Helper()

	ctx, cancel := context.WithTimeout(tenant.WithTenantID(context.Background(), tenantid.System), 90*time.Second)
	defer cancel()

	reg := server.NewRESTServiceRegistry(dbReg, nil, tenantid.System)
	tables, err := reg.TableService(tenantid.System, "system")
	if err != nil {
		t.Fatalf("TableService: %v", err)
	}
	def, err := tables.CreateTable(ctx, &service.CreateTableRequest{
		Database: "system", Name: "reclaim_canary",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	kv, err := reg.KVService(tenantid.System, "system")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}
	oldRows := service.TableDataPrefix("system", def.TableID, 0)
	if err := kv.Put(ctx, oldRows+"row", []byte("orphaned")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := tables.TruncateTable(ctx, "system", "reclaim_canary", true); err != nil {
		t.Fatalf("TruncateTable: %v", err)
	}

	ns := tenant.ComposeNamespace(tenantid.System, "system")
	prefixes := []string{ns + oldRows, ns + "__reclaim:job/", "__reclaim:index/"}
	remaining := func() map[string][]string {
		left := map[string][]string{}
		for _, prefix := range prefixes {
			keys, lerr := raw.ListAll(ctx, prefix)
			if lerr != nil {
				t.Fatalf("ListAll(%q): %v", prefix, lerr)
			}
			if len(keys) > 0 {
				left[prefix] = keys
			}
		}
		return left
	}
	// Confirm the fixture actually landed before waiting on its removal.
	if left := remaining(); len(left) != len(prefixes) {
		t.Fatalf("the row, job and index entry must all exist before the sweep; found %v", left)
	}

	// Drained only after one sweep observes the job and ReclaimQuiescence
	// passes; poll well past both rather than sleeping a fixed budget.
	deadline := time.Now().Add(service.ReclaimQuiescence + 6*service.DefaultReclaimInterval)
	for {
		left := remaining()
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still present after the sweep deadline: %v -- nothing is sweeping pending reclaim jobs "+
				"on this clustered path (go-qrdb#547, following the go-qrdb#174 lesson)", left)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended while waiting for the sweep: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
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
	coord, _ := bootRaftNode(t)

	// Up to maxAttempts attempts of `cycles` renewals each, so this test's
	// sequence is longer than the others'; the bound is otherwise the same
	// kind of failure bound as raftCallBudget and starts after boot for the
	// same reason.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

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
	const (
		renewEvery  = 1500 * time.Millisecond
		leaseTTL    = 2 * time.Second
		cycles      = 4
		maxAttempts = 5
	)

	// THE PRECONDITION. This test can only make its assertion when each cycle
	// actually completes inside the TTL. If the scheduler does not return
	// within the cadence, the lease has genuinely expired and Done closing (or
	// Renew failing) is CORRECT behaviour -- so a failure in that state would
	// assert "the lease renewal is broken" on evidence that shows nothing of
	// the kind.
	//
	// An earlier revision measured exactly that and failed anyway: it printed
	// "the cycle took 2.344938979s against a 2s TTL, so the lease had genuinely
	// expired ... This is host starvation, not a lease defect" and then called
	// t.Fatalf. It computed the discriminator and did not act on it, which is
	// the same defect class this repository has been closing elsewhere -- an
	// assertion naming a cause it has explicitly disclaimed.
	//
	// RETRY, and FAIL when the retries are exhausted. Not skip: CLAUDE.md and
	// AGENTS.md both say "NEVER use skip guards", and a measured precondition is
	// not an exemption from a rule written as never -- the fips140 register is a
	// documented carve-out for unreachable code, not a general licence.
	//
	// A starved attempt is discarded whole: once the deadline passes the lease is
	// gone and every later cycle in that attempt is meaningless, so the run
	// restarts on a fresh key. Exhausting every attempt is itself a real result
	// -- the host could not give this test one clean window in maxAttempts tries
	// -- and it fails naming every measured overrun. That is honest in a way both
	// of the alternatives are not: it does not claim a lease defect it did not
	// observe, and it does not disappear into a skip nobody counts.
	//
	// maxAttempts is 5 rather than 3 because a starved attempt is CHEAP: it
	// aborts at the first overrunning cycle, so five starved attempts cost about
	// as long as one successful run. Retries are the only lever left for how
	// often a contended host takes this test down, so it is worth spending.
	var overruns []renewalOverrun

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// A distinct key per attempt: the previous attempt's lease may or may
		// not have expired yet, and re-acquiring the same key would race that
		// rather than test anything.
		key := fmt.Sprintf("renewed/lock/attempt-%d", attempt)

		// The deadline ANCHOR, recorded before the call that establishes it.
		// See runSubTTLRenewalCycles for why this instant and not another.
		anchor := time.Now()
		l, err := coord.AcquireLease(ctx, key, leaseTTL)
		if err != nil {
			t.Fatalf("AcquireLease(%s): %v", key, err)
		}

		// The acquire's own round trip is what set the first deadline, so it is
		// part of the first cycle's budget exactly as a renewal's would be.
		acquireTook := time.Since(anchor)

		over, overran := runSubTTLRenewalCycles(
			t, ctx, l, anchor, acquireTook, cycles, renewEvery, leaseTTL)
		if !overran {
			// The assertion path ran. Renewal stops here, and the lease must
			// now actually expire and be reported -- the expiry half proves the
			// confirming recheck did not cost the loss signal itself.
			select {
			case <-l.Done():
			case <-time.After(30 * time.Second):
				t.Fatal("Done did not close once renewal stopped: confirming an " +
					"uncertain reading must not cost the loss signal")
			}
			return
		}

		overruns = append(overruns, over)
		t.Logf("attempt %d/%d overran the %v TTL -- %s. Discarding this attempt: the "+
			"assertion's precondition did not hold.", attempt, maxAttempts, leaseTTL, over)
		_ = l.Release(ctx) // best effort; it may already have expired
	}

	var sleepTotal, renewTotal time.Duration
	for _, o := range overruns {
		sleepTotal += o.sleepOver
		renewTotal += o.renewTook + o.prevRenew
	}
	blame := "neither interval dominates across the attempts, so this does not say which cause it was"
	switch {
	case sleepTotal > 2*renewTotal:
		blame = "the SLEEP dominated: the scheduler did not return within the cadence. " +
			"That is host starvation, and a quiet host should fix it"
	case renewTotal > 2*sleepTotal:
		blame = "the RENEW ROUND TRIPS dominated, NOT the scheduler. A quiet host may " +
			"not help -- suspect the keepalive path (go-qrdb#139)"
	}

	t.Fatalf("THE ENVIRONMENT COULD NOT VALIDATE the sub-TTL renewal property: %d "+
		"attempts, every one of which exceeded the %v TTL before a renewal landed. "+
		"The assertion never ran, so this is not a lease-defect finding -- and it is "+
		"not a clean bill of health either.\n  %s\nAcross attempts: %v of sleep "+
		"overrun against %v of renew round trips -- %s. go-qrdb#172's clock seam "+
		"removes the wall-clock dependency entirely.",
		maxAttempts, leaseTTL,
		strings.Join(overrunLines(overruns), "\n  "),
		sleepTotal.Round(time.Millisecond), renewTotal.Round(time.Millisecond), blame)
}

// runSubTTLRenewalCycles drives the renew-below-TTL cadence and reports whether
// the host starved it.
//
// It returns (heldFor, true) when the lease's deadline had already passed -- the
// lease genuinely expired and no conclusion about renewal is available. It calls
// t.Fatal itself for a REAL failure: Done closing or Renew failing while the
// deadline was still in the future, which is the property under test and must
// still fail loudly.
//
// WHICH INTERVAL DECIDES THIS, because it is not the obvious one. sdk's lease
// holds a deadline `heldUntil` and a renewal is refused once it has passed:
//
//	confirmHeld: if l.lost || !time.Now().Before(l.heldUntil) { return false }
//	             ... heldUntil = sent.Add(ttl)
//
// `sent` is the instant the CONFIRMING REQUEST WAS SENT, not when it was
// acknowledged -- dating the floor from the send keeps it on the conservative
// side of the round trip. So the quantity that decides expiry is
//
//	now - (the instant the PREVIOUS confirmation was sent)   vs   ttl
//
// which is `previous renew's round trip + the sleep + this renew's round trip`.
// The sleep alone is NOT that quantity, and neither is sleep-plus-this-renew:
// both omit the previous round trip, which is precisely the part that eats the
// 500ms of slack between a 1.5s cadence and a 2s TTL. A discriminator built on
// the sleep will call a slow pair of raft proposals a lease-renewal defect.
// That was observed here: a failure at exactly 1.500050042s of sleep, which the
// sleep-based measure reports as comfortably inside the TTL.
//
// The anchor is taken just BEFORE each call rather than after, which is
// conservative in the safe direction: it can only over-estimate how long the
// lease has been held, so the test errs toward "the precondition did not hold"
// rather than toward accusing the code.
// overrunLines renders each attempt's breakdown for the terminal message.
func overrunLines(os []renewalOverrun) []string {
	out := make([]string, 0, len(os))
	for i, o := range os {
		out = append(out, fmt.Sprintf("attempt %d -- %s", i+1, o))
	}
	return out
}

// renewalOverrun is what a cycle that lost its lease actually spent, broken
// into the two intervals that can consume the budget.
//
// The split is the point (go-qrdb#273). `heldFor` alone spans sleep + Done
// check + the whole renew round trip, so a message built on it can report THAT
// the budget was consumed but not BY WHAT -- and every prose attempt to say
// which is then an assertion the instrument cannot support. Measured over 32
// cycles at 96-98% CPU pressure on a 24-core host, the two differ by orders of
// magnitude: the sleep overran by at most 1.3ms while the renew round trip
// ranged 2.3ms to 265ms. Reporting them separately is what lets the message
// name a cause it has actually observed.
type renewalOverrun struct {
	cycle     int
	sleepOver time.Duration // beyond the requested cadence
	renewTook time.Duration // this cycle's keepalive round trip
	prevRenew time.Duration // the previous one, which set the deadline
	heldFor   time.Duration // total since the confirmation that set the deadline
}

// String reports the breakdown and names the dominant consumer, or declines to
// when no single interval dominates.
func (o renewalOverrun) String() string {
	s := fmt.Sprintf("cycle %d: sleep overran by %v, this renew took %v, "+
		"the previous renew took %v, total held %v",
		o.cycle, o.sleepOver.Round(time.Millisecond),
		o.renewTook.Round(time.Millisecond),
		o.prevRenew.Round(time.Millisecond),
		o.heldFor.Round(time.Millisecond))

	// Attribute only when one interval plainly dominates. "Plainly" is 2x the
	// rest, which is deliberately coarse: the point is to refuse an attribution
	// that the numbers do not force, not to adjudicate close cases.
	rt := o.renewTook + o.prevRenew
	switch {
	case o.sleepOver > 2*rt:
		s += " -- the SLEEP consumed the budget: the scheduler did not return " +
			"within the cadence, which is host starvation"
	case rt > 2*o.sleepOver:
		s += " -- the RENEW ROUND TRIPS consumed the budget, NOT the scheduler. " +
			"A quiet host will not necessarily help; suspect the keepalive path"
	default:
		s += " -- no single interval dominates, so this does not say which cause it was"
	}
	return s
}

func runSubTTLRenewalCycles(
	t *testing.T,
	ctx context.Context,
	l sdk.Lease,
	anchor time.Time,
	prevRenew time.Duration,
	cycles int,
	renewEvery, leaseTTL time.Duration,
) (renewalOverrun, bool) {
	t.Helper()

	for cycle := 1; cycle <= cycles; cycle++ {
		// The two intervals, measured apart. Neither is inferred from the other.
		sleepStart := time.Now()
		time.Sleep(renewEvery)
		sleepOver := time.Since(sleepStart) - renewEvery

		select {
		case <-l.Done():
			if heldFor := time.Since(anchor); heldFor >= leaseTTL {
				return renewalOverrun{cycle, sleepOver, 0, prevRenew, heldFor}, true
			}
			t.Fatalf("Done closed during renewal cycle %d, %v after the confirmation "+
				"that set the deadline and inside the %v TTL: the holder was told it "+
				"lost a lease it was renewing correctly",
				cycle, time.Since(anchor), leaseTTL)
		default:
		}

		sentAt := time.Now()
		err := l.Renew(ctx)
		renewTook := time.Since(sentAt)
		// Measured after Renew returns, which is when confirmHeld ran.
		heldFor := time.Since(anchor)
		if err != nil {
			if heldFor >= leaseTTL {
				return renewalOverrun{cycle, sleepOver, renewTook, prevRenew, heldFor}, true
			}
			t.Fatalf("Renew on cycle %d: %v (%v since the confirmation that set the "+
				"deadline, within the %v TTL, so the lease should still have been held; "+
				"sleep overran by %v, the renew itself took %v)",
				cycle, err, heldFor, leaseTTL, sleepOver.Round(time.Millisecond),
				renewTook.Round(time.Millisecond))
		}
		anchor, prevRenew = sentAt, renewTook
	}
	return renewalOverrun{}, false
}

// TestRaft_LeaseTimeToLive_NotFoundIsRealNotHandcrafted drives the actual
// state machine -> service -> transport path for go-qrdb#387's "missing
// lease" classification, with no injected or handcrafted error anywhere in
// the chain: bootRaftNodeOpts's node runs every protocol server disabled
// (see its own doc comment), so n.Client() is backed by the embedded
// transport (sdk/go/transport/embedded) talking to a REAL
// pkg/engine.MVCCEngine and pkg/lease.Lessor over a REAL single-replica raft
// group -- the same composition TestRaft_LeaseLifecycle and
// TestRaft_LeaseTTLExpiry already exercise for other lease behavior. A lease
// ID that was never granted must classify as *transport.LeaseNotFoundError,
// the same typed error every OTHER transport's own tests (REST, gRPC, QUIC,
// MCP, Unix) pin -- this is embedded's turn, and the only one of the six
// where the error being asserted comes from a real miss against the real
// Lessor rather than a test double told what to return.
func TestRaft_LeaseTimeToLive_NotFoundIsRealNotHandcrafted(t *testing.T) {
	_, _, client := bootRaftNodeOpts(t, raftNodeOpts{sealEnabled: true})

	ctx, cancel := context.WithTimeout(context.Background(), raftCallBudget)
	defer cancel()

	_, err := client.Lease().LeaseTimeToLive(ctx, 999999999)
	if err == nil {
		t.Fatal("LeaseTimeToLive on a never-granted lease ID succeeded, want error")
	}
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *transport.LeaseNotFoundError, got %T: %v", err, err)
	}
	if notFound.LeaseID != 999999999 {
		t.Errorf("LeaseID = %d, want 999999999", notFound.LeaseID)
	}
}

// TestRaft_LeaseExclusive proves acquisition is exclusive: while one holder
// owns a key, a second AcquireLease on the same key is refused with
// LeaseHeldError rather than silently displacing the holder. After the holder
// releases, the key is acquirable again.
func TestRaft_LeaseExclusive(t *testing.T) {
	coord, _ := bootRaftNode(t)

	ctx, cancel := context.WithTimeout(context.Background(), raftCallBudget)
	defer cancel()

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
	coord, _ := bootRaftNode(t)

	ctx, cancel := context.WithTimeout(context.Background(), raftCallBudget)
	defer cancel()

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

// The attribution logic gets its own tests, on constructed observations, for the
// same reason the wait-diagnostic tests in pkg/server do: the branches that
// matter are only reachable when the environment misbehaves, and a test that
// needs a starved host to run cannot be trusted to report starvation correctly.
//
// What is being asserted is that the message names a cause ONLY when the split
// forces it. go-qrdb#273 exists because the previous instrument could not see
// the two intervals apart, so every attribution it made was unsupported; an
// attribution that fires regardless of the numbers would be the same defect.
func TestRenewalOverrun_AttributesOnlyWhatTheSplitShows(t *testing.T) {
	ms := time.Millisecond
	cases := []struct {
		name       string
		over       renewalOverrun
		wantPhrase string
		denyPhrase string
	}{
		{
			name: "sleep dominating is host starvation",
			over: renewalOverrun{cycle: 1, sleepOver: 600 * ms, renewTook: 5 * ms,
				prevRenew: 5 * ms, heldFor: 2110 * ms},
			wantPhrase: "SLEEP consumed the budget",
			denyPhrase: "RENEW ROUND TRIPS",
		},
		{
			// The shape measured on this fleet: 32 cycles pinned to two real
			// cores at 95% CPU pressure overran the sleep by at most 5.58ms
			// while renew round trips reached 330ms.
			name: "renew round trips dominating is NOT the scheduler",
			over: renewalOverrun{cycle: 3, sleepOver: 1 * ms, renewTook: 330 * ms,
				prevRenew: 265 * ms, heldFor: 2096 * ms},
			wantPhrase: "RENEW ROUND TRIPS consumed the budget, NOT the scheduler",
			denyPhrase: "SLEEP consumed",
		},
		{
			name: "comparable intervals must refuse to attribute",
			over: renewalOverrun{cycle: 2, sleepOver: 300 * ms, renewTook: 200 * ms,
				prevRenew: 100 * ms, heldFor: 2100 * ms},
			wantPhrase: "does not say which cause it was",
			denyPhrase: "consumed the budget",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.over.String()
			if !strings.Contains(got, tc.wantPhrase) {
				t.Errorf("message must contain %q.\ngot: %s", tc.wantPhrase, got)
			}
			if strings.Contains(got, tc.denyPhrase) {
				t.Errorf("message must NOT contain %q -- the attribution would be "+
					"unsupported by the split.\ngot: %s", tc.denyPhrase, got)
			}
		})
	}
}

// Whatever it concludes, it must always show the numbers the conclusion rests
// on -- that is the difference between a diagnosis and an assertion.
func TestRenewalOverrun_AlwaysReportsBothIntervals(t *testing.T) {
	over := renewalOverrun{cycle: 4, sleepOver: 7 * time.Millisecond,
		renewTook: 123 * time.Millisecond, prevRenew: 45 * time.Millisecond,
		heldFor: 2075 * time.Millisecond}
	got := over.String()
	for _, want := range []string{"cycle 4", "7ms", "123ms", "45ms", "2.075s"} {
		if !strings.Contains(got, want) {
			t.Errorf("message must carry %q so the reader can check the verdict.\ngot: %s",
				want, got)
		}
	}
}
