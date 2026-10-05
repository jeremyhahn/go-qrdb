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

package conformance

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestGuardedVerbConformanceMatrix is the go-qrdb#121 deliverable and the
// v0.12 release gate.
//
// The owner ruling on go-qrdb#117 is that CAS and transactions are a
// first-class capability of the database server that must behave IDENTICALLY
// regardless of storage backend, deployment mode, or transport. This walks all
// twelve combinations and asserts exactly that, rather than asserting it in a
// document and testing one corner of it.
//
// WHAT THIS CERTIFIES, EXACTLY (go-qrdb#276): every cell here calls each
// guarded verb SEQUENTIALLY -- once, observe the result, once more, observe
// again -- and inspects the store afterward. That proves SEQUENTIAL
// CONFORMANCE (the right answer, in the right order, on the right engine,
// mode and transport). It does NOT prove
// ATOMICITY. A handler that emulates a guarded verb with a Get followed by a
// Put returns the correct booleans in sequence -- store-and-report-true when
// called on an absent key, decline when called again -- so it satisfies every
// assertion below. What it cannot survive is two callers inside its Get-to-Put
// window at the same time, which nothing in this file ever produces: 29 of the
// (then) 30 cells here passed such an emulation outright (go-qrdb#276,
// reproducing PR #230's mutation). Atomicity under contention is asserted
// separately, in TestGuardedVerbConcurrency (concurrency_test.go), and only on
// the cells that suite runs -- see its doc comment for which those are and
// why running it on every cell here would not widen what gets caught.
//
// The cells are not parallel: raft cells are expensive enough that
// interleaving them would make the leader-election waits fight each other for
// CPU on a shared runner.
func TestGuardedVerbConformanceMatrix(t *testing.T) {
	for _, c := range matrix() {
		t.Run(c.name(), func(t *testing.T) {
			t.Logf("cell %s: expecting %s", c.name(), c.want)
			cl := c.boot(t)

			// Absence is not a guarded-verb capability — it is the read
			// plane the guarded verbs are observed through, and a cell that
			// cannot answer it makes every assertion below vacuous.
			assertAbsenceIsObservable(t, cl)
			assertGuardedPlaneWorks(t, c, cl)
		})
	}
}

// ---------------------------------------------------------------------------
// full passage
// ---------------------------------------------------------------------------

// assertGuardedPlaneWorks is the conformance battery every cell that carries
// the capability must satisfy identically.
//
// Each verb is checked on BOTH branches — the guard holding and the guard
// failing — and the store is inspected afterwards. Checking only the happy
// branch would pass against an implementation that ignored its precondition
// entirely, which is the one thing a guarded write exists to do.
func assertGuardedPlaneWorks(t *testing.T, c cell, cl clients) {
	t.Helper()

	assertSingleKeyGuardedVerbsWork(t, cl)
	assertTransactionsWork(t, c, cl)
	assertGuardedBatchMoveIsAtomic(t, cl)
	if c.mode == raft && c.smType != "" {
		assertLeasedWriteBindsAndRefusesUnknownLease(t, c, cl)
	}
}

