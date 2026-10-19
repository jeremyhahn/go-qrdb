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

// collection_cli_test.go drives the entire `qrdb collection` command tree
// (create, info, list, drop, and the nested `doc` insert/get/update/delete/
// list operations) through the real client CLI binary
// (cmd/qrdb/collection.go), against every transport (REST, gRPC, QUIC,
// MCP, Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). Document collections (pkg/service's
// CollectionService) are built generically on top of the KV service layer
// -- the same primitive every protocol wires through pkg/service -- so no
// special engine or daemon configuration beyond the shared multi-protocol
// harness is required. Prior to this file the `collection` command tree
// had zero integration coverage of any kind.
package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestCollection_CLI_PerProtocol drives the full `qrdb collection` lifecycle
// -- create (default/increment/field_hash ID strategies), info, list, the
// full `doc` CRUD+list+pagination surface, drop, and CLI-side validation
// errors -- through the CLI binary against every transport sharing one
// multiProtoNode.
func TestCollection_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCollectionLifecycleOverProtocol(t, node, proto)
		})
	}
}

func testCollectionLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	colDefault := "col-default-" + proto.name
	colIncrement := "col-increment-" + proto.name
	colFieldHash := "col-fieldhash-" + proto.name

	// --- create (default: unix_nano ID strategy) -------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "collection", "create", colDefault)
	if err != nil {
		t.Fatalf("[%s] collection create %s: %v\noutput: %s", proto.name, colDefault, err, out)
	}
	var created transport.CollectionInfo
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil {
		t.Fatalf("[%s] decode `collection create` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if created.Name != colDefault {
		t.Fatalf("[%s] collection create %s: Name = %q, want %q", proto.name, colDefault, created.Name, colDefault)
	}
	if created.Database != transport.DefaultDatabaseScope {
		t.Fatalf("[%s] collection create %s: Database = %q, want %q", proto.name, colDefault, created.Database, transport.DefaultDatabaseScope)
	}
	if created.IDStrategy != "unix_nano" {
		t.Fatalf("[%s] collection create %s: IDStrategy = %q, want %q", proto.name, colDefault, created.IDStrategy, "unix_nano")
	}

	// creating the same collection twice must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "create", colDefault)
	if err == nil {
		t.Fatalf("[%s] collection create %s (duplicate) succeeded, want error (output: %s)", proto.name, colDefault, out)
	}

	// --- create (increment ID strategy) ---------------------------------
	out, err = runCLIProto(t, node, proto, "collection", "create", colIncrement, "--id-strategy", "increment")
	if err != nil {
		t.Fatalf("[%s] collection create %s --id-strategy increment: %v\noutput: %s", proto.name, colIncrement, err, out)
	}

	// --- create (field_hash ID strategy) ---------------------------------
	out, err = runCLIProto(t, node, proto, "collection", "create", colFieldHash,
		"--id-strategy", "field_hash", "--field-hash", "email")
	if err != nil {
		t.Fatalf("[%s] collection create %s --id-strategy field_hash --field-hash email: %v\noutput: %s",
			proto.name, colFieldHash, err, out)
	}

	// field_hash strategy without --field-hash must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "create", "col-bad-fieldhash-"+proto.name, "--id-strategy", "field_hash")
	if err == nil {
		t.Fatalf("[%s] collection create --id-strategy field_hash with no --field-hash succeeded, want error (output: %s)", proto.name, out)
	}

	// an unrecognized ID strategy must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "create", "col-bad-strategy-"+proto.name, "--id-strategy", "not-a-strategy")
	if err == nil {
		t.Fatalf("[%s] collection create --id-strategy not-a-strategy succeeded, want error (output: %s)", proto.name, out)
	}

	// --- info --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "info", colFieldHash)
	if err != nil {
		t.Fatalf("[%s] collection info %s: %v\noutput: %s", proto.name, colFieldHash, err, out)
	}
	var info transport.CollectionInfo
	if jerr := json.Unmarshal([]byte(out), &info); jerr != nil {
		t.Fatalf("[%s] decode `collection info` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if info.FieldHash != "email" {
		t.Fatalf("[%s] collection info %s: FieldHash = %q, want %q", proto.name, colFieldHash, info.FieldHash, "email")
	}
	if info.DocumentCount != 0 {
		t.Fatalf("[%s] collection info %s: DocumentCount = %d, want 0 (no documents yet)", proto.name, colFieldHash, info.DocumentCount)
	}

	// info on a nonexistent collection must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "info", "no-such-collection-"+proto.name)
	if err == nil {
		t.Fatalf("[%s] collection info on nonexistent collection succeeded, want error (output: %s)", proto.name, out)
	}

	// --- list --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "list")
	if err != nil {
		t.Fatalf("[%s] collection list: %v\noutput: %s", proto.name, err, out)
	}
	var listResp transport.ListCollectionsResponse
	if jerr := json.Unmarshal([]byte(out), &listResp); jerr != nil {
		t.Fatalf("[%s] decode `collection list` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	names := make([]string, len(listResp.Collections))
	for i, c := range listResp.Collections {
		names[i] = c.Name
	}
	for _, want := range []string{colDefault, colIncrement, colFieldHash} {
		if !containsStr(names, want) {
			t.Fatalf("[%s] collection list = %v, want it to contain %s", proto.name, names, want)
		}
	}

	out, err = runCLIProto(t, node, proto, "collection", "list")
	if err != nil {
		t.Fatalf("[%s] collection list (table): %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, colDefault) {
		t.Fatalf("[%s] collection list (table) = %q, want it to contain %s", proto.name, out, colDefault)
	}

	// --- doc insert (increment-strategy collection) ----------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "insert", colIncrement,
		"--data", `{"name":"alice","role":"admin"}`)
	if err != nil {
		t.Fatalf("[%s] collection doc insert %s: %v\noutput: %s", proto.name, colIncrement, err, out)
	}
	var insertResp transport.InsertDocumentResponse
	if jerr := json.Unmarshal([]byte(out), &insertResp); jerr != nil {
		t.Fatalf("[%s] decode `collection doc insert` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if insertResp.ID == 0 {
		t.Fatalf("[%s] collection doc insert %s: ID = 0, want nonzero", proto.name, colIncrement)
	}
	docID := insertResp.ID
	idStr := strconv.FormatUint(docID, 10)

	// insert with malformed JSON must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "doc", "insert", colIncrement, "--data", "{not valid json")
	if err == nil {
		t.Fatalf("[%s] collection doc insert with malformed JSON succeeded, want error (output: %s)", proto.name, out)
	}

	// --- doc insert via --file --------------------------------------
	docPath := writeTempFile(t, "doc.json", []byte(`{"name":"bob","role":"user"}`))
	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "insert", colIncrement, "--file", docPath)
	if err != nil {
		t.Fatalf("[%s] collection doc insert %s --file: %v\noutput: %s", proto.name, colIncrement, err, out)
	}
	var insertResp2 transport.InsertDocumentResponse
	if jerr := json.Unmarshal([]byte(out), &insertResp2); jerr != nil {
		t.Fatalf("[%s] decode `collection doc insert --file` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if insertResp2.ID == docID {
		t.Fatalf("[%s] collection doc insert --file: ID = %d, want distinct from first insert's %d", proto.name, insertResp2.ID, docID)
	}

	// --data and --file are mutually exclusive.
	out, err = runCLIProto(t, node, proto, "collection", "doc", "insert", colIncrement, "--data", "{}", "--file", docPath)
	if err == nil {
		t.Fatalf("[%s] collection doc insert with both --data and --file succeeded, want error (output: %s)", proto.name, out)
	}

	// --- doc get -------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "get", colIncrement, idStr)
	if err != nil {
		t.Fatalf("[%s] collection doc get %s %s: %v\noutput: %s", proto.name, colIncrement, idStr, err, out)
	}
	var got transport.DocumentResponse
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("[%s] decode `collection doc get` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if got.ID != docID {
		t.Fatalf("[%s] collection doc get %s %s: ID = %d, want %d", proto.name, colIncrement, idStr, got.ID, docID)
	}
	if got.Document["name"] != "alice" {
		t.Fatalf("[%s] collection doc get %s %s: Document = %+v, want name=alice", proto.name, colIncrement, idStr, got.Document)
	}

	// get with a non-numeric ID must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "doc", "get", colIncrement, "not-a-number")
	if err == nil {
		t.Fatalf("[%s] collection doc get with non-numeric ID succeeded, want error (output: %s)", proto.name, out)
	}

	// get a nonexistent document ID must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "doc", "get", colIncrement, "999999999")
	if err == nil {
		t.Fatalf("[%s] collection doc get with nonexistent ID succeeded, want error (output: %s)", proto.name, out)
	}

	// --- doc update ----------------------------------------------------
	out, err = runCLIProto(t, node, proto, "collection", "doc", "update", colIncrement, idStr,
		"--data", `{"name":"alice","role":"superadmin"}`)
	if err != nil {
		t.Fatalf("[%s] collection doc update %s %s: %v\noutput: %s", proto.name, colIncrement, idStr, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] collection doc update %s %s: expected OK, got: %s", proto.name, colIncrement, idStr, out)
	}
	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "get", colIncrement, idStr)
	if err != nil {
		t.Fatalf("[%s] collection doc get %s %s (after update): %v\noutput: %s", proto.name, colIncrement, idStr, err, out)
	}
	var afterUpdate transport.DocumentResponse
	if jerr := json.Unmarshal([]byte(out), &afterUpdate); jerr != nil {
		t.Fatalf("[%s] decode post-update `collection doc get` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if afterUpdate.Document["role"] != "superadmin" {
		t.Fatalf("[%s] collection doc get %s %s (after update): Document = %+v, want role=superadmin",
			proto.name, colIncrement, idStr, afterUpdate.Document)
	}

	// update of a nonexistent document ID must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "doc", "update", colIncrement, "999999999", "--data", "{}")
	if err == nil {
		t.Fatalf("[%s] collection doc update with nonexistent ID succeeded, want error (output: %s)", proto.name, out)
	}

	// --- doc list + pagination -----------------------------------------
	for i := 0; i < 3; i++ {
		out, err = runCLIProto(t, node, proto, "collection", "doc", "insert", colIncrement,
			"--data", `{"name":"page-filler"}`)
		if err != nil {
			t.Fatalf("[%s] collection doc insert %s (pagination filler %d): %v\noutput: %s", proto.name, colIncrement, i, err, out)
		}
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "list", colIncrement)
	if err != nil {
		t.Fatalf("[%s] collection doc list %s: %v\noutput: %s", proto.name, colIncrement, err, out)
	}
	var listDocs transport.ListDocumentsResponse
	if jerr := json.Unmarshal([]byte(out), &listDocs); jerr != nil {
		t.Fatalf("[%s] decode `collection doc list` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(listDocs.Documents) < 5 {
		t.Fatalf("[%s] collection doc list %s: got %d documents, want at least 5", proto.name, colIncrement, len(listDocs.Documents))
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "list", colIncrement, "--page-size", "2")
	if err != nil {
		t.Fatalf("[%s] collection doc list %s --page-size 2: %v\noutput: %s", proto.name, colIncrement, err, out)
	}
	var page1 transport.ListDocumentsResponse
	if jerr := json.Unmarshal([]byte(out), &page1); jerr != nil {
		t.Fatalf("[%s] decode `collection doc list --page-size 2` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(page1.Documents) != 2 {
		t.Fatalf("[%s] collection doc list %s --page-size 2: got %d documents, want 2", proto.name, colIncrement, len(page1.Documents))
	}
	if page1.NextPageToken == "" {
		t.Fatalf("[%s] collection doc list %s --page-size 2: NextPageToken empty, want a token (more pages remain)", proto.name, colIncrement)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "doc", "list", colIncrement,
		"--page-size", "2", "--page-token", page1.NextPageToken)
	if err != nil {
		t.Fatalf("[%s] collection doc list %s --page-token: %v\noutput: %s", proto.name, colIncrement, err, out)
	}
	var page2 transport.ListDocumentsResponse
	if jerr := json.Unmarshal([]byte(out), &page2); jerr != nil {
		t.Fatalf("[%s] decode `collection doc list --page-token` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(page2.Documents) == 0 {
		t.Fatalf("[%s] collection doc list %s --page-token: got 0 documents, want at least 1 more page", proto.name, colIncrement)
	}
	if page1.Documents[0].ID == page2.Documents[0].ID {
		t.Fatalf("[%s] collection doc list pagination: page1[0].ID == page2[0].ID == %d, want distinct pages",
			proto.name, page1.Documents[0].ID)
	}

	// --- doc delete ----------------------------------------------------
	out, err = runCLIProto(t, node, proto, "collection", "doc", "delete", colIncrement, idStr)
	if err != nil {
		t.Fatalf("[%s] collection doc delete %s %s: %v\noutput: %s", proto.name, colIncrement, idStr, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] collection doc delete %s %s: expected OK, got: %s", proto.name, colIncrement, idStr, out)
	}
	out, err = runCLIProto(t, node, proto, "collection", "doc", "get", colIncrement, idStr)
	if err == nil {
		t.Fatalf("[%s] collection doc get %s %s (after delete) succeeded, want error (output: %s)", proto.name, colIncrement, idStr, out)
	}

	// deleting an already-deleted document must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "doc", "delete", colIncrement, idStr)
	if err == nil {
		t.Fatalf("[%s] collection doc delete %s %s (already deleted) succeeded, want error (output: %s)", proto.name, colIncrement, idStr, out)
	}

	// --- drop --------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "collection", "drop", colFieldHash)
	if err != nil {
		t.Fatalf("[%s] collection drop %s: %v\noutput: %s", proto.name, colFieldHash, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] collection drop %s: expected OK, got: %s", proto.name, colFieldHash, out)
	}
	out, err = runCLIProto(t, node, proto, "collection", "info", colFieldHash)
	if err == nil {
		t.Fatalf("[%s] collection info %s (after drop) succeeded, want error (output: %s)", proto.name, colFieldHash, out)
	}

	out, err = runCLIProto(t, node, proto, "--output", "json", "collection", "list")
	if err != nil {
		t.Fatalf("[%s] collection list after drop: %v\noutput: %s", proto.name, err, out)
	}
	var afterDrop transport.ListCollectionsResponse
	if jerr := json.Unmarshal([]byte(out), &afterDrop); jerr != nil {
		t.Fatalf("[%s] decode `collection list` JSON after drop: %v\noutput: %s", proto.name, jerr, out)
	}
	for _, c := range afterDrop.Collections {
		if c.Name == colFieldHash {
			t.Fatalf("[%s] collection list after drop still contains %s", proto.name, colFieldHash)
		}
	}

	// dropping an already-dropped collection must fail closed.
	out, err = runCLIProto(t, node, proto, "collection", "drop", colFieldHash)
	if err == nil {
		t.Fatalf("[%s] collection drop %s (already dropped) succeeded, want error (output: %s)", proto.name, colFieldHash, out)
	}
}
