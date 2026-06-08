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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: BarrierServicer
// ---------------------------------------------------------------------------

// mockBarrierService implements service.BarrierServicer for REST handler
// testing. Each method delegates to an overridable function field.
type mockBarrierService struct {
	initializeFn         func(ctx context.Context, secret string) error
	unsealFn             func(ctx context.Context, secret string) error
	sealFn               func(ctx context.Context) error
	statusFn             func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn   func(ctx context.Context, secret string, threshold, totalShares int) (*service.ShamirInitInfo, error)
	unsealWithShareFn    func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	unsealWithSharesFn   func(ctx context.Context, shares []string) error
	rekeyFn              func(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error)
	generateRecoveryFn   func(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error)
	recoverWithKeysFn    func(ctx context.Context, keys []string) error
	deleteRecoveryKeysFn func(ctx context.Context) error
	hasRecoveryKeysFn    func(ctx context.Context) (bool, error)
	generateRootTokenFn  func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)
}

// Compile-time interface check.
var _ service.BarrierServicer = (*mockBarrierService)(nil)

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret string) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret string) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, secret)
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

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, secret string, threshold, totalShares int) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, secret, threshold, totalShares)
	}
	return &service.ShamirInitInfo{
		Shares:      []string{"share-1", "share-2", "share-3"},
		Threshold:   threshold,
		TotalShares: totalShares,
	}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShare(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.unsealWithShareFn != nil {
		return m.unsealWithShareFn(ctx, share)
	}
	return &service.UnsealShareProgress{
		Required:  3,
		Submitted: 1,
		Complete:  false,
	}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShares(ctx context.Context, shares []string) error {
	if m.unsealWithSharesFn != nil {
		return m.unsealWithSharesFn(ctx, shares)
	}
	return nil
}

func (m *mockBarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.rekeyFn != nil {
		return m.rekeyFn(ctx, threshold, total)
	}
	return &service.ShamirInitInfo{
		Shares:      []string{"new-1", "new-2", "new-3"},
		Threshold:   threshold,
		TotalShares: total,
	}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryFn != nil {
		return m.generateRecoveryFn(ctx, threshold, total)
	}
	return &service.RecoveryKeysInfo{
		Keys:      []string{"rk-1", "rk-2"},
		Threshold: threshold,
		Total:     total,
	}, nil
}

func (m *mockBarrierService) BarrierRecoverWithKeys(ctx context.Context, keys []string) error {
	if m.recoverWithKeysFn != nil {
		return m.recoverWithKeysFn(ctx, keys)
	}
	return nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryKeysFn != nil {
		return m.deleteRecoveryKeysFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return false, nil
}

func (m *mockBarrierService) BarrierGenerateRootToken(ctx context.Context, shares []string) (*service.RootTokenInfo, error) {
	if m.generateRootTokenFn != nil {
		return m.generateRootTokenFn(ctx, shares)
	}
	return &service.RootTokenInfo{
		Token:     "root-token-abc",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}, nil
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
// NilService: all endpoints return 501 when barrierSvc is nil
// ---------------------------------------------------------------------------

func TestBarrierHandlers_NilService(t *testing.T) {
	// Build a server without a barrier service. The route-based endpoints
	// will not be registered, so dispatch returns 404. To test the handler
	// guard logic directly (which returns 501), we call handlers on a bare
	// Server that has nil barrierSvc.
	srv := &Server{barrierSvc: nil}

	handlers := []struct {
		name    string
		method  string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"Initialize", http.MethodPost, "/v1/barrier/initialize", `{"secret":"s"}`, srv.handleBarrierInitialize},
		{"Unseal", http.MethodPost, "/v1/barrier/unseal", `{"secret":"s"}`, srv.handleBarrierUnseal},
		{"Seal", http.MethodPost, "/v1/barrier/seal", "", srv.handleBarrierSeal},
		{"Status", http.MethodGet, "/v1/barrier/status", "", srv.handleBarrierStatus},
		{"ShamirInitialize", http.MethodPost, "/v1/barrier/shamir/initialize", `{"secret":"s","threshold":2,"total_shares":3}`, srv.handleBarrierShamirInitialize},
		{"ShamirUnsealShare", http.MethodPost, "/v1/barrier/shamir/unseal-share", `{"share":"abc"}`, srv.handleBarrierShamirUnsealShare},
		{"ShamirUnsealShares", http.MethodPost, "/v1/barrier/shamir/unseal-shares", `{"shares":["a","b"]}`, srv.handleBarrierShamirUnsealShares},
		{"Rekey", http.MethodPost, "/v1/barrier/rekey", `{"threshold":2,"total":3}`, srv.handleBarrierRekey},
		{"RecoveryGenerate", http.MethodPost, "/v1/barrier/recovery/generate", `{"threshold":2,"total":3}`, srv.handleBarrierRecoveryGenerate},
		{"RecoveryRecover", http.MethodPost, "/v1/barrier/recovery/recover", `{"keys":["k1"]}`, srv.handleBarrierRecoveryRecover},
		{"RecoveryDelete", http.MethodDelete, "/v1/barrier/recovery", "", srv.handleBarrierRecoveryDelete},
		{"RecoveryStatus", http.MethodGet, "/v1/barrier/recovery/status", "", srv.handleBarrierRecoveryStatus},
		{"RootToken", http.MethodPost, "/v1/barrier/root-token", `{"shares":["s1"]}`, srv.handleBarrierRootToken},
	}

	for _, tc := range handlers {
		t.Run(tc.name, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			assertStatus(t, rec, http.StatusNotImplemented)
		})
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: Initialize
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Initialize(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, secret string) error {
			if secret != "my-secret" {
				t.Fatalf("secret = %q, want %q", secret, "my-secret")
			}
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"my-secret"}`))
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
		initializeFn: func(_ context.Context, _ string) error {
			return errors.New("already initialized")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"s"}`))
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
		unsealFn: func(_ context.Context, secret string) error {
			if secret != "my-secret" {
				t.Fatalf("secret = %q, want %q", secret, "my-secret")
			}
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"my-secret"}`))
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
		unsealFn: func(_ context.Context, _ string) error {
			return errors.New("bad secret")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"wrong"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
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
	srv := newBarrierTestServer(t, svc)

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
		initializeShamirFn: func(_ context.Context, secret string, threshold, totalShares int) (*service.ShamirInitInfo, error) {
			if secret != "root" {
				t.Fatalf("secret = %q, want %q", secret, "root")
			}
			if threshold != 2 {
				t.Fatalf("threshold = %d, want 2", threshold)
			}
			if totalShares != 5 {
				t.Fatalf("totalShares = %d, want 5", totalShares)
			}
			return &service.ShamirInitInfo{
				Shares:      []string{"s1", "s2", "s3", "s4", "s5"},
				Threshold:   2,
				TotalShares: 5,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	body := `{"secret":"root","threshold":2,"total_shares":5}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/initialize",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	shares, ok := resp["shares"].([]interface{})
	if !ok || len(shares) != 5 {
		t.Fatalf("shares count = %d, want 5", len(shares))
	}
	if thresh, ok := resp["threshold"].(float64); !ok || int(thresh) != 2 {
		t.Fatalf("threshold = %v, want 2", resp["threshold"])
	}
	if total, ok := resp["total_shares"].(float64); !ok || int(total) != 5 {
		t.Fatalf("total_shares = %v, want 5", resp["total_shares"])
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
		initializeShamirFn: func(_ context.Context, _ string, _, _ int) (*service.ShamirInitInfo, error) {
			return nil, errors.New("shamir init failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	body := `{"secret":"s","threshold":2,"total_shares":3}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/initialize",
		strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Shamir: UnsealShare
// ---------------------------------------------------------------------------

func TestBarrierHandlers_ShamirUnsealShare(t *testing.T) {
	svc := &mockBarrierService{
		unsealWithShareFn: func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
			if share != "my-share" {
				t.Fatalf("share = %q, want %q", share, "my-share")
			}
			return &service.UnsealShareProgress{
				Required:  3,
				Submitted: 1,
				Complete:  false,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/unseal-share",
		strings.NewReader(`{"share":"my-share"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	if req_, ok := resp["required"].(float64); !ok || int(req_) != 3 {
		t.Fatalf("required = %v, want 3", resp["required"])
	}
	if sub, ok := resp["submitted"].(float64); !ok || int(sub) != 1 {
		t.Fatalf("submitted = %v, want 1", resp["submitted"])
	}
	if comp, ok := resp["complete"].(bool); !ok || comp {
		t.Fatalf("complete = %v, want false", resp["complete"])
	}
}

func TestBarrierHandlers_ShamirUnsealShare_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/unseal-share",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_ShamirUnsealShare_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		unsealWithShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return nil, errors.New("invalid share")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/unseal-share",
		strings.NewReader(`{"share":"bad"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Shamir: UnsealShares (batch)
// ---------------------------------------------------------------------------

func TestBarrierHandlers_ShamirUnsealShares(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		unsealWithSharesFn: func(_ context.Context, shares []string) error {
			if len(shares) != 2 {
				t.Fatalf("shares len = %d, want 2", len(shares))
			}
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/unseal-shares",
		strings.NewReader(`{"shares":["a","b"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierUnsealWithShares was not called")
	}
}

func TestBarrierHandlers_ShamirUnsealShares_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/unseal-shares",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_ShamirUnsealShares_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		unsealWithSharesFn: func(_ context.Context, _ []string) error {
			return errors.New("threshold not met")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/unseal-shares",
		strings.NewReader(`{"shares":["a"]}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
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
// Rekey
// ---------------------------------------------------------------------------

func TestBarrierHandlers_Rekey(t *testing.T) {
	svc := &mockBarrierService{
		rekeyFn: func(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
			if threshold != 3 {
				t.Fatalf("threshold = %d, want 3", threshold)
			}
			if total != 5 {
				t.Fatalf("total = %d, want 5", total)
			}
			return &service.ShamirInitInfo{
				Shares:      []string{"r1", "r2", "r3", "r4", "r5"},
				Threshold:   3,
				TotalShares: 5,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey",
		strings.NewReader(`{"threshold":3,"total":5}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	shares, ok := resp["shares"].([]interface{})
	if !ok || len(shares) != 5 {
		t.Fatalf("shares count = %d, want 5", len(shares))
	}
}

func TestBarrierHandlers_Rekey_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_Rekey_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		rekeyFn: func(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
			return nil, errors.New("rekey failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey",
		strings.NewReader(`{"threshold":2,"total":3}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected error status, got 200")
	}
}

// ---------------------------------------------------------------------------
// Recovery: Generate
// ---------------------------------------------------------------------------

func TestBarrierHandlers_RecoveryGenerate(t *testing.T) {
	svc := &mockBarrierService{
		generateRecoveryFn: func(_ context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
			if threshold != 2 {
				t.Fatalf("threshold = %d, want 2", threshold)
			}
			if total != 4 {
				t.Fatalf("total = %d, want 4", total)
			}
			return &service.RecoveryKeysInfo{
				Keys:      []string{"rk1", "rk2", "rk3", "rk4"},
				Threshold: 2,
				Total:     4,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/generate",
		strings.NewReader(`{"threshold":2,"total":4}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	keys, ok := resp["keys"].([]interface{})
	if !ok || len(keys) != 4 {
		t.Fatalf("keys count = %d, want 4", len(keys))
	}
	if thresh, ok := resp["threshold"].(float64); !ok || int(thresh) != 2 {
		t.Fatalf("threshold = %v, want 2", resp["threshold"])
	}
	if total, ok := resp["total"].(float64); !ok || int(total) != 4 {
		t.Fatalf("total = %v, want 4", resp["total"])
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
		generateRecoveryFn: func(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
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
// Recovery: Recover
// ---------------------------------------------------------------------------

func TestBarrierHandlers_RecoveryRecover(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		recoverWithKeysFn: func(_ context.Context, keys []string) error {
			if len(keys) != 2 {
				t.Fatalf("keys len = %d, want 2", len(keys))
			}
			called = true
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/recover",
		strings.NewReader(`{"keys":["k1","k2"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Fatal("BarrierRecoverWithKeys was not called")
	}
}

func TestBarrierHandlers_RecoveryRecover_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/recover",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_RecoveryRecover_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		recoverWithKeysFn: func(_ context.Context, _ []string) error {
			return errors.New("recovery failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/recover",
		strings.NewReader(`{"keys":["k1"]}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatal("expected error status, got 204")
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

	if hasKeys, ok := resp["has_recovery_keys"].(bool); !ok || !hasKeys {
		t.Fatalf("has_recovery_keys = %v, want true", resp["has_recovery_keys"])
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

	if hasKeys, ok := resp["has_recovery_keys"].(bool); !ok || hasKeys {
		t.Fatalf("has_recovery_keys = %v, want false", resp["has_recovery_keys"])
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
// Root Token
// ---------------------------------------------------------------------------

func TestBarrierHandlers_RootToken(t *testing.T) {
	tokenTime := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)
	svc := &mockBarrierService{
		generateRootTokenFn: func(_ context.Context, shares []string) (*service.RootTokenInfo, error) {
			if len(shares) != 3 {
				t.Fatalf("shares len = %d, want 3", len(shares))
			}
			return &service.RootTokenInfo{
				Token:     "tok-abc123",
				CreatedAt: tokenTime,
			}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/root-token",
		strings.NewReader(`{"shares":["s1","s2","s3"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)

	if token, ok := resp["token"].(string); !ok || token != "tok-abc123" {
		t.Fatalf("token = %v, want %q", resp["token"], "tok-abc123")
	}
	if at, ok := resp["created_at"].(string); !ok || at != "2026-03-04T15:30:00Z" {
		t.Fatalf("created_at = %v, want %q", resp["created_at"], "2026-03-04T15:30:00Z")
	}
}

func TestBarrierHandlers_RootToken_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/root-token",
		strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHandlers_RootToken_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		generateRootTokenFn: func(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
			return nil, errors.New("token generation failed")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/root-token",
		strings.NewReader(`{"shares":["s1"]}`))
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
		{"UnsealShare_GET", http.MethodGet, "/v1/barrier/shamir/unseal-share"},
		{"UnsealShares_GET", http.MethodGet, "/v1/barrier/shamir/unseal-shares"},
		{"Rekey_GET", http.MethodGet, "/v1/barrier/rekey"},
		{"RecoveryGenerate_GET", http.MethodGet, "/v1/barrier/recovery/generate"},
		{"RecoveryRecover_GET", http.MethodGet, "/v1/barrier/recovery/recover"},
		{"RecoveryDelete_GET", http.MethodGet, "/v1/barrier/recovery"},
		{"RecoveryStatus_POST", http.MethodPost, "/v1/barrier/recovery/status"},
		{"RootToken_GET", http.MethodGet, "/v1/barrier/root-token"},
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

func TestShamirInitToResponse(t *testing.T) {
	info := &service.ShamirInitInfo{
		Shares:      []string{"a", "b"},
		Threshold:   1,
		TotalShares: 2,
	}

	resp := shamirInitToResponse(info)

	shares, ok := resp["shares"].([]string)
	if !ok || len(shares) != 2 {
		t.Fatalf("shares = %v, want 2 items", resp["shares"])
	}
	if thresh, ok := resp["threshold"].(int); !ok || thresh != 1 {
		t.Fatalf("threshold = %v, want 1", resp["threshold"])
	}
	if total, ok := resp["total_shares"].(int); !ok || total != 2 {
		t.Fatalf("total_shares = %v, want 2", resp["total_shares"])
	}
}

func TestUnsealShareProgressToResponse(t *testing.T) {
	info := &service.UnsealShareProgress{
		Required:  3,
		Submitted: 2,
		Complete:  false,
	}

	resp := unsealShareProgressToResponse(info)

	if req, ok := resp["required"].(int); !ok || req != 3 {
		t.Fatalf("required = %v, want 3", resp["required"])
	}
	if sub, ok := resp["submitted"].(int); !ok || sub != 2 {
		t.Fatalf("submitted = %v, want 2", resp["submitted"])
	}
	if comp, ok := resp["complete"].(bool); !ok || comp {
		t.Fatalf("complete = %v, want false", resp["complete"])
	}
}

func TestRecoveryKeysToResponse(t *testing.T) {
	info := &service.RecoveryKeysInfo{
		Keys:      []string{"k1", "k2"},
		Threshold: 1,
		Total:     2,
	}

	resp := recoveryKeysToResponse(info)

	keys, ok := resp["keys"].([]string)
	if !ok || len(keys) != 2 {
		t.Fatalf("keys = %v, want 2 items", resp["keys"])
	}
	if thresh, ok := resp["threshold"].(int); !ok || thresh != 1 {
		t.Fatalf("threshold = %v, want 1", resp["threshold"])
	}
	if total, ok := resp["total"].(int); !ok || total != 2 {
		t.Fatalf("total = %v, want 2", resp["total"])
	}
}

func TestRootTokenToResponse(t *testing.T) {
	tokenTime := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	info := &service.RootTokenInfo{
		Token:     "tok-xyz",
		CreatedAt: tokenTime,
	}

	resp := rootTokenToResponse(info)

	if token, ok := resp["token"].(string); !ok || token != "tok-xyz" {
		t.Fatalf("token = %v, want %q", resp["token"], "tok-xyz")
	}
	if at, ok := resp["created_at"].(string); !ok || at != "2026-12-25T00:00:00Z" {
		t.Fatalf("created_at = %v, want %q", resp["created_at"], "2026-12-25T00:00:00Z")
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
		name              string
		strategy          string
		shamirThreshold   int
		shamirTotal       int
		wantThresholdKey  bool
		wantTotalKey      bool
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
		strings.NewReader(`{"secret":"s"}`))
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
