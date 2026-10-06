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

//go:build qrdb_embedded && !integration

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// writePEMFiles (certs.go; replaced writeCertBundle when server-side keygen
// was retired -- see cmd/qrdb/certs.go newCertsIssueCmd)
// ---------------------------------------------------------------------------

func TestWritePEMFiles_Success(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	files := map[string][]byte{
		"cert.pem": []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"),
		"key.pem":  []byte("-----BEGIN EC PRIVATE KEY-----\ntest\n-----END EC PRIVATE KEY-----\n"),
	}
	if err := writePEMFiles(dir, files); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	certData, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatalf("failed to read cert.pem: %v", err)
	}
	if string(certData) != string(files["cert.pem"]) {
		t.Fatalf("cert.pem content mismatch: got %q", certData)
	}

	keyPath := filepath.Join(dir, "key.pem")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read key.pem: %v", err)
	}
	if string(keyData) != string(files["key.pem"]) {
		t.Fatalf("key.pem content mismatch: got %q", keyData)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key.pem: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected key.pem mode 0600, got %o", perm)
	}
}

func TestWritePEMFiles_DirectoryCreateError(t *testing.T) {
	// Use a path under /dev/null to force MkdirAll to fail.
	err := writePEMFiles("/dev/null/impossible", map[string][]byte{"cert.pem": []byte("x")})
	if err == nil {
		t.Fatal("expected error when directory cannot be created")
	}
	var target *DirectoryCreateError
	if !errors.As(err, &target) {
		t.Fatalf("expected *DirectoryCreateError, got %T: %v", err, err)
	}
}

func TestWritePEMFiles_WriteError(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "certs")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Place a directory where cert.pem would be written - WriteFile will fail
	// because the path is a directory (works even as root, unlike chmod).
	certPath := filepath.Join(targetDir, "cert.pem")
	if err := os.MkdirAll(certPath, 0700); err != nil {
		t.Fatalf("mkdir cert.pem: %v", err)
	}

	err := writePEMFiles(targetDir, map[string][]byte{"cert.pem": []byte("cert")})
	if err == nil {
		t.Fatal("expected error when cert.pem cannot be written")
	}
	var target *FileWriteError
	if !errors.As(err, &target) {
		t.Fatalf("expected *FileWriteError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Batch execute commands
// ---------------------------------------------------------------------------

func TestBatchExecuteCmd_TableOutput(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(&mockBatchService{}))

	// Create a temp file with valid batch JSON.
	tmpFile := filepath.Join(t.TempDir(), "batch.json")
	batchReq := transport.BatchRequest{
		Ops: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		},
	}
	data, _ := json.Marshal(batchReq)
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		t.Fatalf("write batch file: %v", err)
	}

	output, err := executeCommand(root, "batch", "execute", "--file", tmpFile, "--output", "table")
	if err != nil {
		t.Fatalf("expected no error, got: %v\noutput: %s", err, output)
	}
	if !strings.Contains(output, "Response[0]") {
		t.Fatalf("expected table response output, got: %q", output)
	}
}

func TestBatchExecuteCmd_InvalidJSON(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(&mockBatchService{}))

	tmpFile := filepath.Join(t.TempDir(), "batch.json")
	if err := os.WriteFile(tmpFile, []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("write batch file: %v", err)
	}

	_, err := executeCommand(root, "batch", "execute", "--file", tmpFile)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBatchExecuteCmd_FileNotFound(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBatchService(&mockBatchService{}))

	_, err := executeCommand(root, "batch", "execute", "--file", "/nonexistent/batch.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected *FileReadError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Transaction execute commands
// ---------------------------------------------------------------------------

func TestTxExecuteCmd_TableOutput(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(&mockTransactionService{}))

	tmpFile := filepath.Join(t.TempDir(), "tx.json")
	txReq := transport.TransactionRequest{
		Compares:   []transport.Compare{},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")}},
		FailureOps: []transport.Op{},
	}
	data, _ := json.Marshal(txReq)
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		t.Fatalf("write tx file: %v", err)
	}

	output, err := executeCommand(root, "tx", "execute", "--file", tmpFile, "--output", "table")
	if err != nil {
		t.Fatalf("expected no error, got: %v\noutput: %s", err, output)
	}
	if !strings.Contains(output, "Succeeded") {
		t.Fatalf("expected Succeeded in table output, got: %q", output)
	}
}

func TestTxExecuteCmd_FileNotFound(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(&mockTransactionService{}))

	_, err := executeCommand(root, "tx", "execute", "--file", "/nonexistent/tx.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected *FileReadError, got %T: %v", err, err)
	}
}

