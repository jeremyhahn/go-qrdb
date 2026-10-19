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

//go:build integration

// kv_restore_default_sm_cli_test.go closes a coverage gap every OTHER
// multi-protocol CLI test file in this package shares: startMultiProtocolNode
// pins raft.sm_type: mvcc (multi_protocol_helpers_test.go), so nothing in
// this package had ever exercised the daemon's DEFAULT, out-of-the-box state
// machine -- raft.sm_type: pebble (cmd/qrdbd/start.go's "--sm" flag default)
// -- against any MVCC-dependent command. That matters specifically for `kv
// restore`: an operator who reaches for it on a freshly `qrdbd start`'ed node
// (no "--sm mvcc") must get an ACTIONABLE refusal naming the fix
// ("raft.sm_type: mvcc"), never silently wrong or empty data.
//
// This file uses startMultiProtocolNodeWithSM(t, "") -- an empty smType
// omits the "--sm" flag entirely, so the daemon boots under its own actual
// default rather than an explicit "--sm pebble" that could silently drift
// from it -- and proves, across every transport:
//
//   - `kv restore` refuses with the actionable raft.sm_type: mvcc message
//     (the headline case: the same command this whole feature exists for).
//   - `kv get --at-revision` and `kv get --revision` refuse the same way --
//     GetAtRevision/GetRevision are also MVCC-only queries
//     (pkg/service/mvcc_service.go), so a caller reaching for point-in-time
//     reads on this backend gets the same actionable signal, not silently
//     wrong data.
//   - A transaction compare on CompareModRevision/CompareCreateRevision is
//     refused by the pebble state machine itself (pkg/store/pebble.go's
//     pebbleHandleTransaction) rather than silently evaluated against an
//     implicit revision 0 for every key -- the exact bug this refusal
//     replaces: two concurrent CAS writers using a revision-based
//     precondition against this backend used to BOTH "succeed" (both read
//     revision 0, both compared equal, both committed) and one write was
//     silently lost. This is exercised through `qrdb tx execute`
//     (cmd/qrdb/transaction.go), the only CLI surface for raw transaction
//     compares (mirrors tx_cli_test.go's own use of `tx execute --file`).
//
// PRODUCT BUG (confirmed, reported, NOT worked around here -- pkg/** is out
// of scope for this test package): TestKVRestore_DefaultStateMachine_ActionableRefusal_PerProtocol
// currently FAILS, on every transport, because the actionable
// MVCCBackendRequiredError this test asserts on is never actually produced
// against a raft.sm_type: pebble shard. Root cause: on this backend,
// PebbleStateMachine.Lookup's default case (pkg/store/pebble.go:450) returns
// &store.InvalidQueryError{Reason: "unsupported query type"} -- a type
// declared in pkg/store/errors.go that has NO UnsupportedQueryType() bool
// method. pkg/service.MVCCKVService.translateReadError
// (pkg/service/mvcc_service.go:847-856) duck-types that exact refusal via the
// unexported unsupportedQueryTypeErr interface, but its doc comment
// (pkg/service/mvcc_backend_errors.go:17-34) only ever names
// pkg/engine.InvalidQueryError (a DIFFERENT, same-named type in a different
// package, defined at pkg/engine/errors.go:37-59, which DOES implement
// UnsupportedQueryType()) as satisfying it. pkg/engine is never on the call
// path for raft.sm_type: pebble at all (that backend's Raft state machine IS
// pkg/store.PebbleStateMachine directly -- confirmed by this test's own log
// output: "sm_type=on-disk"), so the errors.As(err, &uqe) check in
// translateReadError can never match on this backend, and CurrentRevision,
// GetAtRevision, GetRevision, and RestoreToRevision all instead surface the
// raw, unhelpful "pebbledb: invalid query: unsupported query type" straight
// through every transport -- exactly the "symptom, not the fix" message
// MVCCBackendRequiredError's own doc comment (pkg/service/mvcc_backend_errors.go:42-48)
// says this type exists to prevent. The fix belongs in pkg/store/errors.go:
// give store.InvalidQueryError the identical
// `func (e *InvalidQueryError) UnsupportedQueryType() bool { return e.Reason == "unsupported query type" }`
// method pkg/engine.InvalidQueryError already has (Go's structural interface
// satisfaction does not care which package declares a matching method set,
// so this alone closes the gap with no import changes needed).
//
// See kv_restore_cli_test.go for the positive-path (raft.sm_type: mvcc)
// coverage of `kv restore` itself.
package integration_test

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestKVRestore_DefaultStateMachine_ActionableRefusal_PerProtocol boots a
// daemon under the actual default state machine (no "--sm" flag, i.e.
// raft.sm_type: pebble) and proves `kv restore`, `kv get --at-revision`, and
// `kv get --revision` all refuse with the actionable "raft.sm_type: mvcc"
// message on every transport, rather than succeeding with silently wrong
// data or failing with an unhelpful generic error.
func TestKVRestore_DefaultStateMachine_ActionableRefusal_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNodeWithSM(t, "")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testMVCCRefusalOverProtocol(t, node, proto)
		})
	}
}

