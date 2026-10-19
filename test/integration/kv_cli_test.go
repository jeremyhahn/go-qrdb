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

// kv_cli_test.go drives the entire `qrdb kv` command tree (put, get, del,
// list, exists, range, compact, revision) through the real client CLI
// binary (cmd/qrdb), against every transport (REST, gRPC, QUIC, MCP, Unix)
// using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). This closes the two biggest KV gaps
// identified by the CLI coverage audit: the whole tree was previously
// exercised only via raw REST helpers (never the CLI binary) or only on the
// default transport, and `kv exists` had zero coverage of any kind.
//
// Every subtest below the top-level per-protocol loop operates on keys
// namespaced by protocol name (kvcli/<proto>/...) so the five protocol
// subtests -- which share one node and therefore one keyspace/revision
// counter -- never contend over the same key.
//
// Flag surface note: the CLI's actual `kv list`/`kv range` flags differ from
// the raw REST helpers used elsewhere in this package. `kv list` exposes
// only --consistency (no --limit/--keys-only -- those exist on the REST
// wire and on `kv range`, but transport.KVClient.List's signature carries no
// limit parameter, so the CLI cannot expose one without an SDK client
// interface change). This file exercises the flags that actually exist.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestKV_CLI_PerProtocol drives the full `qrdb kv` lifecycle -- put, get (all
// its flag permutations), exists, list, range (all its flag permutations),
// del, compact, and revision -- through the CLI binary against every
// transport sharing one multiProtoNode.
func TestKV_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testKVLifecycleOverProtocol(t, node, proto)
		})
	}
}

// kvGetRevisionDisplay mirrors cmd/qrdb/kv.go's mvccKeyValueDisplay JSON
// shape, decoded from `kv get --revision --output json`.
type kvGetRevisionDisplay struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// kvRangeDisplay mirrors cmd/qrdb/kv.go's rangeResponseDisplay JSON shape,
// decoded from `kv range --output json`.
type kvRangeDisplay struct {
	Kvs   []kvGetRevisionDisplay `json:"kvs"`
	Count int64                  `json:"count"`
}

// kvCLIRevision runs `kv revision --output json` and returns the current
// MVCC revision.
func kvCLIRevision(t *testing.T, node *multiProtoNode, proto multiProtocol) int64 {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "kv", "revision")
	if err != nil {
		t.Fatalf("[%s] kv revision: %v\noutput: %s", proto.name, err, out)
	}
	var result struct {
		Revision int64 `json:"revision"`
	}
	if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
		t.Fatalf("[%s] decode `kv revision` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	return result.Revision
}

