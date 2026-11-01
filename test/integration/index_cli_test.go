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

// index_cli_test.go drives the entire `qrdb index` command tree (create,
// list, query, scan, drop) through the real client CLI binary
// (cmd/qrdb/index.go), against every transport (REST, gRPC, QUIC, MCP,
// Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). `index create/drop/query` were
// previously exercised only via raw REST helpers (never the CLI binary);
// `index scan` and `index list` had zero coverage of any kind. Secondary
// indexes are populated automatically whenever a `kv put` value is a JSON
// object containing the indexed field (see kv_test.go's
// TestIndex_SingleNode_* for the REST-level precedent this file mirrors
// through the CLI).
package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestIndex_CLI_PerProtocol drives the full `qrdb index` lifecycle --
// create (plain, unique, sparse), query, scan, list, drop, and CLI-side
// validation errors -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestIndex_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testIndexLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testIndexLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	kprefix := "indexcli/" + proto.name + "/"
	idxStatus := "idx-status-" + proto.name
	idxPriority := "idx-priority-" + proto.name
	idxUnique := "idx-unique-" + proto.name
	idxSparse := "idx-sparse-" + proto.name

	// Field names, like index names and keys, are namespaced per protocol.
	// All 5 protocol subtests share one node and therefore one KV
	// keyspace/index registry; index creation backfills from a full-
	// keyspace scan (pkg/engine/mvcc_engine.go's BackfillIndex ranges
	// [0x00,0xFF)) and every subsequent `kv put` populates EVERY
	// registered index whose field is present in the value, regardless of
	// which protocol's subtest created that index or wrote that key. Two
	// indexes with different names but the SAME field name are therefore
	// NOT independent -- most sharply for a unique index, where a value
	// already claimed under one same-named-field index correctly (by
	// design) blocks a write meant for a different index instance.
	// Namespacing every field name avoids this cross-subtest coupling.
	fieldStatus := "status-" + proto.name
	fieldPriority := "priority-" + proto.name
	fieldCode := "code-" + proto.name
	fieldOptional := "optional-" + proto.name

	// --- create (plain) --------------------------------------------------
	out, err := runCLIProto(t, node, proto, "index", "create", idxStatus, "--field", fieldStatus)
	if err != nil {
		t.Fatalf("[%s] index create %s --field %s: %v\noutput: %s", proto.name, idxStatus, fieldStatus, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] index create %s: expected OK, got: %s", proto.name, idxStatus, out)
	}

	// create with no --field: cobra must reject the required flag.
	out, err = runCLIProto(t, node, proto, "index", "create", kprefix+"noflag")
	if err == nil {
		t.Fatalf("[%s] index create with no --field succeeded, want error (output: %s)", proto.name, out)
	}

	// Populate the index by writing JSON KV values with the indexed field.
	docs := map[string]string{
		kprefix + "doc1": `{"` + fieldStatus + `":"active","name":"doc1"}`,
		kprefix + "doc2": `{"` + fieldStatus + `":"active","name":"doc2"}`,
		kprefix + "doc3": `{"` + fieldStatus + `":"inactive","name":"doc3"}`,
	}
	for k, v := range docs {
		out, err = runCLIProto(t, node, proto, "kv", "put", k, v)
		if err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, k, err, out)
		}
	}

	// --- query -------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "index", "query", idxStatus, "--value", "active")
	if err != nil {
		t.Fatalf("[%s] index query %s --value active: %v\noutput: %s", proto.name, idxStatus, err, out)
	}
	var queryResp transport.IndexQueryResponse
	if jerr := json.Unmarshal([]byte(out), &queryResp); jerr != nil {
		t.Fatalf("[%s] decode `index query` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !containsStr(queryResp.Keys, kprefix+"doc1") || !containsStr(queryResp.Keys, kprefix+"doc2") {
		t.Fatalf("[%s] index query %s --value active = %v, want it to contain %sdoc1 and %sdoc2",
			proto.name, idxStatus, queryResp.Keys, kprefix, kprefix)
	}
	if containsStr(queryResp.Keys, kprefix+"doc3") {
		t.Fatalf("[%s] index query %s --value active = %v, want it NOT to contain %sdoc3 (status=inactive)",
			proto.name, idxStatus, queryResp.Keys, kprefix)
	}

	// query with no --value: cobra must reject the required flag.
	out, err = runCLIProto(t, node, proto, "index", "query", idxStatus)
	if err == nil {
		t.Fatalf("[%s] index query with no --value succeeded, want error (output: %s)", proto.name, out)
	}

	// query against a nonexistent index must fail closed.
	out, err = runCLIProto(t, node, proto, "index", "query", "no-such-index-"+proto.name, "--value", "x")
	if err == nil {
		t.Fatalf("[%s] index query against nonexistent index succeeded, want error (output: %s)", proto.name, out)
	}
	// The failure is the typed not-found, not a generic server fault.
	if !strings.Contains(out, "transport: index not found: ") {
		t.Fatalf("[%s] index query against nonexistent index: want index-not-found, got: %s", proto.name, out)
	}

	// --- create (priority index) + scan ---------------------------------
	out, err = runCLIProto(t, node, proto, "index", "create", idxPriority, "--field", fieldPriority)
	if err != nil {
		t.Fatalf("[%s] index create %s --field %s: %v\noutput: %s", proto.name, idxPriority, fieldPriority, err, out)
	}
	scanDocs := map[string]string{
		kprefix + "p1": `{"` + fieldPriority + `":"a","item":"one"}`,
		kprefix + "p2": `{"` + fieldPriority + `":"b","item":"two"}`,
		kprefix + "p3": `{"` + fieldPriority + `":"c","item":"three"}`,
	}
	for k, v := range scanDocs {
		out, err = runCLIProto(t, node, proto, "kv", "put", k, v)
		if err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, k, err, out)
		}
	}

	// Each bound is optional: an omitted --start or --end is an OPEN bound,
	// not an empty string (#115), so a one-sided or unbounded scan returns
	// every match on the open side. The priority index holds exactly p1..p3
	// ("a", "b", "c"), which makes every range's membership exact.
	p1, p2, p3 := kprefix+"p1", kprefix+"p2", kprefix+"p3"
	scanCases := []struct {
		bounds []string
		want   []string
		absent []string
	}{
		{bounds: []string{"--start", "a", "--end", "d"}, want: []string{p1, p2, p3}},
		{bounds: nil, want: []string{p1, p2, p3}},
		{bounds: []string{"--start", "b"}, want: []string{p2, p3}, absent: []string{p1}},
		{bounds: []string{"--end", "b"}, want: []string{p1}, absent: []string{p2, p3}},
	}
	for _, sc := range scanCases {
		args := append([]string{"--output", "json", "index", "scan", idxPriority}, sc.bounds...)
		out, err = runCLIProto(t, node, proto, args...)
		if err != nil {
			t.Fatalf("[%s] index scan %s %v: %v\noutput: %s", proto.name, idxPriority, sc.bounds, err, out)
		}
		var scanResp transport.IndexScanResponse
		if jerr := json.Unmarshal([]byte(out), &scanResp); jerr != nil {
			t.Fatalf("[%s] decode `index scan %v` JSON: %v\noutput: %s", proto.name, sc.bounds, jerr, out)
		}
		scannedKeys := map[string]bool{}
		for _, entry := range scanResp.Entries {
			for _, k := range entry.Keys {
				scannedKeys[k] = true
			}
		}
		for _, want := range sc.want {
			if !scannedKeys[want] {
				t.Fatalf("[%s] index scan %s %v entries = %+v, want it to contain %s",
					proto.name, idxPriority, sc.bounds, scanResp.Entries, want)
			}
		}
		for _, absent := range sc.absent {
			if scannedKeys[absent] {
				t.Fatalf("[%s] index scan %s %v entries = %+v, want it to exclude %s",
					proto.name, idxPriority, sc.bounds, scanResp.Entries, absent)
			}
		}
	}

	testNumericIndexScanOverProtocol(t, node, proto, kprefix)

	// --- create (unique index) enforces the uniqueness constraint. -----
	out, err = runCLIProto(t, node, proto, "index", "create", idxUnique, "--field", fieldCode, "--unique")
	if err != nil {
		t.Fatalf("[%s] index create %s --field %s --unique: %v\noutput: %s", proto.name, idxUnique, fieldCode, err, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "put", kprefix+"u1", `{"`+fieldCode+`":"dup"}`)
	if err != nil {
		t.Fatalf("[%s] kv put %su1 (first, unique code): %v\noutput: %s", proto.name, kprefix, err, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "put", kprefix+"u2", `{"`+fieldCode+`":"dup"}`)
	if err == nil {
		t.Fatalf("[%s] kv put %su2 with a duplicate value on unique index %s succeeded, want UniqueViolationError (output: %s)",
			proto.name, kprefix, idxUnique, out)
	}

	// --- create (sparse index) tolerates missing field values. ---------
	out, err = runCLIProto(t, node, proto, "index", "create", idxSparse, "--field", fieldOptional, "--sparse")
	if err != nil {
		t.Fatalf("[%s] index create %s --field %s --sparse: %v\noutput: %s", proto.name, idxSparse, fieldOptional, err, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "put", kprefix+"s1", `{"other":"no optional field here"}`)
	if err != nil {
		t.Fatalf("[%s] kv put %ss1 missing the sparse-indexed field: %v\noutput: %s", proto.name, kprefix, err, out)
	}

	// --- list ------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "index", "list")
	if err != nil {
		t.Fatalf("[%s] index list: %v\noutput: %s", proto.name, err, out)
	}
	var listResp transport.IndexListResponse
	if jerr := json.Unmarshal([]byte(out), &listResp); jerr != nil {
		t.Fatalf("[%s] decode `index list` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	for _, want := range []string{idxStatus, idxPriority, idxUnique, idxSparse} {
		if !containsStr(listResp.Names, want) {
			t.Fatalf("[%s] index list = %v, want it to contain %s", proto.name, listResp.Names, want)
		}
	}

	out, err = runCLIProto(t, node, proto, "index", "list")
	if err != nil {
		t.Fatalf("[%s] index list (table): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, idxStatus) {
		t.Fatalf("[%s] index list (table) = %q, want it to contain %s", proto.name, out, idxStatus)
	}

	// --- drop --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "index", "drop", idxSparse)
	if err != nil {
		t.Fatalf("[%s] index drop %s: %v\noutput: %s", proto.name, idxSparse, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] index drop %s: expected OK, got: %s", proto.name, idxSparse, out)
	}

	// Querying a dropped index must fail closed, not silently return an
	// empty result set.
	out, err = runCLIProto(t, node, proto, "index", "query", idxSparse, "--value", "anything")
	if err == nil {
		t.Fatalf("[%s] index query %s (dropped) succeeded, want error (output: %s)", proto.name, idxSparse, out)
	}
	if want := "transport: index not found: " + idxSparse; !strings.Contains(out, want) {
		t.Fatalf("[%s] index query %s (dropped): want %q, got: %s", proto.name, idxSparse, want, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "index", "list")
	if err != nil {
		t.Fatalf("[%s] index list after drop: %v\noutput: %s", proto.name, err, out)
	}
	var afterDrop transport.IndexListResponse
	if jerr := json.Unmarshal([]byte(out), &afterDrop); jerr != nil {
		t.Fatalf("[%s] decode `index list` JSON after drop: %v\noutput: %s", proto.name, jerr, out)
	}
	if containsStr(afterDrop.Names, idxSparse) {
		t.Fatalf("[%s] index list after drop %s = %v, want it removed", proto.name, idxSparse, afterDrop.Names)
	}

	// Dropping an already-dropped index is a BY-DESIGN idempotent no-op
	// success, not an error: pkg/engine/mvcc_engine.go's handleIndexDrop
	// explicitly documents (and implements) that Raft state machine
	// handlers must be idempotent so a duplicate/cleanup drop proposal
	// does not produce a fatal Apply error that would stall consensus --
	// it detects the missing index name and returns success instead
	// (the same pattern lease_cli_test.go documents for `lease revoke`
	// via pkg/engine/mvcc_engine.go's handleLeaseRevoke). Confirm that
	// documented contract explicitly rather than asserting an error.
	out, err = runCLIProto(t, node, proto, "index", "drop", idxSparse)
	if err != nil {
		t.Fatalf("[%s] index drop %s (already dropped) failed, want idempotent OK per pkg/engine/mvcc_engine.go's "+
			"handleIndexDrop contract: %v\noutput: %s", proto.name, idxSparse, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] index drop %s (already dropped): expected idempotent OK, got: %s", proto.name, idxSparse, out)
	}
}

// testNumericIndexScanOverProtocol covers `index create --numeric` and RANGE
// scans over it. Text order puts "10" before "2", so a text index scanned
// from 2 drops the record holding 10; a numeric index must return it. The
// values span signs, fractions and an exponent, and every scan's membership
// is asserted exactly, so a partial answer fails rather than passing as one.
func testNumericIndexScanOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol, kprefix string) {
	t.Helper()

	idxAge := "idx-age-" + proto.name
	fieldAge := "age-" + proto.name

	out, err := runCLIProto(t, node, proto, "index", "create", idxAge, "--field", fieldAge, "--numeric")
	if err != nil {
		t.Fatalf("[%s] index create %s --field %s --numeric: %v\noutput: %s", proto.name, idxAge, fieldAge, err, out)
	}
	ages := map[string]string{
		kprefix + "age-neg":   "-5",
		kprefix + "age-two":   "2",
		kprefix + "age-frac":  "2.5",
		kprefix + "age-ten":   "10",
		kprefix + "age-huge":  "1e21",
		kprefix + "age-text":  `"ten"`,
		kprefix + "age-zero":  "-0",
		kprefix + "age-whole": "100",
	}
	for k, v := range ages {
		out, err = runCLIProto(t, node, proto, "kv", "put", k, `{"`+fieldAge+`":`+v+`}`)
		if err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, k, err, out)
		}
	}

	key := func(name string) string { return kprefix + "age-" + name }
	for _, sc := range []struct {
		bounds []string
		want   []string
	}{
		// The acceptance case: >= 2 holds both 2 and 10.
		{[]string{"--start", "2"}, []string{key("two"), key("frac"), key("ten"), key("whole"), key("huge")}},
		{[]string{"--start", "2", "--end", "10"}, []string{key("two"), key("frac")}},
		{[]string{"--end", "0"}, []string{key("neg")}},
		{[]string{"--start", "-0", "--end", "2.5"}, []string{key("zero"), key("two")}},
		{[]string{"--start", "1e20"}, []string{key("huge")}},
		// A non-number holds no place in the order and is in no range.
		{nil, []string{key("neg"), key("zero"), key("two"), key("frac"), key("ten"), key("whole"), key("huge")}},
	} {
		args := append([]string{"--output", "json", "index", "scan", idxAge}, sc.bounds...)
		out, err = runCLIProto(t, node, proto, args...)
		if err != nil {
			t.Fatalf("[%s] index scan %s %v: %v\noutput: %s", proto.name, idxAge, sc.bounds, err, out)
		}
		var scanResp transport.IndexScanResponse
		if jerr := json.Unmarshal([]byte(out), &scanResp); jerr != nil {
			t.Fatalf("[%s] decode `index scan %v` JSON: %v\noutput: %s", proto.name, sc.bounds, jerr, out)
		}
		var got []string
		for _, entry := range scanResp.Entries {
			got = append(got, entry.Keys...)
		}
		if len(got) == 0 {
			t.Fatalf("[%s] index scan %s %v returned no keys; want %v", proto.name, idxAge, sc.bounds, sc.want)
		}
		if !sameStrings(got, sc.want) {
			t.Fatalf("[%s] index scan %s %v keys = %v; want exactly %v", proto.name, idxAge, sc.bounds, got, sc.want)
		}
	}

	// Equality finds a number by value, whatever text it was written as.
	out, err = runCLIProto(t, node, proto, "--output", "json", "index", "query", idxAge, "--value", "0")
	if err != nil {
		t.Fatalf("[%s] index query %s --value 0: %v\noutput: %s", proto.name, idxAge, err, out)
	}
	var queryResp transport.IndexQueryResponse
	if jerr := json.Unmarshal([]byte(out), &queryResp); jerr != nil {
		t.Fatalf("[%s] decode `index query` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !sameStrings(queryResp.Keys, []string{key("zero")}) {
		t.Fatalf("[%s] index query %s --value 0 = %v; want [%s] (-0 is 0)", proto.name, idxAge, queryResp.Keys, key("zero"))
	}

	// NaN has no place in the order, so it is refused as a bound rather
	// than answered as an empty range.
	out, err = runCLIProto(t, node, proto, "index", "scan", idxAge, "--start", "NaN")
	if err == nil {
		t.Fatalf("[%s] index scan %s --start NaN succeeded, want an invalid-bound error (output: %s)", proto.name, idxAge, out)
	}
}

// sameStrings reports whether a and b hold the same strings, ignoring order.
func sameStrings(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// TestIndex_CLI_MaxIndexesRefusesPastTheCap starts qrdbd with
// index.max_indexes 1, fills the one slot with `qrdb index create` over REST,
// and proves every transport then refuses a further create with the
// index-limit error. Dropping the held index frees its slot, so a create
// after the drop succeeds.
func TestIndex_CLI_MaxIndexesRefusesPastTheCap(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNodeWithConfig(t, "mvcc", "index:\n  max_indexes: 1\n")

	holderProto := multiProtocol{name: "rest", scheme: "https"}
	if out, err := runCLIProto(t, node, holderProto, "index", "create", "idxcap-held", "--field", "held"); err != nil {
		t.Fatalf("[rest] index create idxcap-held under max_indexes 1: %v\noutput: %s", err, out)
	}

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			name := "idxcap-refused-" + proto.name
			out, err := runCLIProto(t, node, proto, "index", "create", name, "--field", "refused")
			if err == nil || !strings.Contains(strings.ToLower(out), "index limit reached") {
				t.Fatalf("[%s] index create %s past max_indexes 1: err = %v, output = %q, want a non-zero exit with the index-limit refusal",
					proto.name, name, err, out)
			}
		})
	}

	if out, err := runCLIProto(t, node, holderProto, "index", "drop", "idxcap-held"); err != nil {
		t.Fatalf("[rest] index drop idxcap-held: %v\noutput: %s", err, out)
	}
	if out, err := runCLIProto(t, node, holderProto, "index", "create", "idxcap-next", "--field", "next"); err != nil {
		t.Fatalf("[rest] index create after the held index was dropped: %v\noutput: %s", err, out)
	}
}
