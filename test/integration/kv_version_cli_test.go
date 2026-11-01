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

// kv_version_cli_test.go drives the supported ways to read a key's MVCC
// version -- `kv put --revision` (PutWithLease with lease 0), `kv get
// --revision` (GetRevision) and a `batch execute` of get ops -- through the
// real CLI binary on every transport of a three-node MVCC cluster, and shows
// that a transaction's version compare reads the same number: a
// read-then-compare-and-set on the version takes the success branch, and
// with an intervening write the failure branch.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// kvVersionDisplay is the version metadata of `kv put|get --revision
// --output json`.
type kvVersionDisplay struct {
	CreateRevision int64 `json:"create_revision"`
	ModRevision    int64 `json:"mod_revision"`
	Version        int64 `json:"version"`
	LeaseID        int64 `json:"lease_id"`
}

// TestKVVersion_CLI_ClusterPerProtocol runs the version scenario on every
// transport, against a fresh key per transport, on one shared cluster.
func TestKVVersion_CLI_ClusterPerProtocol(t *testing.T) {
	t.Parallel()
	nodes, _ := startMultiProtocolCluster(t, 0)

	for i, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testKVVersionOverProtocol(t, nodes, i, proto)
		})
	}
}

// testKVVersionOverProtocol writes through node writer and reads the version
// back through every node.
func testKVVersionOverProtocol(t *testing.T, nodes [3]*multiProtoNode, writer int, proto multiProtocol) {
	t.Helper()
	w := nodes[writer%len(nodes)]
	key := "kvversion/" + proto.name + "/key"
	marker := "kvversion/" + proto.name + "/branch"

	if out, err := runCLIProto(t, w, proto, "kv", "put", key, "v1"); err != nil {
		t.Fatalf("[%s] kv put v1: %v\noutput: %s", proto.name, err, out)
	}
	put := kvVersionCLI(t, w, proto, "kv", "put", key, "v2", "--revision")
	if put.Version != 2 || put.LeaseID != 0 || put.ModRevision <= put.CreateRevision || put.CreateRevision == 0 {
		t.Fatalf("[%s] kv put --revision after an overwrite = %+v, want version 2 with no lease and a later mod revision", proto.name, put)
	}

	for n, node := range nodes {
		if got := kvVersionCLI(t, node, proto, "kv", "get", key, "--revision"); got != put {
			t.Fatalf("[%s] node %d kv get --revision = %+v, want the put's %+v", proto.name, n, got, put)
		}
		if got := kvVersionBatchGet(t, node, proto, key); got != put {
			t.Fatalf("[%s] node %d batch get = %+v, want the put's %+v", proto.name, n, got, put)
		}
	}

	// Read-then-compare-and-set on the version the reads reported.
	resp, out, err := txExecuteFile(t, w, proto, kvVersionCAS(key, marker, put.Version, "v3"))
	if err != nil || !resp.Succeeded {
		t.Fatalf("[%s] tx compare version == %d with no intervening write: succeeded=%t err=%v, want the success branch\noutput: %s",
			proto.name, put.Version, resp.Succeeded, err, out)
	}
	read := kvVersionCLI(t, w, proto, "kv", "get", key, "--revision")
	if read.Version != put.Version+1 {
		t.Fatalf("[%s] version after the compare-and-set = %d, want %d", proto.name, read.Version, put.Version+1)
	}

	// An intervening write between the read and the compare-and-set.
	if out, err := runCLIProto(t, w, proto, "kv", "put", key, "intervening"); err != nil {
		t.Fatalf("[%s] intervening kv put: %v\noutput: %s", proto.name, err, out)
	}
	resp, out, err = txExecuteFile(t, w, proto, kvVersionCAS(key, marker, read.Version, "lost-update"))
	if err != nil || resp.Succeeded {
		t.Fatalf("[%s] tx compare version == %d after an intervening write: succeeded=%t err=%v, want the failure branch\noutput: %s",
			proto.name, read.Version, resp.Succeeded, err, out)
	}
	out, err = runCLIProto(t, w, proto, "kv", "get", key)
	if err != nil || strings.TrimSpace(out) != "intervening" {
		t.Fatalf("[%s] kv get after the failed compare-and-set = %q, err=%v, want %q", proto.name, out, err, "intervening")
	}
	out, err = runCLIProto(t, w, proto, "kv", "get", marker)
	if err != nil || strings.TrimSpace(out) != "failure" {
		t.Fatalf("[%s] failure-branch marker = %q, err=%v, want %q", proto.name, out, err, "failure")
	}
}

// kvVersionCAS is a transaction that writes value to key when key's version
// is still version, and records the failure branch in marker otherwise.
func kvVersionCAS(key, marker string, version int64, value string) transport.TransactionRequest {
	return transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(key), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: version},
		},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte(key), Value: []byte(value)}},
		FailureOps: []transport.Op{{Type: transport.OpPut, Key: []byte(marker), Value: []byte("failure")}},
	}
}

// kvVersionCLI runs a `kv ... --revision` command with JSON output and
// decodes its version metadata.
func kvVersionCLI(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) kvVersionDisplay {
	t.Helper()
	out, err := runCLIProto(t, node, proto, append([]string{"--output", "json"}, args...)...)
	if err != nil {
		t.Fatalf("[%s] %s: %v\noutput: %s", proto.name, strings.Join(args, " "), err, out)
	}
	var kv kvVersionDisplay
	if jerr := json.Unmarshal([]byte(out), &kv); jerr != nil {
		t.Fatalf("[%s] decode %s JSON: %v\noutput: %s", proto.name, strings.Join(args, " "), jerr, out)
	}
	return kv
}

// kvVersionBatchGet reads key through a one-op `batch execute` and returns
// the version metadata of the key it read.
func kvVersionBatchGet(t *testing.T, node *multiProtoNode, proto multiProtocol, key string) kvVersionDisplay {
	t.Helper()
	data, err := json.Marshal(transport.BatchRequest{Ops: []transport.Op{{Type: transport.OpGet, Key: []byte(key)}}})
	if err != nil {
		t.Fatalf("[%s] marshal BatchRequest: %v", proto.name, err)
	}
	path := writeTempFile(t, "batch-get.json", data)
	out, err := runCLIProto(t, node, proto, "--output", "json", "batch", "execute", "--file", path)
	if err != nil {
		t.Fatalf("[%s] batch execute (get): %v\noutput: %s", proto.name, err, out)
	}
	var resp transport.BatchResponse
	if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
		t.Fatalf("[%s] decode batch execute JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(resp.Responses) != 1 || len(resp.Responses[0].Kvs) != 1 {
		t.Fatalf("[%s] batch get returned %d responses, want one with one key\noutput: %s", proto.name, len(resp.Responses), out)
	}
	kv := resp.Responses[0].Kvs[0]
	return kvVersionDisplay{CreateRevision: kv.CreateRevision, ModRevision: kv.ModRevision, Version: kv.Version, LeaseID: kv.LeaseID}
}
