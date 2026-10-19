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

// vector_cli_test.go drives the entire `qrdb vector` command tree -- the
// namespace subgroup (create/delete/list/info), vector CRUD (upsert/get/
// delete/batch-upsert/batch-delete/list/update-metadata), search
// (search/search-metadata/search-hybrid/search-by-id), and index/compaction
// maintenance (rebuild-index/rebuild-progress/index-stats/compact/
// compact-progress) -- through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix) using the shared
// startMultiProtocolNode harness (multi_protocol_helpers_test.go). This
// closes Batch D of the CLI coverage audit: 5 vector subcommands were
// previously REST-only (never through the CLI binary) and 12 advanced
// subcommands had zero coverage of any kind.
//
// pkg/service/vector.go's vectorService backs every one of these operations
// natively (no xkms/quicbaas delegation, no stub) -- CreateNamespace,
// UpsertVector, SearchSimilar, SearchByMetadata, HybridSearch, SearchByID,
// BatchUpsert, BatchDeleteVectors, ListVectors, UpdateMetadata,
// RebuildIndex, GetRebuildProgress, GetIndexStats, CompactNamespace, and
// GetCompactionProgress are all real, synchronous (the CLI never exposes
// the --async flag transport.RebuildIndexRequest/CompactNamespaceRequest
// carry, so every rebuild/compact this file drives runs to completion
// in-line before the CLI call returns), so this file asserts real,
// positive-path behavior throughout -- there is no NotImplemented/
// xkms-delegated contract to fall back to for this command tree.
//
// Every subtest below the top-level per-protocol loop operates on a
// namespace namespaced by protocol name (veccli-<proto>-<ts>) so the five
// protocol subtests -- which share one node -- never contend over the same
// namespace.
//
// This file itself only needs the "integration" build tag: pkg/service/
// vector.go and sdk/go/transport's vector types are compiled unconditionally
// (only cmd/qrdb's `vector` cobra subcommand tree and the RAG surface are
// gated behind qrdb_vector). The qrdb_vector tag this file's docstring keeps
// referring to is instead carried by helpers_test.go's buildBinary/
// buildCLIBinary, which always compile the qrdbd daemon and qrdb CLI with
// "qrdb_ts qrdb_vector" regardless of this test package's own tags -- see
// the CLAUDE.md task brief's harness note.
package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// vectorSearchResultJSON mirrors transport.VectorSearchResult, decoded from
// `vector search --output json`.
type vectorSearchResultJSON struct {
	ID       uint64  `json:"id"`
	Score    float32 `json:"score"`
	Distance float32 `json:"distance"`
	Rank     int     `json:"rank"`
}

// vectorSearchResponseJSON mirrors the map cmd/qrdb's printSearchResults
// marshals for `vector search`/`search-hybrid`/`search-by-id --output json`.
type vectorSearchResponseJSON struct {
	Results       []vectorSearchResultJSON `json:"results"`
	QueryDuration int64                    `json:"query_duration"`
}

// vectorListResponseJSON mirrors transport.ListVectorsResponse, decoded from
// `vector list --output json`.
type vectorListResponseJSON struct {
	Vectors []struct {
		ID uint64 `json:"id"`
	} `json:"vectors"`
	NextCursor string `json:"next_cursor,omitempty"`
	TotalCount int    `json:"total_count"`
}

// vectorSearchMetadataResponseJSON mirrors transport.SearchByMetadataResponse.
type vectorSearchMetadataResponseJSON struct {
	Results []struct {
		ID uint64 `json:"id"`
	} `json:"results"`
	TotalCount int `json:"total_count"`
}

// TestVector_CLI_PerProtocol drives the full `qrdb vector` lifecycle --
// namespace management, CRUD, search, and index/compaction maintenance --
// through the CLI binary against every transport sharing one multiProtoNode.
func TestVector_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVectorLifecycleOverProtocol(t, node, proto)
		})
	}
}

