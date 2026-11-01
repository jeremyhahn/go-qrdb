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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: BarrierServicer
// ---------------------------------------------------------------------------

// mockBarrierService implements service.BarrierServicer for REST handler
// testing. Each method delegates to an overridable function field.
type mockBarrierService struct {
	initializeFn         func(ctx context.Context, secret []byte) error
	unsealFn             func(ctx context.Context, secret []byte) error
	changePassphraseFn   func(ctx context.Context, current, next []byte) error
	sealFn               func(ctx context.Context) error
	statusFn             func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn   func(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	generateRecoveryFn   func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	deleteRecoveryKeysFn func(ctx context.Context) error
	hasRecoveryKeysFn    func(ctx context.Context) (bool, error)
	officerRegisterFn    func(ctx context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn        func(ctx context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error)
	claimBeginFn         func(ctx context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	claimCompleteFn      func(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	quorumBeginFn        func(ctx context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error)
	quorumSubmitFn       func(ctx context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error)
	rekeyStartFn         func(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy) (*service.QuorumRoundInfo, error)
	officerRevokeFn      func(ctx context.Context, domain seal.QuorumDomain, username string, threshold int) (*service.QuorumRoundInfo, error)
	clusterUnsealFn      func(ctx context.Context) (*clusterseal.Report, error)
	// soPINs records the SO PIN each rekey or revocation handed the service.
	soPINs []string
}

// Compile-time interface check.
var _ service.BarrierServicer = (*mockBarrierService)(nil)

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierChangePassphrase(ctx context.Context, current, next []byte) error {
	if m.changePassphraseFn != nil {
		return m.changePassphraseFn(ctx, current, next)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.sealFn != nil {
		return m.sealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.BarrierStatusInfo{
		Sealed:         true,
		Strategy:       "shamir",
		HardwareBacked: false,
		InitializedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryKeysFn != nil {
		return m.deleteRecoveryKeysFn(ctx)
	}
	return nil
}

// BarrierInitializePrecondition admits every initialize: the double's
// initialize paths answer for themselves through their canned results.
func (m *mockBarrierService) BarrierInitializePrecondition(_ context.Context) error { return nil }

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return false, nil
}

// errMockUnset is returned by a holder method whose fn the test did not set,
// so an unexpected call fails loudly instead of answering a zero value.
var errMockUnset = errors.New("mockBarrierService: method not configured by this test")

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, threshold, totalShares, policy)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryFn != nil {
		return m.generateRecoveryFn(ctx, threshold, policy)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierOfficerRegister(ctx context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, domain, req)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierOfficerList(ctx context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx, domain)
	}
	return nil, errMockUnset
}

// BarrierClusterUnseal answers as a node without cluster sealing does unless
// clusterUnsealFn overrides it.
func (m *mockBarrierService) BarrierClusterUnseal(ctx context.Context) (*clusterseal.Report, error) {
	if m.clusterUnsealFn != nil {
		return m.clusterUnsealFn(ctx)
	}
	return nil, clusterseal.ErrInactive
}

func TestHandleBarrierClusterUnseal(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		fn        func(context.Context) (*clusterseal.Report, error)
		wantCode  int
		wantState clusterseal.Report
	}{
		{
			name: "active report is returned verbatim",
			fn: func(context.Context) (*clusterseal.Report, error) {
				return &clusterseal.Report{Active: true, Epoch: 7, Principal: "admin", Node: "n1", At: at}, nil
			},
			wantCode:  http.StatusOK,
			wantState: clusterseal.Report{Active: true, Epoch: 7, Principal: "admin", Node: "n1", At: at},
		},
		{
			name:      "nil report renders as inactive, never null",
			fn:        func(context.Context) (*clusterseal.Report, error) { return nil, nil },
			wantCode:  http.StatusOK,
			wantState: clusterseal.Report{},
		},
		{
			name:     "inactive cluster seal is a failed precondition",
			fn:       func(context.Context) (*clusterseal.Report, error) { return nil, clusterseal.ErrInactive },
			wantCode: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0",
				WithBarrierService(&mockBarrierService{clusterUnsealFn: tc.fn}))
			rr := httptest.NewRecorder()
			srv.handleBarrierClusterUnseal(rr, httptest.NewRequest(http.MethodPost, "/v1/barrier/cluster/unseal", nil))
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var got clusterseal.Report
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %q: %v", rr.Body.String(), err)
			}
			if !got.At.Equal(tc.wantState.At) {
				t.Fatalf("At = %v, want %v", got.At, tc.wantState.At)
			}
			got.At, tc.wantState.At = time.Time{}, time.Time{}
			if got != tc.wantState {
				t.Fatalf("report = %+v, want %+v", got, tc.wantState)
			}
		})
	}
}