func TestTxExecuteCmd_InvalidJSON(t *testing.T) {
	root := newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithTransactionService(&mockTransactionService{}))

	tmpFile := filepath.Join(t.TempDir(), "tx.json")
	if err := os.WriteFile(tmpFile, []byte("{bad json"), 0600); err != nil {
		t.Fatalf("write tx file: %v", err)
	}

	_, err := executeCommand(root, "tx", "execute", "--file", tmpFile)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// certRecordRow
// ---------------------------------------------------------------------------

func TestCertRecordRow_AllFields(t *testing.T) {
	rec := &transport.CertRecord{
		Serial:    "ABC123",
		Username:  "alice",
		TenantID:  "system",
		IssuedAt:  "2026-01-01T00:00:00Z",
		ExpiresAt: "2027-01-01T00:00:00Z",
		Revoked:   false,
	}
	row := certRecordRow(rec)
	if len(row) != 6 {
		t.Fatalf("expected 6 columns, got %d", len(row))
	}
	if row[0] != "ABC123" {
		t.Fatalf("expected serial ABC123, got %q", row[0])
	}
	if row[1] != "alice" {
		t.Fatalf("expected username alice, got %q", row[1])
	}
	if row[5] != "false" {
		t.Fatalf("expected revoked false, got %q", row[5])
	}
}

func TestCertRecordRow_Revoked(t *testing.T) {
	rec := &transport.CertRecord{
		Serial:    "DEF456",
		Username:  "bob",
		TenantID:  "tenant-2",
		IssuedAt:  "2025-06-01T00:00:00Z",
		ExpiresAt: "2026-06-01T00:00:00Z",
		Revoked:   true,
	}
	row := certRecordRow(rec)
	if row[5] != "true" {
		t.Fatalf("expected revoked true, got %q", row[5])
	}
	if row[3] != "2025-06-01T00:00:00Z" {
		t.Fatalf("expected issuedAt, got %q", row[3])
	}
}

// ---------------------------------------------------------------------------
// printEnrolledCert (certs.go; replaced printCertBundle when server-side
// keygen was retired -- there is no key to print any more)
// ---------------------------------------------------------------------------

func TestPrintEnrolledCert_DefaultFormat(t *testing.T) {
	origWriter := outputWriter
	origFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = origWriter
		outputFormat = origFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "table"

	e := &transport.EnrolledCert{
		Username:   "alice",
		TenantID:   "system",
		Serial:     "SER123",
		Thumbprint: "thumb123",
		CertPEM:    "-----BEGIN CERTIFICATE-----\nDATA\n-----END CERTIFICATE-----\n",
	}
	if err := printEnrolledCert(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "Serial: SER123") {
		t.Fatalf("expected serial in output, got: %q", output)
	}
	if !strings.Contains(output, "--- Certificate ---") {
		t.Fatalf("expected certificate header, got: %q", output)
	}
	if strings.Contains(output, "PRIVATE KEY") {
		t.Fatalf("expected no private key in output: the server never holds one, got: %q", output)
	}
}

func TestPrintEnrolledCert_JSONFormat(t *testing.T) {
	origWriter := outputWriter
	origFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = origWriter
		outputFormat = origFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "json"

	e := &transport.EnrolledCert{
		Username: "alice",
		Serial:   "JSON-SERIAL",
		CertPEM:  "cert-pem-data",
	}
	if err := printEnrolledCert(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "JSON-SERIAL") {
		t.Fatalf("expected serial in JSON output, got: %q", output)
	}
}

