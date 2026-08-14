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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestIndex_CLI_PerProtocol drives the full `qrdb index` lifecycle --
// create (plain, unique, sparse), query, scan, list, drop, and CLI-side
// validation errors -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestIndex_CLI_PerProtocol(t *testing.T) {
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

	out, err = runCLIProto(t, node, proto, "--output", "json", "index", "scan", idxPriority, "--start", "a", "--end", "d")
	if err != nil {
		t.Fatalf("[%s] index scan %s --start a --end d: %v\noutput: %s", proto.name, idxPriority, err, out)
	}
	var scanResp transport.IndexScanResponse
	if jerr := json.Unmarshal([]byte(out), &scanResp); jerr != nil {
		t.Fatalf("[%s] decode `index scan` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	scannedKeys := map[string]bool{}
	for _, entry := range scanResp.Entries {
		for _, k := range entry.Keys {
			scannedKeys[k] = true
		}
	}
	for _, want := range []string{kprefix + "p1", kprefix + "p2", kprefix + "p3"} {
		if !scannedKeys[want] {
			t.Fatalf("[%s] index scan %s --start a --end d entries = %+v, want it to contain %s",
				proto.name, idxPriority, scanResp.Entries, want)
		}
	}

	// scan with no --start/--end: cobra must reject the required flags.
	out, err = runCLIProto(t, node, proto, "index", "scan", idxPriority)
	if err == nil {
		t.Fatalf("[%s] index scan with no --start/--end succeeded, want error (output: %s)", proto.name, out)
	}

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