// assertGuardedBatchMoveIsAtomic proves kvstore.MultiGuardedBatchKVStore --
// reached here exactly as an SDK consumer reaches it, through
// sdk.NewKVStoreAdapter over cl.kv -- is one atomic step on every cell
// (go-qrdb#589): a move (delete the old key, put the new one) guarded on the
// old key's value lands as a whole, and a batch whose guard does not hold
// applies NEITHER of its writes. The underlying ExecuteTransaction RPC's own
// atomicity is already the subject of assertTransactionsWork above; what this
// adds is proof that kvStoreAdapter's guard/write translation (Guard ->
// Compare, KV.Delete -> OpDelete) is correct end to end, on the real wire,
// on every engine, mode and transport this matrix covers.
func assertGuardedBatchMoveIsAtomic(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	idx, ok := cl.kv.(transport.IndexClient)
	if !ok {
		t.Fatalf("the matrix's KV client does not implement transport.IndexClient")
	}
	store, err := sdk.NewKVStoreAdapter(cl.kv, idx)
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	batch, ok := store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		t.Fatalf("the matrix's kvStoreAdapter does not implement kvstore.MultiGuardedBatchKVStore")
	}

	if err := cl.kv.Put(ctx, "batch/old", []byte("payload")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if err := cl.kv.Put(ctx, "batch/untouched", []byte("still-here")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	// The guard holds: the move lands as one step. Both writes must be
	// visible together -- there is no window where the old key is gone and
	// the new one is not yet there, or the reverse.
	applied, err := batch.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "batch/old", Value: []byte("payload")}},
		[]kvstore.KV{
			{Key: "batch/old", Delete: true},
			{Key: "batch/new", Value: []byte("payload")},
		})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged(guard holds): %v", err)
	}
	if !applied {
		t.Fatal("a move whose guard holds must report applied=true")
	}
	assertKeyAbsent(t, cl, "batch/old", "a successful move left the old key behind")
	if got, err := cl.kv.Get(ctx, "batch/new"); err != nil || string(got) != "payload" {
		t.Fatalf("batch/new after the move = %q err=%v; want %q", got, err, "payload")
	}

	// The guard fails: NEITHER write lands, not even the delete.
	applied, err = batch.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "batch/new", Value: []byte("STALE")}},
		[]kvstore.KV{
			{Key: "batch/untouched", Delete: true},
			{Key: "batch/should-not-exist", Value: []byte("x")},
		})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged(guard fails): %v", err)
	}
	if applied {
		t.Fatal("a batch whose guard fails must report applied=false")
	}
	if got, err := cl.kv.Get(ctx, "batch/untouched"); err != nil || string(got) != "still-here" {
		t.Fatalf("batch/untouched after a refused batch = %q err=%v; want %q (the delete must not have landed)",
			got, err, "still-here")
	}
	assertKeyAbsent(t, cl, "batch/should-not-exist", "the put half of a refused batch landed anyway")

	// An absent-guard batch creates a brand-new pair of keys atomically too,
	// covering the nil-Value guard translation (version-equals-zero) the
	// present-value branch above does not exercise.
	applied, err = batch.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "batch/fresh-a", Value: nil}, {Key: "batch/fresh-b", Value: nil}},
		[]kvstore.KV{
			{Key: "batch/fresh-a", Value: []byte("a")},
			{Key: "batch/fresh-b", Value: []byte("b")},
		})
	if err != nil {
		t.Fatalf("PutAllIfAllUnchanged(absent guards): %v", err)
	}
	if !applied {
		t.Fatal("a batch guarded on two absent keys must report applied=true")
	}
	for k, want := range map[string]string{"batch/fresh-a": "a", "batch/fresh-b": "b"} {
		if got, err := cl.kv.Get(ctx, k); err != nil || string(got) != want {
			t.Errorf("%s = %q err=%v; want %q", k, got, err, want)
		}
	}
}

// assertLeasedWriteBindsAndRefusesUnknownLease pins the non-default raft
// sm_types' lease-bearing transaction write.
//
// Every `pkg/statemachine` KV family (`memory`/`standard`, `fast`, `disk`,
// and their siblings) keeps a durable lease table of its own since
// go-qrdb's "the KV state machines keep durable leases" change — only the
// legacy `regular` type refuses one at all. A transaction write naming a
// lease this state machine actually granted binds the key to it, the same
// as a plain `Put` naming one would; this is no longer a capability gap, so
// asserting a blanket refusal (as this test did before that change shipped)
// pins behavior the product deliberately stopped having. A lease ID the
// state machine's own table does NOT hold is still refused — the same
// `lease-not-found` answer a plain lease-bound `Put` naming it would give,
// typed `*transport.LeaseNotFoundError` rather than a capability refusal,
// since the capability to bind a lease is present; the named one just does
// not exist.
//
// Refusing must not cost the shard. Before go-qrdb#129 a refused verb tripped
// the shard's circuit breaker, after which every proposal failed including a
// plain Put, and a linearizable read never returned; the Put and Get after the
// refusal are that regression's pin.
func assertLeasedWriteBindsAndRefusesUnknownLease(t *testing.T, c cell, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	granted, err := cl.lease.LeaseGrant(ctx, 60)
	if err != nil {
		t.Fatalf("%s: LeaseGrant: %v", c.name(), err)
	}

	resp, err := cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("leased"), Value: []byte("v"), LeaseID: granted.ID}},
	})
	if err != nil {
		t.Fatalf("%s: ExecuteTransaction(leased write, granted lease): %v", c.name(), err)
	}
	if !resp.Succeeded {
		t.Errorf("%s: ExecuteTransaction(leased write, granted lease) did not take the success branch", c.name())
	}
	if got, gerr := cl.kv.Get(ctx, "leased"); gerr != nil || string(got) != "v" {
		t.Errorf("%s: Get(leased)=%q err=%v after a lease-bound write; want %q", c.name(), got, gerr, "v")
	}

	// A lease ID the table does not hold: revoking the one just granted
	// guarantees it is unknown rather than merely unlikely.
	if err := cl.lease.LeaseRevoke(ctx, granted.ID); err != nil {
		t.Fatalf("%s: LeaseRevoke: %v", c.name(), err)
	}
	_, err = cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("unknown-lease"), Value: []byte("v"), LeaseID: granted.ID}},
	})
	var notFound *transport.LeaseNotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("%s: ExecuteTransaction(leased write, revoked lease) = %v, want *transport.LeaseNotFoundError", c.name(), err)
	}
	if got, gerr := cl.kv.Get(ctx, "unknown-lease"); gerr == nil {
		t.Errorf("%s: a refused leased write stored unknown-lease=%q", c.name(), got)
	}

	if err := cl.kv.Put(ctx, "after", []byte("2")); err != nil {
		t.Fatalf("%s: a plain Put FAILED after the refusal (%v). Refusing an unsupported "+
			"write must not cost the cluster its shard (go-qrdb#129)", c.name(), err)
	}
	got, err := cl.kv.Get(ctx, "after")
	if err != nil || string(got) != "2" {
		t.Fatalf("%s: read back %q err=%v after the refusal; want %q", c.name(), got, err, "2")
	}
}

