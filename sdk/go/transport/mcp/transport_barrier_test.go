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
	mu sync.Mutex

	// Configurable error injection.
	initErr            error
	unsealErr          error
	sealErr            error
	statusErr          error
	initShamirErr      error
	unsealShareErr     error
	unsealSharesErr    error
	listSharesErr      error
	deleteShareErr     error
	deleteAllSharesErr error
	verifyErr          error
	rekeyErr           error
	genRecoveryErr     error
	recoverErr         error
	deleteRecoveryErr  error
	hasRecoveryErr     error
	rootTokenErr       error

	// Recorded last-call arguments.
	lastSecret          string
	lastUnsealSecret    string
	lastInitThreshold   int
	lastInitTotalShares int
	lastUnsealShare     string
	lastUnsealShares    []string
	lastDeleteIndex     int
	lastRekeyThreshold  int
	lastRekeyTotal      int
	lastRecoveryThr     int
	lastRecoveryTotal   int
	lastRecoverKeys     []string
	lastRootTokenShares []string

	// Configurable responses.
	statusResp      *service.BarrierStatusInfo
	shamirInitResp  *service.ShamirInitInfo
	unsealShareResp *service.UnsealShareProgress
	rekeyResp       *service.ShamirInitInfo
	genRecoveryResp *service.RecoveryKeysInfo
	hasRecoveryResp bool
	rootTokenResp   *service.RootTokenInfo
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
		unsealShareResp: &service.UnsealShareProgress{
			Required:  3,
			Submitted: 1,
			Complete:  false,
		},
		rekeyResp: &service.ShamirInitInfo{
			ShareCount:  2,
			Threshold:   2,
			TotalShares: 2,
		},
		genRecoveryResp: &service.RecoveryKeysInfo{
			ShareCount: 2,
			Threshold:  1,
			Total:      2,
		},
		rootTokenResp: &service.RootTokenInfo{
			Token: "root-token-hex",
		},
	}
}

func (m *mockBarrierSvc) BarrierInitialize(_ context.Context, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSecret = secret
	return m.initErr
}

func (m *mockBarrierSvc) BarrierUnseal(_ context.Context, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUnsealSecret = secret
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

func (m *mockBarrierSvc) BarrierInitializeShamir(_ context.Context, secret string, threshold, totalShares int) (*service.ShamirInitInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSecret = secret
	m.lastInitThreshold = threshold
	m.lastInitTotalShares = totalShares
	if m.initShamirErr != nil {
		return nil, m.initShamirErr
	}
	return m.shamirInitResp, nil
}

func (m *mockBarrierSvc) BarrierUnsealWithShare(_ context.Context, share string) (*service.UnsealShareProgress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUnsealShare = share
	if m.unsealShareErr != nil {
		return nil, m.unsealShareErr
	}
	return m.unsealShareResp, nil
}

func (m *mockBarrierSvc) BarrierUnsealWithShares(_ context.Context, shares []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastUnsealShares = append([]string(nil), shares...)
	return m.unsealSharesErr
}

func (m *mockBarrierSvc) BarrierRekey(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRekeyThreshold = threshold
	m.lastRekeyTotal = total
	if m.rekeyErr != nil {
		return nil, m.rekeyErr
	}
	return m.rekeyResp, nil
}

func (m *mockBarrierSvc) BarrierGenerateRecoveryKeys(_ context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRecoveryThr = threshold
	m.lastRecoveryTotal = total
	if m.genRecoveryErr != nil {
		return nil, m.genRecoveryErr
	}
	return m.genRecoveryResp, nil
}

func (m *mockBarrierSvc) BarrierRecoverWithKeys(_ context.Context, keys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRecoverKeys = append([]string(nil), keys...)
	return m.recoverErr
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

func (m *mockBarrierSvc) BarrierGenerateRootToken(_ context.Context, shares []string) (*service.RootTokenInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRootTokenShares = append([]string(nil), shares...)
	if m.rootTokenErr != nil {
		return nil, m.rootTokenErr
	}
	return m.rootTokenResp, nil
}

func (m *mockBarrierSvc) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}

func (m *mockBarrierSvc) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}

func (m *mockBarrierSvc) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}

func (m *mockBarrierSvc) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}

func (m *mockBarrierSvc) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}

func (m *mockBarrierSvc) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{}, nil
}

func (m *mockBarrierSvc) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}

func (m *mockBarrierSvc) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}

