//go:build !integration

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

// batch_import_cmd_test.go verifies `qrdb batch import` and
// `qrdb batch import-formats` using the in-process embedded transport
// mock pattern (see watch_cmd_test.go / lock_cmd_test.go).
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// fnBatchService implements service.BatchServicer, delegating Import to a
// configurable function field so each test controls the returned
// ImportResult/error. Every other method is inherited from the embedded
// nil interface and panics if invoked, keeping the mock honest about what
// 'batch import' actually calls. 'batch import' drives Import through the
// chunked session (ImportBegin/ImportAppend/ImportCommit), so this double
// stages ImportAppend's chunks per session and hands the assembled file to
// importFn from ImportCommit, exactly as the real session does.
type fnBatchService struct {
	service.BatchServicer
	importFn func(ctx context.Context, data io.Reader, opts service.ImportOptions) (*service.ImportResult, error)

	mu          sync.Mutex
	sessions    map[string]*fnImportSession
	nextSession int
}

// fnImportSession accumulates the chunks ImportAppend stages for a session
// started by ImportBegin.
type fnImportSession struct {
	opts service.ImportOptions
	buf  []byte
}

func (m *fnBatchService) Import(ctx context.Context, data io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	return m.importFn(ctx, data, opts)
}

func (m *fnBatchService) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sessions == nil {
		m.sessions = make(map[string]*fnImportSession)
	}
	m.nextSession++
	sessionID := strconv.Itoa(m.nextSession)
	m.sessions[sessionID] = &fnImportSession{opts: opts}
	return sessionID, nil
}

func (m *fnBatchService) ImportAppend(_ context.Context, sessionID string, _ uint64, chunk []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	sess.buf = append(sess.buf, chunk...)
	return nil
}

func (m *fnBatchService) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	return m.importFn(ctx, bytes.NewReader(sess.buf), sess.opts)
}

func (m *fnBatchService) ImportAbort(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[sessionID]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(m.sessions, sessionID)
	return nil
}

// SetImportKeyGuard satisfies service.ImportKeyGuardSetter as a no-op:
// this double tests the "batch import" command's own plumbing (flags,
// output formatting, error propagation), never the hard-confinement
// feature, and its fixtures never name the confined key -- a deliberate,
// visible opt-out for this one concern, not an accidental one.
func (m *fnBatchService) SetImportKeyGuard(func(string) error) {}

// executeBatchCmd builds an embedded SDK client over batchSvc, installs it
// as the package-level client, and executes the "batch" command tree with
// args.
func executeBatchCmd(t *testing.T, batchSvc service.BatchServicer, format string, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(),
		embedded.WithBatchService(batchSvc),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient := client
	oldWriter := outputWriter
	oldFormat := outputFormat
	t.Cleanup(func() {
		client = oldClient
		outputWriter = oldWriter
		outputFormat = oldFormat
	})

	client = sdkClient
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newBatchCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)

	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

// writeBatchTempFile writes content to a file in a per-test temp dir and
// returns its path.
func writeBatchTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// batch import
// ---------------------------------------------------------------------------

func TestBatchImportCmd_TextOutput(t *testing.T) {
	path := writeBatchTempFile(t, "data.csv", []byte("id,val\n1,a\n2,b\n"))

	var gotOpts service.ImportOptions
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
			gotOpts = opts
			return &service.ImportResult{Total: 2, Succeeded: 2, Failed: 0}, nil
		},
	}

	out, err := executeBatchCmd(t, svc, "table", "import",
		"--file", path, "--key-field", "id", "--value-mode", "row")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.KeyField != "id" || gotOpts.ValueMode != "row" || gotOpts.Filename != "data.csv" {
		t.Errorf("import options not forwarded correctly: %+v", gotOpts)
	}
	if !strings.Contains(out, "Total:     2") || !strings.Contains(out, "Succeeded: 2") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestBatchImportCmd_JSONOutput(t *testing.T) {
	path := writeBatchTempFile(t, "data.json", []byte(`[{"id":"1","val":"a"}]`))
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{Total: 1, Succeeded: 1}, nil
		},
	}

	out, err := executeBatchCmd(t, svc, "json", "import", "--file", path, "--key-field", "id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\"total\"") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