// assertSingleKeyGuardedVerbsWork is the acquire / edit / release battery for
// the three verbs that are decided against ONE key.
//
// It is shared by both outcomes, as assertPutIfAbsentWorks already was and for
// the same reason: since go-qrdb#175 every cell of the matrix carries these
// three. They are native opcodes dispatched against the state machine's own
// store (kv.OpPutIfAbsent, protocol.OpCompareAndDelete, protocol.OpCompareAndSwap)
// and need no MVCC store, so a cell without the multi-key plane still has
// them. Writing the battery twice would let the two copies drift into two
// different definitions of "works", which is the whole reason "identical
// regardless of backend, mode or transport" is the ruling being tested.
func assertSingleKeyGuardedVerbsWork(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	// --- PutIfAbsent: the acquire half of a claim ---
	assertPutIfAbsentWorks(t, cl)

	// --- CompareAndSwap: the edit half ---
	swapped, err := cl.guarded.CompareAndSwap(ctx, "claim", []byte("WRONG"), []byte("globex"))
	if err != nil {
		t.Fatalf("CompareAndSwap(stale): %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap must refuse a stale expected value")
	}
	if got, err := cl.kv.Get(ctx, "claim"); err != nil || string(got) != "acme" {
		t.Errorf("a refused swap must leave the value untouched: Get(claim)=%q err=%v; want %q",
			got, err, "acme")
	}
	swapped, err = cl.guarded.CompareAndSwap(ctx, "claim", []byte("acme"), []byte("globex"))
	if err != nil {
		t.Fatalf("CompareAndSwap(match): %v", err)
	}
	if !swapped {
		t.Error("CompareAndSwap must swap when the expected value matches")
	}
	if got, err := cl.kv.Get(ctx, "claim"); err != nil || string(got) != "globex" {
		t.Errorf("a successful swap must store the new value: Get(claim)=%q err=%v; want %q",
			got, err, "globex")
	}

	// A swap is not a create. This is what keeps the three verbs distinct, and
	// an implementation that forwarded a swap to an unguarded Put would satisfy
	// every assertion above while silently creating keys.
	swapped, err = cl.guarded.CompareAndSwap(ctx, "absent-key", nil, []byte("created"))
	if err != nil {
		t.Fatalf("CompareAndSwap(absent): %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap reported a swap on an absent key")
	}
	assertKeyAbsent(t, cl, "absent-key", "CompareAndSwap CREATED an absent key")

	// --- CompareAndDelete: the release half ---
	deleted, err := cl.guarded.CompareAndDelete(ctx, "claim", []byte("WRONG"))
	if err != nil {
		t.Fatalf("CompareAndDelete(stale): %v", err)
	}
	if deleted {
		t.Error("CompareAndDelete must refuse a stale expected value")
	}
	assertKeyPresent(t, cl, "claim", "a refused CompareAndDelete removed the key anyway")
	deleted, err = cl.guarded.CompareAndDelete(ctx, "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("CompareAndDelete(match): %v", err)
	}
	if !deleted {
		t.Error("CompareAndDelete must delete when the expected value matches")
	}
	assertKeyAbsent(t, cl, "claim", "key still present after a successful CompareAndDelete")
}

