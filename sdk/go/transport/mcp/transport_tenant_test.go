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

	mcpserver "github.com/mark3labs/mcp-go/server"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockTenantSvc is an in-memory tenant.TenantServicer used by transport
// round-trip tests. It records the last call for the per-tenant barrier
// methods so assertions can verify wire-level argument decoding.
// ---------------------------------------------------------------------------

type mockTenantSvc struct {
	mu sync.Mutex

	// Configurable error injection.
	createErr        error
	getErr           error
	listErr          error
	updateErr        error
	deleteErr        error
	updateQuotasErr  error
	barrierInitErr   error
	barrierUnsealErr error
	barrierSealErr   error
	barrierStatusErr error

	// Configurable status response.
	statusResp *tenant.TenantBarrierStatus

	// Recorded last-call state for assertions.
	lastBarrierID        string
	lastBarrierThreshold int
	lastBarrierShares    int
	lastBarrierKey       []byte
	lastSealID           string
	lastStatusID         string
}

var _ tenant.TenantServicer = (*mockTenantSvc)(nil)

func newMockTenantSvc() *mockTenantSvc {
	return &mockTenantSvc{}
}

func (m *mockTenantSvc) CreateTenant(_ context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso}, nil
}

func (m *mockTenantSvc) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return &tenant.TenantInfo{ID: id, Name: "test", IsolationType: tenant.IsolationBarrier}, nil
}

func (m *mockTenantSvc) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return []tenant.TenantInfo{{ID: "t1", Name: "Tenant One"}}, nil
}

func (m *mockTenantSvc) UpdateTenant(_ context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return &tenant.TenantInfo{ID: id, Name: p.Name, IsolationType: p.IsolationType}, nil
}

func (m *mockTenantSvc) DeleteTenant(_ context.Context, _ string) error {
	return m.deleteErr
}

func (m *mockTenantSvc) UpdateQuotas(_ context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	if m.updateQuotasErr != nil {
		return nil, m.updateQuotasErr
	}
	return &tenant.TenantInfo{ID: id, Name: "test", Quotas: q}, nil
}

func (m *mockTenantSvc) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return &quota.Quotas{}, nil
}

func (m *mockTenantSvc) TenantBarrierInit(_ context.Context, id, _ string, threshold, shares int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastBarrierID = id
	m.lastBarrierThreshold = threshold
	m.lastBarrierShares = shares
	return m.barrierInitErr
}

func (m *mockTenantSvc) TenantBarrierInitWithStrategy(ctx context.Context, id, passphrase string, threshold, shares int, _ seal.StrategyID) error {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, shares)
}

func (m *mockTenantSvc) TenantBarrierUnseal(_ context.Context, id string, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastBarrierID = id
	m.lastBarrierKey = append([]byte(nil), key...)
	return m.barrierUnsealErr
}

func (m *mockTenantSvc) TenantBarrierSeal(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSealID = id
	return m.barrierSealErr
}

func (m *mockTenantSvc) TenantBarrierStatus(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStatusID = id
	if m.barrierStatusErr != nil {
		return nil, m.barrierStatusErr
	}
	if m.statusResp != nil {
		return m.statusResp, nil
	}
	return &tenant.TenantBarrierStatus{
		TenantID:       id,
		Sealed:         false,
		Initialized:    true,
		Strategy:       "passphrase",
		HardwareBacked: false,
	}, nil
}
func (m *mockTenantSvc) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantSvc) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantSvc) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

// connectTenantInProcess builds an in-process MCP server with the given
// tenant service registered and returns a connected SDK transport.
func connectTenantInProcess(t *testing.T, svc tenant.TenantServicer) (*Transport, func()) {
	t.Helper()

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithTenantService(svc),
	)

	mcpSrv := srv.MCPServer()
	if mcpSrv == nil {
		t.Fatal("MCPServer is nil")
	}

	tr, err := NewInProcess(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}

	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}

	return tr, func() { _ = tr.Close() }
}

// ensure we use mcpserver to avoid unused import lint when tests are minimal.
var _ = mcpserver.NewMCPServer

// ---------------------------------------------------------------------------
// TenantBarrierInit
// ---------------------------------------------------------------------------

