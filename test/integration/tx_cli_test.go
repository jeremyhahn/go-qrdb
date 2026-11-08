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

// tx_cli_test.go drives `qrdb tx execute` (cmd/qrdb/transaction.go) through
// the real client CLI binary against every transport (REST, gRPC, QUIC,
// MCP, Unix) using the shared startMultiProtocolNode harness
// (multi_protocol_helpers_test.go). `tx execute` was previously exercised
// only via the raw REST helper httpTx, never through the CLI binary, and
// never multi-protocol. This file closes that gap and exercises the
// compare-and-swap / put-if-not-exists / multi-compare permutations that
// give transactions their value over plain kv put. On a three-node MVCC
// cluster it drives get and range ops inside a transaction branch: every node
// answers a read-only transaction with the key's MVCC metadata, a branch that
// reads, writes and reads again sees its own write at the transaction's
// revision, a failure branch reads the current value back after an
// intervening write, and a range answers every key in it.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTx_CLI_PerProtocol drives `qrdb tx execute` -- CAS success, CAS
// failure, put-if-not-exists (success and failure), multi-compare AND
// semantics, stdin input, and CLI-side validation errors -- through the CLI
// binary against every transport sharing one multiProtoNode.
func TestTx_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTxExecuteOverProtocol(t, node, proto)
		})
	}
}

// txExecuteFile marshals req to JSON, writes it to a temp file scoped to t,
// runs `tx execute --file <path> --output json` over proto, and decodes the
// response. It returns the decoded response (zero value on error), the raw
// output, and the error from the CLI invocation.
func txExecuteFile(t *testing.T, node *multiProtoNode, proto multiProtocol, req transport.TransactionRequest) (transport.TransactionResponse, string, error) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("[%s] marshal TransactionRequest: %v", proto.name, err)
	}
	path := writeTempFile(t, "tx.json", data)

	out, err := runCLIProto(t, node, proto, "--output", "json", "tx", "execute", "--file", path)
	var resp transport.TransactionResponse
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
			t.Fatalf("[%s] decode `tx execute` JSON: %v\noutput: %s", proto.name, jerr, out)
		}
	}
	return resp, out, err
}