// assertKeyAbsent and assertKeyPresent check both point-read APIs after a
// guarded operation, and check them against their TYPED contract rather than
// against "some answer came back".
//
// Absence requires a *transport.KeyNotFoundError, not merely a non-nil error.
// "Get returned an error" is satisfied by an authorization failure, a timeout,
// a closed transport or an internal fault — none of which say the key is
// absent, and all of which would let a real regression through while the
// assertion reported success. Measured: with Get returning an unrelated error
// for absent keys, the previous version of this helper PASSED and this one
// fails on all four memory cells.
//
// Asserting Get and not only Exists matters for a reason worth stating exactly,
// because the obvious reason is wrong. Every SDK transport derives Exists FROM
// Get — embedded, gRPC, REST, unix, QUIC and MCP all call Get and map a
// KeyNotFoundError to false — so the two cannot disagree here, and go-qrdb#214
// was not a case of them disagreeing. It was a case of them failing TOGETHER:
// Get reported an absent key as a successful read of an empty value, so Exists
// derived "true" from it and was wrong in the same breath.
//
// That is precisely why Exists alone is the weaker assertion. It tests the
// DERIVATION; Get's typed error tests the SOURCE the derivation reads. A
// regression in the read path shows up in Get first and reaches Exists only
// through whatever mapping the transport happens to apply.
//
// The Exists check on the presence side is kept, and Get corroborates it, as
// cheap insurance against a transport that one day implements Exists natively
// — a HEAD request, a server-side existence RPC — at which point the two CAN
// diverge and this matrix is the gate that should see it.
//
// These assertions previously went through List, as a deliberate concession
// while #214 was open. #237 fixed it and the concession has expired; restoring
// them is what that issue named as its acceptance test.
func assertKeyAbsent(t *testing.T, cl clients, key, msg string) {
	t.Helper()
	ctx := boundedCtx(t)

	exists, err := cl.kv.Exists(ctx, key)
	if err != nil {
		t.Fatalf("%s: Exists(%q): %v", msg, key, err)
	}
	if exists {
		t.Errorf("%s: Exists(%q) = true, want false", msg, key)
	}

	got, err := cl.kv.Get(ctx, key)
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("%s: Get(%q) = %q err=%v; want a *transport.KeyNotFoundError — an "+
			"absent key must be reported as not-found, not as a successful read of "+
			"an empty value and not as an unrelated failure", msg, key, got, err)
	}
}

func assertKeyPresent(t *testing.T, cl clients, key, msg string) {
	t.Helper()
	ctx := boundedCtx(t)

	exists, err := cl.kv.Exists(ctx, key)
	if err != nil {
		t.Fatalf("%s: Exists(%q): %v", msg, key, err)
	}
	if !exists {
		t.Errorf("%s: Exists(%q) = false, want true", msg, key)
	}

	// Get must agree. On today's transports it necessarily does, since Exists
	// is derived from it; the check costs one call and becomes load-bearing the
	// moment any transport serves Exists natively.
	if _, err := cl.kv.Get(ctx, key); err != nil {
		t.Errorf("%s: Get(%q) failed with %v while Exists reported the key present; "+
			"the two point-read APIs disagree", msg, key, err)
	}
}

// assertTransactionsWork covers the multi-key half of the plane: the value
// compare that every engine can evaluate, both transaction branches, and
// all-or-nothing application.
func assertTransactionsWork(t *testing.T, c cell, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	if err := cl.kv.Put(ctx, "cas", []byte("v1")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	// The guard holds: success ops apply.
	resp, err := cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares: []transport.Compare{valueEquals("cas", "v1")},
		SuccessOps: []transport.Op{
			put("multi/a", "a"),
			put("multi/b", "b"),
		},
		FailureOps: []transport.Op{put("multi/never", "never")},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction(guard holds): %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("a transaction whose compare holds must report Succeeded")
	}
	for k, want := range map[string]string{"multi/a": "a", "multi/b": "b"} {
		if got, err := cl.kv.Get(ctx, k); err != nil || string(got) != want {
			t.Errorf("multi-key success op %s = %q err=%v; want %q — a transaction "+
				"must apply its operation set atomically, not partially", k, got, err, want)
		}
	}
	assertKeyAbsent(t, cl, "multi/never",
		"the FAILURE branch was applied for a transaction whose compare held")

	// The guard fails: failure ops apply and success ops do not. Without this
	// branch the test passes against a transaction service that ignores its
	// preconditions, which is exactly the answer a key store depends on.
	resp, err = cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares:   []transport.Compare{valueEquals("cas", "STALE")},
		SuccessOps: []transport.Op{put("multi/wrong", "wrong")},
		FailureOps: []transport.Op{put("multi/fallback", "fallback")},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction(guard fails): %v", err)
	}
	if resp.Succeeded {
		t.Error("a transaction whose compare fails must not report Succeeded")
	}
	assertKeyAbsent(t, cl, "multi/wrong",
		"the SUCCESS branch was applied for a transaction whose compare failed")
	if got, err := cl.kv.Get(ctx, "multi/fallback"); err != nil || string(got) != "fallback" {
		t.Errorf("failure branch not applied: %q err=%v", got, err)
	}

	assertRevisionCompareBehaviour(t, c, cl)
	assertEmptyKeyRefusedSafely(t, c, cl)
}

