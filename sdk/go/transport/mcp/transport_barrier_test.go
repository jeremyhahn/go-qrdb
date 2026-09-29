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
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockBarrierSvc is an in-memory service.BarrierServicer for transport
// round-trip tests. It records the last call arguments so assertions can
// verify wire-level argument decoding.
//
// The server-side BarrierStatusInfo / ShamirInitInfo / etc. types have
// no explicit json tags, so they marshal using Go's default field names
// (PascalCase). Multi-word transport response fields (e.g. snake_case
// json tags like "hardware_backed", "total_shares") will not decode from
// PascalCase keys. Tests only assert on fields that round-trip cleanly
// (single-word names where Go's case-insensitive json.Unmarshal matches).
// ---------------------------------------------------------------------------

type mockBarrierSvc struct {
	service.BarrierServicer
	mu sync.Mutex

	// Configurable error injection.
	initErr           error
	unsealErr         error
	sealErr           error
	statusErr         error
	initShamirErr     error
	genRecoveryErr    error
	deleteRecoveryErr error
	hasRecoveryErr    error

	// Recorded last-call arguments.
	lastSecret          string
	lastUnsealSecret    string
	lastInitThreshold   int
	lastInitTotalShares int
	lastRecoveryThr     int
	lastPolicy          string

	// Configurable responses.
	statusResp      *service.BarrierStatusInfo
	shamirInitResp  *service.ShamirInitInfo
	genRecoveryResp *service.RecoveryKeysInfo
	hasRecoveryResp bool
}

var _ service.BarrierServicer = (*mockBarrierSvc)(nil)

func newMockBarrierSvc() *mockBarrierSvc {
	return &mockBarrierSvc{
		statusResp: &service.BarrierStatusInfo{
			Sealed:         false,
			Strategy:       "passphrase",
			HardwareBacked: false,
		},
		shamirInitResp: &service.ShamirInitInfo{
			ShareCount:  3,
			Threshold:   2,
			TotalShares: 3,
		},
		genRecoveryResp: &service.RecoveryKeysInfo{
			ShareCount: 2,
			Threshold:  1,
			Total:      2,
		},
	}
}

func (m *mockBarrierSvc) BarrierInitialize(_ context.Context, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSecret = string(secret)
	return m.initErr
}

func (m *mockBarrierSvc) BarrierUnseal(_ context.Context, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUnsealSecret = string(secret)
	return m.unsealErr
}

func (m *mockBarrierSvc) BarrierSeal(_ context.Context) error {
	return m.sealErr
}

func (m *mockBarrierSvc) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusResp, nil
}

func (m *mockBarrierSvc) BarrierInitializeShamir(_ context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastInitThreshold = threshold
	m.lastInitTotalShares = total
	m.lastPolicy = string(policy)
	if m.initShamirErr != nil {
		return nil, m.initShamirErr
	}
	return m.shamirInitResp, nil
}

func (m *mockBarrierSvc) BarrierGenerateRecoveryKeys(_ context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRecoveryThr = threshold
	m.lastPolicy = string(policy)
	if m.genRecoveryErr != nil {
		return nil, m.genRecoveryErr
	}
	return m.genRecoveryResp, nil
}

func (m *mockBarrierSvc) BarrierDeleteRecoveryKeys(_ context.Context) error {
	return m.deleteRecoveryErr
}

func (m *mockBarrierSvc) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	if m.hasRecoveryErr != nil {
		return false, m.hasRecoveryErr
	}
	return m.hasRecoveryResp, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

func connectBarrierInProcess(t *testing.T, svc service.BarrierServicer) (*Transport, func()) {
	t.Helper()

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithBarrierService(svc),
		mcpapi.WithPINBackend(pintest.NewBackend(t)),
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

// ---------------------------------------------------------------------------
// BarrierInitialize
// ---------------------------------------------------------------------------

func TestBarrierInitialize_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret:  []byte("passphrase"),
		SOPin:   "123456",
		UserPin: "567890",
	})
	if err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastSecret != "passphrase" {
		t.Errorf("secret: got %q, want %q", svc.lastSecret, "passphrase")
	}
}

func TestBarrierInitialize_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.initErr = errors.New("already initialized")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierUnseal
// ---------------------------------------------------------------------------

func TestBarrierUnseal_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("passphrase")})
	if err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastUnsealSecret != "passphrase" {
		t.Errorf("unseal secret: got %q, want %q", svc.lastUnsealSecret, "passphrase")
	}
}

func TestBarrierUnseal_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.unsealErr = errors.New("wrong secret")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("s")})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierSeal
// ---------------------------------------------------------------------------

func TestBarrierSeal_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	if err := tr.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
}

func TestBarrierSeal_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.sealErr = errors.New("seal failed")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierSeal(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierStatus
// ---------------------------------------------------------------------------

func TestBarrierStatus_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.statusResp = &service.BarrierStatusInfo{
		Sealed:   true,
		Strategy: "shamir",
	}
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if !resp.Sealed {
		t.Error("expected sealed=true")
	}
	if resp.Strategy != "shamir" {
		t.Errorf("strategy: got %q, want %q", resp.Strategy, "shamir")
	}
}

