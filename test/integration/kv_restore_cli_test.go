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

// kv_restore_cli_test.go drives the selective point-in-time restore
// (`qrdb kv restore`, cmd/qrdb/kv.go's newKVRestoreCmd) end to end through
// the real client CLI binary, against every transport (REST, gRPC, QUIC,
// MCP, Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go, which pins raft.sm_type: mvcc -- the
// state machine this feature requires). It is a sibling to kv_cli_test.go
// rather than an extension of it: kv_cli_test.go's own doc comment already
// scopes it to put/get/del/list/exists/range/compact/revision, and restore
// is large enough (three plan actions, CAS-vs-force semantics, a distinct
// non-zero-exit-code contract, its own guard family) to warrant its own
// file, matching the tx_cli_test.go / transaction.go precedent.
//
// Coverage in this file:
//   - TestKVRestore_CLI_PerProtocol: per protocol, a "lifecycle" subtest
//     (revert, recreate-after-delete via --prefix -- the durable-revision-
//     index-backed bulk-delete-recovery headline case, delete-of-a-key-
//     created-after-R, --dry-run writing nothing, the request-shape/
//     internal-key/compaction-floor guards, and --output json shape with
//     plaintext keys) and a "conflict" subtest (a genuine CAS conflict
//     produced by racing a live external writer against the restore's own
//     internal plan-then-apply window, proving the non-zero exit code on an
//     unforced partial apply and that --force then converges and exits 0).
//   - TestKVRestore_CLI_TenantIsolation_PerProtocol: a restore scoped to one
//     tenant's database never reads or writes another tenant's identically
//     named key, and a tenant cannot restore another tenant's database at
//     all -- mirrors tenant_isolation_cli_test.go's proof style, applied to
//     restore specifically.
//
// See kv_restore_default_sm_cli_test.go for the complementary negative
// space: this feature's actionable refusal on a daemon that does NOT run
// raft.sm_type: mvcc (the default).
//
// STALE PRODUCT-BUG NOTE, CORRECTED 2026-09-02 (go-qrdb#185). The paragraph
// that stood here said "every 'rest' subtest in this file FAILS" and named a
// root cause in pkg/auth/middleware/rest.go's normalizePath: an exact-match
// switch listing /kv/scan, /kv/range, /kv/lease, /kv/revision and /kv/compact
// but not /kv/restore, so /kv/restore fell through to a generic
// `HasPrefix(path, "/kv/") -> "/kv"` rewrite and the POST lookup missed.
//
// Both halves of that root cause are gone from the tree:
//
//   - normalizePath NO LONGER EXISTS. The middleware resolves a route's
//     permission through RESTRouteRegistry, which matches with chi's own trie,
//     so templated routes and most-specific-wins resolution come from the
//     router rather than a parallel pattern table. There is no path-rewrite
//     step left to lose a sub-route in.
//   - POST /kv/restore IS registered with its permission, at
//     pkg/api/rest/server.go:1035
//     (`s.route(http.MethodPost, "/kv/restore", permission.MVCCRestore, ...)`).
//
// What is NOT claimed: that the "rest" subtests now pass. That was not
// verified when this note was corrected -- the suite needs the docker harness
// -- and asserting it from a code reading would repeat the mistake this note
// is being corrected for. What is claimed is narrower and checkable: the
// mechanism this note names as the cause is not in the tree, so a reader must
// re-measure rather than trust the diagnosis.
//
// That a fix landed and this note never noticed is itself the finding, and it
// is tracked: go-qrdb#203 measured that no CI leg compiles test/integration/*
// at all (test/integration/auth has not compiled since the guarded verbs
// landed, in silence). A suite nothing builds cannot tell you when its
// known-failing cases start passing.
//
// One thing HAS changed for this path as of go-qrdb#185: a known path with an
// unregistered verb now answers 405 with an Allow header, not 403 "route not
// authorized (no permission mapping)". Any assertion anywhere on that 403
// string for a wrong-verb request is now wrong on both the code and the text.
package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Display types mirroring cmd/qrdb/kv.go's unexported JSON display shapes
// (restoreKeyChangeDisplay / restoreRevisionResultDisplay), so `kv restore
// --output json` can be decoded and asserted on precisely. Mirrors
// kvGetRevisionDisplay/kvRangeDisplay's existing pattern in kv_cli_test.go.
// ---------------------------------------------------------------------------

// kvRestoreChangeDisplay mirrors cmd/qrdb/kv.go's restoreKeyChangeDisplay.
type kvRestoreChangeDisplay struct {
	Key          string                  `json:"key"`
	Action       transport.RestoreAction `json:"action"`
	FromRevision int64                   `json:"from_revision,omitempty"`
	ToRevision   int64                   `json:"to_revision,omitempty"`
	ValueBytes   int64                   `json:"value_bytes,omitempty"`
}