// testTxExecuteOverProtocol exercises the full `tx execute` permutation
// matrix against a single protocol.
func testTxExecuteOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	prefix := "txcli/" + proto.name + "/"
	keyA := prefix + "alpha"
	keyNew := prefix + "not-yet-created"

	// --- seed keyA -------------------------------------------------------
	out, err := runCLIProto(t, node, proto, "kv", "put", keyA, "v1")
	if err != nil {
		t.Fatalf("[%s] seed kv put %s: %v\noutput: %s", proto.name, keyA, err, out)
	}

	// --- CAS success: compare value == "v1", success op replaces with
	// "v2". FailureOps is empty -- it never executes since the compare
	// holds. Reads in a branch are TestTxRead_CLI_ClusterPerProtocol's. --
	resp, out, err := txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyA), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v1")},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyA), Value: []byte("v2")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] tx execute (CAS success): %v\noutput: %s", proto.name, err, out)
	}
	if !resp.Succeeded {
		t.Fatalf("[%s] tx execute (CAS success): Succeeded = false, want true (output: %s)", proto.name, out)
	}
	if resp.Revision == 0 {
		t.Fatalf("[%s] tx execute (CAS success): Revision = 0, want nonzero", proto.name)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA)
	if err != nil || strings.TrimSpace(out) != "v2" {
		t.Fatalf("[%s] kv get %s after CAS success = %q, err=%v, want %q", proto.name, keyA, out, err, "v2")
	}

	// --- CAS failure: compare value == "v1" no longer holds (it's "v2"
	// now), so the successOps must NOT run and the key must be
	// unchanged. FailureOps is intentionally empty. ----------------------
	resp, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyA), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v1")},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyA), Value: []byte("should-not-be-written")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] tx execute (CAS failure): %v\noutput: %s", proto.name, err, out)
	}
	if resp.Succeeded {
		t.Fatalf("[%s] tx execute (CAS failure): Succeeded = true, want false (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA)
	if err != nil || strings.TrimSpace(out) != "v2" {
		t.Fatalf("[%s] kv get %s after CAS failure = %q, err=%v, want unchanged %q", proto.name, keyA, out, err, "v2")
	}

	// --- put-if-not-exists success: keyNew has never been written, so
	// its Version is 0; compare Version == 0 holds and the put runs. --
	resp, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyNew), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: 0},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyNew), Value: []byte("created")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] tx execute (put-if-not-exists success): %v\noutput: %s", proto.name, err, out)
	}
	if !resp.Succeeded {
		t.Fatalf("[%s] tx execute (put-if-not-exists success): Succeeded = false, want true (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyNew)
	if err != nil || strings.TrimSpace(out) != "created" {
		t.Fatalf("[%s] kv get %s after put-if-not-exists success = %q, err=%v, want %q", proto.name, keyNew, out, err, "created")
	}

	// --- put-if-not-exists failure: keyNew now exists (Version > 0), so
	// the identical Version == 0 precondition must fail and the value
	// must be left untouched. FailureOps is intentionally empty. --------
	resp, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyNew), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: 0},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyNew), Value: []byte("should-not-overwrite")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] tx execute (put-if-not-exists failure): %v\noutput: %s", proto.name, err, out)
	}
	if resp.Succeeded {
		t.Fatalf("[%s] tx execute (put-if-not-exists failure): Succeeded = true, want false (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyNew)
	if err != nil || strings.TrimSpace(out) != "created" {
		t.Fatalf("[%s] kv get %s after put-if-not-exists failure = %q, err=%v, want unchanged %q", proto.name, keyNew, out, err, "created")
	}

	// --- multiple compares, AND semantics: both hold -> success. -------
	resp, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyA), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v2")},
			{Key: []byte(keyNew), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("created")},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyA), Value: []byte("v3")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] tx execute (multi-compare AND success): %v\noutput: %s", proto.name, err, out)
	}
	if !resp.Succeeded {
		t.Fatalf("[%s] tx execute (multi-compare AND success): Succeeded = false, want true (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA)
	if err != nil || strings.TrimSpace(out) != "v3" {
		t.Fatalf("[%s] kv get %s after multi-compare success = %q, err=%v, want %q", proto.name, keyA, out, err, "v3")
	}

	// --- multiple compares, AND semantics: one holds, one doesn't ->
	// overall failure even though the first compare is true. FailureOps
	// is intentionally empty. --------------------------------------------
	resp, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(keyA), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v3")},
			{Key: []byte(keyNew), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("this-value-is-wrong")},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyA), Value: []byte("should-not-be-written")},
		},
	})
	if err != nil {
		t.Fatalf("[%s] tx execute (multi-compare AND failure): %v\noutput: %s", proto.name, err, out)
	}
	if resp.Succeeded {
		t.Fatalf("[%s] tx execute (multi-compare AND failure): Succeeded = true, want false (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyA)
	if err != nil || strings.TrimSpace(out) != "v3" {
		t.Fatalf("[%s] kv get %s after multi-compare failure = %q, err=%v, want unchanged %q", proto.name, keyA, out, err, "v3")
	}

	// --- stdin form (--file -) -----------------------------------------
	// SuccessOps here has no compares, so it always executes; a Put keeps
	// this scenario focused on the --file - plumbing.
	keyStdin := prefix + "stdin"
	stdinReq := transport.TransactionRequest{
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte(keyStdin), Value: []byte("stdin-tx")},
		},
	}
	stdinData, err := json.Marshal(stdinReq)
	if err != nil {
		t.Fatalf("[%s] marshal stdin TransactionRequest: %v", proto.name, err)
	}
	out, err = runQRDBStdin(t, node.cli, string(stdinData), node.cliArgsFor(proto), "--output", "json", "tx", "execute", "--file", "-")
	if err != nil {
		t.Fatalf("[%s] tx execute --file -: %v\noutput: %s", proto.name, err, out)
	}
	var stdinResp transport.TransactionResponse
	if jerr := json.Unmarshal([]byte(out), &stdinResp); jerr != nil {
		t.Fatalf("[%s] decode `tx execute --file -` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	if !stdinResp.Succeeded {
		t.Fatalf("[%s] tx execute --file - : Succeeded = false, want true (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyStdin)
	if err != nil || strings.TrimSpace(out) != "stdin-tx" {
		t.Fatalf("[%s] kv get %s after stdin tx execute = %q, err=%v, want %q", proto.name, keyStdin, out, err, "stdin-tx")
	}

	// --- CLI-side validation: --file is required. -----------------------
	out, err = runCLIProto(t, node, proto, "tx", "execute")
	if err == nil {
		t.Fatalf("[%s] tx execute with no --file succeeded, want error (output: %s)", proto.name, out)
	}

	// --- CLI-side validation: nonexistent file must fail closed. -------
	out, err = runCLIProto(t, node, proto, "tx", "execute", "--file", "/nonexistent/path/tx.json")
	if err == nil {
		t.Fatalf("[%s] tx execute --file /nonexistent/path/tx.json succeeded, want error (output: %s)", proto.name, out)
	}

	// --- CLI-side validation: malformed JSON must fail closed. ----------
	badPath := writeTempFile(t, "tx-bad.json", []byte("{not valid json"))
	out, err = runCLIProto(t, node, proto, "tx", "execute", "--file", badPath)
	if err == nil {
		t.Fatalf("[%s] tx execute with malformed JSON file succeeded, want error (output: %s)", proto.name, out)
	}
}

// txReadActivation bounds the wait for the transaction-reads feature to
// activate across the cluster after it boots.
const txReadActivation = 90 * time.Second

func TestTxRead_CLI_ClusterPerProtocol(t *testing.T) {
	t.Parallel()
	nodes, _ := startMultiProtocolCluster(t, 0)

	for i, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			testTxReadOverProtocol(t, nodes, i, proto)
		})
	}
}