// testVectorLifecycleOverProtocol exercises the full vector command tree
// against a single protocol.
func testVectorLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	ns := fmt.Sprintf("veccli-%s-%d", proto.name, time.Now().UnixNano())

	// --- namespace create --------------------------------------------------
	out, err := runCLIProto(t, node, proto, "--output", "json", "vector", "namespace", "create",
		"--namespace", ns, "--dimension", "4", "--distance-metric", "cosine", "--index-type", "hnsw")
	if err != nil {
		t.Fatalf("[%s] vector namespace create: %v\noutput: %s", proto.name, err, out)
	}
	// printJSON pretty-prints with indentation, so match loosely rather than
	// pinning the exact compact-JSON spacing.
	if !strings.Contains(out, `"success": true`) {
		t.Fatalf("[%s] vector namespace create: expected success:true, got: %s", proto.name, out)
	}

	// An invalid --distance-metric must fail closed client-side before ever
	// reaching the server.
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "create",
		"--namespace", ns+"-bad", "--dimension", "4", "--distance-metric", "manhattan")
	if err == nil {
		t.Fatalf("[%s] vector namespace create --distance-metric manhattan succeeded, want error (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "create",
		"--namespace", ns+"-bad2", "--dimension", "4", "--index-type", "not-an-index")
	if err == nil {
		t.Fatalf("[%s] vector namespace create --index-type not-an-index succeeded, want error (output: %s)", proto.name, out)
	}

	// --- namespace list ------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "list")
	if err != nil {
		t.Fatalf("[%s] vector namespace list: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, ns) {
		t.Fatalf("[%s] vector namespace list = %q, want it to contain %s", proto.name, out, ns)
	}

	// --- namespace info --------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "info", "--namespace", ns)
	if err != nil {
		t.Fatalf("[%s] vector namespace info: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Dimension:      4") {
		t.Fatalf("[%s] vector namespace info = %q, want Dimension: 4", proto.name, out)
	}
	// Every transport stores the canonical LOWERCASE distance metric and
	// index type: the CLI normalizes to the transport.Distance*/Index*
	// constants before any transport sees the request, and MCP's former
	// server-side re-uppercasing (the one historical divergence) was
	// removed — pkg/api/mcp/tools_vector.go now lowercases like the rest,
	// pinned by TestHandleVectorCreateNamespace_LowercaseCanonical.
	wantDistanceMetric := "cosine"
	wantIndexType := "hnsw"
	if !strings.Contains(out, "DistanceMetric: "+wantDistanceMetric) {
		t.Fatalf("[%s] vector namespace info = %q, want DistanceMetric: %s", proto.name, out, wantDistanceMetric)
	}
	if !strings.Contains(out, "IndexType:      "+wantIndexType) {
		t.Fatalf("[%s] vector namespace info = %q, want IndexType: %s", proto.name, out, wantIndexType)
	}

	// --- upsert ----------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "upsert",
		"--namespace", ns, "--id", "1", "--vector", "1,0,0,0", "--metadata", `{"category":"alpha","idx":1}`)
	if err != nil {
		t.Fatalf("[%s] vector upsert id=1: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Success:   true") {
		t.Fatalf("[%s] vector upsert id=1 = %q, want Success: true", proto.name, out)
	}

	out, err = runCLIProto(t, node, proto, "vector", "upsert",
		"--namespace", ns, "--id", "2", "--vector", "0,1,0,0", "--metadata", `{"category":"beta","idx":2}`)
	if err != nil {
		t.Fatalf("[%s] vector upsert id=2: %v\noutput: %s", proto.name, err, out)
	}

	out, err = runCLIProto(t, node, proto, "vector", "upsert",
		"--namespace", ns, "--id", "3", "--vector", "0,0,1,0", "--metadata", `{"category":"alpha","idx":3}`)
	if err != nil {
		t.Fatalf("[%s] vector upsert id=3: %v\noutput: %s", proto.name, err, out)
	}

	// An invalid --vector component must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "vector", "upsert",
		"--namespace", ns, "--id", "99", "--vector", "not,a,vector,here")
	if err == nil {
		t.Fatalf("[%s] vector upsert with malformed --vector succeeded, want error (output: %s)", proto.name, out)
	}
	// An invalid --id must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "vector", "upsert",
		"--namespace", ns, "--id", "not-a-uint64", "--vector", "1,0,0,0")
	if err == nil {
		t.Fatalf("[%s] vector upsert with malformed --id succeeded, want error (output: %s)", proto.name, out)
	}
	// Malformed --metadata JSON must fail closed client-side.
	out, err = runCLIProto(t, node, proto, "vector", "upsert",
		"--namespace", ns, "--id", "4", "--vector", "1,1,0,0", "--metadata", "{not json")
	if err == nil {
		t.Fatalf("[%s] vector upsert with malformed --metadata succeeded, want error (output: %s)", proto.name, out)
	}

	// --- get ---------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "1")
	if err != nil {
		t.Fatalf("[%s] vector get id=1: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Found:     true") {
		t.Fatalf("[%s] vector get id=1 = %q, want Found: true", proto.name, out)
	}
	if !strings.Contains(out, "Vector:    1,0,0,0") {
		t.Fatalf("[%s] vector get id=1 = %q, want Vector: 1,0,0,0", proto.name, out)
	}

	for _, level := range []string{"leader", "any"} {
		out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "1", "--consistency", level)
		if err != nil {
			t.Fatalf("[%s] vector get id=1 --consistency %s: %v\noutput: %s", proto.name, level, err, out)
		}
		if !strings.Contains(out, "Found:     true") {
			t.Fatalf("[%s] vector get id=1 --consistency %s = %q, want Found: true", proto.name, level, out)
		}
	}
	out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "1", "--consistency", "bogus")
	if err == nil {
		t.Fatalf("[%s] vector get --consistency bogus succeeded, want error (output: %s)", proto.name, out)
	}

	// pkg/service/vector.go's GetVector returns a typed ErrNotFound error for
	// a missing ID (v.storage.GetVector's error is wrapped, never surfaced as
	// a zero-error GetVectorResponse{Found:false}); GetVectorResponse.Found
	// is consequently always true on every success path. This mirrors `kv
	// get` of a missing key (kv_cli_test.go), so this asserts the real,
	// confirmed contract rather than a Found:false response the service
	// never produces.
	out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "9999")
	if err == nil {
		t.Fatalf("[%s] vector get id=9999 (missing) succeeded, want a not-found error (output: %s)", proto.name, out)
	}

	// --- search (k-NN) ------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "search",
		"--namespace", ns, "--vector", "1,0,0,0", "--k", "3")
	if err != nil {
		t.Fatalf("[%s] vector search: %v\noutput: %s", proto.name, err, out)
	}
	var searchResp vectorSearchResponseJSON
	if jerr := json.Unmarshal([]byte(out), &searchResp); jerr != nil {
		t.Fatalf("[%s] decode `vector search --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(searchResp.Results) == 0 || searchResp.Results[0].ID != 1 {
		t.Fatalf("[%s] vector search results = %+v, want id=1 to rank first (exact match)", proto.name, searchResp.Results)
	}

	out, err = runCLIProto(t, node, proto, "vector", "search",
		"--namespace", ns, "--vector", "1,0,0,0", "--k", "1", "--ef-search", "64", "--min-score", "0")
	if err != nil {
		t.Fatalf("[%s] vector search --ef-search --min-score: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "1") {
		t.Fatalf("[%s] vector search --ef-search --min-score = %q, want it to contain id 1", proto.name, out)
	}

	// --- search-by-id ------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "search-by-id",
		"--namespace", ns, "--id", "1", "--k", "3")
	if err != nil {
		t.Fatalf("[%s] vector search-by-id: %v\noutput: %s", proto.name, err, out)
	}
	var searchByIDResp vectorSearchResponseJSON
	if jerr := json.Unmarshal([]byte(out), &searchByIDResp); jerr != nil {
		t.Fatalf("[%s] decode `vector search-by-id --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(searchByIDResp.Results) == 0 {
		t.Fatalf("[%s] vector search-by-id results empty, want at least the source vector's neighbors", proto.name)
	}

	// --- search-metadata -----------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "search-metadata",
		"--namespace", ns, "--filters", `{"category":"alpha"}`, "--limit", "10")
	if err != nil {
		t.Fatalf("[%s] vector search-metadata: %v\noutput: %s", proto.name, err, out)
	}
	var metaResp vectorSearchMetadataResponseJSON
	if jerr := json.Unmarshal([]byte(out), &metaResp); jerr != nil {
		t.Fatalf("[%s] decode `vector search-metadata --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(metaResp.Results) != 2 {
		t.Fatalf("[%s] vector search-metadata category=alpha results = %+v, want 2 (ids 1,3)", proto.name, metaResp.Results)
	}

	out, err = runCLIProto(t, node, proto, "vector", "search-metadata",
		"--namespace", ns, "--filters", `{"category":"alpha"}`, "--offset", "1")
	if err != nil {
		t.Fatalf("[%s] vector search-metadata --offset 1: %v\noutput: %s", proto.name, err, out)
	}

	// --- search-hybrid -----------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "search-hybrid",
		"--namespace", ns, "--vector", "1,0,0,0", "--filters", `{"category":"alpha"}`, "--k", "5")
	if err != nil {
		t.Fatalf("[%s] vector search-hybrid: %v\noutput: %s", proto.name, err, out)
	}
	var hybridResp vectorSearchResponseJSON
	if jerr := json.Unmarshal([]byte(out), &hybridResp); jerr != nil {
		t.Fatalf("[%s] decode `vector search-hybrid --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	for _, r := range hybridResp.Results {
		if r.ID == 2 {
			t.Fatalf("[%s] vector search-hybrid filtered by category=alpha returned id=2 (category=beta): %+v", proto.name, hybridResp.Results)
		}
	}

	// --- update-metadata -------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "update-metadata",
		"--namespace", ns, "--id", "2", "--metadata", `{"category":"gamma","idx":2}`)
	if err != nil {
		t.Fatalf("[%s] vector update-metadata id=2: %v\noutput: %s", proto.name, err, out)
	}
	out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "2")
	if err != nil {
		t.Fatalf("[%s] vector get id=2 after update-metadata: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "gamma") {
		t.Fatalf("[%s] vector get id=2 after update-metadata = %q, want it to contain updated category gamma", proto.name, out)
	}

	// --- batch-upsert (JSON file) ----------------------------------------
	batchFile := filepath.Join(t.TempDir(), "batch.json")
	batchPayload := `[
		{"id":10,"vector":[1,1,0,0],"metadata":{"category":"batch"}},
		{"id":11,"vector":[0,1,1,0],"metadata":{"category":"batch"}},
		{"id":12,"vector":[1,0,1,0],"metadata":{"category":"batch"}}
	]`
	if werr := os.WriteFile(batchFile, []byte(batchPayload), 0o600); werr != nil {
		t.Fatalf("[%s] write batch-upsert fixture: %v", proto.name, werr)
	}
	out, err = runCLIProto(t, node, proto, "vector", "batch-upsert", "--namespace", ns, "--file", batchFile)
	if err != nil {
		t.Fatalf("[%s] vector batch-upsert: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "InsertedCount: 3") {
		t.Fatalf("[%s] vector batch-upsert = %q, want InsertedCount: 3", proto.name, out)
	}

	// --- list --------------------------------------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "list", "--namespace", ns, "--limit", "100")
	if err != nil {
		t.Fatalf("[%s] vector list: %v\noutput: %s", proto.name, err, out)
	}
	var listResp vectorListResponseJSON
	if jerr := json.Unmarshal([]byte(out), &listResp); jerr != nil {
		t.Fatalf("[%s] decode `vector list --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(listResp.Vectors) < 6 {
		t.Fatalf("[%s] vector list = %+v, want at least 6 vectors (3 upserted + 3 batch-upserted)", proto.name, listResp.Vectors)
	}

	out, err = runCLIProto(t, node, proto, "vector", "list", "--namespace", ns, "--limit", "1")
	if err != nil {
		t.Fatalf("[%s] vector list --limit 1: %v\noutput: %s", proto.name, err, out)
	}

	// --- batch-delete ------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "batch-delete", "--namespace", ns, "--ids", "10,11,12")
	if err != nil {
		t.Fatalf("[%s] vector batch-delete: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "DeletedCount: 3") {
		t.Fatalf("[%s] vector batch-delete = %q, want DeletedCount: 3", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "10")
	if err == nil {
		t.Fatalf("[%s] vector get id=10 after batch-delete succeeded, want a not-found error (output: %s)", proto.name, out)
	}

	// --- rebuild-index / rebuild-progress -----------------------------
	// The CLI exposes no --async flag (transport.RebuildIndexRequest.Async
	// is always false from this code path), so RebuildIndex runs
	// synchronously and returns a JobID whose progress is immediately
	// queryable as "completed" -- there is no in-progress window to observe
	// from the CLI.
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "rebuild-index", "--namespace", ns)
	if err != nil {
		t.Fatalf("[%s] vector rebuild-index: %v\noutput: %s", proto.name, err, out)
	}
	var rebuildResp struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal([]byte(out), &rebuildResp); jerr != nil {
		t.Fatalf("[%s] decode `vector rebuild-index --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if rebuildResp.JobID == "" || rebuildResp.Status != "completed" {
		t.Fatalf("[%s] vector rebuild-index result = %+v, want a non-empty JobID and status=completed", proto.name, rebuildResp)
	}
	out, err = runCLIProto(t, node, proto, "vector", "rebuild-progress", "--namespace", ns, "--job-id", rebuildResp.JobID)
	if err != nil {
		t.Fatalf("[%s] vector rebuild-progress: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Status:           completed") {
		t.Fatalf("[%s] vector rebuild-progress = %q, want Status: completed", proto.name, out)
	}

	// --- index-stats -------------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "index-stats", "--namespace", ns)
	if err != nil {
		t.Fatalf("[%s] vector index-stats: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Namespace:        "+ns) {
		t.Fatalf("[%s] vector index-stats = %q, want it to name the namespace", proto.name, out)
	}

	// --- compact / compact-progress -----------------------------------
	out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "compact", "--namespace", ns)
	if err != nil {
		t.Fatalf("[%s] vector compact: %v\noutput: %s", proto.name, err, out)
	}
	var compactResp struct {
		OperationID string `json:"operation_id"`
		Success     bool   `json:"success"`
	}
	if jerr := json.Unmarshal([]byte(out), &compactResp); jerr != nil {
		t.Fatalf("[%s] decode `vector compact --output json`: %v\noutput: %s", proto.name, jerr, out)
	}
	if compactResp.OperationID == "" || !compactResp.Success {
		t.Fatalf("[%s] vector compact result = %+v, want a non-empty OperationID and Success=true", proto.name, compactResp)
	}
	out, err = runCLIProto(t, node, proto, "vector", "compact-progress", "--namespace", ns, "--operation-id", compactResp.OperationID)
	if err != nil {
		t.Fatalf("[%s] vector compact-progress: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "InProgress:      false") {
		t.Fatalf("[%s] vector compact-progress = %q, want InProgress: false (compact ran synchronously)", proto.name, out)
	}

	// --- delete (single vector) -----------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "delete", "--namespace", ns, "--id", "3")
	if err != nil {
		t.Fatalf("[%s] vector delete id=3: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Success:   true") {
		t.Fatalf("[%s] vector delete id=3 = %q, want Success: true", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "vector", "get", "--namespace", ns, "--id", "3")
	if err == nil {
		t.Fatalf("[%s] vector get id=3 after delete succeeded, want a not-found error (output: %s)", proto.name, out)
	}

	// --- namespace delete --------------------------------------------
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "delete", "--namespace", ns)
	if err != nil {
		t.Fatalf("[%s] vector namespace delete: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, "Success:        true") {
		t.Fatalf("[%s] vector namespace delete = %q, want Success: true", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "info", "--namespace", ns)
	if err == nil {
		t.Fatalf("[%s] vector namespace info after delete succeeded, want error (output: %s)", proto.name, out)
	}
}
