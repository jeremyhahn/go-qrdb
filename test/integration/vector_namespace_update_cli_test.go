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

// vector_namespace_update_cli_test.go drives `qrdb vector namespace update`
// through the real client CLI binary against every transport (REST, gRPC,
// QUIC, MCP, Unix) on the shared startMultiProtocolNode harness: an in-place
// update of a namespace's metadata and index parameters keeps every vector,
// shows the merged values in `namespace info`, reports whether the index
// needs a rebuild, and refuses a missing namespace and a dimension change
// with the typed refusal every transport rebuilds.
package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// vectorNamespaceInfoJSON mirrors transport.NamespaceInfo, decoded from
// `vector namespace info --output json`.
type vectorNamespaceInfoJSON struct {
	Namespace   string            `json:"namespace"`
	Dimension   uint32            `json:"dimension"`
	VectorCount uint64            `json:"vector_count"`
	IndexConfig map[string]string `json:"index_config"`
	Metadata    map[string]string `json:"metadata"`
}

// vectorUpdateNamespaceJSON mirrors transport.UpdateNamespaceResponse.
type vectorUpdateNamespaceJSON struct {
	Namespace       string `json:"namespace"`
	RequiresRebuild bool   `json:"requires_rebuild"`
	Success         bool   `json:"success"`
}

// TestVectorNamespaceUpdate_CLI_PerProtocol runs the namespace-update round
// trip and its refusals over every transport sharing one multiProtoNode.
func TestVectorNamespaceUpdate_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testVectorNamespaceUpdateOverProtocol(t, node, proto)
		})
	}
}

