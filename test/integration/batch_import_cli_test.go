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
// against a real, freshly-spawned qrdbd node. TestCLI_BatchImport_CSV,
// TestCLI_BatchImport_CSV_Unix, TestCLI_BatchImport_Stdin, and
// TestCLI_BatchImportFormats_ListsSupportedFormats below predate the
// multi-protocol harness and only ever drove REST (plus a dedicated
// Unix-only parity check); TestBatchImport_CLI_PerProtocol closes that
// gap by exercising both commands over all five CLI-selectable transports
// (REST, gRPC, QUIC, MCP, Unix) against one shared multiProtoNode.
package integration_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// TestCLI_BatchImport_CSV imports a small CSV file and verifies the
// reported statistics and that the imported records are readable
// afterward via `qrdb kv get`.
func TestCLI_BatchImport_CSV(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[batch-import] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	csvPath := writeTempFile(t, "import.csv", []byte("id,value\nk1,v1\nk2,v2\n"))

	out, err := runQRDB(t, cli, node.restArgsAdmin(), "batch", "import",
		"--file", csvPath, "--format", "csv", "--key-field", "id", "--value-mode", "row",
		"--skip-header", "--output", "json")
	if err != nil {
		t.Fatalf("batch import: %v\noutput: %s", err, out)
	}
	var result struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	decodeJSON(t, out, &result)
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 {
		t.Fatalf("unexpected import result: %+v (output: %s)", result, out)
	}

	getOut, err := runQRDB(t, cli, node.restArgsAdmin(), "kv", "get", "k1")
	if err != nil {
		t.Fatalf("kv get k1 after import: %v\noutput: %s", err, getOut)
	}
	if !strings.Contains(getOut, "k1") {
		t.Errorf("expected the imported row's key to appear in the row-mode value, got: %s", getOut)
	}
}

// TestCLI_BatchImport_CSV_Unix is the multi-transport-parity counterpart of
// TestCLI_BatchImport_CSV: it drives `qrdb batch import` over the Unix
// socket (grpc-over-UDS) transport, which is the qrdb CLI's DEFAULT
// transport (see cmd/qrdb/CLAUDE.md). Prior to the BatchImport gRPC/Unix/
// QUIC/MCP RPC, ImportFile on every transport but REST and embedded
// returned transport.NotImplementedError, so `qrdb batch import` with no
// --transport/--server override (i.e. the CLI's out-of-the-box default)
// failed outright. This test boots the daemon with --enable-unix and
// exercises the identical CSV-import scenario as TestCLI_BatchImport_CSV
// over node.unixArgsAdmin, proving the two transports are at parity.
func TestCLI_BatchImport_CSV_Unix(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	dataDir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "qrdb-batch-import-unix-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	cfgPath := writeSealYAML(t, "seal:\n  enabled: true\n  strategy: none\n")
	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	startNode(t, bin, args, "[batch-import-unix] ")

	node := &selfGenCANode{
		dataDir:       dataDir,
		adminPort:     adminPort,
		grpcPort:      grpcPort,
		raftPort:      raftPort,
		caPath:        filepath.Join(dataDir, "ca.pem"),
		adminCertPath: filepath.Join(dataDir, "admin-cert.pem"),
		adminKeyPath:  filepath.Join(dataDir, "admin-key.pem"),
	}
	waitForFile(t, node.caPath, 30*time.Second)
	waitForFile(t, node.adminCertPath, 30*time.Second)
	waitForFile(t, node.adminKeyPath, 30*time.Second)

	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)
	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)

	unixArgs := node.unixArgsAdmin(unixSocket)
	pollUntilCLISucceeds(t, cli, unixArgs, 60*time.Second)

	csvPath := writeTempFile(t, "import.csv", []byte("id,value\nk1,v1\nk2,v2\n"))

	out, err := runQRDB(t, cli, unixArgs, "batch", "import",
		"--file", csvPath, "--format", "csv", "--key-field", "id", "--value-mode", "row",
		"--skip-header", "--output", "json")
	if err != nil {
		t.Fatalf("batch import over unix: %v\noutput: %s", err, out)
	}
	var result struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
	}
	decodeJSON(t, out, &result)
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 {
		t.Fatalf("unexpected import result: %+v (output: %s)", result, out)
	}

	getOut, err := runQRDB(t, cli, unixArgs, "kv", "get", "k1")
	if err != nil {
		t.Fatalf("kv get k1 after import (unix): %v\noutput: %s", err, getOut)
	}
	if !strings.Contains(getOut, "k1") {
		t.Errorf("expected the imported row's key to appear in the row-mode value, got: %s", getOut)
	}
}

// TestCLI_BatchImport_Stdin verifies the "-" stdin form.
func TestCLI_BatchImport_Stdin(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[batch-import-stdin] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err := runQRDBStdin(t, cli, `{"id":"s1","value":"a"}`+"\n", node.restArgsAdmin(),
		"batch", "import", "--file", "-", "--format", "ndjson",
		"--key-field", "id", "--value-mode", "row", "--output", "json")
	if err != nil {
		t.Fatalf("batch import --file -: %v\noutput: %s", err, out)
	}
	var result struct {
		Total     int `json:"total"`
		Succeeded int `json:"succeeded"`
	}
	decodeJSON(t, out, &result)
	if result.Total != 1 || result.Succeeded != 1 {
		t.Fatalf("unexpected import result: %+v (output: %s)", result, out)
	}
}

// TestCLI_BatchImportFormats_ListsSupportedFormats verifies
// `qrdb batch import-formats` lists the formats `batch import --format`
// accepts.
func TestCLI_BatchImportFormats_ListsSupportedFormats(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[batch-import-formats] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err := runQRDB(t, cli, node.restArgsAdmin(), "batch", "import-formats")
	if err != nil {
		t.Fatalf("batch import-formats: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"auto", "csv", "json", "ndjson", "yaml", "archive"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected format %q in output, got: %s", want, out)
		}
	}
}

// TestBatchImport_CLI_PerProtocol drives `qrdb batch import` (CSV and
// stdin/ndjson forms) and `qrdb batch import-formats` through the CLI
// binary against every transport sharing one multiProtoNode. Neither
// command is identity-gated; TestCLI_BatchImport_CSV/CSV_Unix/Stdin and
// TestCLI_BatchImportFormats_ListsSupportedFormats above only ever
// exercised REST (plus a dedicated Unix-only parity check), leaving
// gRPC/QUIC/MCP unverified for `import`, and REST alone for
// `import-formats`.
func TestBatchImport_CLI_PerProtocol(t *testing.T) {
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
