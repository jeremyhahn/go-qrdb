// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// batch_import_cli_test.go exercises the `qrdb batch import` and
// `qrdb batch import-formats` CLI surface (cmd/qrdb/batch.go) end-to-end
// against a real qrdbd node: TestBatchImport_CLI_PerProtocol exercises
// both commands over all five CLI-selectable transports (REST, gRPC, QUIC,
// MCP, Unix) against one shared multiProtoNode.
package integration_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runQRDBStdin runs the qrdb CLI binary with connArgs+subArgs, feeding
// stdin to the process, and returns its combined output. It mirrors
// runCLI/runQRDB (helpers_test.go / enable_encryption_cli_test.go) but
// additionally wires stdin, which neither of those helpers do.
func runQRDBStdin(t *testing.T, cli, stdin string, connArgs []string, subArgs ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append(append([]string{}, connArgs...), subArgs...)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestBatchImport_CLI_PerProtocol drives `qrdb batch import` (CSV and
// stdin/ndjson forms) and `qrdb batch import-formats` through the CLI
// binary against every transport sharing one multiProtoNode. Neither
// command is identity-gated, so every transport takes the same path.
func TestBatchImport_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBatchImportOverProtocol(t, node, proto)
		})
	}
}

// testBatchImportOverProtocol exercises `batch import` (CSV and NDJSON
// stdin forms) and `batch import-formats` against a single protocol. Each
// subtest uses a key prefix scoped to proto.name so the five subtests can
// safely share one multiProtoNode.
func testBatchImportOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	prefix := "batchimport/" + proto.name + "/"
	keyA := prefix + "k1"
	keyB := prefix + "k2"

	csvPath := writeTempFile(t, "import-"+proto.name+".csv",
		[]byte("id,value\n"+keyA+",v1\n"+keyB+",v2\n"))

	out, err := runCLIProto(t, node, proto, "batch", "import",
		"--file", csvPath, "--format", "csv", "--key-field", "id", "--value-mode", "row",
		"--skip-header", "--output", "json")
	if err != nil {
		t.Fatalf("[%s] batch import (csv): %v\noutput: %s", proto.name, err, out)
	}
	var result struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	decodeJSON(t, out, &result)
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 {
		t.Fatalf("[%s] unexpected import result: %+v (output: %s)", proto.name, result, out)
	}

	getOut, err := runCLIProto(t, node, proto, "kv", "get", keyA)
	if err != nil {
		t.Fatalf("[%s] kv get %s after csv import: %v\noutput: %s", proto.name, keyA, err, getOut)
	}
	if !strings.Contains(getOut, keyA) {
		t.Errorf("[%s] expected the imported row's key to appear in the row-mode value, got: %s", proto.name, getOut)
	}

	// --- stdin/ndjson form. ---------------------------------------------
	keyStdin := prefix + "s1"
	out, err = runCLIProtoStdin(t, node, proto, `{"id":"`+keyStdin+`","value":"a"}`+"\n",
		"batch", "import", "--file", "-", "--format", "ndjson",
		"--key-field", "id", "--value-mode", "row", "--output", "json")
	if err != nil {
		t.Fatalf("[%s] batch import --file - (ndjson stdin): %v\noutput: %s", proto.name, err, out)
	}
	var stdinResult struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
	}
	decodeJSON(t, out, &stdinResult)
	if stdinResult.Total != 1 || stdinResult.Succeeded != 1 {
		t.Fatalf("[%s] unexpected stdin import result: %+v (output: %s)", proto.name, stdinResult, out)
	}

	// --- import-formats. --------------------------------------------------
	out, err = runCLIProto(t, node, proto, "batch", "import-formats")
	if err != nil {
		t.Fatalf("[%s] batch import-formats: %v\noutput: %s", proto.name, err, out)
	}
	for _, want := range []string{"auto", "csv", "json", "ndjson", "yaml", "archive"} {
		if !strings.Contains(out, want) {
			t.Errorf("[%s] expected format %q in import-formats output, got: %s", proto.name, want, out)
		}
	}
}