// testVectorNamespaceUpdateOverProtocol exercises `vector namespace update`
// against a single protocol.
func testVectorNamespaceUpdateOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	ns := fmt.Sprintf("vecnsupd-%s-%d", proto.name, time.Now().UnixNano())
	run := func(args ...string) string {
		t.Helper()
		out, err := runCLIProto(t, node, proto, args...)
		if err != nil {
			t.Fatalf("[%s] %s: %v\noutput: %s", proto.name, strings.Join(args, " "), err, out)
		}
		return out
	}
	update := func(args ...string) vectorUpdateNamespaceJSON {
		t.Helper()
		full := append([]string{"--output", "json", "vector", "namespace", "update", "--namespace", ns}, args...)
		out := run(full...)
		var resp vectorUpdateNamespaceJSON
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("[%s] decode `vector namespace update --output json`: %v\noutput: %s", proto.name, err, out)
		}
		if !resp.Success || resp.Namespace != ns {
			t.Fatalf("[%s] vector namespace update %v = %+v, want success for %s", proto.name, args, resp, ns)
		}
		return resp
	}
	info := func() vectorNamespaceInfoJSON {
		t.Helper()
		out := run("--output", "json", "vector", "namespace", "info", "--namespace", ns)
		var resp vectorNamespaceInfoJSON
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("[%s] decode `vector namespace info --output json`: %v\noutput: %s", proto.name, err, out)
		}
		return resp
	}

	run("vector", "namespace", "create", "--namespace", ns,
		"--dimension", "4", "--distance-metric", "cosine", "--index-type", "hnsw")
	vectors := map[string]string{"1": "1,0,0,0", "2": "0,1,0,0", "3": "0,0,1,0"}
	for id, vec := range vectors {
		run("vector", "upsert", "--namespace", ns, "--id", id, "--vector", vec,
			"--metadata", `{"category":"c`+id+`"}`)
	}

	// --- metadata-only update: no index parameter changed, no rebuild -----
	resp := update("--metadata", `{"owner":"team-a","tier":"gold"}`)
	if resp.RequiresRebuild {
		t.Fatalf("[%s] metadata-only update reported requires_rebuild=true", proto.name)
	}

	// --- index parameter update: a changed parameter needs a rebuild ------
	resp = update("--index-param", "ef_construction=321", "--metadata", `{"tier":"platinum"}`, "--dimension", "4")
	if !resp.RequiresRebuild {
		t.Fatalf("[%s] index-param update reported requires_rebuild=false, want true", proto.name)
	}
	// The same value again changes nothing the index depends on.
	resp = update("--index-param", "ef_construction=321")
	if resp.RequiresRebuild {
		t.Fatalf("[%s] unchanged index-param update reported requires_rebuild=true", proto.name)
	}

	// The default text output carries the same fields.
	if out := run("vector", "namespace", "update", "--namespace", ns, "--metadata", `{"owner":"team-a"}`); !strings.Contains(out, "Success:          true") ||
		!strings.Contains(out, "Requires rebuild: false") {
		t.Fatalf("[%s] vector namespace update (text) = %q, want Success: true and Requires rebuild: false", proto.name, out)
	}

	// --- namespace info shows the merged values ---------------------------
	got := info()
	if got.Dimension != 4 || got.VectorCount != uint64(len(vectors)) {
		t.Fatalf("[%s] namespace info after update = %+v, want dimension 4 and %d vectors", proto.name, got, len(vectors))
	}
	if got.IndexConfig["ef_construction"] != "321" {
		t.Fatalf("[%s] namespace info index_config = %v, want ef_construction=321", proto.name, got.IndexConfig)
	}
	if got.Metadata["owner"] != "team-a" || got.Metadata["tier"] != "platinum" {
		t.Fatalf("[%s] namespace info metadata = %v, want owner=team-a (kept) and tier=platinum (merged over)", proto.name, got.Metadata)
	}

	// --- every vector survives the update ---------------------------------
	for id, vec := range vectors {
		out := run("vector", "get", "--namespace", ns, "--id", id)
		if !strings.Contains(out, "Vector:    "+vec) || !strings.Contains(out, `"category":"c`+id+`"`) {
			t.Fatalf("[%s] vector get id=%s after namespace update = %q, want vector %s and its metadata", proto.name, id, out, vec)
		}
	}
	run("--output", "json", "vector", "rebuild-index", "--namespace", ns)
	out := run("--output", "json", "vector", "search", "--namespace", ns, "--vector", "1,0,0,0", "--k", "3")
	var searchResp vectorSearchResponseJSON
	if err := json.Unmarshal([]byte(out), &searchResp); err != nil {
		t.Fatalf("[%s] decode `vector search --output json`: %v\noutput: %s", proto.name, err, out)
	}
	if len(searchResp.Results) != len(vectors) || searchResp.Results[0].ID != 1 {
		t.Fatalf("[%s] vector search after rebuild = %+v, want all %d vectors with id=1 first", proto.name, searchResp.Results, len(vectors))
	}

	// --- typed refusals ---------------------------------------------------
	missing := ns + "-missing"
	out, err := runCLIProto(t, node, proto, "vector", "namespace", "update", "--namespace", missing,
		"--metadata", `{"owner":"team-b"}`)
	// The refusal names the namespace qualified by the caller's tenant and
	// database, so match the reason and the name rather than the prefix.
	if err == nil || !strings.Contains(out, "namespace not found: ") || !strings.Contains(out, "/"+missing+"\n") {
		t.Fatalf("[%s] update of a missing namespace: err=%v output=%q, want the namespace-not-found refusal", proto.name, err, out)
	}
	out, err = runCLIProto(t, node, proto, "vector", "namespace", "update", "--namespace", ns,
		"--dimension", "8", "--metadata", `{"owner":"team-b"}`)
	if err == nil || !strings.Contains(out, "dimension mismatch: expected 4, got 8") {
		t.Fatalf("[%s] dimension change: err=%v output=%q, want the dimension-mismatch refusal", proto.name, err, out)
	}
	// A refused update changes nothing.
	if md := info().Metadata; md["owner"] != "team-a" {
		t.Fatalf("[%s] namespace metadata after refused update = %v, want owner=team-a unchanged", proto.name, md)
	}

	run("vector", "namespace", "delete", "--namespace", ns)
}
