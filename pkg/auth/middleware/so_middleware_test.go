//go:build !integration

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// discardLogger MOVED UP, to so_audit_failclosed_test.go, which carries NO
// build constraint (go-qrdb#445). This file is //go:build !integration and
// that proof is not, so the helper's constraint has to be the superset of
// every caller's: the helper moves up rather than the constraint being pushed
// down onto a security proof that must run in every tag set.

// ---------------------------------------------------------------------------
// Mock helpers for SOMiddleware tests
// ---------------------------------------------------------------------------

type mockSOVerifier struct {
	err   error
	calls atomic.Int64
}

func (m *mockSOVerifier) VerifySOPIN(_ context.Context, _ string, _ []byte) error {
	m.calls.Add(1)
	return m.err
}

type mockSOAuditLogger struct {
	events []audit.AuditEvent
}

func (m *mockSOAuditLogger) Emit(_ context.Context, evt audit.AuditEvent) error {
	m.events = append(m.events, evt)
	return nil
}

func (m *mockSOAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (m *mockSOAuditLogger) Close() error { return nil }

// newSORequest builds an httptest.Request with TLS state set and an
// optional X-QRDB-SO-PIN header.
func newSORequest(method, path, pin string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.TLS = &tls.ConnectionState{}
	if pin != "" {
		req.Header.Set(SOPINHeader, pin)
	}
	return req
}

// withSOIdentity stores an SO identity in the request context. soRoleID is
// the identifier the role store persisted the SO role under (seededSORoles):
// identity.Roles carries role identifiers, never role names (go-qrdb#569).
func withSOIdentity(req *http.Request, tenantID, soRoleID string) *http.Request {
	id := &auth.Identity{
		Subject:  "custodian",
		TenantID: tenantID,
		Roles:    []string{soRoleID},
	}
	return req.WithContext(auth.WithIdentity(req.Context(), id))
}

// ---------------------------------------------------------------------------
// SOMiddleware — plain HTTP rejected with 401
// ---------------------------------------------------------------------------

func TestSOMiddleware_PlainHTTP_Rejected(t *testing.T) {
	roles, _ := seededSORoles(t)
	verifier := &mockSOVerifier{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next should not be called for plain HTTP")
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", nil)
	// req.TLS is nil — plain HTTP
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if verifier.calls.Load() != 0 {
		t.Error("VerifySOPIN should not be called for plain HTTP")
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — unmapped route passes through
// ---------------------------------------------------------------------------

func TestSOMiddleware_UnmappedRoute_PassesThrough(t *testing.T) {
	roles, _ := seededSORoles(t)
	verifier := &mockSOVerifier{}
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodGet, "/v1/status", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !nextCalled {
		t.Error("expected next to be called for unmapped route")
	}
	if verifier.calls.Load() != 0 {
		t.Error("VerifySOPIN should not be called for unmapped routes")
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — mapped but non-SO route passes through
// ---------------------------------------------------------------------------

func TestSOMiddleware_NonSOPermission_PassesThrough(t *testing.T) {
	roles, _ := seededSORoles(t)
	verifier := &mockSOVerifier{}
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	// PUT /v1/kv/{key} maps to KVPut which is NOT an SO permission.
	req := newSORequest(http.MethodPut, "/v1/kv/mykey", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !nextCalled {
		t.Error("expected next to be called for non-SO permission")
	}
	if verifier.calls.Load() != 0 {
		t.Error("VerifySOPIN should not be called for non-SO permissions")
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — SO route, identity missing → 403
// ---------------------------------------------------------------------------

func TestSOMiddleware_SORoute_NoIdentity_Forbidden(t *testing.T) {
	roles, _ := seededSORoles(t)
	verifier := &mockSOVerifier{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next should not be called without identity")
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodPost, "/v1/ceremony/initialize", "correct-pin")
	// No identity stored in context.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — SO route, identity without SO role → 403
// ---------------------------------------------------------------------------

func TestSOMiddleware_SORoute_NonSORole_Forbidden(t *testing.T) {
	roles, _ := seededSORoles(t)
	verifier := &mockSOVerifier{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next should not be called without SO role")
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodPost, "/v1/ceremony/initialize", "pin")
	id := &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"operator"}}
	req = req.WithContext(auth.WithIdentity(req.Context(), id))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — SO route, SO identity, missing PIN → 401 + audit failure
// ---------------------------------------------------------------------------

func TestSOMiddleware_SORoute_MissingPIN_Unauthorized(t *testing.T) {
	roles, soRoleID := seededSORoles(t)
	verifier := &mockSOVerifier{}
	al := &mockSOAuditLogger{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next should not be called when PIN is missing")
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, al, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodPost, "/v1/ceremony/initialize", "" /* no PIN */)
	req = withSOIdentity(req, soGateTenant, soRoleID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if len(al.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(al.events))
	}
	if al.events[0].Outcome == "" {
		t.Error("expected non-empty audit outcome")
	}
	if verifier.calls.Load() != 0 {
		t.Error("VerifySOPIN should not be called when PIN header is missing")
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — SO route, SO identity, wrong PIN → 401 + audit failure
// ---------------------------------------------------------------------------

func TestSOMiddleware_SORoute_WrongPIN_Unauthorized(t *testing.T) {
	roles, soRoleID := seededSORoles(t)
	verifier := &mockSOVerifier{err: errors.New("pin mismatch")}
	al := &mockSOAuditLogger{}
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next should not be called with wrong PIN")
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, al, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodPost, "/v1/ceremony/initialize", "wrong-pin")
	req = withSOIdentity(req, soGateTenant, soRoleID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if verifier.calls.Load() != 1 {
		t.Errorf("expected 1 VerifySOPIN call, got %d", verifier.calls.Load())
	}
	if len(al.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(al.events))
	}

	var errResp restErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("expected non-empty error field")
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — SO route, SO identity, correct PIN → next called + audit success
// ---------------------------------------------------------------------------

func TestSOMiddleware_SORoute_CorrectPIN_Success(t *testing.T) {
	roles, soRoleID := seededSORoles(t)
	verifier := &mockSOVerifier{err: nil}
	al := &mockSOAuditLogger{}
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, al, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodPost, "/v1/ceremony/initialize", "correct-pin")
	req = withSOIdentity(req, soGateTenant, soRoleID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !nextCalled {
		t.Error("expected next to be called on success")
	}
	if verifier.calls.Load() != 1 {
		t.Errorf("expected 1 VerifySOPIN call, got %d", verifier.calls.Load())
	}
	if len(al.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(al.events))
	}
	if al.events[0].Outcome != audit.OutcomeSuccess {
		t.Errorf("expected success outcome, got %q", al.events[0].Outcome)
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — nil audit logger (no panic)
// ---------------------------------------------------------------------------

func TestSOMiddleware_NilAuditLogger_NoPanic(t *testing.T) {
	roles, soRoleID := seededSORoles(t)
	verifier := &mockSOVerifier{err: errors.New("bad pin")}
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next should not be called")
	})

	// nil audit logger — must not panic.
	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	req := newSORequest(http.MethodPost, "/v1/ceremony/initialize", "pin")
	req = withSOIdentity(req, soGateTenant, soRoleID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// SOMiddleware — plain HTTP on a NON-SO route passes through
// ---------------------------------------------------------------------------

// TestSOMiddleware_PlainHTTP_NonSORoute_PassesThrough verifies the TLS
// requirement is scoped to SO-gated routes only: a mapped non-SO route
// over plain HTTP must pass through untouched, so enabling the SO
// dual-factor flag never changes behavior outside SOPermissions.
func TestSOMiddleware_PlainHTTP_NonSORoute_PassesThrough(t *testing.T) {
	roles, _ := seededSORoles(t)
	verifier := &mockSOVerifier{}
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := SOMiddleware(testRESTRoutes(), roles, verifier, nil, audit.FailClosed, discardLogger(), next)

	// GET /v1/barrier/status maps to a non-SO permission; req.TLS is nil.
	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !nextCalled {
		t.Error("expected next to be called for a non-SO route over plain HTTP")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if verifier.calls.Load() != 0 {
		t.Error("VerifySOPIN should not be called for non-SO routes")
	}
}