// assertEmptyKeyRefusedSafely: a transaction naming an empty key is refused —
// by the service layer before it is proposed, and by every state machine at
// apply — and refusing it costs nothing. Before go-qrdb#136's review fix the
// empty key reached storage on the disk and fast state machines, and the
// storage error halted Apply on every replica.
func assertEmptyKeyRefusedSafely(t *testing.T, c cell, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	_, err := cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares:   []transport.Compare{{Target: transport.CompareVersion, Result: transport.CompareEqual}},
		SuccessOps: []transport.Op{put("empty-key/written", "x")},
	})
	if err == nil {
		t.Errorf("%s: a transaction comparing an empty key was accepted", c.name())
	}
	if got, gerr := cl.kv.Get(ctx, "empty-key/written"); gerr == nil {
		t.Errorf("%s: a refused empty-key transaction wrote %q", c.name(), got)
	}
	if err := cl.kv.Put(ctx, "empty-key/after", []byte("ok")); err != nil {
		t.Fatalf("%s: a plain Put FAILED after the empty-key refusal (%v)", c.name(), err)
	}
}

// assertRevisionCompareBehaviour pins the one documented deviation in the
// matrix, and pins it as a deviation rather than letting it pass unnoticed.
//
// create_revision and mod_revision compares need MVCC revisions to compare
// against. The file engine keeps none, nor does any raft sm_type but the
// default MVCC store (their transactions compare on value and version, as the
// daemon's pebble state machine does), and they refuse them explicitly rather
// than reading revision 0 — because a client written for the MVCC engine does
// read-then-CAS on a revision, would read 0 here, compare equal, and commit, so
// two concurrent writers both "succeed" and one update is lost with no signal
// (go-qrdb#117 / the #120 engine work).
//
// The refusal is therefore the correct behaviour, not a gap, and asserting it
// per-engine is what stops someone "fixing" it into a silent fail-open. Note
// the native PutIfAbsent verb works on every engine — it is only the etcd
// revision idiom that is unavailable, so nothing is unreachable.
func assertRevisionCompareBehaviour(t *testing.T, c cell, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	req := &transport.TransactionRequest{
		Compares: []transport.Compare{{
			Key:            []byte("fresh"),
			Target:         transport.CompareCreateRevision,
			Result:         transport.CompareEqual,
			CreateRevision: 0,
		}},
		SuccessOps: []transport.Op{put("fresh", "first")},
	}
	resp, err := cl.txn.ExecuteTransaction(ctx, req)

	if !c.keepsRevisions() {
		if err == nil {
			t.Errorf("a create_revision compare on %s returned %+v with no error; it must "+
				"REFUSE, because reading a revision this engine does not keep would let two "+
				"writers both believe they won and lose one update silently", c.name(), resp)
		}
		if got, gerr := cl.kv.Get(ctx, "fresh"); gerr == nil {
			t.Errorf("a refused revision compare wrote fresh=%q", got)
		}
		// The refusal must not cost the shard: before go-qrdb#129 a refused
		// verb tripped the circuit breaker and every later call failed.
		if err := cl.kv.Put(ctx, "after-refusal", []byte("2")); err != nil {
			t.Fatalf("%s: a plain Put FAILED after the refusal (%v)", c.name(), err)
		}
		if got, err := cl.kv.Get(ctx, "after-refusal"); err != nil || string(got) != "2" {
			t.Fatalf("%s: read back %q err=%v after the refusal; want %q", c.name(), got, err, "2")
		}
		return
	}

	if err != nil {
		t.Fatalf("create_revision compare on an MVCC-backed engine: %v", err)
	}
	if !resp.Succeeded {
		t.Error("the etcd guarded-create idiom (create_revision == 0) must succeed on an absent key")
	}
	if got, err := cl.kv.Get(ctx, "fresh"); err != nil || string(got) != "first" {
		t.Errorf("guarded create wrote %q err=%v; want %q", got, err, "first")
	}
}