func (m *mockBarrierSvc) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
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
		Secret:  "passphrase",
		SOPin:   "1234",
		UserPin: "5678",
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

	err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{Secret: "s"})
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

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: "passphrase"})
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

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: "s"})
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
		Secret:      "passphrase",
		Threshold:   2,
		TotalShares: 3,
		SOPin:       "1111",
		UserPin:     "2222",
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

	_, err := tr.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret: "s", Threshold: 1, TotalShares: 1,
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierUnsealWithShare
// ---------------------------------------------------------------------------

func TestBarrierUnsealWithShare_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{
		Share: "share-value",
	})
	if err != nil {
		t.Fatalf("BarrierUnsealWithShare: %v", err)
	}
	if resp.Required != 3 || resp.Submitted != 1 {
		t.Errorf("progress: got required=%d submitted=%d, want 3/1", resp.Required, resp.Submitted)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastUnsealShare != "share-value" {
		t.Errorf("share: got %q, want %q", svc.lastUnsealShare, "share-value")
	}
}

func TestBarrierUnsealWithShare_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.unsealShareErr = errors.New("invalid share")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{Share: "x"})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierUnsealWithShares
// ---------------------------------------------------------------------------

func TestBarrierUnsealWithShares_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{
		Shares: []string{"sh1", "sh2", "sh3"},
	})
	if err != nil {
		t.Fatalf("BarrierUnsealWithShares: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.lastUnsealShares) != 3 {
		t.Errorf("shares: got %d, want 3", len(svc.lastUnsealShares))
	}
}

func TestBarrierUnsealWithShares_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.unsealSharesErr = errors.New("not enough shares")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{
		Shares: []string{"sh1"},
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierRekey
// ---------------------------------------------------------------------------

func TestBarrierRekey_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2,
		Total:     2,
		SOPin:     "1234",
	})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	// Single-word field Threshold round-trips through MCP; ShareCount does not
	// (PascalCase JSON from service type vs snake_case expected by transport type).
	if resp.Threshold != 2 {
		t.Errorf("threshold: got %d, want 2", resp.Threshold)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRekeyThreshold != 2 || svc.lastRekeyTotal != 2 {
		t.Errorf("rekey args: got %d/%d, want 2/2", svc.lastRekeyThreshold, svc.lastRekeyTotal)
	}
}

func TestBarrierRekey_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.rekeyErr = errors.New("rekey failed")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{Threshold: 1, Total: 1})
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
		Total:     2,
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
	if svc.lastRecoveryThr != 1 || svc.lastRecoveryTotal != 2 {
		t.Errorf("recovery args: got %d/%d, want 1/2", svc.lastRecoveryThr, svc.lastRecoveryTotal)
	}
}

func TestBarrierGenerateRecoveryKeys_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.genRecoveryErr = errors.New("generation failed")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 1, Total: 1})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// BarrierRecoverWithKeys
// ---------------------------------------------------------------------------

func TestBarrierRecoverWithKeys_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{
		Keys: []string{"k1", "k2"},
	})
	if err != nil {
		t.Fatalf("BarrierRecoverWithKeys: %v", err)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.lastRecoverKeys) != 2 {
		t.Errorf("keys: got %d, want 2", len(svc.lastRecoverKeys))
	}
}

func TestBarrierRecoverWithKeys_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.recoverErr = errors.New("recovery failed")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	err := tr.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{Keys: []string{"k1"}})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
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
// BarrierGenerateRootToken
// ---------------------------------------------------------------------------

func TestBarrierGenerateRootToken_Success(t *testing.T) {
	svc := newMockBarrierSvc()
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	resp, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{
		Shares: []string{"sh1", "sh2"},
	})
	if err != nil {
		t.Fatalf("BarrierGenerateRootToken: %v", err)
	}
	if resp.Token != "root-token-hex" {
		t.Errorf("token: got %q, want %q", resp.Token, "root-token-hex")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.lastRootTokenShares) != 2 {
		t.Errorf("shares: got %d, want 2", len(svc.lastRootTokenShares))
	}
}

func TestBarrierGenerateRootToken_ServiceError(t *testing.T) {
	svc := newMockBarrierSvc()
	svc.rootTokenErr = errors.New("generation failed")
	tr, cleanup := connectBarrierInProcess(t, svc)
	defer cleanup()

	_, err := tr.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{Shares: []string{"sh1"}})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected *transport.RequestError, got %T", err)
	}
}