func TestBatchImportCmd_WithErrors(t *testing.T) {
	path := writeBatchTempFile(t, "data.csv", []byte("id,val\n1,a\n"))
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{
				Total: 1, Succeeded: 0, Failed: 1,
				Errors: []service.ImportError{{Row: 1, Key: "1", Message: "boom"}},
			}, nil
		},
	}

	out, err := executeBatchCmd(t, svc, "table", "import", "--file", path, "--key-field", "id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "row=1 key=1: boom") {
		t.Errorf("expected per-row error line, got: %q", out)
	}
}

func TestBatchImportCmd_StdinForm(t *testing.T) {
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { os.Stdin = oldStdin })
	os.Stdin = r
	go func() {
		_, _ = w.WriteString("id,val\n1,a\n")
		_ = w.Close()
	}()

	var gotFilename string
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
			gotFilename = opts.Filename
			return &service.ImportResult{Total: 1, Succeeded: 1}, nil
		},
	}

	out, err := executeBatchCmd(t, svc, "table", "import", "--file", "-", "--key-field", "id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotFilename != "" {
		t.Errorf("expected empty filename for stdin import, got %q", gotFilename)
	}
	if !strings.Contains(out, "Total:     1") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestBatchImportCmd_MissingFileFlag(t *testing.T) {
	svc := &fnBatchService{}
	_, err := executeBatchCmd(t, svc, "table", "import")
	if err == nil {
		t.Fatal("expected an error when --file is not supplied")
	}
}

func TestBatchImportCmd_FileNotFound(t *testing.T) {
	svc := &fnBatchService{}
	_, err := executeBatchCmd(t, svc, "table", "import",
		"--file", filepath.Join(t.TempDir(), "missing.csv"), "--key-field", "id")
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	var fileErr *FileOpenError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileOpenError, got: %v (%T)", err, err)
	}
}

// TestBatchImportCmd_CollectionFlagRemoved verifies that --collection is no
// longer a recognized flag on `batch import`: no transport wires it through
// to the server, so accepting it would silently misroute the import into
// the raw KV keyspace instead of the requested collection (see H2). The
// flag must be rejected by cobra at parse time, not silently ignored.
func TestBatchImportCmd_CollectionFlagRemoved(t *testing.T) {
	path := writeBatchTempFile(t, "data.csv", []byte("id,val\n1,a\n"))
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{}, nil
		},
	}

	_, err := executeBatchCmd(t, svc, "table", "import",
		"--file", path, "--key-field", "id", "--collection", "accounts")
	if err == nil {
		t.Fatal("expected an error for the removed --collection flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected an 'unknown flag' error, got: %v", err)
	}
}

// TestBatchImportCmd_LocalDatabaseFlagRemoved verifies that the redundant
// local --database flag on `batch import` was removed: the global root
// --database flag already scopes /batch (a data-plane endpoint) for the
// whole command invocation.
func TestBatchImportCmd_LocalDatabaseFlagRemoved(t *testing.T) {
	path := writeBatchTempFile(t, "data.csv", []byte("id,val\n1,a\n"))
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{}, nil
		},
	}

	_, err := executeBatchCmd(t, svc, "table", "import",
		"--file", path, "--key-field", "id", "--database", "accounts")
	if err == nil {
		t.Fatal("expected an error for the removed local --database flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected an 'unknown flag' error, got: %v", err)
	}
}

func TestBatchImportCmd_ServiceError(t *testing.T) {
	path := writeBatchTempFile(t, "data.csv", []byte("id,val\n1,a\n"))
	errBoom := errors.New("import boom")
	svc := &fnBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, errBoom
		},
	}

	_, err := executeBatchCmd(t, svc, "table", "import", "--file", path, "--key-field", "id")
	if !errors.Is(err, errBoom) {
		t.Errorf("expected service error to propagate, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// batch import-formats
// ---------------------------------------------------------------------------

func TestBatchImportFormatsCmd_TableOutput(t *testing.T) {
	out, err := executeBatchCmd(t, &fnBatchService{}, "table", "import-formats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"auto", "csv", "json", "ndjson", "yaml", "archive"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected format %q in output, got: %q", want, out)
		}
	}
}

func TestBatchImportFormatsCmd_JSONOutput(t *testing.T) {
	out, err := executeBatchCmd(t, &fnBatchService{}, "json", "import-formats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "csv") {
		t.Errorf("unexpected JSON output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Verb-tree wiring
// ---------------------------------------------------------------------------

func TestBatchCmd_ImportVerbsRegistered(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"batch", "import"},
		{"batch", "import-formats"},
	} {
		if _, _, err := root.Find(path); err != nil {
			t.Errorf("verb %v not registered: %v", path, err)
		}
	}
}