func TestPrintEnrolledCert_YAMLFormat(t *testing.T) {
	origWriter := outputWriter
	origFormat := outputFormat
	t.Cleanup(func() {
		outputWriter = origWriter
		outputFormat = origFormat
	})

	var buf bytes.Buffer
	outputWriter = &buf
	outputFormat = "yaml"

	e := &transport.EnrolledCert{
		Username: "alice",
		Serial:   "YAML-SERIAL",
		CertPEM:  "yaml-cert",
	}
	if err := printEnrolledCert(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "YAML-SERIAL") {
		t.Fatalf("expected serial in YAML output, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// printList
// ---------------------------------------------------------------------------

func TestPrintList_TableFormat(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	items := []string{"alpha", "beta", "gamma"}
	if err := printList(items, "table"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	for _, item := range items {
		if !strings.Contains(output, item) {
			t.Fatalf("expected %q in output, got: %q", item, output)
		}
	}
}

func TestPrintList_JSONFormat(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	items := []string{"one", "two"}
	if err := printList(items, "json"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, `"one"`) {
		t.Fatalf("expected JSON array item, got: %q", output)
	}
}

func TestPrintList_YAMLFormat(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	items := []string{"x", "y"}
	if err := printList(items, "yaml"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "- x") {
		t.Fatalf("expected YAML sequence, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// printValue and printOK
// ---------------------------------------------------------------------------

func TestPrintValue_WritesLine(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	printValue("hello-world")
	output := buf.String()
	if !strings.Contains(output, "hello-world") {
		t.Fatalf("expected hello-world in output, got: %q", output)
	}
	if !strings.HasSuffix(output, "\n") {
		t.Fatal("expected trailing newline")
	}
}

func TestPrintOK_WritesOK(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	printOK()
	output := buf.String()
	if strings.TrimSpace(output) != "OK" {
		t.Fatalf("expected OK, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// printTable
// ---------------------------------------------------------------------------

func TestPrintTable_EmptyRows(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	printTable([]string{"A", "B"}, nil)
	output := buf.String()
	if !strings.Contains(output, "A") {
		t.Fatalf("expected header A in output, got: %q", output)
	}
}

func TestPrintTable_WithRows(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	headers := []string{"KEY", "VALUE"}
	rows := [][]string{{"foo", "bar"}, {"baz", "qux"}}
	printTable(headers, rows)
	output := buf.String()
	if !strings.Contains(output, "foo") {
		t.Fatalf("expected foo in output, got: %q", output)
	}
	if !strings.Contains(output, "qux") {
		t.Fatalf("expected qux in output, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// printJSON and printYAML
// ---------------------------------------------------------------------------

func TestPrintJSON_Success(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	data := map[string]string{"key": "value"}
	if err := printJSON(data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, `"key"`) {
		t.Fatalf("expected key in JSON output, got: %q", output)
	}
}

func TestPrintYAML_Success(t *testing.T) {
	origWriter := outputWriter
	t.Cleanup(func() { outputWriter = origWriter })

	var buf bytes.Buffer
	outputWriter = &buf

	data := map[string]string{"key": "value"}
	if err := printYAML(data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "key: value") {
		t.Fatalf("expected key: value in YAML output, got: %q", output)
	}
}

// ---------------------------------------------------------------------------
// EmbeddedServerBinding: startup/shutdown lifecycle
// ---------------------------------------------------------------------------

func TestEmbeddedServerBinding_Startup(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewEmbeddedServerBinding(testLogger(t), conn, NewSQLBinding(testLogger(t), conn))

	// startup should store the context.
	ctx := t.Context()
	b.startup(ctx)
	// No panic and no error means the method executed correctly.
	if b.IsEmbedded() {
		t.Fatal("expected IsEmbedded=false after startup without StartEmbedded")
	}
}

func TestEmbeddedServerBinding_ShutdownWhenIdle(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewEmbeddedServerBinding(testLogger(t), conn, NewSQLBinding(testLogger(t), conn))

	// shutdown when not running should not panic.
	b.shutdown(t.Context())
	if b.IsEmbedded() {
		t.Fatal("expected IsEmbedded=false after shutdown")
	}
}

// ---------------------------------------------------------------------------
// resolveStorageSM additional tests
// ---------------------------------------------------------------------------

func TestResolveStorageSM_MVCCBackend(t *testing.T) {
	result := resolveStorageSM("mvcc")
	if result != "mvcc" {
		t.Fatalf("expected mvcc, got %q", result)
	}
}

func TestResolveStorageSM_FileBackend(t *testing.T) {
	result := resolveStorageSM("file")
	if result != "pebble" {
		t.Fatalf("expected pebble for file backend, got %q", result)
	}
}

func TestResolveStorageSM_RaftBackend(t *testing.T) {
	result := resolveStorageSM("raft")
	if result != "pebble" {
		t.Fatalf("expected pebble for raft backend, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// validateOutputFormat
// ---------------------------------------------------------------------------

func TestValidateOutputFormat_ValidFormats(t *testing.T) {
	formats := []string{"json", "yaml", "table"}
	for _, f := range formats {
		t.Run(f, func(t *testing.T) {
			err := validateOutputFormat(f)
			if err != nil {
				t.Fatalf("unexpected error for format %q: %v", f, err)
			}
		})
	}
}

func TestValidateOutputFormat_InvalidFormat(t *testing.T) {
	err := validateOutputFormat("xml")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	var target *OutputFormatError
	if !errors.As(err, &target) {
		t.Fatalf("expected *OutputFormatError, got %T: %v", err, err)
	}
}