func testTxReadOverProtocol(t *testing.T, nodes [3]*multiProtoNode, writer int, proto multiProtocol) {
	t.Helper()
	w := nodes[writer%len(nodes)]
	prefix := "txread/" + proto.name + "/"
	key := prefix + "key"
	get := transport.Op{Type: transport.OpGet, Key: []byte(key)}

	for _, v := range []string{"v1", "v2"} {
		if out, err := runCLIProto(t, w, proto, "kv", "put", key, v); err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, v, err, out)
		}
	}

	// Every node answers a read-only transaction with the same key-value.
	var seen *transport.MVCCKeyValue
	for n, node := range nodes {
		resp := txReadWhenActive(t, node, proto, transport.TransactionRequest{SuccessOps: []transport.Op{get}})
		got := txReadOne(t, proto, resp, 0)
		if string(got.Value) != "v2" || got.Version != 2 || got.CreateRevision == 0 || got.ModRevision <= got.CreateRevision {
			t.Fatalf("[%s] node %d read = %+v, want v2 at version 2 with a later mod revision", proto.name, n, got)
		}
		if seen != nil && !sameMVCCKeyValue(got, seen) {
			t.Fatalf("[%s] node %d read = %+v, node 0 read %+v", proto.name, n, got, seen)
		}
		seen = got
	}

	// Read, write and read again in one branch, guarded by the version read.
	resp, out, err := txExecuteFile(t, w, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(key), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: seen.Version},
		},
		SuccessOps: []transport.Op{get, {Type: transport.OpPut, Key: []byte(key), Value: []byte("v3")}, get},
		FailureOps: []transport.Op{get},
	})
	if err != nil || !resp.Succeeded || len(resp.Responses) != 3 {
		t.Fatalf("[%s] tx read-write-read: succeeded=%t responses=%d err=%v, want the success branch's 3 answers\noutput: %s",
			proto.name, resp.Succeeded, len(resp.Responses), err, out)
	}
	if before := txReadOne(t, proto, resp, 0); !sameMVCCKeyValue(before, seen) {
		t.Fatalf("[%s] read before the put = %+v, want %+v", proto.name, before, seen)
	}
	if len(resp.Responses[1].Kvs) != 0 {
		t.Fatalf("[%s] the put's answer carries key-values: %+v", proto.name, resp.Responses[1])
	}
	after := txReadOne(t, proto, resp, 2)
	if string(after.Value) != "v3" || after.Version != seen.Version+1 ||
		after.ModRevision != resp.Revision || after.CreateRevision != seen.CreateRevision {
		t.Fatalf("[%s] read after the put = %+v, want v3 at version %d, mod revision %d, create revision %d",
			proto.name, after, seen.Version+1, resp.Revision, seen.CreateRevision)
	}

	// An intervening write sends the same compare to the failure branch,
	// which reads the current value back.
	if out, err := runCLIProto(t, w, proto, "kv", "put", key, "intervening"); err != nil {
		t.Fatalf("[%s] intervening kv put: %v\noutput: %s", proto.name, err, out)
	}
	resp, out, err = txExecuteFile(t, w, proto, transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte(key), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: after.Version},
		},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte(key), Value: []byte("lost-update")}},
		FailureOps: []transport.Op{get},
	})
	if err != nil || resp.Succeeded || len(resp.Responses) != 1 {
		t.Fatalf("[%s] tx after an intervening write: succeeded=%t responses=%d err=%v, want the failure branch's answer\noutput: %s",
			proto.name, resp.Succeeded, len(resp.Responses), err, out)
	}
	if cur := txReadOne(t, proto, resp, 0); string(cur.Value) != "intervening" || cur.Version != after.Version+1 {
		t.Fatalf("[%s] failure-branch read = %+v, want intervening at version %d", proto.name, cur, after.Version+1)
	}

	// A range answers every key in it, in key order.
	for _, k := range []string{"r/a", "r/b"} {
		if out, err := runCLIProto(t, w, proto, "kv", "put", prefix+k, k); err != nil {
			t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, k, err, out)
		}
	}
	resp, out, err = txExecuteFile(t, w, proto, transport.TransactionRequest{
		SuccessOps: []transport.Op{{Type: transport.OpRange, Key: []byte(prefix + "r/"), RangeEnd: []byte(prefix + "r0")}},
	})
	if err != nil || len(resp.Responses) != 1 || resp.Responses[0].Count != 2 || len(resp.Responses[0].Kvs) != 2 {
		t.Fatalf("[%s] tx range: %+v err=%v, want one answer of 2 keys\noutput: %s", proto.name, resp, err, out)
	}
	if a, b := string(resp.Responses[0].Kvs[0].Key), string(resp.Responses[0].Kvs[1].Key); a != prefix+"r/a" || b != prefix+"r/b" {
		t.Fatalf("[%s] tx range keys = %q, %q, want %q, %q", proto.name, a, b, prefix+"r/a", prefix+"r/b")
	}
}

