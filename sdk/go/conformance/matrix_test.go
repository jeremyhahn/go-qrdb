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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
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
// The cells are not parallel. Two of them drive process-global state-machine
// storage selection (statemachine.SetSMStorage), and raft cells are expensive
// enough that interleaving them would make the leader-election waits fight each
// other for CPU on a shared runner.
func TestGuardedVerbConformanceMatrix(t *testing.T) {
	for _, c := range matrix() {
		t.Run(c.name(), func(t *testing.T) {
			t.Logf("cell %s: expecting %s", c.name(), c.want)
			cl := c.boot(t)

			// Every cell, both outcomes. Absence is not a guarded-verb
			// capability — it is the read plane the guarded verbs are
			// observed through, and a cell that cannot answer it makes every
			// assertion below vacuous.
			assertAbsenceIsObservable(t, cl)

			switch c.want {
			case fullPassage:
				assertGuardedPlaneWorks(t, c, cl)
			case typedRefusal:
				assertGuardedPlaneRefusesSafely(t, c, cl)
			case guardedWriteRefusal:
				assertGuardedWritesRefuseSafely(t, c, cl)
			}
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
}

// assertRevisionCompareBehaviour pins the one documented deviation in the
// matrix, and pins it as a deviation rather than letting it pass unnoticed.
//
// create_revision and mod_revision compares need MVCC revisions to compare
// against. The file engine keeps none, and refuses them explicitly rather than
// reading revision 0 — because a client written for the MVCC engine does
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

	if c.backend == database.BackendFile {
		if err == nil {
			t.Errorf("a create_revision compare on the file engine returned %+v with no error; "+
				"it must REFUSE, because reading a revision this engine does not keep would "+
				"let two writers both believe they won and lose one update silently", resp)
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
// typed refusal
// ---------------------------------------------------------------------------

// assertGuardedWritesRefuseSafely is the contract for a cell that DISPATCHES
// the guarded delete and swap but whose backend cannot carry what their result
// promises.
//
// Only sm_type "fast" is in this state: it is a KVStateMachine over
// FastBackend, whose background persist is ordered against nothing and swallows
// its error, so an acknowledged guarded write can be reversed by an older
// pending one. Measured with ORDINARY writes in
// pkg/statemachine/guarded_ops_test.go — Put(k,"old") then Put(k,"new") leaves
// "old" durable, and Put(k,"v") then Delete(k) leaves the key present
// (go-qrdb#224). Applied to CompareAndDelete that is a released lock coming
// back, so the verbs refuse rather than report a success the disk can undo.
//
// The three assertions are all load-bearing and each rules out a different
// wrong fix:
//
//   - PutIfAbsent still WORKS. A blanket "refuse everything guarded on fast"
//     would also satisfy a refusal-only check, and it would break first boot —
//     the identity index claims identities with PutIfAbsent. That it keeps
//     working is deliberate and is recorded on go-qrdb#216.
//   - The delete and swap refuse with a TYPED capability error, not an
//     absent opcode. A caller must be able to tell "this backend will not do
//     it" from "this build has no such verb"; only the first is fixed by
//     changing raft.sm_type.
//   - The node still serves afterwards, which is the go-qrdb#129 regression
//     every refusing cell pins.
func assertGuardedWritesRefuseSafely(t *testing.T, c cell, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	if err := cl.kv.Put(ctx, "before", []byte("1")); err != nil {
		t.Fatalf("%s: plain Put failed before any guarded verb, so this cell never "+
			"reached the state under test: %v", c.name(), err)
	}

	// The acquire half is unaffected and must stay that way.
	assertPutIfAbsentWorks(t, cl)

	for _, r := range []struct {
		verb string
		call func() error
	}{
		{"CompareAndSwap", func() error {
			_, err := cl.guarded.CompareAndSwap(ctx, "claim", []byte("acme"), []byte("globex"))
			return err
		}},
		{"CompareAndDelete", func() error {
			_, err := cl.guarded.CompareAndDelete(ctx, "claim", []byte("acme"))
			return err
		}},
		{"ExecuteTransaction", func() error {
			_, err := cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
				Compares:   []transport.Compare{valueEquals("before", "1")},
				SuccessOps: []transport.Op{put("txn/x", "x")},
			})
			return err
		}},
	} {
		// The guard REFUSED, so the expected value it named must be untouched.
		// Asserting only that an error came back would pass against an
		// implementation that refused after writing.
		assertCapabilityRefusal(t, r.verb, r.call())
	}

	// The claim PutIfAbsent took is still exactly as it was: a refused swap did
	// not edit it and a refused delete did not remove it.
	if got, err := cl.kv.Get(ctx, "claim"); err != nil || string(got) != "acme" {
		t.Errorf("%s: after two refused guarded verbs the claim reads %q err=%v, want %q "+
			"untouched — a refusal that mutates is worse than one that executes",
			c.name(), got, err, "acme")
	}

	if err := cl.kv.Put(ctx, "after", []byte("2")); err != nil {
		t.Fatalf("%s: a plain Put FAILED after the guarded verbs were refused (%v). "+
			"Refusing an unsupported verb must not cost the cluster its shard "+
			"(go-qrdb#129)", c.name(), err)
	}
	if got, err := cl.kv.Get(ctx, "after"); err != nil || string(got) != "2" {
		t.Fatalf("%s: a linearizable Get FAILED after the guarded verbs were refused "+
			"(%q, %v)", c.name(), got, err)
	}
}

// assertGuardedPlaneRefusesSafely is the contract for a cell whose state
// machine does not carry the MULTI-KEY half of the plane: refusing is allowed,
// breaking is not.
//
// Both halves matter and the second is the one with history. Before
// go-qrdb#129 these cells did not refuse — an unimplemented guarded verb
// returned an error from SM.Update, go-quicraft treated it as a fatal Apply
// failure, and the shard's circuit breaker tripped. After that ONE call, every
// later proposal failed including plain Put, and a linearizable read never
// returned at all. So "the node still serves afterwards" is not a courtesy
// check; it is the regression, and asserting it here pins the fix permanently
// across every remaining cell of this kind.
func assertGuardedPlaneRefusesSafely(t *testing.T, c cell, cl clients) {
	t.Helper()
	ctx := boundedCtx(t)

	// Prove the node works BEFORE the guarded verbs, so a failure afterwards
	// is attributable to them rather than to a cell that never came up.
	if err := cl.kv.Put(ctx, "before", []byte("1")); err != nil {
		t.Fatalf("%s: plain Put failed before any guarded verb, so this cell never "+
			"reached the state under test: %v", c.name(), err)
	}

	// The SINGLE-KEY verbs are not part of the refusal set, and this suite
	// measures that rather than assuming it.
	//
	// PutIfAbsent never was: it is a native KV opcode handled against the
	// state machine's own store, so it is present and genuinely atomic with no
	// MVCC store behind it. go-qrdb#175 gave CompareAndDelete (opcode 66) and
	// CompareAndSwap (67) exactly the same treatment — they were missing from
	// these state machines' dispatch tables, not from their capabilities — so
	// all three now belong on this side of the line and the same battery
	// covers them.
	//
	// What is still absent here is the MULTI-KEY plane: ExecuteTransaction
	// needs an MVCC store to evaluate its compares against, and these cells
	// have none (WithKVMVCCStore has no production caller — go-qrdb#135/#136).
	//
	// So the capability on these cells is PARTIAL, not missing, and saying so
	// matters: a consumer that needs a claim it can acquire, renew and release
	// is now fully served here, and a matrix that lumped the single-key verbs
	// in with the refusals would understate what works and could later be
	// "fixed" by making a working verb refuse.
	assertSingleKeyGuardedVerbsWork(t, cl)

	refusals := []struct {
		verb string
		call func() error
	}{
		{"ExecuteTransaction", func() error {
			_, err := cl.txn.ExecuteTransaction(ctx, &transport.TransactionRequest{
				Compares:   []transport.Compare{valueEquals("before", "1")},
				SuccessOps: []transport.Op{put("txn/x", "x")},
			})
			return err
		}},
	}

	for _, r := range refusals {
		// Not merely "an error". assertCapabilityRefusal rejects a deadline, a
		// not-leader error, and a remote auth or availability failure, then
		// requires the refusal to name the absent opcode or subsystem. Without
		// that, a WEDGED cell — the go-qrdb#129 regression these cells exist to
		// pin — returns DeadlineExceeded on its bounded context and would be
		// scored as "refused as required", so the gate would go green on the
		// very defect it was written to catch.
		assertCapabilityRefusal(t, r.verb, r.call())
	}

	// The load-bearing assertion. A wedged shard fails these because the
	// bounded context expires — which is precisely the pre-go-qrdb#129
	// behaviour this pins against.
	if err := cl.kv.Put(ctx, "after", []byte("2")); err != nil {
		t.Fatalf("%s: a plain Put FAILED after the guarded verbs were refused (%v). "+
			"Refusing an unsupported verb must not cost the cluster its shard "+
			"(go-qrdb#129)", c.name(), err)
	}
	got, err := cl.kv.Get(ctx, "after")
	if err != nil {
		t.Fatalf("%s: a linearizable Get FAILED after the guarded verbs were refused (%v). "+
			"Before go-qrdb#129 this call never returned at all", c.name(), err)
	}
	if string(got) != "2" {
		t.Errorf("%s: read back %q after the refusals; want %q", c.name(), got, "2")
	}
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