// assertPutIfAbsentWorks is the acquire-half battery, shared by both outcomes.
//
// PutIfAbsent is required to work on EVERY cell of the matrix — it is a native
// KV opcode present even where the MVCC-backed verbs are not — so its
// assertions live in one place rather than being written twice and allowed to
// drift into two different definitions of "works".
//
// Both branches are checked. The second call is what makes this a test of the
// GUARD rather than of the wiring: an implementation that ignored its
// precondition would satisfy the first assertion and still be useless, because
// "did I win the claim?" is the entire question a caller is asking.
func assertPutIfAbsentWorks(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	stored, err := cl.guarded.PutIfAbsent(ctx, "claim", []byte("acme"))
	if err != nil {
		t.Fatalf("PutIfAbsent(absent): %v", err)
	}
	if !stored {
		t.Error("PutIfAbsent must store when the key is absent")
	}

	stored, err = cl.guarded.PutIfAbsent(ctx, "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("PutIfAbsent(present): %v", err)
	}
	if stored {
		t.Error("PutIfAbsent must decline when the key is present")
	}
	if got, err := cl.kv.Get(ctx, "claim"); err != nil || string(got) != "acme" {
		t.Errorf("the losing acquirer overwrote the winner: key=%q err=%v; want %q", got, err, "acme")
	}
}

// ---------------------------------------------------------------------------
// absence
// ---------------------------------------------------------------------------

// assertAbsenceIsObservable is go-qrdb#214's acceptance test: on every cell,
// Exists and Get must be able to tell "this key is not here" from "this key
// holds a value".
//
// It runs on EVERY cell rather than on the sm_types that were observed broken.
// The defect was invisible for exactly as long as the matrix asserted absence
// only where it already worked: on raft.sm_type disk and highperf the state
// machine adapter implements sm.NALookup, go-quicraft's Lookup fast path boxed
// its typed nil []byte into a non-nil interface{}, and service.KVService
// reported every missing key as a successful read of an empty value — so
// Exists was true for keys never written and stayed true after a delete
// (measured 2026-09-02, file/raft). The sm_types without NALookup took the
// generic path and were correct. A per-cell assertion is the only thing that
// separates those two populations, which is why this is a matrix assertion and
// not a unit test in the affected package.
//
// The delete half is asserted separately from the never-written half. They
// reach the read plane the same way today, but a tombstone that reads back as
// an empty value is a distinct failure a store could reintroduce on its own.
func assertAbsenceIsObservable(t *testing.T, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	const key = "absence/probe"

	assertAbsent := func(stage string) {
		exists, err := cl.kv.Exists(ctx, key)
		if err != nil {
			t.Errorf("Exists(%q) %s: %v", key, stage, err)
		}
		if exists {
			t.Errorf("Exists(%q) = true %s; want false", key, stage)
		}
		got, err := cl.kv.Get(ctx, key)
		var knf *transport.KeyNotFoundError
		if !errors.As(err, &knf) {
			t.Errorf("Get(%q) %s = %q err=%v; want a KeyNotFoundError, not a "+
				"successful read of an empty value", key, stage, got, err)
		}
	}

	assertAbsent("for a key that was never written")

	if err := cl.kv.Put(ctx, key, []byte("v1")); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	exists, err := cl.kv.Exists(ctx, key)
	if err != nil || !exists {
		t.Errorf("Exists(%q) after Put = %v err=%v; want true", key, exists, err)
	}
	if got, err := cl.kv.Get(ctx, key); err != nil || string(got) != "v1" {
		t.Errorf("Get(%q) after Put = %q err=%v; want %q", key, got, err, "v1")
	}

	if err := cl.kv.Delete(ctx, key); err != nil {
		t.Fatalf("Delete(%q): %v", key, err)
	}
	assertAbsent("after a Delete")
}

// ---------------------------------------------------------------------------
// small builders, so the batteries above read as intent
// ---------------------------------------------------------------------------

func valueEquals(key, value string) transport.Compare {
	return transport.Compare{
		Key:    []byte(key),
		Target: transport.CompareValue,
		Result: transport.CompareEqual,
		Value:  []byte(value),
	}
}

func put(key, value string) transport.Op {
	return transport.Op{Type: transport.OpPut, Key: []byte(key), Value: []byte(value)}
}