// txReadWhenActive executes a read-only transaction on node, retrying only
// while the cluster has not yet activated the transaction-reads feature.
func txReadWhenActive(t *testing.T, node *multiProtoNode, proto multiProtocol, req transport.TransactionRequest) transport.TransactionResponse {
	t.Helper()
	var (
		resp transport.TransactionResponse
		out  string
		err  error
	)
	pollUntil(txReadActivation, 250*time.Millisecond, func() bool {
		resp, out, err = txExecuteFile(t, node, proto, req)
		return err == nil || !strings.Contains(out, "is not active yet")
	})
	if err != nil {
		t.Fatalf("[%s] read-only tx: %v\noutput: %s", proto.name, err, out)
	}
	return resp
}

// txReadOne returns the single key-value answered by op i of resp.
func txReadOne(t *testing.T, proto multiProtocol, resp transport.TransactionResponse, i int) *transport.MVCCKeyValue {
	t.Helper()
	if i >= len(resp.Responses) || resp.Responses[i].Count != 1 || len(resp.Responses[i].Kvs) != 1 {
		t.Fatalf("[%s] answer %d of %+v, want one key-value", proto.name, i, resp)
	}
	return resp.Responses[i].Kvs[0]
}

// sameMVCCKeyValue reports whether a and b are the same key-value at the same
// MVCC position.
func sameMVCCKeyValue(a, b *transport.MVCCKeyValue) bool {
	return string(a.Key) == string(b.Key) && string(a.Value) == string(b.Value) &&
		a.CreateRevision == b.CreateRevision && a.ModRevision == b.ModRevision &&
		a.Version == b.Version && a.LeaseID == b.LeaseID
}
