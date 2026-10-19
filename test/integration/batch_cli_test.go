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

// batch_cli_test.go drives `qrdb batch execute` (cmd/qrdb/batch.go) through
// the real client CLI binary against every transport (REST, gRPC, QUIC,
// MCP, Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). `batch execute` was previously
// exercised only via the raw REST helper httpBatch, never through the CLI
// binary, and never multi-protocol. `batch import`/`batch import-formats`
// are covered separately by batch_import_cli_test.go and are NOT
// duplicated here.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestBatch_CLI_PerProtocol drives `qrdb batch execute` -- bulk put, bulk
// delete, a mixed put/get/delete batch, stdin input, and CLI-side
// validation errors -- through the CLI binary against every transport
// sharing one multiProtoNode.
func TestBatch_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBatchExecuteOverProtocol(t, node, proto)
		})
	}
}

// batchExecuteFile marshals req to JSON, writes it to a temp file scoped to
// t, runs `batch execute --file <path> --output json` over proto, and
// decodes the response. It returns the decoded response (zero value on
// error), the raw output, and the error from the CLI invocation.
func batchExecuteFile(t *testing.T, node *multiProtoNode, proto multiProtocol, req transport.BatchRequest) (transport.BatchResponse, string, error) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("[%s] marshal BatchRequest: %v", proto.name, err)
	}
	path := writeTempFile(t, "batch.json", data)

	out, err := runCLIProto(t, node, proto, "--output", "json", "batch", "execute", "--file", path)
	var resp transport.BatchResponse
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
			t.Fatalf("[%s] decode `batch execute` JSON: %v\noutput: %s", proto.name, jerr, out)
		}
	}
	return resp, out, err
}

