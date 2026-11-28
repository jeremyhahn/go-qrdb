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

package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestVector_CLI_NoShardIDFlag_PerProtocol asserts over every transport that
// the vector CLI has no --shard-id flag: the database's binding decides the
// vector shard (OD-14 A). The same commands without the flag succeed.
func TestVector_CLI_NoShardIDFlag_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			ns := fmt.Sprintf("vecnoshard-%s-%d", proto.name, time.Now().UnixNano())
			create := []string{"vector", "namespace", "create",
				"--namespace", ns, "--dimension", "4", "--distance-metric", "cosine", "--index-type", "hnsw"}

			out, err := runCLIProto(t, node, proto, append(create, "--shard-id", "1")...)
			if err == nil {
				t.Fatalf("[%s] namespace create --shard-id 1 succeeded, output: %s", proto.name, out)
			}
			if !strings.Contains(out, "unknown flag: --shard-id") {
				t.Fatalf("[%s] namespace create --shard-id 1: want unknown flag, got: %s", proto.name, out)
			}

			if out, err = runCLIProto(t, node, proto, create...); err != nil {
				t.Fatalf("[%s] namespace create: %v\noutput: %s", proto.name, err, out)
			}
			if out, err = runCLIProto(t, node, proto, "vector", "upsert",
				"--namespace", ns, "--id", "1", "--vector", "1,0,0,0"); err != nil {
				t.Fatalf("[%s] upsert: %v\noutput: %s", proto.name, err, out)
			}
			out, err = runCLIProto(t, node, proto, "--output", "json", "vector", "search",
				"--namespace", ns, "--vector", "1,0,0,0", "--k", "1")
			if err != nil {
				t.Fatalf("[%s] search: %v\noutput: %s", proto.name, err, out)
			}
			var resp vectorSearchResponseJSON
			if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
				t.Fatalf("[%s] decode search: %v\noutput: %s", proto.name, jerr, out)
			}
			if len(resp.Results) != 1 || resp.Results[0].ID != 1 {
				t.Fatalf("[%s] search results = %+v, want id=1", proto.name, resp.Results)
			}
			if out, err = runCLIProto(t, node, proto, "vector", "namespace", "delete", "--namespace", ns); err != nil {
				t.Fatalf("[%s] namespace delete: %v\noutput: %s", proto.name, err, out)
			}

			assertVectorsStayInTheirDatabase(t, node, proto)
		})
	}
}

// assertVectorsStayInTheirDatabase creates the same namespace in two
// databases of one tenant, upserts a different vector into each, and asserts
// a search in each database finds only that database's vector: with no shard
// ID on the request, the database's binding picks the store.
func assertVectorsStayInTheirDatabase(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	dbs := []struct {
		name string
		id   uint64
	}{
		{name: "vecdb-a-" + suffix, id: 11},
		{name: "vecdb-b-" + suffix, id: 22},
	}
	const ns = "shared-ns"
	for _, db := range dbs {
		steps := [][]string{
			{"database", "create", db.name},
			{"--database", db.name, "vector", "namespace", "create",
				"--namespace", ns, "--dimension", "4", "--distance-metric", "cosine", "--index-type", "hnsw"},
			{"--database", db.name, "vector", "upsert",
				"--namespace", ns, "--id", fmt.Sprint(db.id), "--vector", "1,0,0,0"},
		}
		for _, step := range steps {
			if out, err := runCLIProto(t, node, proto, step...); err != nil {
				t.Fatalf("[%s] %v: %v\noutput: %s", proto.name, step, err, out)
			}
		}
	}
	for _, db := range dbs {
		out, err := runCLIProto(t, node, proto, "--output", "json", "--database", db.name, "vector", "search",
			"--namespace", ns, "--vector", "1,0,0,0", "--k", "10")
		if err != nil {
			t.Fatalf("[%s] search in %s: %v\noutput: %s", proto.name, db.name, err, out)
		}
		var resp vectorSearchResponseJSON
		if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
			t.Fatalf("[%s] decode search in %s: %v\noutput: %s", proto.name, db.name, jerr, out)
		}
		if len(resp.Results) != 1 || resp.Results[0].ID != db.id {
			t.Fatalf("[%s] search in %s = %+v, want only id=%d", proto.name, db.name, resp.Results, db.id)
		}
	}
	for _, db := range dbs {
		if out, err := runCLIProto(t, node, proto, "database", "drop", db.name); err != nil {
			t.Fatalf("[%s] database drop %s: %v\noutput: %s", proto.name, db.name, err, out)
		}
	}
}
