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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockTxSvc is an in-memory service.TransactionServicer for transport tests.
// ---------------------------------------------------------------------------

type mockTxSvc struct {
	mu sync.Mutex

	executeErr     error
	executeModeErr error
	executeEntErr  error

	lastCompares []service.Compare
	lastSuccess  []service.Op
	lastFailure  []service.Op
	lastMode     writemode.Mode

	resp *service.TransactionResponse
}

var _ service.TransactionServicer = (*mockTxSvc)(nil)

func newMockTxSvc() *mockTxSvc {
	return &mockTxSvc{
		resp: &service.TransactionResponse{
			Succeeded: true,
			Revision:  42,
		},
	}
}

func (m *mockTxSvc) Execute(_ context.Context, compares []service.Compare, success, failure []service.Op) (*service.TransactionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCompares = compares
	m.lastSuccess = success
	m.lastFailure = failure
	if m.executeErr != nil {
		return nil, m.executeErr
	}
	return m.resp, nil
}

func (m *mockTxSvc) ExecuteWithMode(_ context.Context, compares []service.Compare, success, failure []service.Op, mode writemode.Mode) (*service.TransactionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCompares = compares
	m.lastSuccess = success
	m.lastFailure = failure
	m.lastMode = mode
	if m.executeModeErr != nil {
		return nil, m.executeModeErr
	}
	return m.resp, nil
}

func (m *mockTxSvc) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	if m.executeEntErr != nil {
		return nil, m.executeEntErr
	}
	return &service.TransactionEntityResponse{Succeeded: true}, nil
}

func connectTxInProcess(t *testing.T, svc service.TransactionServicer) (*Transport, func()) {
	t.Helper()
	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithTransaction(svc),
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

// ---------------------------------------------------------------------------
// ExecuteTransaction
// ---------------------------------------------------------------------------

func TestExecuteTransaction_Success(t *testing.T) {
	svc := newMockTxSvc()
	tr, cleanup := connectTxInProcess(t, svc)
	defer cleanup()

	resp, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{
				Key:     []byte("k1"),
				Target:  transport.CompareVersion,
				Result:  transport.CompareEqual,
				Version: 1,
			},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		},
		FailureOps: []transport.Op{
			{Type: transport.OpGet, Key: []byte("k1")},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded {
		t.Error("expected succeeded=true")
	}
	if resp.Revision != 42 {
		t.Errorf("revision: %d, want 42", resp.Revision)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.lastCompares) != 1 {
		t.Errorf("compares: %d", len(svc.lastCompares))
	}
	if len(svc.lastSuccess) != 1 {
		t.Errorf("success ops: %d", len(svc.lastSuccess))
	}
}

func TestExecuteTransaction_WithWriteMode(t *testing.T) {
	svc := newMockTxSvc()
	tr, cleanup := connectTxInProcess(t, svc)
	defer cleanup()

	_, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}},
	}, transport.WithWriteMode(transport.WriteModeSync))
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastMode != writemode.Sync {
		t.Errorf("mode: got %v, want %v", svc.lastMode, writemode.Sync)
	}
}

func TestExecuteTransaction_ServiceError(t *testing.T) {
	svc := newMockTxSvc()
	svc.executeErr = errors.New("transaction failed")
	tr, cleanup := connectTxInProcess(t, svc)
	defer cleanup()

	_, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// marshalOpsWire
// ---------------------------------------------------------------------------

func TestMarshalOpsWire(t *testing.T) {
	ops := []transport.Op{
		{Type: transport.OpPut, Key: []byte("k1"), Value: []byte("v1")},
		{Type: transport.OpDelete, Key: []byte("k2")},
		{Type: transport.OpRange, Key: []byte("k3"), RangeEnd: []byte("k4")},
	}
	data, err := marshalOpsWire(ops)
	if err != nil {
		t.Fatalf("marshalOpsWire: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty data")
	}
}