// kvRestoreResultDisplay mirrors cmd/qrdb/kv.go's restoreRevisionResultDisplay.
type kvRestoreResultDisplay struct {
	Revision   int64                    `json:"revision"`
	Changes    []kvRestoreChangeDisplay `json:"changes"`
	Conflicts  []kvRestoreChangeDisplay `json:"conflicts,omitempty"`
	Applied    int                      `json:"applied"`
	Skipped    int                      `json:"skipped"`
	Conflicted int                      `json:"conflicted"`
	DryRun     bool                     `json:"dry_run"`
}

// kvRestoreJSON runs `kv restore <args...> --output json` over proto and
// decodes the result. args carries only the restore-specific flags
// (--revision, --prefix/--keys, --dry-run, --force); the command name and
// --output json are added here. The decoded result is the zero value when
// err != nil.
func kvRestoreJSON(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) (kvRestoreResultDisplay, string, error) {
	t.Helper()
	full := append([]string{"--output", "json", "kv", "restore"}, args...)
	out, err := runCLIProto(t, node, proto, full...)
	var result kvRestoreResultDisplay
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
			t.Fatalf("[%s] decode `kv restore --output json`: %v\noutput: %s", proto.name, jerr, out)
		}
	}
	return result, out, err
}

// kvModRevision runs `kv get <key> --revision --output json` over proto and
// returns the key's current ModRevision.
func kvModRevision(t *testing.T, node *multiProtoNode, proto multiProtocol, key string) int64 {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "kv", "get", key, "--revision")
	if err != nil {
		t.Fatalf("[%s] kv get %s --revision: %v\noutput: %s", proto.name, key, err, out)
	}
	var kv kvGetRevisionDisplay
	if jerr := json.Unmarshal([]byte(out), &kv); jerr != nil {
		t.Fatalf("[%s] decode `kv get --revision` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return kv.ModRevision
}

// ---------------------------------------------------------------------------
// TestKVRestore_CLI_PerProtocol
// ---------------------------------------------------------------------------

// TestKVRestore_CLI_PerProtocol drives `qrdb kv restore` -- the full plan
// action set, --dry-run, every guard, --output json, and the CAS-conflict /
// --force / exit-code contract -- through the CLI binary against every
// transport sharing one multiProtoNode.
func TestKVRestore_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Run("lifecycle", func(t *testing.T) {
				testKVRestoreLifecycleOverProtocol(t, node, proto)
			})
			t.Run("conflict", func(t *testing.T) {
				testKVRestoreConflictExitCodeOverProtocol(t, node, proto)
			})
		})
	}
}

