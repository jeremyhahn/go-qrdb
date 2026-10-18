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

package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/mvcchost"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockBatchSvc implements service.BatchServicer for transport tests.
// ---------------------------------------------------------------------------

type mockBatchSvc struct {
	mu sync.Mutex

	executeErr     error
	executeModeErr error
	importErr      error
	revision       int64 // the batch revision Execute reports

	lastOps        []service.Op
	lastMode       writemode.Mode
	lastImportFile []byte
	lastImportOpts service.ImportOptions

	resp       []service.OpResponse
	importResp *service.ImportResult

	sessions    map[string]*mockImportSession
	nextSession int
}

// mockImportSession accumulates the chunks ImportAppend stages for a session
// started by ImportBegin, so ImportCommit can hand the assembled file to
// Import exactly as the real chunked session does.
type mockImportSession struct {
	opts service.ImportOptions
	buf  []byte
}

var _ service.BatchServicer = (*mockBatchSvc)(nil)

func newMockBatchSvc() *mockBatchSvc {
	return &mockBatchSvc{
		resp: []service.OpResponse{
			{Succeeded: true, Count: 1},
		},
	}
}

func (m *mockBatchSvc) Execute(_ context.Context, ops []service.Op) (*service.BatchResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOps = ops
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	return &service.BatchResponse{Revision: m.revision, Responses: m.resp}, nil
}

func (m *mockBatchSvc) ExecuteWithMode(_ context.Context, ops []service.Op, mode writemode.Mode) (*service.BatchResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOps = ops
	m.lastMode = mode
	if m.executeModeErr != nil {
		return nil, m.executeModeErr
	}
	return &service.BatchResponse{Revision: m.revision, Responses: m.resp}, nil
}

func (m *mockBatchSvc) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	return &service.BatchEntityResponse{}, nil
}

func connectBatchInProcess(t *testing.T, svc service.BatchServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithBatch(svc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// TestTransport_ExecuteBatch_RevisionFromRealEngine drives a batch through the
// MCP server into a real MVCC engine and checks that the revision the engine
// assigned reaches the client unchanged.
func TestTransport_ExecuteBatch_RevisionFromRealEngine(t *testing.T) {
	host := mvcchost.New(t)
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithBatch(host.Batch()),
		mcpapi.WithMVCC(host.MVCC()),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	t.Cleanup(func() { _ = tr.Close() })

	mvcchost.CheckBatchRevision(t, tr, "mcp-")
}

func TestExecuteBatch_Success(t *testing.T) {
	svc := newMockBatchSvc()
	tr, cleanup := connectBatchInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(resp.Responses) != 1 {
		t.Errorf("responses: %d", len(resp.Responses))
	}
}

func TestExecuteBatch_WithWriteMode(t *testing.T) {
	svc := newMockBatchSvc()
	tr, cleanup := connectBatchInProcess(t, svc)
	defer cleanup()

	_, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
	}, transport.WithWriteMode(transport.WriteModeSync))
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
}

func TestExecuteBatch_ServiceError(t *testing.T) {
	svc := newMockBatchSvc()
	svc.executeErr = errors.New("batch failed")
	tr, cleanup := connectBatchInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ExecuteBatch(context.Background(), []transport.Op{
		{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func (m *mockBatchSvc) Import(_ context.Context, file io.Reader, opts service.ImportOptions) (*service.ImportResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	m.lastImportFile = data
	m.lastImportOpts = opts

	if m.importErr != nil {
		return nil, m.importErr
	}
	if m.importResp != nil {
		return m.importResp, nil
	}
	return &service.ImportResult{}, nil
}

func (m *mockBatchSvc) ImportBegin(_ context.Context, opts service.ImportOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sessions == nil {
		m.sessions = make(map[string]*mockImportSession)
	}
	m.nextSession++
	sessionID := strconv.Itoa(m.nextSession)
	m.sessions[sessionID] = &mockImportSession{opts: opts}
	return sessionID, nil
}

func (m *mockBatchSvc) ImportAppend(_ context.Context, sessionID string, _ uint64, chunk []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	sess.buf = append(sess.buf, chunk...)
	return nil
}

func (m *mockBatchSvc) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if !ok {
		return nil, &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	return m.Import(ctx, bytes.NewReader(sess.buf), sess.opts)
}

func (m *mockBatchSvc) ImportAbort(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[sessionID]; !ok {
		return &service.ImportSessionNotFoundError{SessionID: sessionID}
	}
	delete(m.sessions, sessionID)
	return nil
}

// ---------------------------------------------------------------------------
// ImportFile
// ---------------------------------------------------------------------------

func TestImportFile_Success(t *testing.T) {
	svc := newMockBatchSvc()
	svc.importResp = &service.ImportResult{
		Total:     2,
		Succeeded: 1,
		Failed:    1,
		Errors:    []service.ImportError{{Row: 2, Key: "1", Message: "duplicate"}},
	}
	tr, cleanup := connectBatchInProcess(t, svc)
	defer cleanup()

	result, err := tr.ImportFile(context.Background(), "data.csv",
		strings.NewReader("id,val\n1,a\n2,b\n"),
		transport.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row", SkipHeader: true})
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if result.Total != 2 || result.Succeeded != 1 || result.Failed != 1 {
		t.Errorf("result = %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].Message != "duplicate" {
		t.Errorf("errors = %+v", result.Errors)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if string(svc.lastImportFile) != "id,val\n1,a\n2,b\n" {
		t.Errorf("file = %q", svc.lastImportFile)
	}
	if svc.lastImportOpts.Format != "csv" || svc.lastImportOpts.KeyField != "id" || !svc.lastImportOpts.SkipHeader {
		t.Errorf("opts = %+v", svc.lastImportOpts)
	}
}

func TestImportFile_ServiceError(t *testing.T) {
	svc := newMockBatchSvc()
	svc.importErr = errors.New("import failed")
	tr, cleanup := connectBatchInProcess(t, svc)
	defer cleanup()

	if _, err := tr.ImportFile(context.Background(), "data.csv", strings.NewReader("x"), transport.ImportOptions{Format: "csv"}); err == nil {
		t.Fatal("expected error")
	}
}