// testKVLifecycleOverProtocol exercises the full kv command tree against a
// single protocol.
func testKVLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	prefix := fmt.Sprintf("kvcli/%s/", proto.name)
	keyA := prefix + "alpha"
	keyB := prefix + "bravo"
	missing := prefix + "does-not-exist"

	revBefore := kvCLIRevision(t, node, proto)

	// --- put -----------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "kv", "put", keyA, "one")
	if err != nil {
		t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, keyA, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] kv put %s: expected OK, got: %s", proto.name, keyA, out)
	}

	out, err = runCLIProto(t, node, proto, "kv", "put", keyB, "two", "--write-mode", "sync")
	if err != nil {
		t.Fatalf("[%s] kv put %s --write-mode sync: %v\noutput: %s", proto.name, keyB, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] kv put %s --write-mode sync: expected OK, got: %s", proto.name, keyB, out)
	}

	// An invalid --write-mode must fail closed with a client-side validation
	// error rather than being silently ignored.
	out, err = runCLIProto(t, node, proto, "kv", "put", keyA, "bogus-write", "--write-mode", "not-a-mode")
	if err == nil {
		t.Fatalf("[%s] kv put --write-mode not-a-mode succeeded, want error (output: %s)", proto.name, out)
	}

	// --- get -------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA)
	if err != nil {
		t.Fatalf("[%s] kv get %s: %v\noutput: %s", proto.name, keyA, err, out)
	}
	if strings.TrimSpace(out) != "one" {
		t.Fatalf("[%s] kv get %s = %q, want %q", proto.name, keyA, strings.TrimSpace(out), "one")
	}

	for _, level := range []string{"strong", "linearizable", "eventual"} {
		out, err = runCLIProto(t, node, proto, "kv", "get", keyA, "--consistency", level)
		if err != nil {
			t.Fatalf("[%s] kv get %s --consistency %s: %v\noutput: %s", proto.name, keyA, level, err, out)
		}
		if strings.TrimSpace(out) != "one" {
			t.Fatalf("[%s] kv get %s --consistency %s = %q, want %q", proto.name, keyA, level, strings.TrimSpace(out), "one")
		}
	}

	// An invalid --consistency value must fail closed.
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA, "--consistency", "not-a-level")
	if err == nil {
		t.Fatalf("[%s] kv get --consistency not-a-level succeeded, want error (output: %s)", proto.name, out)
	}

	// get --revision (full MVCC metadata) and get --at-revision (historical
	// read at that exact revision) must agree.
	out, err = runCLIProto(t, node, proto, "--output", "json", "kv", "get", keyA, "--revision")
	if err != nil {
		t.Fatalf("[%s] kv get %s --revision: %v\noutput: %s", proto.name, keyA, err, out)
	}
	var withRev kvGetRevisionDisplay
	if jerr := json.Unmarshal([]byte(out), &withRev); jerr != nil {
		t.Fatalf("[%s] decode `kv get --revision` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if withRev.Value != "one" {
		t.Fatalf("[%s] kv get %s --revision value = %q, want %q", proto.name, keyA, withRev.Value, "one")
	}
	if withRev.CreateRevision == 0 {
		t.Fatalf("[%s] kv get %s --revision: CreateRevision = 0, want nonzero", proto.name, keyA)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "kv", "get", keyA,
		"--at-revision", strconv.FormatInt(withRev.ModRevision, 10))
	if err != nil {
		t.Fatalf("[%s] kv get %s --at-revision %d: %v\noutput: %s", proto.name, keyA, withRev.ModRevision, err, out)
	}
	var atRev kvGetRevisionDisplay
	if jerr := json.Unmarshal([]byte(out), &atRev); jerr != nil {
		t.Fatalf("[%s] decode `kv get --at-revision` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if atRev.Value != "one" {
		t.Fatalf("[%s] kv get %s --at-revision %d value = %q, want %q", proto.name, keyA, withRev.ModRevision, atRev.Value, "one")
	}

	// get of a missing key must fail (not silently return empty).
	out, err = runCLIProto(t, node, proto, "kv", "get", missing)
	if err == nil {
		t.Fatalf("[%s] kv get %s succeeded, want KeyNotFoundError (output: %s)", proto.name, missing, out)
	}

	// --- binary value round-trip ------------------------------------
	//
	// KV values are raw bytes end to end. The REST transport in particular
	// must encode Key/Value as base64 (not a plain JSON string, which would
	// silently replace invalid UTF-8 sequences with U+FFFD and corrupt the
	// value). Exercise this through the real client CLI process -- argv and
	// stdout/stderr are raw byte streams on Linux, so a non-UTF-8 value
	// survives unmangled through exec.Command as long as it carries no NUL
	// byte (C argv strings are NUL-terminated).
	//
	// MCP is excluded from the byte-exact assertion: its tool-call transport
	// conveys arguments as JSON text per the MCP protocol spec, so an
	// arbitrary non-UTF-8 byte string is not guaranteed to survive the
	// round trip. That is a pre-existing, protocol-inherent property, not
	// the REST string/[]byte defect this block targets -- `kv put` is still
	// exercised over MCP so a hard transport error would still fail the
	// test.
	binKey := prefix + "binary"
	binValue := string([]byte{0xff, 0xfe, 0x80, 0x81, 'x'})
	out, err = runCLIProto(t, node, proto, "kv", "put", binKey, binValue)
	if err != nil {
		t.Fatalf("[%s] kv put %s (binary value): %v\noutput: %s", proto.name, binKey, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] kv put %s (binary value): expected OK, got: %s", proto.name, binKey, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", binKey)
	if err != nil {
		t.Fatalf("[%s] kv get %s (binary value): %v\noutput: %s", proto.name, binKey, err, out)
	}
	if proto.name != "mcp" {
		// printValue appends exactly one trailing newline; strip it to
		// recover the raw stored bytes for an exact comparison.
		gotBin := strings.TrimSuffix(out, "\n")
		if gotBin != binValue {
			t.Fatalf("[%s] kv get %s (binary value) = %q, want %q (byte-exact round-trip)", proto.name, binKey, gotBin, binValue)
		}
	}
	// Remove the binary-value key so it doesn't perturb the prefix-scoped
	// list/range/count assertions below, which expect exactly keyA and keyB.
	out, err = runCLIProto(t, node, proto, "kv", "del", binKey)
	if err != nil {
		t.Fatalf("[%s] kv del %s (binary value cleanup): %v\noutput: %s", proto.name, binKey, err, out)
	}

	// --- exists ------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "kv", "exists", keyA)
	if err != nil {
		t.Fatalf("[%s] kv exists %s: %v\noutput: %s", proto.name, keyA, err, out)
	}
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("[%s] kv exists %s = %q, want %q", proto.name, keyA, strings.TrimSpace(out), "true")
	}

	out, err = runCLIProto(t, node, proto, "kv", "exists", missing)
	if err != nil {
		t.Fatalf("[%s] kv exists %s: %v\noutput: %s", proto.name, missing, err, out)
	}
	if strings.TrimSpace(out) != "false" {
		t.Fatalf("[%s] kv exists %s = %q, want %q", proto.name, missing, strings.TrimSpace(out), "false")
	}

	// --- list ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "kv", "list", prefix)
	if err != nil {
		t.Fatalf("[%s] kv list %s: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, keyA) || !strings.Contains(out, keyB) {
		t.Fatalf("[%s] kv list %s = %q, want it to contain %s and %s", proto.name, prefix, out, keyA, keyB)
	}

	out, err = runCLIProto(t, node, proto, "kv", "list", prefix, "--consistency", "eventual")
	if err != nil {
		t.Fatalf("[%s] kv list %s --consistency eventual: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, keyA) {
		t.Fatalf("[%s] kv list %s --consistency eventual = %q, want it to contain %s", proto.name, prefix, out, keyA)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "kv", "list", prefix)
	if err != nil {
		t.Fatalf("[%s] kv list %s --output json: %v\noutput: %s", proto.name, prefix, err, out)
	}
	var listed []string
	if jerr := json.Unmarshal([]byte(out), &listed); jerr != nil {
		t.Fatalf("[%s] decode `kv list --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if !containsStr(listed, keyA) || !containsStr(listed, keyB) {
		t.Fatalf("[%s] kv list %s --output json = %v, want it to contain %s and %s", proto.name, prefix, listed, keyA, keyB)
	}

	// --- range -----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "kv", "range", prefix)
	if err != nil {
		t.Fatalf("[%s] kv range %s: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, keyA) || !strings.Contains(out, keyB) {
		t.Fatalf("[%s] kv range %s = %q, want it to contain %s and %s", proto.name, prefix, out, keyA, keyB)
	}

	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--limit", "1")
	if err != nil {
		t.Fatalf("[%s] kv range %s --limit 1: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, keyA) || strings.Contains(out, keyB) {
		t.Fatalf("[%s] kv range %s --limit 1 = %q, want only %s (sorted first)", proto.name, prefix, out, keyA)
	}

	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--count-only")
	if err != nil {
		t.Fatalf("[%s] kv range %s --count-only: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, "Count: 2") {
		t.Fatalf("[%s] kv range %s --count-only = %q, want it to contain %q", proto.name, prefix, out, "Count: 2")
	}

	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--keys-only")
	if err != nil {
		t.Fatalf("[%s] kv range %s --keys-only: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, keyA) || strings.Contains(out, "one") {
		t.Fatalf("[%s] kv range %s --keys-only = %q, want keys but not values", proto.name, prefix, out)
	}

	// --sort-order/--sort-by must actually reorder the results, not just be
	// accepted as no-ops: descending by key must put "bravo" before "alpha".
	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--sort-order", "desc", "--sort-by", "key")
	if err != nil {
		t.Fatalf("[%s] kv range %s --sort-order desc --sort-by key: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if !strings.Contains(out, keyA) || !strings.Contains(out, keyB) {
		t.Fatalf("[%s] kv range %s --sort-order desc --sort-by key = %q, want it to still contain both keys", proto.name, prefix, out)
	}
	if idxA, idxB := strings.Index(out, keyA), strings.Index(out, keyB); idxA < 0 || idxB < 0 || idxB > idxA {
		t.Fatalf("[%s] kv range %s --sort-order desc --sort-by key = %q, want %s (bravo) before %s (alpha)", proto.name, prefix, out, keyB, keyA)
	}

	// Ascending is the mirror image: "alpha" before "bravo".
	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--sort-order", "asc", "--sort-by", "key")
	if err != nil {
		t.Fatalf("[%s] kv range %s --sort-order asc --sort-by key: %v\noutput: %s", proto.name, prefix, err, out)
	}
	if idxA, idxB := strings.Index(out, keyA), strings.Index(out, keyB); idxA < 0 || idxB < 0 || idxA > idxB {
		t.Fatalf("[%s] kv range %s --sort-order asc --sort-by key = %q, want %s (alpha) before %s (bravo)", proto.name, prefix, out, keyA, keyB)
	}

	// An invalid --sort-order/--sort-by must fail closed.
	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--sort-order", "sideways")
	if err == nil {
		t.Fatalf("[%s] kv range --sort-order sideways succeeded, want error (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "range", prefix, "--sort-by", "nonsense")
	if err == nil {
		t.Fatalf("[%s] kv range --sort-by nonsense succeeded, want error (output: %s)", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "kv", "range", prefix)
	if err != nil {
		t.Fatalf("[%s] kv range %s --output json: %v\noutput: %s", proto.name, prefix, err, out)
	}
	var rangeOut kvRangeDisplay
	if jerr := json.Unmarshal([]byte(out), &rangeOut); jerr != nil {
		t.Fatalf("[%s] decode `kv range --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(rangeOut.Kvs) != 2 {
		t.Fatalf("[%s] kv range %s --output json kvs = %+v, want 2 entries", proto.name, prefix, rangeOut.Kvs)
	}
	// rangeOut.Count must be the real match count (2), not the store's
	// current MVCC revision -- the two are captured separately below (revNow
	// is well past 2 by the time `kv range` without --count-only is issued
	// here) so this assertion cannot pass by numeric coincidence.
	if rangeOut.Count != 2 {
		t.Fatalf("[%s] kv range %s --output json count = %d, want 2 (match count, not revision)", proto.name, prefix, rangeOut.Count)
	}

	// --sort-by selects the field to sort by, independent of key order.
	// Values are chosen to invert key order (sortValA sorts after sortValB
	// by key, but its value sorts before sortValB's), so a target-sorted
	// result can only match if the store actually sorted by value rather
	// than falling back to key order.
	sortValPrefix := prefix + "sortval-"
	sortValA := sortValPrefix + "1"
	sortValB := sortValPrefix + "2"
	if out, err = runCLIProto(t, node, proto, "kv", "put", sortValA, "zzz"); err != nil {
		t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, sortValA, err, out)
	}
	if out, err = runCLIProto(t, node, proto, "kv", "put", sortValB, "aaa"); err != nil {
		t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, sortValB, err, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "range", sortValPrefix, "--sort-by", "value")
	if err != nil {
		t.Fatalf("[%s] kv range %s --sort-by value: %v\noutput: %s", proto.name, sortValPrefix, err, out)
	}
	// Ascending is the implicit default when --sort-order is omitted but
	// --sort-by is set: "aaa" (sortValB) before "zzz" (sortValA).
	if idxA, idxB := strings.Index(out, sortValA), strings.Index(out, sortValB); idxA < 0 || idxB < 0 || idxB > idxA {
		t.Fatalf("[%s] kv range %s --sort-by value = %q, want %s (aaa) before %s (zzz)", proto.name, sortValPrefix, out, sortValB, sortValA)
	}
	out, err = runCLIProto(t, node, proto, "kv", "range", sortValPrefix, "--sort-order", "desc", "--sort-by", "value")
	if err != nil {
		t.Fatalf("[%s] kv range %s --sort-order desc --sort-by value: %v\noutput: %s", proto.name, sortValPrefix, err, out)
	}
	if idxA, idxB := strings.Index(out, sortValA), strings.Index(out, sortValB); idxA < 0 || idxB < 0 || idxA > idxB {
		t.Fatalf("[%s] kv range %s --sort-order desc --sort-by value = %q, want %s (zzz) before %s (aaa)", proto.name, sortValPrefix, out, sortValA, sortValB)
	}

	out, err = runCLIProto(t, node, proto, "kv", "range", keyA, keyB)
	if err != nil {
		t.Fatalf("[%s] kv range %s %s (explicit end): %v\noutput: %s", proto.name, keyA, keyB, err, out)
	}
	if !strings.Contains(out, keyA) || strings.Contains(out, keyB) {
		t.Fatalf("[%s] kv range %s %s (half-open [start,end)) = %q, want %s but not %s", proto.name, keyA, keyB, out, keyA, keyB)
	}

	// --- del -------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "kv", "del", keyB)
	if err != nil {
		t.Fatalf("[%s] kv del %s: %v\noutput: %s", proto.name, keyB, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] kv del %s: expected OK, got: %s", proto.name, keyB, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "exists", keyB)
	if err != nil {
		t.Fatalf("[%s] kv exists %s after del: %v\noutput: %s", proto.name, keyB, err, out)
	}
	if strings.TrimSpace(out) != "false" {
		t.Fatalf("[%s] kv exists %s after del = %q, want %q", proto.name, keyB, strings.TrimSpace(out), "false")
	}

	out, err = runCLIProto(t, node, proto, "kv", "del", keyA, "--write-mode", "wal_durable")
	if err != nil {
		t.Fatalf("[%s] kv del %s --write-mode wal_durable: %v\noutput: %s", proto.name, keyA, err, out)
	}

	// --- compact -----------------------------------------------------
	revNow := kvCLIRevision(t, node, proto)
	out, err = runCLIProto(t, node, proto, "kv", "compact", "--revision", strconv.FormatInt(revNow, 10))
	if err != nil {
		t.Fatalf("[%s] kv compact --revision %d: %v\noutput: %s", proto.name, revNow, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] kv compact --revision %d: expected OK, got: %s", proto.name, revNow, out)
	}

	// `kv compact` with no --revision is a required flag; cobra must reject
	// the invocation before it ever reaches the server.
	out, err = runCLIProto(t, node, proto, "kv", "compact")
	if err == nil {
		t.Fatalf("[%s] kv compact with no --revision succeeded, want error (output: %s)", proto.name, out)
	}

	// A revision that has already been compacted away must fail closed
	// rather than silently returning stale or wrong data.
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA, "--at-revision", strconv.FormatInt(withRev.CreateRevision, 10))
	if err == nil {
		t.Fatalf("[%s] kv get %s --at-revision %d (pre-compaction revision) succeeded, want RevisionCompactedError (output: %s)",
			proto.name, keyA, withRev.CreateRevision, out)
	}

	// --- revision --------------------------------------------------------
	revAfter := kvCLIRevision(t, node, proto)
	if revAfter <= revBefore {
		t.Fatalf("[%s] kv revision after writes = %d, want > pre-write revision %d", proto.name, revAfter, revBefore)
	}
}

// containsStr reports whether items contains s.
func containsStr(items []string, s string) bool {
	for _, item := range items {
		if item == s {
			return true
		}
	}
	return false
}