// testKVRestoreLifecycleOverProtocol exercises revert, recreate-after-delete
// (prefix scope, the durable-revision-index-backed headline case), delete of
// a key created after the target revision, --dry-run's write-nothing
// guarantee, every request-shape/internal-key/compaction-floor guard, and
// --output json's machine-readable, plaintext-key shape.
func testKVRestoreLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	prefix := fmt.Sprintf("kvrestore/%s/", proto.name)

	// --- revert: a key mutated after R comes back to its value AT R, and
	// the restore is a forward write (a NEW, higher revision), never a
	// rewind. -------------------------------------------------------------
	t.Run("revert", func(t *testing.T) {
		key := prefix + "revert"
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v1"); err != nil {
			t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		revV1 := kvModRevision(t, node, proto, key)

		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v2"); err != nil {
			t.Fatalf("[%s] mutate put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		currentBefore := kvCLIRevision(t, node, proto)

		result, out, err := kvRestoreJSON(t, node, proto, "--revision", strconv.FormatInt(revV1, 10), "--keys", key)
		if err != nil {
			t.Fatalf("[%s] kv restore (revert) %s: %v\noutput: %s", proto.name, key, err, out)
		}
		if result.Applied != 1 || result.Conflicted != 0 {
			t.Fatalf("[%s] kv restore (revert) %s result = %+v, want Applied=1 Conflicted=0", proto.name, key, result)
		}
		if len(result.Changes) != 1 || result.Changes[0].Action != transport.RestoreActionRevert {
			t.Fatalf("[%s] kv restore (revert) %s changes = %+v, want a single revert", proto.name, key, result.Changes)
		}

		got, err := runCLIProto(t, node, proto, "kv", "get", key)
		if err != nil || strings.TrimSpace(got) != "v1" {
			t.Fatalf("[%s] kv get %s after revert = %q, err=%v, want %q", proto.name, key, got, err, "v1")
		}

		// History moved FORWARD, not backward: the key's ModRevision after
		// the restore must be strictly greater than the value it is now
		// holding was originally written at, and the store's global
		// revision counter must have advanced past its pre-restore value --
		// a Raft rewind would leave both unchanged (or lower).
		revAfterRestore := kvModRevision(t, node, proto, key)
		if revAfterRestore <= revV1 {
			t.Fatalf("[%s] kv restore (revert) %s: ModRevision after restore = %d, want > original write revision %d (restore must be a new write, not a rewind)",
				proto.name, key, revAfterRestore, revV1)
		}
		currentAfter := kvCLIRevision(t, node, proto)
		if currentAfter <= currentBefore {
			t.Fatalf("[%s] kv restore (revert) %s: current revision after = %d, want > pre-restore revision %d",
				proto.name, key, currentAfter, currentBefore)
		}
	})

	// --- recreate-after-delete via --prefix: THE headline bulk-delete
	// recovery case. A range scan alone cannot see a deleted key at any
	// revision; only the durable revision index (consulted by
	// planRestorePrefix's scanChangedKeys) can, so this deliberately uses
	// --prefix rather than --keys. ----------------------------------------
	t.Run("recreate_after_delete_via_prefix", func(t *testing.T) {
		recreatePrefix := prefix + "recreate/"
		keyA := recreatePrefix + "alpha"
		keyB := recreatePrefix + "bravo"

		if out, err := runCLIProto(t, node, proto, "kv", "put", keyA, "alive-before-delete-a"); err != nil {
			t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, keyA, err, out)
		}
		if out, err := runCLIProto(t, node, proto, "kv", "put", keyB, "alive-before-delete-b"); err != nil {
			t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, keyB, err, out)
		}
		revBeforeDelete := kvCLIRevision(t, node, proto)

		if out, err := runCLIProto(t, node, proto, "kv", "del", keyA); err != nil {
			t.Fatalf("[%s] kv del %s: %v\noutput: %s", proto.name, keyA, err, out)
		}
		if out, err := runCLIProto(t, node, proto, "kv", "exists", keyA); err != nil || strings.TrimSpace(out) != "false" {
			t.Fatalf("[%s] kv exists %s after delete = %q (err=%v), want false", proto.name, keyA, out, err)
		}

		result, out, err := kvRestoreJSON(t, node, proto, "--revision", strconv.FormatInt(revBeforeDelete, 10), "--prefix", recreatePrefix)
		if err != nil {
			t.Fatalf("[%s] kv restore (recreate via prefix) %s: %v\noutput: %s", proto.name, recreatePrefix, err, out)
		}
		if result.Applied != 1 || result.Conflicted != 0 {
			t.Fatalf("[%s] kv restore (recreate via prefix) %s result = %+v, want Applied=1 Conflicted=0 (keyB is unchanged and must be Skipped, not a Change)",
				proto.name, recreatePrefix, result)
		}
		if len(result.Changes) != 1 || result.Changes[0].Key != keyA || result.Changes[0].Action != transport.RestoreActionRecreate {
			t.Fatalf("[%s] kv restore (recreate via prefix) %s changes = %+v, want a single recreate of %s", proto.name, recreatePrefix, result.Changes, keyA)
		}

		if out, err := runCLIProto(t, node, proto, "kv", "exists", keyA); err != nil || strings.TrimSpace(out) != "true" {
			t.Fatalf("[%s] kv exists %s after recreate = %q (err=%v), want true", proto.name, keyA, out, err)
		}
		got, err := runCLIProto(t, node, proto, "kv", "get", keyA)
		if err != nil || strings.TrimSpace(got) != "alive-before-delete-a" {
			t.Fatalf("[%s] kv get %s after recreate = %q, err=%v, want %q", proto.name, keyA, got, err, "alive-before-delete-a")
		}
		// keyB was never deleted or mutated, so it must be left exactly
		// alone (Skipped, not touched).
		got, err = runCLIProto(t, node, proto, "kv", "get", keyB)
		if err != nil || strings.TrimSpace(got) != "alive-before-delete-b" {
			t.Fatalf("[%s] kv get %s (never mutated) after restore = %q, err=%v, want unchanged %q", proto.name, keyB, got, err, "alive-before-delete-b")
		}
	})

	// --- delete: a key created AFTER R does not exist at R, so restoring
	// to R removes it. -----------------------------------------------------
	t.Run("delete_key_created_after_target", func(t *testing.T) {
		key := prefix + "created-after-target"
		r0 := kvCLIRevision(t, node, proto)

		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "should-be-removed"); err != nil {
			t.Fatalf("[%s] put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		if out, err := runCLIProto(t, node, proto, "kv", "exists", key); err != nil || strings.TrimSpace(out) != "true" {
			t.Fatalf("[%s] kv exists %s before restore = %q (err=%v), want true", proto.name, key, out, err)
		}

		result, out, err := kvRestoreJSON(t, node, proto, "--revision", strconv.FormatInt(r0, 10), "--keys", key)
		if err != nil {
			t.Fatalf("[%s] kv restore (delete) %s: %v\noutput: %s", proto.name, key, err, out)
		}
		if result.Applied != 1 || result.Conflicted != 0 {
			t.Fatalf("[%s] kv restore (delete) %s result = %+v, want Applied=1 Conflicted=0", proto.name, key, result)
		}
		if len(result.Changes) != 1 || result.Changes[0].Action != transport.RestoreActionDelete {
			t.Fatalf("[%s] kv restore (delete) %s changes = %+v, want a single delete", proto.name, key, result.Changes)
		}

		if out, err := runCLIProto(t, node, proto, "kv", "exists", key); err != nil || strings.TrimSpace(out) != "false" {
			t.Fatalf("[%s] kv exists %s after restore = %q (err=%v), want false", proto.name, key, out, err)
		}
	})

	// --- --dry-run writes nothing. -----------------------------------------
	t.Run("dry_run_writes_nothing", func(t *testing.T) {
		key := prefix + "dryrun"
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "orig"); err != nil {
			t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		revOrig := kvModRevision(t, node, proto, key)
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "changed"); err != nil {
			t.Fatalf("[%s] mutate put %s: %v\noutput: %s", proto.name, key, err, out)
		}

		revBefore := kvCLIRevision(t, node, proto)
		result, out, err := kvRestoreJSON(t, node, proto, "--revision", strconv.FormatInt(revOrig, 10), "--keys", key, "--dry-run")
		if err != nil {
			t.Fatalf("[%s] kv restore --dry-run %s: %v\noutput: %s", proto.name, key, err, out)
		}
		if !result.DryRun || result.Applied != 0 {
			t.Fatalf("[%s] kv restore --dry-run %s result = %+v, want DryRun=true Applied=0", proto.name, key, result)
		}
		if len(result.Changes) != 1 || result.Changes[0].Action != transport.RestoreActionRevert {
			t.Fatalf("[%s] kv restore --dry-run %s changes = %+v, want a single planned revert", proto.name, key, result.Changes)
		}
		revAfter := kvCLIRevision(t, node, proto)
		if revAfter != revBefore {
			t.Fatalf("[%s] kv restore --dry-run %s: current revision moved from %d to %d, want unchanged (dry run must write nothing)",
				proto.name, key, revBefore, revAfter)
		}

		got, err := runCLIProto(t, node, proto, "kv", "get", key)
		if err != nil || strings.TrimSpace(got) != "changed" {
			t.Fatalf("[%s] kv get %s after --dry-run = %q, err=%v, want unchanged %q", proto.name, key, got, err, "changed")
		}

		// Table output (the default, non-JSON, render) must also show the
		// dry-run plan without applying it.
		out, err = runCLIProto(t, node, proto, "kv", "restore", "--revision", strconv.FormatInt(revOrig, 10), "--keys", key, "--dry-run")
		if err != nil {
			t.Fatalf("[%s] kv restore --dry-run %s (table): %v\noutput: %s", proto.name, key, err, out)
		}
		for _, want := range []string{"ACTION", "KEY", "FROM-REV", "TO-REV", "revert", key, "Mode: dry-run", "Applied: 0"} {
			if !strings.Contains(out, want) {
				t.Fatalf("[%s] kv restore --dry-run %s table output missing %q, got:\n%s", proto.name, key, want, out)
			}
		}
	})

	// --- --output json: machine-readable, and keys are plaintext, never
	// base64. --------------------------------------------------------------
	t.Run("output_json_plaintext_keys", func(t *testing.T) {
		key := prefix + "jsonshape"
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "beforejson"); err != nil {
			t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		revBefore := kvModRevision(t, node, proto, key)
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "afterjson"); err != nil {
			t.Fatalf("[%s] mutate put %s: %v\noutput: %s", proto.name, key, err, out)
		}

		result, out, err := kvRestoreJSON(t, node, proto, "--revision", strconv.FormatInt(revBefore, 10), "--keys", key, "--dry-run")
		if err != nil {
			t.Fatalf("[%s] kv restore --output json %s: %v\noutput: %s", proto.name, key, err, out)
		}
		if result.Revision != revBefore {
			t.Fatalf("[%s] kv restore --output json %s: Revision = %d, want %d", proto.name, key, result.Revision, revBefore)
		}
		if len(result.Changes) != 1 || result.Changes[0].Key != key {
			t.Fatalf("[%s] kv restore --output json %s: Changes = %+v, want exactly one change for key %q", proto.name, key, result.Changes, key)
		}
		if result.Changes[0].Action != transport.RestoreActionRevert {
			t.Fatalf("[%s] kv restore --output json %s: Action = %q, want %q", proto.name, key, result.Changes[0].Action, transport.RestoreActionRevert)
		}
		// The key must appear verbatim (plaintext, decoded above into
		// result.Changes[0].Key already), never base64-encoded in the raw
		// wire text -- checked independently of the decode so a future bug
		// that base64-encodes the key AND still round-trips through
		// json.Unmarshal (e.g. because Key were typed []byte instead of
		// string) would still be caught here.
		if strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(key))) {
			t.Fatalf("[%s] kv restore --output json %s: found base64-encoded key in output, want plaintext: %s", proto.name, key, out)
		}
		if !strings.Contains(out, key) {
			t.Fatalf("[%s] kv restore --output json %s: expected plaintext key %q in output, got: %s", proto.name, key, key, out)
		}
	})

	// --- guards --------------------------------------------------------
	t.Run("guard_empty_scope", func(t *testing.T) {
		out, err := runCLIProto(t, node, proto, "kv", "restore", "--revision", "1")
		if err == nil {
			t.Fatalf("[%s] kv restore --revision 1 (no scope) succeeded, want error (output: %s)", proto.name, out)
		}
	})

	t.Run("guard_both_prefix_and_keys", func(t *testing.T) {
		out, err := runCLIProto(t, node, proto, "kv", "restore", "--revision", "1", "--prefix", prefix, "--keys", prefix+"x")
		if err == nil {
			t.Fatalf("[%s] kv restore --prefix and --keys together succeeded, want error (output: %s)", proto.name, out)
		}
	})

	t.Run("guard_internal_key_via_keys", func(t *testing.T) {
		out, err := runCLIProto(t, node, proto, "kv", "restore", "--revision", "1", "--keys", "__sys:roles/admin")
		if err == nil {
			t.Fatalf("[%s] kv restore --keys __sys:roles/admin succeeded, want error (internal key namespace must be refused, output: %s)", proto.name, out)
		}
	})

	t.Run("guard_internal_key_via_prefix", func(t *testing.T) {
		out, err := runCLIProto(t, node, proto, "kv", "restore", "--revision", "1", "--prefix", "__sys:")
		if err == nil {
			t.Fatalf("[%s] kv restore --prefix __sys: succeeded, want error (internal key namespace must be refused, output: %s)", proto.name, out)
		}
	})

	t.Run("guard_revision_at_compaction_floor", func(t *testing.T) {
		key := prefix + "compaction-floor"
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v1"); err != nil {
			t.Fatalf("[%s] seed put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		revV1 := kvModRevision(t, node, proto, key)
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, "v2"); err != nil {
			t.Fatalf("[%s] mutate put %s: %v\noutput: %s", proto.name, key, err, out)
		}
		floorRevision := kvCLIRevision(t, node, proto)
		if out, err := runCLIProto(t, node, proto, "kv", "compact", "--revision", strconv.FormatInt(floorRevision, 10)); err != nil {
			t.Fatalf("[%s] kv compact --revision %d: %v\noutput: %s", proto.name, floorRevision, err, out)
		}

		out, err := runCLIProto(t, node, proto, "kv", "restore", "--revision", strconv.FormatInt(revV1, 10), "--keys", key)
		if err == nil {
			t.Fatalf("[%s] kv restore --revision %d (at/below the compaction floor %d) succeeded, want RevisionCompactedError (output: %s)",
				proto.name, revV1, floorRevision, out)
		}
	})
}