// testBatchExecuteOverProtocol exercises the full `batch execute`
// permutation matrix against a single protocol.
func testBatchExecuteOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	prefix := "batchcli/" + proto.name + "/"
	keyA := prefix + "alpha"
	keyB := prefix + "bravo"
	keyC := prefix + "charlie"

	// --- bulk put: three keys in one batch. -----------------------------
	resp, out, err := batchExecuteFile(t, node, proto, transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyA), Value: []byte("one")},
			{Type: transport.OpPut, Key: []byte(keyB), Value: []byte("two")},
			{Type: transport.OpPut, Key: []byte(keyC), Value: []byte("three")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] batch execute (bulk put): %v\noutput: %s", proto.name, err, out)
	}
	if len(resp.Responses) != 3 {
		t.Fatalf("[%s] batch execute (bulk put): got %d responses, want 3 (output: %s)", proto.name, len(resp.Responses), out)
	}
	for i, r := range resp.Responses {
		if !r.Succeeded {
			t.Fatalf("[%s] batch execute (bulk put): response[%d].Succeeded = false, want true (output: %s)", proto.name, i, out)
		}
	}

	for key, want := range map[string]string{keyA: "one", keyB: "two", keyC: "three"} {
		got, err := runCLIProto(t, node, proto, "kv", "get", key)
		if err != nil || strings.TrimSpace(got) != want {
			t.Fatalf("[%s] kv get %s after bulk put = %q, err=%v, want %q", proto.name, key, got, err, want)
		}
	}

	// --- bulk delete: remove two of the three keys in one batch. -------
	resp, out, err = batchExecuteFile(t, node, proto, transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpDelete, Key: []byte(keyA)},
			{Type: transport.OpDelete, Key: []byte(keyB)},
		},
	})
	if err != nil {
		t.Fatalf("[%s] batch execute (bulk delete): %v\noutput: %s", proto.name, err, out)
	}
	if len(resp.Responses) != 2 {
		t.Fatalf("[%s] batch execute (bulk delete): got %d responses, want 2 (output: %s)", proto.name, len(resp.Responses), out)
	}

	out, err = runCLIProto(t, node, proto, "kv", "exists", keyA)
	if err != nil || strings.TrimSpace(out) != "false" {
		t.Fatalf("[%s] kv exists %s after bulk delete = %q, err=%v, want %q", proto.name, keyA, out, err, "false")
	}
	out, err = runCLIProto(t, node, proto, "kv", "exists", keyB)
	if err != nil || strings.TrimSpace(out) != "false" {
		t.Fatalf("[%s] kv exists %s after bulk delete = %q, err=%v, want %q", proto.name, keyB, out, err, "false")
	}
	out, err = runCLIProto(t, node, proto, "kv", "exists", keyC)
	if err != nil || strings.TrimSpace(out) != "true" {
		t.Fatalf("[%s] kv exists %s (untouched by bulk delete) = %q, err=%v, want %q", proto.name, keyC, out, err, "true")
	}

	// --- mixed batch: put + get + delete in a single call. -------------
	keyD := prefix + "delta"
	resp, out, err = batchExecuteFile(t, node, proto, transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyD), Value: []byte("mixed")},
			{Type: transport.OpGet, Key: []byte(keyC)},
			{Type: transport.OpDelete, Key: []byte(keyC)},
		},
	})
	if err != nil {
		t.Fatalf("[%s] batch execute (mixed): %v\noutput: %s", proto.name, err, out)
	}
	if len(resp.Responses) != 3 {
		t.Fatalf("[%s] batch execute (mixed): got %d responses, want 3 (output: %s)", proto.name, len(resp.Responses), out)
	}
	if len(resp.Responses[1].Kvs) != 1 || string(resp.Responses[1].Kvs[0].Value) != "three" {
		t.Fatalf("[%s] batch execute (mixed) get response = %+v, want value %q", proto.name, resp.Responses[1], "three")
	}

	out, err = runCLIProto(t, node, proto, "kv", "get", keyD)
	if err != nil || strings.TrimSpace(out) != "mixed" {
		t.Fatalf("[%s] kv get %s after mixed batch = %q, err=%v, want %q", proto.name, keyD, out, err, "mixed")
	}
	out, err = runCLIProto(t, node, proto, "kv", "exists", keyC)
	if err != nil || strings.TrimSpace(out) != "false" {
		t.Fatalf("[%s] kv exists %s after mixed batch delete = %q, err=%v, want %q", proto.name, keyC, out, err, "false")
	}

	// --- stdin form (--file -) -----------------------------------------
	keyE := prefix + "echo"
	stdinReq := transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyE), Value: []byte("stdin")},
		},
	}
	stdinData, err := json.Marshal(stdinReq)
	if err != nil {
		t.Fatalf("[%s] marshal stdin BatchRequest: %v", proto.name, err)
	}
	out, err = runQRDBStdin(t, node.cli, string(stdinData), node.cliArgsFor(proto), "--output", "json", "batch", "execute", "--file", "-")
	if err != nil {
		t.Fatalf("[%s] batch execute --file -: %v\noutput: %s", proto.name, err, out)
	}
	var stdinResp transport.BatchResponse
	if jerr := json.Unmarshal([]byte(out), &stdinResp); jerr != nil {
		t.Fatalf("[%s] decode `batch execute --file -` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if len(stdinResp.Responses) != 1 || !stdinResp.Responses[0].Succeeded {
		t.Fatalf("[%s] batch execute --file - responses = %+v, want one succeeded response", proto.name, stdinResp.Responses)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyE)
	if err != nil || strings.TrimSpace(out) != "stdin" {
		t.Fatalf("[%s] kv get %s after stdin batch = %q, err=%v, want %q", proto.name, keyE, out, err, "stdin")
	}

	// --- CLI-side validation: --file is required. -----------------------
	out, err = runCLIProto(t, node, proto, "batch", "execute")
	if err == nil {
		t.Fatalf("[%s] batch execute with no --file succeeded, want error (output: %s)", proto.name, out)
	}

	// --- CLI-side validation: nonexistent file must fail closed. -------
	out, err = runCLIProto(t, node, proto, "batch", "execute", "--file", "/nonexistent/path/batch.json")
	if err == nil {
		t.Fatalf("[%s] batch execute --file /nonexistent/path/batch.json succeeded, want error (output: %s)", proto.name, out)
	}

	// --- CLI-side validation: malformed JSON must fail closed. ----------
	badPath := writeTempFile(t, "batch-bad.json", []byte("{not valid json"))
	out, err = runCLIProto(t, node, proto, "batch", "execute", "--file", badPath)
	if err == nil {
		t.Fatalf("[%s] batch execute with malformed JSON file succeeded, want error (output: %s)", proto.name, out)
	}
}