func TestTenantBarrierInit_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID:   "acme",
		Passphrase: "supplied-secret",
		Threshold:  3,
		Shares:     5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.lastBarrierID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", svc.lastBarrierID)
	}
	if svc.lastBarrierThreshold != 3 {
		t.Errorf("expected threshold 3, got %d", svc.lastBarrierThreshold)
	}
	if svc.lastBarrierShares != 5 {
		t.Errorf("expected shares 5, got %d", svc.lastBarrierShares)
	}
}

func TestTenantBarrierInit_DefaultsOmitted(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID:   "acme",
		Passphrase: "supplied-secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.lastBarrierThreshold != 0 || svc.lastBarrierShares != 0 {
		t.Errorf("expected zero threshold/shares, got %d/%d", svc.lastBarrierThreshold, svc.lastBarrierShares)
	}
}

func TestTenantBarrierInit_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.barrierInitErr = tenant.ErrTenantNotFound
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID:   "missing",
		Passphrase: "supplied-secret",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "TenantBarrierInit" {
		t.Errorf("expected method 'TenantBarrierInit', got %q", reqErr.Method)
	}
}

func TestTenantBarrierInit_NilRequest(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierInit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTenantBarrierInit_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	err := tr.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected ClosedError")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestTenantBarrierUnseal_SuccessWithKey(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	want := []byte("super-secret-key")
	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "acme",
		Key:      want,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.lastBarrierID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", svc.lastBarrierID)
	}
	if string(svc.lastBarrierKey) != string(want) {
		t.Errorf("expected key %q, got %q", want, svc.lastBarrierKey)
	}
}

func TestTenantBarrierUnseal_FallbackToShare(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	want := []byte("shamir-share")
	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "acme",
		Share:    want,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(svc.lastBarrierKey) != string(want) {
		t.Errorf("expected key %q (from Share), got %q", want, svc.lastBarrierKey)
	}
}

func TestTenantBarrierUnseal_NoKey(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(svc.lastBarrierKey) != 0 {
		t.Errorf("expected empty key, got %q", svc.lastBarrierKey)
	}
}

func TestTenantBarrierUnseal_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.barrierUnsealErr = tenant.ErrTenantNotFound
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "missing",
		Key:      []byte("k"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTenantBarrierUnseal_NilRequest(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	if err := tr.TenantBarrierUnseal(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTenantBarrierUnseal_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	err := tr.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierSeal
// ---------------------------------------------------------------------------

func TestTenantBarrierSeal_Success(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.lastSealID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", svc.lastSealID)
	}
}

func TestTenantBarrierSeal_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.barrierSealErr = errors.New("registry: not initialized")
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{
		TenantID: "acme",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.Method != "TenantBarrierSeal" {
		t.Errorf("expected method 'TenantBarrierSeal', got %q", reqErr.Method)
	}
}

func TestTenantBarrierSeal_NilRequest(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	if err := tr.TenantBarrierSeal(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTenantBarrierSeal_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	err := tr.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierStatus
// ---------------------------------------------------------------------------

func TestTenantBarrierStatus_Success(t *testing.T) {
	svc := newMockTenantSvc()
	svc.statusResp = &tenant.TenantBarrierStatus{
		TenantID:       "acme",
		Sealed:         true,
		Initialized:    true,
		Strategy:       "shamir",
		HardwareBacked: true,
	}
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	resp, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.lastStatusID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", svc.lastStatusID)
	}
	if resp.TenantID != "acme" {
		t.Errorf("expected response tenant_id 'acme', got %q", resp.TenantID)
	}
	if !resp.Sealed {
		t.Error("expected sealed=true")
	}
	if !resp.Initialized {
		t.Error("expected initialized=true")
	}
	if resp.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", resp.Strategy)
	}
	if !resp.HardwareBacked {
		t.Error("expected hardware_backed=true")
	}
}

func TestTenantBarrierStatus_ServiceError(t *testing.T) {
	svc := newMockTenantSvc()
	svc.barrierStatusErr = tenant.ErrTenantNotFound
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{
		TenantID: "missing",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestTenantBarrierStatus_NilRequest(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	defer cleanup()

	if _, err := tr.TenantBarrierStatus(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestTenantBarrierStatus_Closed(t *testing.T) {
	svc := newMockTenantSvc()
	tr, cleanup := connectTenantInProcess(t, svc)
	cleanup()

	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T", err)
	}
}
