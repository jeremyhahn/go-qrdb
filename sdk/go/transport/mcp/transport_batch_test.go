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
	"context"
	"errors"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockBatchSvc implements service.BatchServicer for transport tests.
// ---------------------------------------------------------------------------

type mockBatchSvc struct {
	mu sync.Mutex

	executeErr     error
	executeModeErr error

	lastOps  []service.Op
	lastMode writemode.Mode

	resp []service.OpResponse
}

var _ service.BatchServicer = (*mockBatchSvc)(nil)

func newMockBatchSvc() *mockBatchSvc {
	return &mockBatchSvc{
		resp: []service.OpResponse{
			{Succeeded: true, Count: 1},
		},
	}
}

func (m *mockBatchSvc) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOps = ops
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	return m.resp, nil
}

func (m *mockBatchSvc) ExecuteWithMode(_ context.Context, ops []service.Op, mode writemode.Mode) ([]service.OpResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastOps = ops
	m.lastMode = mode
	if m.executeModeErr != nil {
		return nil, m.executeModeErr
	}
	return m.resp, nil
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

func (m *mockBatchSvc) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	return &service.ImportResult{}, nil
}