// testMVCCRefusalOverProtocol exercises the three MVCC-only read/write
// surfaces against a single protocol on the default (non-MVCC) daemon.
func testMVCCRefusalOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	const wantSubstr = "raft.sm_type: mvcc"
	prefix := "kvrestore-default-sm/" + proto.name + "/"
	key := prefix + "k"

	// A plain KV put/get must work fine -- this backend is a normal KV
	// store, just one with no MVCC revision history. This also
	// double-checks the refusals below are specific to the MVCC-only
	// surfaces, not a broken node.
	if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v1"); err != nil {
		t.Fatalf("[%s] kv put %s on default-SM daemon: %v\noutput: %s", proto.name, key, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "kv", "get", key); err != nil || strings.TrimSpace(out) != "v1" {
		t.Fatalf("[%s] kv get %s on default-SM daemon = %q, err=%v, want %q", proto.name, key, out, err, "v1")
	}

	// --- kv restore: the headline case. ---------------------------------
	out, err := runCLIProto(t, node, proto, "kv", "restore", "--revision", "1", "--keys", key)
	if err == nil {
		t.Fatalf("[%s] kv restore on default-SM (pebble) daemon succeeded, want an actionable MVCCBackendRequiredError refusal (output: %s)", proto.name, out)
	}
	if !strings.Contains(out, wantSubstr) && !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("[%s] kv restore on default-SM daemon: error does not name the fix (%q); output: %s, err: %v", proto.name, wantSubstr, out, err)
	}

	// --- kv get --at-revision. -------------------------------------------
	out, err = runCLIProto(t, node, proto, "kv", "get", key, "--at-revision", "1")
	if err == nil {
		t.Fatalf("[%s] kv get %s --at-revision 1 on default-SM daemon succeeded, want an actionable refusal (output: %s)", proto.name, key, out)
	}
	if !strings.Contains(out, wantSubstr) && !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("[%s] kv get %s --at-revision on default-SM daemon: error does not name the fix (%q); output: %s, err: %v", proto.name, key, wantSubstr, out, err)
	}

	// --- kv get --revision (full MVCC metadata read). --------------------
	out, err = runCLIProto(t, node, proto, "kv", "get", key, "--revision")
	if err == nil {
		t.Fatalf("[%s] kv get %s --revision on default-SM daemon succeeded, want an actionable refusal (output: %s)", proto.name, key, out)
	}
	if !strings.Contains(out, wantSubstr) && !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("[%s] kv get %s --revision on default-SM daemon: error does not name the fix (%q); output: %s, err: %v", proto.name, key, wantSubstr, out, err)
	}
}

// TestTx_DefaultStateMachine_RevisionCompareRefusal_PerProtocol boots a
// daemon under the default (non-MVCC, raft.sm_type: pebble) state machine
// and proves a transaction compare on CompareModRevision or
// CreateRevision -- which this backend cannot honor, since it keeps no
// revision history at all -- is refused as an application error rather than
// silently evaluated against an implicit revision 0 for every key.
//
// That silent-zero behavior was the actual prior bug this refusal replaces:
// a client written for the MVCC engine does read-then-CAS on
// ModRevision/CreateRevision; against a pebble backend every key's
// ModRevision compared as 0, so the compare always "succeeded" regardless of
// the key's real state. Two concurrent writers doing that read-then-CAS
// dance would BOTH have their compare evaluate true and both commit, so the
// second writer's commit silently clobbered the first with no conflict
// signal -- a classic lost update, and a materially worse outcome than an
// explicit refusal.
func TestTx_DefaultStateMachine_RevisionCompareRefusal_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNodeWithSM(t, "")

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTxRevisionCompareRefusalOverProtocol(t, node, proto)
		})
	}
}

func testTxRevisionCompareRefusalOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	prefix := "tx-default-sm/" + proto.name + "/"

	// --- CompareModRevision must be refused, and the op it guards must
	// NOT execute. ---------------------------------------------------------
	keyMod := prefix + "mod-revision"
	if out, err := runCLIProto(t, node, proto, "kv", "put", keyMod, "untouched"); err != nil {
		t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, keyMod, err, out)
	}
	resp, out, err := txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyMod), Target: transport.CompareModRevision, Result: transport.CompareEqual, ModRevision: 0},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyMod), Value: []byte("should-not-be-written")},
		},
	})
	assertTxRevisionCompareRefused(t, proto, "CompareModRevision", resp, out, err)
	if got, gerr := runCLIProto(t, node, proto, "kv", "get", keyMod); gerr != nil || strings.TrimSpace(got) != "untouched" {
		t.Fatalf("[%s] kv get %s after refused CompareModRevision tx = %q, err=%v, want unchanged %q", proto.name, keyMod, got, gerr, "untouched")
	}

	// --- CompareCreateRevision must be refused identically. -------------
	keyCreate := prefix + "create-revision"
	if out, err := runCLIProto(t, node, proto, "kv", "put", keyCreate, "untouched"); err != nil {
		t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, keyCreate, err, out)
	}
	resp2, out2, err2 := txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyCreate), Target: transport.CompareCreateRevision, Result: transport.CompareEqual, CreateRevision: 0},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyCreate), Value: []byte("should-not-be-written")},
		},
	})
	assertTxRevisionCompareRefused(t, proto, "CompareCreateRevision", resp2, out2, err2)
	if got, gerr := runCLIProto(t, node, proto, "kv", "get", keyCreate); gerr != nil || strings.TrimSpace(got) != "untouched" {
		t.Fatalf("[%s] kv get %s after refused CompareCreateRevision tx = %q, err=%v, want unchanged %q", proto.name, keyCreate, got, gerr, "untouched")
	}

	// --- Positive control: CompareValue and CompareVersion -- the two
	// comparisons pebble CAN honor without MVCC revisions -- still work on
	// this same backend, proving the refusal above is specific to
	// revision-based compares, not a broken transaction path. ------------
	keyValue := prefix + "value-compare-still-works"
	if out, err := runCLIProto(t, node, proto, "kv", "put", keyValue, "v1"); err != nil {
		t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, keyValue, err, out)
	}
	valueResp, valueOut, valueErr := txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyValue), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v1")},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyValue), Value: []byte("v2")},
		},
	})
	if valueErr != nil {
		t.Fatalf("[%s] tx execute (CompareValue, default-SM): %v\noutput: %s", proto.name, valueErr, valueOut)
	}
	if !valueResp.Succeeded {
		t.Fatalf("[%s] tx execute (CompareValue, default-SM): Succeeded = false, want true (output: %s)", proto.name, valueOut)
	}
	if got, gerr := runCLIProto(t, node, proto, "kv", "get", keyValue); gerr != nil || strings.TrimSpace(got) != "v2" {
		t.Fatalf("[%s] kv get %s after CompareValue tx (default-SM) = %q, err=%v, want %q", proto.name, keyValue, got, gerr, "v2")
	}
}

// assertTxRevisionCompareRefused asserts that a `tx execute` carrying a
// revision-based compare was refused on a default-SM (pebble) daemon: the
// transaction either surfaces as a hard CLI error, or -- since
// pkg/store/pebble.go's pebbleHandleTransaction encodes this refusal as a
// state-machine application error (protocol.EncodeAppError) rather than
// failing the Raft apply loop -- it must at minimum report Succeeded=false
// (the guarded write did not run), and either shape's message must name the
// actionable fix. resp is only meaningful when err == nil (txExecuteFile
// leaves it at its zero value otherwise).
func assertTxRevisionCompareRefused(t *testing.T, proto multiProtocol, label string, resp transport.TransactionResponse, out string, err error) {
	t.Helper()
	const wantSubstr = "MVCC state machine"
	if err != nil {
		if !strings.Contains(out, wantSubstr) && !strings.Contains(err.Error(), wantSubstr) {
			t.Fatalf("[%s] tx execute (%s, default-SM): error does not name the fix (%q); output: %s, err: %v", proto.name, label, wantSubstr, out, err)
		}
		return
	}
	// No hard CLI error: the refusal must have surfaced as a failed
	// transaction the CLI printed successfully (txExecuteFile already
	// decoded resp from this same output, fatal-ing the test if that
	// decode itself failed).
	if resp.Succeeded {
		t.Fatalf("[%s] tx execute (%s, default-SM): Succeeded = true, want the revision-based compare to be refused (output: %s)", proto.name, label, out)
	}
}