// ---------------------------------------------------------------------------
// Conflict + exit code
// ---------------------------------------------------------------------------

// raceOverwriteUntilStopped repeatedly PUTs a fresh value to url (an
// authenticated /kv/<key> REST endpoint) until stop is closed. It is test
// scaffolding to induce a genuine, non-simulated CAS conflict -- see
// testKVRestoreConflictExitCodeOverProtocol -- not the feature under test, so
// it deliberately uses a raw mTLS HTTP client rather than the CLI: spawning a
// fresh `qrdb kv put` process per iteration would add process-start latency
// that shrinks the very race window this loop exists to hit. Errors are
// logged, not fatal: a transient failure here reflects contention with the
// restore call under test, not a defect.
func raceOverwriteUntilStopped(t *testing.T, client *http.Client, url string, stop <-chan struct{}) {
	t.Helper()
	i := 0
	for {
		select {
		case <-stop:
			return
		default:
		}
		i++
		body := []byte(fmt.Sprintf("racer-%d", i))
		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
		if err != nil {
			continue
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			t.Logf("raceOverwriteUntilStopped: transient PUT error (expected under contention): %v", doErr)
		} else {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// startAsyncKVRestore starts `qrdb kv restore <restoreArgs...>` against node
// over proto in the background (cmd.Start, not Wait), capturing stdout and
// stderr into SEPARATE thread-safe buffers (watchOutputBuf, shared with
// watch_cli_test.go's identical need), and returns the running *exec.Cmd so
// the caller can race a concurrent mutation against the store while this
// single restore call is still executing its own internal plan-then-apply
// sequence, then Wait() for it to finish.
//
// stdout/stderr are deliberately NOT combined into one buffer here (unlike
// runCLI's CombinedOutput, which every other helper in this package uses):
// a conflicted, unforced apply is the one case in this whole file where the
// command BOTH prints a complete, valid JSON result to stdout (printed by
// printRestoreRevisionResult before RunE returns) AND returns a non-nil
// error that cobra prints to stderr afterward (*RestoreConflictsError, so
// scripted callers see a non-zero exit code) -- so a combined buffer is
// valid JSON followed by trailing prose, which breaks strict JSON decoding.
// Keeping the streams separate lets the JSON result and the error text each
// be inspected on their own terms.
func startAsyncKVRestore(t *testing.T, node *multiProtoNode, proto multiProtocol, restoreArgs ...string) (*exec.Cmd, *watchOutputBuf, *watchOutputBuf) {
	t.Helper()
	full := append(append([]string{}, node.cliArgsFor(proto)...), append([]string{"--output", "json", "kv", "restore"}, restoreArgs...)...)
	cmd := exec.Command(node.cli, full...)
	stdout := &watchOutputBuf{}
	stderr := &watchOutputBuf{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("[%s] start `qrdb kv restore %s`: %v", proto.name, strings.Join(restoreArgs, " "), err)
	}
	return cmd, stdout, stderr
}

// testKVRestoreConflictExitCodeOverProtocol proves the default (non-Force)
// CAS-conflict contract with a REAL race, not a simulated one: a live
// external writer is raced against a single `kv restore` apply call's own
// internal plan-then-apply window (see pkg/service/mvcc_restore.go's
// RestoreToRevision doc comment -- the plan is a single upfront read and the
// apply is a sequential, per-key, Raft-replicated compare-and-swap in
// ascending key order), so that the LAST key in the scope -- applied only
// after every other key's own Raft round trip -- has a wide, real wall-clock
// window for the external write to land in before its own CAS runs.
//
// This deliberately does NOT try to reproduce the conflict as "dry-run,
// mutate, then apply" via two separate CLI invocations: RestoreToRevision
// recomputes its plan fresh on every call, so a mutation between two
// SEPARATE invocations is simply absorbed into the second call's own plan
// and never conflicts -- the CAS guard only ever fires against a write that
// lands INSIDE one call's plan-to-apply window. A two-invocation "preview
// then apply" race would therefore be flaky-by-construction (it would only
// occasionally coincide with a real internal window) rather than reliably
// reproducing the documented behavior.
func testKVRestoreConflictExitCodeOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	const n = 40
	racePrefix := fmt.Sprintf("kvrestore-race/%s/", proto.name)

	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("%sk%02d", racePrefix, i)
		if out, err := runCLIProto(t, node, proto, "kv", "put", keys[i], fmt.Sprintf("v1-%d", i)); err != nil {
			t.Fatalf("[%s] seed %s: %v\noutput: %s", proto.name, keys[i], err, out)
		}
	}
	targetRevision := kvCLIRevision(t, node, proto)

	// Mutate every key so the whole scope is in a "revert" plan.
	for i, key := range keys {
		if out, err := runCLIProto(t, node, proto, "kv", "put", key, fmt.Sprintf("v2-%d", i)); err != nil {
			t.Fatalf("[%s] mutate %s: %v\noutput: %s", proto.name, key, err, out)
		}
	}

	// sortRestoreSteps applies the plan in ascending key order, and the
	// zero-padded numeric suffixes sort exactly like the integers they
	// represent, so keys[n-1] is applied LAST -- after n-1 other Raft round
	// trips have already happened inside the SAME restore call.
	raceKey := keys[n-1]
	raceURL := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", node.restPort, raceKey)
	racer := mtlsHTTPClient(t, node.ca, 10*time.Second)

	cmd, stdoutBuf, stderrBuf := startAsyncKVRestore(t, node, proto, "--revision", strconv.FormatInt(targetRevision, 10), "--prefix", racePrefix)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		raceOverwriteUntilStopped(t, racer, raceURL, stop)
	}()

	waitErr := cmd.Wait()
	close(stop)
	wg.Wait()
	out := stdoutBuf.String()
	errOut := stderrBuf.String()

	if waitErr == nil {
		t.Fatalf("[%s] kv restore with a raced (conflicted) key exited 0, want non-zero (stdout: %s, stderr: %s)", proto.name, out, errOut)
	}
	if !strings.Contains(errOut, "unrestored due to conflicts") && !strings.Contains(errOut, "retry with --force") {
		t.Fatalf("[%s] kv restore non-zero exit: stderr does not mention the conflict/--force remedy: %q (stdout: %s)", proto.name, errOut, out)
	}

	var result kvRestoreResultDisplay
	if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
		t.Fatalf("[%s] decode `kv restore --output json` (conflict case): %v\nstdout: %s\nstderr: %s", proto.name, jerr, out, errOut)
	}
	if result.Conflicted < 1 {
		t.Fatalf("[%s] kv restore Conflicted = %d, want >= 1 (output: %s)", proto.name, result.Conflicted, out)
	}
	foundRaceKey := false
	for _, c := range result.Conflicts {
		if c.Key == raceKey {
			foundRaceKey = true
		}
	}
	if !foundRaceKey {
		t.Fatalf("[%s] kv restore Conflicts = %+v, want %s among them", proto.name, result.Conflicts, raceKey)
	}
	// Every OTHER key in the scope had no external writer racing it, so the
	// restore must have converged them all.
	if result.Applied != n-len(result.Conflicts) {
		t.Fatalf("[%s] kv restore Applied = %d, Conflicted = %d, want Applied == %d - Conflicted", proto.name, result.Applied, result.Conflicted, n)
	}

	// The conflicted key must have been left untouched by the restore --
	// i.e. it must NOT hold the restored (target) value.
	got, err := runCLIProto(t, node, proto, "kv", "get", raceKey)
	wantRestored := fmt.Sprintf("v1-%d", n-1)
	if err != nil {
		t.Fatalf("[%s] kv get %s after conflicted restore: %v\noutput: %s", proto.name, raceKey, err, got)
	}
	if strings.TrimSpace(got) == wantRestored {
		t.Fatalf("[%s] kv get %s after conflicted restore = %q, want it left unchanged from the restored value (a conflict means the write was skipped)",
			proto.name, raceKey, got)
	}

	// Retry with --force: it recomputes a fresh plan against whatever
	// raceKey holds now and applies unconditionally, converging every key
	// -- including the previously conflicted one -- to the target revision,
	// and must exit 0.
	forceResult, forceOut, forceErr := kvRestoreJSON(t, node, proto,
		"--revision", strconv.FormatInt(targetRevision, 10), "--prefix", racePrefix, "--force")
	if forceErr != nil {
		t.Fatalf("[%s] kv restore --force after conflict: %v\noutput: %s", proto.name, forceErr, forceOut)
	}
	if forceResult.Conflicted != 0 {
		t.Fatalf("[%s] kv restore --force Conflicted = %d, want 0 (output: %s)", proto.name, forceResult.Conflicted, forceOut)
	}
	got, err = runCLIProto(t, node, proto, "kv", "get", raceKey)
	if err != nil || strings.TrimSpace(got) != wantRestored {
		t.Fatalf("[%s] kv get %s after --force restore = %q, err=%v, want %q", proto.name, raceKey, got, err, wantRestored)
	}
}

