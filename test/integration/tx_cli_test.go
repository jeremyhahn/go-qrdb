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
// give transactions their value over plain kv put.
package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTx_CLI_PerProtocol drives `qrdb tx execute` -- CAS success, CAS
// failure, put-if-not-exists (success and failure), multi-compare AND
// semantics, stdin input, and CLI-side validation errors -- through the CLI
// binary against every transport sharing one multiProtoNode.
func TestTx_CLI_PerProtocol(t *testing.T) {
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
	// holds, and (per the fail-closed rejection of reads in a transaction
	// branch, see the dedicated block below) a Get is no longer accepted
	// as a placeholder op in either branch, executed or not. ------------
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

	// --- fail-closed rejection of reads in a transaction branch: a
	// transaction whose successOps or failureOps list contains an OpGet or
	// OpRange must be rejected as a client error rather than silently
	// coerced into a write. Compares are evaluated server-side against
	// live state, so the client cannot know in advance which branch will
	// actually execute -- both op lists are validated up front, whether or
	// not the branch they belong to ends up selected. This was previously
	// a data-corruption bug when the invalid op landed in the *executed*
	// branch (pkg/service/transaction_service.go's encodeServiceOp
	// defaulted unmapped op types, including OpGet/OpRange, to
	// protocol.OpMVCCPut, so an executed Get overwrote the target key with
	// an empty value). The fix rejects any non-Put/Delete branch op with a
	// typed UnsupportedTransactionOpError before the command is ever
	// encoded or proposed to Raft, mapped to a 400-class error on every
	// transport. This is shared pkg/service logic (not transport wiring),
	// so the rejection reproduces identically on all 5 transports.
	// Contrast with `batch execute` (pkg/service/batch_service.go's
	// Execute/ExecuteWithMode), which explicitly special-cases OpGet/
	// OpRange via executeGet/executeRange as real linearizable reads
	// BEFORE encoding the write ops (see batch_cli_test.go's mixed-batch
	// Get assertion) -- transactions have no such per-op read-result
	// channel on the wire, so a read in a branch is unsupported rather
	// than executed.
	keyOpGetRejected := prefix + "get-in-executed-ops-rejected"
	out, err = runCLIProto(t, node, proto, "kv", "put", keyOpGetRejected, "untouched")
	if err != nil {
		t.Fatalf("[%s] seed kv put %s (OpGet-in-branch rejection): %v\noutput: %s", proto.name, keyOpGetRejected, err, out)
	}
	_, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		// No compares: the transaction always "succeeds" and successOps
		// -- containing a Get -- would be the executed branch, if it were
		// ever proposed. It must instead be rejected up front.
		SuccessOps: []transport.Op{
			{Type: transport.OpGet, Key: []byte(keyOpGetRejected)},
		},
	})
	if err == nil {
		t.Fatalf("[%s] tx execute (OpGet in successOps): succeeded, want rejected (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyOpGetRejected)
	if err != nil || strings.TrimSpace(out) != "untouched" {
		t.Fatalf("[%s] kv get %s after rejected OpGet-in-branch tx = %q, err=%v, want unchanged %q", proto.name, keyOpGetRejected, out, err, "untouched")
	}

	// Same rejection for OpRange in a branch (etcd-style range-as-Get with
	// RangeEnd set).
	keyOpRangeRejected := prefix + "range-in-executed-ops-rejected"
	out, err = runCLIProto(t, node, proto, "kv", "put", keyOpRangeRejected, "untouched")
	if err != nil {
		t.Fatalf("[%s] seed kv put %s (OpRange-in-branch rejection): %v\noutput: %s", proto.name, keyOpRangeRejected, err, out)
	}
	_, out, err = txExecuteFile(t, node, proto, transport.TransactionRequest{
		SuccessOps: []transport.Op{
			{Type: transport.OpRange, Key: []byte(keyOpRangeRejected), RangeEnd: []byte(keyOpRangeRejected + "\xff")},
		},
	})
	if err == nil {
		t.Fatalf("[%s] tx execute (OpRange in successOps): succeeded, want rejected (output: %s)", proto.name, out)
	}
	out, err = runCLIProto(t, node, proto, "kv", "get", keyOpRangeRejected)
	if err != nil || strings.TrimSpace(out) != "untouched" {
		t.Fatalf("[%s] kv get %s after rejected OpRange-in-branch tx = %q, err=%v, want unchanged %q", proto.name, keyOpRangeRejected, out, err, "untouched")
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