func TestHandleBarrierClusterUnseal_NoBarrierServiceIsNotImplemented(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0")
	rr := httptest.NewRecorder()
	srv.handleBarrierClusterUnseal(rr, httptest.NewRequest(http.MethodPost, "/v1/barrier/cluster/unseal", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rr.Code)
	}
}

func (m *mockBarrierService) BarrierShareClaimBegin(ctx context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.claimBeginFn != nil {
		return m.claimBeginFn(ctx, domain, username, thumbprint)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierShareClaimComplete(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.claimCompleteFn != nil {
		return m.claimCompleteFn(ctx, domain, username, nonceHex, sig, thumbprint)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierQuorumBegin(ctx context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if m.quorumBeginFn != nil {
		return m.quorumBeginFn(ctx, req)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierQuorumSubmit(ctx context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	if m.quorumSubmitFn != nil {
		return m.quorumSubmitFn(ctx, req)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierRekeyStart(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, soPIN string) (*service.QuorumRoundInfo, error) {
	m.soPINs = append(m.soPINs, soPIN)
	if m.rekeyStartFn != nil {
		return m.rekeyStartFn(ctx, domain, threshold, policy)
	}
	return nil, errMockUnset
}

func (m *mockBarrierService) BarrierOfficerRevoke(ctx context.Context, domain seal.QuorumDomain, username string, threshold int, soPIN string) (*service.QuorumRoundInfo, error) {
	m.soPINs = append(m.soPINs, soPIN)
	if m.officerRevokeFn != nil {
		return m.officerRevokeFn(ctx, domain, username, threshold)
	}
	return nil, errMockUnset
}

// ---------------------------------------------------------------------------
// Helper: construct server with barrier service
// ---------------------------------------------------------------------------

func newBarrierTestServer(t *testing.T, barrierSvc service.BarrierServicer) *Server {
	t.Helper()
	opts := []ServerOption{}
	if barrierSvc != nil {
		opts = append(opts, WithBarrierService(barrierSvc))
	}
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
}

// ---------------------------------------------------------------------------
// Lifecycle: Initialize
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Initialize(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, secret []byte) error {
			if string(secret) != "my-secret" {
				t.Fatalf("secret = %q, want %q", secret, "my-secret")
			}
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"bXktc2VjcmV0"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierInitialize was not called")
	}
}

func TestBarrierHandlers_Initialize_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{invalid json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_Initialize_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, _ []byte) error {
			return errors.New("already initialized")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"cw=="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Unseal
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Unseal(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, secret []byte) error {
			if string(secret) != "my-secret" {
				t.Fatalf("secret = %q, want %q", secret, "my-secret")
			}
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"bXktc2VjcmV0"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierUnseal was not called")
	}
}

func TestBarrierHandlers_Unseal_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_Unseal_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, _ []byte) error {
			return errors.New("bad secret")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"d3Jvbmc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
	}
}

// TestBarrierHandlers_Unseal_WrongPassphrase proves a wrong passphrase — which
// surfaces as a go-quicraft *UnsealError wrapping ErrDecryptionFailed — is
// mapped to 403 Forbidden (client-supplied bad credential), NOT 500. Regression
// guard: a wrong passphrase used to leak the raw decryption error as a 500.
func TestBarrierHandlers_Unseal_WrongPassphrase(t *testing.T) {
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, _ []byte) error {
			return &quicraftseal.UnsealError{
				Strategy: "passphrase",
				Err:      quicraftseal.ErrDecryptionFailed,
			}
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"d3Jvbmc="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

// TestBarrierHandlers_Unseal_WrongPassphraseSentinel proves the qrdb-level
// re-exported sentinel (seal.ErrDecryptionFailed) maps to 403 the same way the
// wrapped go-quicraft error does.
func TestBarrierHandlers_Unseal_WrongPassphraseSentinel(t *testing.T) {
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, _ []byte) error {
			return seal.ErrDecryptionFailed
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"d3Jvbmc="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

func TestBarrierHandlers_ChangePassphrase(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		changePassphraseFn: func(_ context.Context, current, next []byte) error {
			if string(current) != "old-secret" || string(next) != "new-secret" {
				t.Fatalf("passphrases = (%q, %q), want (old-secret, new-secret)", current, next)
			}
			called = true
			return nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, newStubPINBackend())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/change-passphrase",
		strings.NewReader(`{"current":"b2xkLXNlY3JldA==","next":"bmV3LXNlY3JldA=="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierChangePassphrase was not called")
	}
}

func TestBarrierHandlers_ChangePassphrase_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/change-passphrase",
		strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

// TestBarrierHandlers_ChangePassphrase_TypedRefusals pins the status each
// refusal of a passphrase change maps to: a wrong current passphrase is 403,
// and a concurrent root-key write 409.
func TestBarrierHandlers_ChangePassphrase_TypedRefusals(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"credential mismatch": {seal.ErrCredentialMismatch, http.StatusForbidden},
		"change conflict":     {seal.ErrPassphraseChangeConflict, http.StatusConflict},
		"write in progress":   {seal.ErrRootKeyWriteInProgress, http.StatusConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &mockBarrierService{
				changePassphraseFn: func(context.Context, []byte, []byte) error { return tc.err },
			}
			srv := newBarrierPINTestServer(t, svc, newStubPINBackend())

			req := httptest.NewRequest(http.MethodPost, "/v1/barrier/change-passphrase",
				strings.NewReader(`{"current":"d3Jvbmc=","next":"bmV3"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Seal
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Seal(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		sealFn: func(_ context.Context) error {
			called = true
			return nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, newStubPINBackend())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierSeal was not called")
	}
}

func TestBarrierHandlers_Seal_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		sealFn: func(_ context.Context) error {
			return errors.New("seal failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/seal", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Status
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Status(t *testing.T) {
	initTime := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:         true,
				Strategy:       "shamir",
				HardwareBacked: false,
				InitializedAt:  initTime,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	if sealed, ok := resp["sealed"].(bool); !ok || !sealed {
		t.Fatalf("sealed = %v, want true", resp["sealed"])
	}
	if strategy, ok := resp["strategy"].(string); !ok || strategy != "shamir" {
		t.Fatalf("strategy = %v, want %q", resp["strategy"], "shamir")
	}
	if hw, ok := resp["hardware_backed"].(bool); !ok || hw {
		t.Fatalf("hardware_backed = %v, want false", resp["hardware_backed"])
	}
	if at, ok := resp["initialized_at"].(string); !ok || at != "2026-03-01T10:00:00Z" {
		t.Fatalf("initialized_at = %v, want %q", resp["initialized_at"], "2026-03-01T10:00:00Z")
	}
}

func TestBarrierHandlers_Status_ZeroInitTime(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:   false,
				Strategy: "passphrase",
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	if _, exists := resp["initialized_at"]; exists {
		t.Fatal("initialized_at should be omitted when zero")
	}
}

func TestBarrierHandlers_Status_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return nil, errors.New("status failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Shamir: Initialize
// ---------------------------------------------------------------------------

func TestBarrierHandlers_ShamirInitialize(t *testing.T) {
	svc := &mockBarrierService{
		initializeShamirFn: func(_ context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
			if policy != seal.FactorPolicyAnyEnrolled {
				t.Fatalf("policy = %q, want %q", policy, seal.FactorPolicyAnyEnrolled)
			}
			if threshold != 2 {
				t.Fatalf("threshold = %d, want 2", threshold)
			}
			if totalShares != 5 {
				t.Fatalf("totalShares = %d, want 5", totalShares)
			}
			return &service.ShamirInitInfo{
				Threshold:   2,
				TotalShares: 5,
				ShareCount:  5,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	body := `{"threshold":2,"total_shares":5,"policy":"any-enrolled"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/initialize",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	// INV-1/INV-11: shares must not appear in the init response.
	if _, hasShares := resp["shares"]; hasShares {
		t.Fatal("shares must not appear in shamir initialize response")
	}
	if thresh, ok := resp["threshold"].(float64); !ok || int(thresh) != 2 {
		t.Fatalf("threshold = %v, want 2", resp["threshold"])
	}
	if total, ok := resp["total_shares"].(float64); !ok || int(total) != 5 {
		t.Fatalf("total_shares = %v, want 5", resp["total_shares"])
	}
	if count, ok := resp["share_count"].(float64); !ok || int(count) != 5 {
		t.Fatalf("share_count = %v, want 5", resp["share_count"])
	}
}

func TestBarrierHandlers_ShamirInitialize_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/initialize",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_ShamirInitialize_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		initializeShamirFn: func(_ context.Context, _, _ int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
			return nil, errors.New("shamir init failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	body := `{"threshold":2,"total_shares":3}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/initialize",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Shamir: ListShares (via dispatch path fallback)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Shamir: DeleteShare by index (via dispatch path fallback)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Shamir: DeleteAllShares (via dispatch path fallback)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Shamir: Shares method not allowed (via dispatch path fallback)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Shamir: Verify
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Recovery: Generate
// ---------------------------------------------------------------------------

func TestBarrierHandlers_RecoveryGenerate(t *testing.T) {
	svc := &mockBarrierService{
		generateRecoveryFn: func(_ context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
			if threshold != 2 {
				t.Fatalf("threshold = %d, want 2", threshold)
			}
			if policy != seal.FactorPolicyDesignated {
				t.Fatalf("policy = %q, want %q", policy, seal.FactorPolicyDesignated)
			}
			return &service.RecoveryKeysInfo{
				Threshold:  2,
				Total:      4,
				ShareCount: 4,
			}, nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, newStubPINBackend())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/generate",
		strings.NewReader(`{"threshold":2,"policy":"designated"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	// INV-1/INV-11: recovery key bytes must not appear in the generate response.
	if _, hasKeys := resp["keys"]; hasKeys {
		t.Fatal("keys must not appear in recovery generate response")
	}
	if thresh, ok := resp["threshold"].(float64); !ok || int(thresh) != 2 {
		t.Fatalf("threshold = %v, want 2", resp["threshold"])
	}
	if total, ok := resp["total"].(float64); !ok || int(total) != 4 {
		t.Fatalf("total = %v, want 4", resp["total"])
	}
	if count, ok := resp["share_count"].(float64); !ok || int(count) != 4 {
		t.Fatalf("share_count = %v, want 4", resp["share_count"])
	}
}

func TestBarrierHandlers_RecoveryGenerate_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/generate",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_RecoveryGenerate_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		generateRecoveryFn: func(_ context.Context, _ int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
			return nil, errors.New("generate failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/generate",
		strings.NewReader(`{"threshold":2,"total":3}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Recovery: Delete
// ---------------------------------------------------------------------------

func TestBarrierHandlers_RecoveryDelete(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		deleteRecoveryKeysFn: func(_ context.Context) error {
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/barrier/recovery", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierDeleteRecoveryKeys was not called")
	}
}

func TestBarrierHandlers_RecoveryDelete_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		deleteRecoveryKeysFn: func(_ context.Context) error {
			return errors.New("delete failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodDelete, "/v1/barrier/recovery", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
	}
}

// ---------------------------------------------------------------------------
// Recovery: Status
// ---------------------------------------------------------------------------

func TestBarrierHandlers_RecoveryStatus(t *testing.T) {
	svc := &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return true, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/recovery/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	// The field name MUST be "has_keys" -- it must match
	// transport.BarrierHasRecoveryKeysResponse.HasKeys's json tag, the shared
	// SDK type gRPC/QUIC/unix all decode into. A prior "has_recovery_keys" key
	// here silently left every REST-transport caller's HasKeys field at its
	// zero value (false) regardless of the server's actual answer -- see
	// TestBarrierCLI_ShamirLifecycle in test/integration for the end-to-end
	// regression this caused.
	if hasKeys, ok := resp["has_keys"].(bool); !ok || !hasKeys {
		t.Fatalf("has_keys = %v, want true", resp["has_keys"])
	}
}

func TestBarrierHandlers_RecoveryStatus_NoKeys(t *testing.T) {
	svc := &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return false, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/recovery/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	if hasKeys, ok := resp["has_keys"].(bool); !ok || hasKeys {
		t.Fatalf("has_keys = %v, want false", resp["has_keys"])
	}
}

func TestBarrierHandlers_RecoveryStatus_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) {
			return false, errors.New("status check failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/recovery/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Method not allowed: static routes enforce method
// ---------------------------------------------------------------------------

func TestBarrierHandlers_MethodNotAllowed(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"Initialize_GET", http.MethodGet, "/v1/barrier/initialize"},
		{"Unseal_GET", http.MethodGet, "/v1/barrier/unseal"},
		{"Seal_GET", http.MethodGet, "/v1/barrier/seal"},
		{"Status_POST", http.MethodPost, "/v1/barrier/status"},
		{"ShamirInit_GET", http.MethodGet, "/v1/barrier/shamir/initialize"},
		{"QuorumBegin_GET", http.MethodGet, "/v1/barrier/quorum/begin"},
		{"QuorumSubmit_GET", http.MethodGet, "/v1/barrier/quorum/submit"},
		{"RekeyStart_GET", http.MethodGet, "/v1/barrier/rekey/start"},
		{"OfficerRevoke_GET", http.MethodGet, "/v1/barrier/officers/revoke"},
		{"RecoveryGenerate_GET", http.MethodGet, "/v1/barrier/recovery/generate"},
		{"RecoveryDelete_GET", http.MethodGet, "/v1/barrier/recovery"},
		{"RecoveryStatus_POST", http.MethodPost, "/v1/barrier/recovery/status"},
		{"RecoveryOfficers_GET", http.MethodGet, "/v1/barrier/recovery/officers"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)
			assertStatus(t, rec, http.StatusMethodNotAllowed)
		})
	}
}

// ---------------------------------------------------------------------------
// Response mapping helpers
// ---------------------------------------------------------------------------

func TestBarrierStatusToResponse_WithInitTime(t *testing.T) {
	initTime := time.Date(2026, 6, 15, 8, 30, 0, 0, time.UTC)
	info := &service.BarrierStatusInfo{
		Sealed:         false,
		Strategy:       "passphrase",
		HardwareBacked: true,
		InitializedAt:  initTime,
	}

	resp := barrierStatusToResponse(info)

	if sealed, ok := resp["sealed"].(bool); !ok || sealed {
		t.Fatalf("sealed = %v, want false", resp["sealed"])
	}
	if strategy, ok := resp["strategy"].(string); !ok || strategy != "passphrase" {
		t.Fatalf("strategy = %v, want %q", resp["strategy"], "passphrase")
	}
	if hw, ok := resp["hardware_backed"].(bool); !ok || !hw {
		t.Fatalf("hardware_backed = %v, want true", resp["hardware_backed"])
	}
	if at, ok := resp["initialized_at"].(string); !ok || at != "2026-06-15T08:30:00Z" {
		t.Fatalf("initialized_at = %v, want %q", resp["initialized_at"], "2026-06-15T08:30:00Z")
	}
}

func TestBarrierStatusToResponse_ZeroInitTime(t *testing.T) {
	info := &service.BarrierStatusInfo{
		Sealed:   true,
		Strategy: "shamir",
	}

	resp := barrierStatusToResponse(info)

	if _, exists := resp["initialized_at"]; exists {
		t.Fatal("initialized_at should be omitted for zero time")
	}
}

// TestBarrierStatusToResponse_CarriesInitialized: "initialized" is always on
// the response, false as well as true, since it is the one fact a sealed node
// reports besides "sealed".
func TestBarrierStatusToResponse_CarriesInitialized(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		resp := barrierStatusToResponse(&service.BarrierStatusInfo{Sealed: true, Initialized: initialized})
		if got, ok := resp["initialized"]; !ok || got != initialized {
			t.Errorf("initialized = (%v, %v), want (%v, true)", got, ok, initialized)
		}
		for _, absent := range []string{"initialized_at", "shamir_threshold", "shamir_total"} {
			if _, ok := resp[absent]; ok {
				t.Errorf("sealed response carries %q", absent)
			}
		}
	}
}

func TestBarrierStatusToResponse_ClusterMigration(t *testing.T) {
	if _, ok := barrierStatusToResponse(&service.BarrierStatusInfo{})["cluster_migration"]; ok {
		t.Error("no migration in progress carries cluster_migration, want it absent")
	}
	raw, err := json.Marshal(barrierStatusToResponse(&service.BarrierStatusInfo{ClusterMigration: &clustermig.Report{
		Target: seal.StrategyPassphrase, Generation: 3, Acked: 1, Total: 2,
		Nodes: []clustermig.NodeReport{
			{NodeID: "n1", Step: clustermig.StepServing, Phase: seal.PhaseComplete},
			{NodeID: "n2", Step: clustermig.StepPending},
		},
	}}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		ClusterMigration map[string]any `json:"cluster_migration"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{
		"target": "passphrase", "generation": float64(3), "acked": float64(1), "total": float64(2),
		"nodes": []any{
			map[string]any{"node_id": "n1", "step": "serving", "phase": "complete"},
			map[string]any{"node_id": "n2", "step": "pending"},
		},
	}
	if !reflect.DeepEqual(wire.ClusterMigration, want) {
		t.Errorf("cluster_migration = %v, want %v", wire.ClusterMigration, want)
	}
}

func TestShamirInitToResponse(t *testing.T) {
	info := &service.ShamirInitInfo{
		Threshold:   1,
		TotalShares: 2,
		ShareCount:  2,
	}

	resp := shamirInitToResponse(info)

	// INV-1/INV-11: share bytes must not appear in the init response.
	if _, hasShares := resp["shares"]; hasShares {
		t.Fatal("shares must not appear in shamir init response")
	}
	if thresh, ok := resp["threshold"].(int); !ok || thresh != 1 {
		t.Fatalf("threshold = %v, want 1", resp["threshold"])
	}
	if total, ok := resp["total_shares"].(int); !ok || total != 2 {
		t.Fatalf("total_shares = %v, want 2", resp["total_shares"])
	}
	if count, ok := resp["share_count"].(int); !ok || count != 2 {
		t.Fatalf("share_count = %v, want 2", resp["share_count"])
	}
}

func TestRecoveryKeysToResponse(t *testing.T) {
	info := &service.RecoveryKeysInfo{
		Threshold:  1,
		Total:      2,
		ShareCount: 2,
	}

	resp := recoveryKeysToResponse(info)

	// INV-1/INV-11: recovery key bytes must not appear in the generate response.
	if _, hasKeys := resp["keys"]; hasKeys {
		t.Fatal("keys must not appear in recovery generate response")
	}
	if thresh, ok := resp["threshold"].(int); !ok || thresh != 1 {
		t.Fatalf("threshold = %v, want 1", resp["threshold"])
	}
	if total, ok := resp["total"].(int); !ok || total != 2 {
		t.Fatalf("total = %v, want 2", resp["total"])
	}
	if count, ok := resp["share_count"].(int); !ok || count != 2 {
		t.Fatalf("share_count = %v, want 2", resp["share_count"])
	}
}

// ---------------------------------------------------------------------------
// Shamir shares: trailing slash normalization
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Fix 2: /v1/barrier/status includes shamir_threshold / shamir_total
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Status_ShamirFields_Exposed(t *testing.T) {
	tests := []struct {
		name             string
		strategy         string
		shamirThreshold  int
		shamirTotal      int
		wantThresholdKey bool
		wantTotalKey     bool
	}{
		{
			name:             "shamir strategy includes fields",
			strategy:         "shamir",
			shamirThreshold:  3,
			shamirTotal:      5,
			wantThresholdKey: true,
			wantTotalKey:     true,
		},
		{
			name:             "software strategy omits fields",
			strategy:         "software",
			shamirThreshold:  0,
			shamirTotal:      0,
			wantThresholdKey: false,
			wantTotalKey:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockBarrierService{
				statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
					return &service.BarrierStatusInfo{
						Sealed:          false,
						Strategy:        tc.strategy,
						HardwareBacked:  false,
						ShamirThreshold: tc.shamirThreshold,
						ShamirTotal:     tc.shamirTotal,
					}, nil
				},
			}
			srv := newBarrierTestServer(t, svc)

			req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, http.StatusOK)

			var resp map[string]interface{}
			decodeJSON(t, rec, &resp)

			_, hasThreshold := resp["shamir_threshold"]
			_, hasTotal := resp["shamir_total"]

			if tc.wantThresholdKey && !hasThreshold {
				t.Error("expected shamir_threshold key in response, not present")
			}
			if !tc.wantThresholdKey && hasThreshold {
				t.Errorf("expected no shamir_threshold key for non-shamir, got %v", resp["shamir_threshold"])
			}
			if tc.wantTotalKey && !hasTotal {
				t.Error("expected shamir_total key in response, not present")
			}
			if !tc.wantTotalKey && hasTotal {
				t.Errorf("expected no shamir_total key for non-shamir, got %v", resp["shamir_total"])
			}

			if tc.wantThresholdKey {
				got := int(resp["shamir_threshold"].(float64))
				if got != tc.shamirThreshold {
					t.Errorf("shamir_threshold = %d, want %d", got, tc.shamirThreshold)
				}
			}
			if tc.wantTotalKey {
				got := int(resp["shamir_total"].(float64))
				if got != tc.shamirTotal {
					t.Errorf("shamir_total = %d, want %d", got, tc.shamirTotal)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WithBarrierService option test
// ---------------------------------------------------------------------------

func TestWithBarrierService(t *testing.T) {
	svc := &mockBarrierService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithBarrierService(svc))
	if srv.barrierSvc == nil {
		t.Fatal("barrierSvc should not be nil after WithBarrierService")
	}
}

// ---------------------------------------------------------------------------
// JSON error response structure validation
// ---------------------------------------------------------------------------

func TestBarrierHandlers_NilService_ErrorBody(t *testing.T) {
	srv := &Server{barrierSvc: nil}

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"cw=="}`))
	rec := httptest.NewRecorder()
	srv.handleBarrierInitialize(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)

	var errResp map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if _, ok := errResp["error"]; !ok {
		t.Fatal("error response should contain 'error' field")
	}
}