// ---------------------------------------------------------------------------
// Cross-tenant isolation
// ---------------------------------------------------------------------------

// kvRestoreTenantPermissions is dataPlanePermissions (tenant_isolation_cli_test.go)
// plus the MVCC permissions a tenant identity needs to drive `kv restore`
// and the JSON-revision reads this file uses to compute target revisions:
// mvcc:restore (POST /kv/restore), mvcc:get_revision (`kv get --revision`,
// used by this file's inline ModRevision lookups and kvModRevision), and
// mvcc:current_revision (`kv revision`, used by kvCLIRevision). Note this is
// mvcc:get_revision, NOT mvcc:get_at_revision -- GetRevision (full MVCC
// metadata for the CURRENT value) and GetAtRevision (a historical read at an
// explicit revision) are separate RPCs with separate permissions; this file
// only ever uses the former.
var kvRestoreTenantPermissions = append(append([]string{}, dataPlanePermissions...),
	"mvcc:restore", "mvcc:get_revision", "mvcc:current_revision")

// TestKVRestore_CLI_TenantIsolation_PerProtocol proves selective restore
// respects the same tenant boundary as every other data-plane command
// (tenant_isolation_cli_test.go): restoring a key in one tenant's database
// never reads or writes the identically-named key in another tenant's
// database, and a tenant cannot restore another tenant's database at all --
// against every transport sharing one multiProtoNode.
func TestKVRestore_CLI_TenantIsolation_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testKVRestoreTenantIsolationOverProtocol(t, node, proto)
		})
	}
}

func testKVRestoreTenantIsolationOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA := "restore-iso-a-" + suffix
	tenantB := "restore-iso-b-" + suffix
	dbA := "restoredb-a-" + suffix
	dbB := "restoredb-b-" + suffix
	const key = "shared-key"

	createTenant(t, node, proto, tenantA, "Restore Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "Restore Tenant B", "prefix", false)

	certA, keyA := provisionTenantIdentity(t, node, tenantA, "restore-admin", kvRestoreTenantPermissions)
	certB, keyB := provisionTenantIdentity(t, node, tenantB, "restore-admin", kvRestoreTenantPermissions)

	connA := tenantCLIArgsForProto(node, proto, certA, keyA)
	connB := tenantCLIArgsForProto(node, proto, certB, keyB)

	if out, err := runQRDB(t, node.cli, connA, "database", "create", dbA, "--engine", "kv", "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant A database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbB, "--engine", "kv", "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant B database create: %v\noutput: %s", proto.name, err, out)
	}

	// Seed the SAME key name in both tenants' own databases, each with an
	// "original" value at a captured revision, then mutate both -- the
	// isolation proof below restores tenant A's copy and confirms tenant
	// B's copy of the identically-named key is untouched.
	valueA1 := "tenant-a-original-" + suffix
	valueB1 := "tenant-b-original-" + suffix

	if out, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "put", key, valueA1); err != nil {
		t.Fatalf("[%s] tenant A seed put: %v\noutput: %s", proto.name, err, out)
	}
	revA1Out, err := runQRDB(t, node.cli, connA, "--database", dbA, "--output", "json", "kv", "get", key, "--revision")
	if err != nil {
		t.Fatalf("[%s] tenant A kv get --revision: %v\noutput: %s", proto.name, err, revA1Out)
	}
	var revA1KV kvGetRevisionDisplay
	if jerr := json.Unmarshal([]byte(revA1Out), &revA1KV); jerr != nil {
		t.Fatalf("[%s] decode tenant A kv get --revision JSON: %v\noutput: %s", proto.name, jerr, revA1Out)
	}
	if out, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "put", key, "tenant-a-mutated-"+suffix); err != nil {
		t.Fatalf("[%s] tenant A mutate put: %v\noutput: %s", proto.name, err, out)
	}

	if out, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "put", key, valueB1); err != nil {
		t.Fatalf("[%s] tenant B seed put: %v\noutput: %s", proto.name, err, out)
	}
	revB1Out, err := runQRDB(t, node.cli, connB, "--database", dbB, "--output", "json", "kv", "get", key, "--revision")
	if err != nil {
		t.Fatalf("[%s] tenant B kv get --revision: %v\noutput: %s", proto.name, err, revB1Out)
	}
	var revB1KV kvGetRevisionDisplay
	if jerr := json.Unmarshal([]byte(revB1Out), &revB1KV); jerr != nil {
		t.Fatalf("[%s] decode tenant B kv get --revision JSON: %v\noutput: %s", proto.name, jerr, revB1Out)
	}
	if out, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "put", key, "tenant-b-mutated-"+suffix); err != nil {
		t.Fatalf("[%s] tenant B mutate put: %v\noutput: %s", proto.name, err, out)
	}

	// PROOF (1): tenant A restores its OWN database's copy of the key.
	// Tenant B's identically named key, in tenant B's OWN database, is
	// completely unaffected.
	if out, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "restore",
		"--revision", strconv.FormatInt(revA1KV.ModRevision, 10), "--keys", key); err != nil {
		t.Fatalf("[%s] tenant A kv restore (own database): %v\noutput: %s", proto.name, err, out)
	}
	outA, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "get", key)
	if err != nil || strings.TrimSpace(outA) != valueA1 {
		t.Fatalf("[%s] tenant A kv get %s after restore = %q (err=%v), want %q", proto.name, key, outA, err, valueA1)
	}
	outB, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "get", key)
	if err != nil || strings.TrimSpace(outB) != "tenant-b-mutated-"+suffix {
		t.Fatalf("[%s] tenant B kv get %s after tenant A's restore = %q (err=%v), want unaffected %q",
			proto.name, key, outB, err, "tenant-b-mutated-"+suffix)
	}

	// PROOF (2): tenant B cannot restore tenant A's database AT ALL -- the
	// attempt fails closed (database ownership is checked before any KV
	// dispatch, same as every other cross-tenant data-plane proof in
	// tenant_isolation_cli_test.go), and tenant A's now-restored value is
	// completely unaffected by the rejected attempt.
	if out, err := runQRDB(t, node.cli, connB, "--database", dbA, "kv", "restore",
		"--revision", strconv.FormatInt(revA1KV.ModRevision, 10), "--keys", key); err == nil {
		t.Fatalf("[%s] tenant B restoring tenant A's database %q should be rejected, got: %s", proto.name, dbA, out)
	}
	outA, err = runQRDB(t, node.cli, connA, "--database", dbA, "kv", "get", key)
	if err != nil || strings.TrimSpace(outA) != valueA1 {
		t.Fatalf("[%s] tenant A kv get %s after tenant B's rejected cross-tenant restore = %q (err=%v), want unchanged %q",
			proto.name, key, outA, err, valueA1)
	}

	// PROOF (3): tenant B's own restore, scoped to its own database, still
	// works -- this is a positive control proving PROOF (2)'s rejection is
	// about tenant ownership, not restore being broken for tenant B.
	if out, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "restore",
		"--revision", strconv.FormatInt(revB1KV.ModRevision, 10), "--keys", key); err != nil {
		t.Fatalf("[%s] tenant B kv restore (own database): %v\noutput: %s", proto.name, err, out)
	}
	outB, err = runQRDB(t, node.cli, connB, "--database", dbB, "kv", "get", key)
	if err != nil || strings.TrimSpace(outB) != valueB1 {
		t.Fatalf("[%s] tenant B kv get %s after own restore = %q (err=%v), want %q", proto.name, key, outB, err, valueB1)
	}
}