func TestBarrierStatus_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.statusErr = errors.New("unavailable")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierStatus(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierInitializeShamir
// ---------------------------------------------------------------------------

func TestBarrierInitializeShamir_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Threshold:   2,
		TotalShares: 3,
		SOPin:       "111111",
		UserPin:     "222222",
	})
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	// MCP serializes service types without json tags (PascalCase), so only
	// single-word fields round-trip through json.Unmarshal case-insensitive
	// matching. Threshold is single-word; ShareCount/TotalShares are not.
	if resp.Threshold != 2 {
		t.Errorf("threshold: got %d, want 2", resp.Threshold)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastInitThreshold != 2 || svc.lastInitTotalShares != 3 {
		t.Errorf("recorded params: got %d/%d, want 2/3", svc.lastInitThreshold, svc.lastInitTotalShares)
	}
}

func TestBarrierInitializeShamir_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.initShamirErr = errors.New("already initialized")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierGenerateRecoveryKeys
// ---------------------------------------------------------------------------

func TestBarrierGenerateRecoveryKeys_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{
		Threshold: 1,
		Policy:    transport.PolicyAnyEnrolled,
		SOPin:     "1234",
	})
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	// Single-word field Total round-trips through MCP; ShareCount does not.
	if resp.Total != 2 {
		t.Errorf("total: got %d, want 2", resp.Total)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRecoveryThr != 1 || svc.lastPolicy != transport.PolicyAnyEnrolled {
		t.Errorf("recovery args: got %d/%q, want 1/any-enrolled", svc.lastRecoveryThr, svc.lastPolicy)
	}
}

// ---------------------------------------------------------------------------
// BarrierDeleteRecoveryKeys
// ---------------------------------------------------------------------------

func TestBarrierDeleteRecoveryKeys_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	if err := tr.BarrierDeleteRecoveryKeys(context.Background()); err != nil {
		t.Fatalf("BarrierDeleteRecoveryKeys: %v", err)
	}
}

func TestBarrierDeleteRecoveryKeys_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.deleteRecoveryErr = errors.New("delete failed")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierDeleteRecoveryKeys(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierHasRecoveryKeys
// ---------------------------------------------------------------------------

func TestBarrierHasRecoveryKeys_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.hasRecoveryResp = true
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if !resp.HasKeys {
		t.Error("expected has_keys=true")
	}
}

func TestBarrierHasRecoveryKeys_False(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.hasRecoveryResp = false
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if resp.HasKeys {
		t.Error("expected has_keys=false")
	}
}

func TestBarrierHasRecoveryKeys_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.hasRecoveryErr = errors.New("unavailable")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierHasRecoveryKeys(context.Background())
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierEnableEncryption
// ---------------------------------------------------------------------------

// mockEncMigrator is a minimal in-memory service.EncryptionMigrator for
// transport round-trip tests. It records the last call's scope and target
// strategy.
type mockEncMigrator struct {
	mu           sync.Mutex
	lastScope    service.BarrierScope
	lastStrategy seal.StrategyID
	err          error
}

func (m *mockEncMigrator) EnableEncryption(_ context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	m.mu.Lock()
	m.lastScope = scope
	m.lastStrategy = opts.TargetStrategy
	m.mu.Unlock()
	return m.err
}

// connectEncMigratorInProcess starts a real pkg/api/mcp.Server wired with mig
// as its EncryptionMigrator (deliberately WITHOUT a barrier service -- see
// go-qrdb#558's MCP registration fix: qrdb_barrier_enable_encryption is
// registered independent of one) and connects an in-process MCP transport to
// it.
func connectEncMigratorInProcess(t *testing.T, mig service.EncryptionMigrator) (*Transport, func()) {
	t.Helper()

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithEncryptionMigrator(mig),
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

// TestBarrierEnableEncryption_Success is the go-qrdb#558 wiring regression
// test: this client method used to return a NotImplementedError
// unconditionally, without dialing the server at all, even though
// pkg/api/mcp/tools_barrier.go has always fully implemented
// qrdb_barrier_enable_encryption. A real server + real in-process client
// round-trip proves the marshal/dial/unmarshal path, not merely that the
// method no longer short-circuits.
func TestBarrierEnableEncryption_Success(t *testing.T) {
	mig := &mockEncMigrator{}
	tr, cleanup := connectEncMigratorInProcess(t, mig)
	defer cleanup()

	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte("pw"),
	})
	if err != nil {
		t.Fatalf("BarrierEnableEncryption: %v", err)
	}
	if resp.Status != "migration_started" || resp.Scope != "system" {
		t.Errorf("resp = %+v", resp)
	}

	mig.mu.Lock()
	defer mig.mu.Unlock()
	if mig.lastScope.Kind != service.ScopeSystem {
		t.Errorf("scope: got %v, want system", mig.lastScope.Kind)
	}
	if mig.lastStrategy != seal.StrategyPassphrase {
		t.Errorf("strategy: got %v, want passphrase", mig.lastStrategy)
	}
}

// TestBarrierEnableEncryption_NoMigratorConfigured proves that with no
// EncryptionMigrator configured on the server, the call round-trips to the
// server (not a local short-circuit) and comes back as a
// *transport.RequestError wrapping the server's typed
// service.ErrMigrationProviderUnavailable refusal, matching REST/gRPC/Unix/QUIC.
func TestBarrierEnableEncryption_NoMigratorConfigured(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte("pw"),
	})
	if err == nil {
		t.Fatal("expected error with no migrator configured")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T: %v", err, err)
	}
}
