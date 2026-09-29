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

// Package-level embeddability integration tests.
//
// These tests verify that the REST server can be started with only a subset
// of services populated. This architectural property lets go-xkms (which
// absorbed go-trusted-ca) embed the QRDB REST server with only the services it needs
// (e.g., barrier + cert) without being forced to provide KV or admin services.
//
// All tests use httptest.NewRecorder for in-process dispatch; no network I/O
// is performed and no host-system resources are modified.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock barrier service (minimal, embeddability-test scope)
// ---------------------------------------------------------------------------

// embeddableBarrierService implements service.BarrierServicer for the
// embeddability tests. All methods return a well-typed success result by
// default; per-test overrides are not needed for embeddability validation.
type embeddableBarrierService struct {
	service.BarrierServicer
}

var _ service.BarrierServicer = (*embeddableBarrierService)(nil)

func (s *embeddableBarrierService) BarrierInitialize(_ context.Context, _ []byte) error {
	return nil
}

func (s *embeddableBarrierService) BarrierUnseal(_ context.Context, _ []byte) error {
	return nil
}

func (s *embeddableBarrierService) BarrierSeal(_ context.Context) error {
	return nil
}

func (s *embeddableBarrierService) BarrierStatus(_ context.Context) (*service.BarrierStatusInfo, error) {
	return &service.BarrierStatusInfo{
		Sealed:         false,
		Strategy:       "shamir",
		HardwareBacked: false,
		InitializedAt:  time.Now(),
	}, nil
}

func (s *embeddableBarrierService) BarrierInitializeShamir(_ context.Context, threshold, total int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total, ShareCount: total}, nil
}

func (s *embeddableBarrierService) BarrierDeleteRecoveryKeys(_ context.Context) error {
	return nil
}

func (s *embeddableBarrierService) BarrierHasRecoveryKeys(_ context.Context) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// Helper: build an embeddable server with barrier only (nil KV, nil admin)
// ---------------------------------------------------------------------------

// newBarrierOnlyServer creates a REST server that has only the barrier
// service populated. KV and admin services are nil.
func newBarrierOnlyServer(barrierSvc service.BarrierServicer) *Server {
	return NewServer(nil, nil, "127.0.0.1:0", WithBarrierService(barrierSvc))
}

// ---------------------------------------------------------------------------
// Embeddability: barrier operations work without KV / admin services
// ---------------------------------------------------------------------------

// TestEmbeddable_BarrierStatus_NoKVNoAdmin verifies that the barrier status
// endpoint works when no KV or admin service is provided. This is the primary
// embeddability proof: a consumer can embed QRDB with only barrier services.
func TestEmbeddable_BarrierStatus_NoKVNoAdmin(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/barrier/status status = %d, want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestEmbeddable_BarrierInitialize_NoKVNoAdmin verifies that barrier
// initialization works when the admin and KV services are nil. The endpoint
// returns 204 No Content on success (no response body).
func TestEmbeddable_BarrierInitialize_NoKVNoAdmin(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	body, _ := json.Marshal(map[string]any{"secret": []byte("my-master-secret")}) // []byte -> base64, the wire shape of a secret field
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /v1/barrier/initialize status = %d, want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

// TestEmbeddable_BarrierUnseal_NoKVNoAdmin verifies that barrier unseal works
// when the admin and KV services are nil. The endpoint returns 204 No Content
// on success.
func TestEmbeddable_BarrierUnseal_NoKVNoAdmin(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	body, _ := json.Marshal(map[string]any{"secret": []byte("my-master-secret")}) // []byte -> base64, the wire shape of a secret field
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /v1/barrier/unseal status = %d, want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

// TestEmbeddable_BarrierShamirInitialize_NoKVNoAdmin verifies that Shamir
// initialization works when the admin and KV services are nil.
func TestEmbeddable_BarrierShamirInitialize_NoKVNoAdmin(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	body, _ := json.Marshal(map[string]interface{}{
		"threshold":    2,
		"total_shares": 3,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/initialize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/barrier/shamir/initialize status = %d, want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Embeddability: KV operations return 501 (not 500/panic) when kvSvc is nil
// ---------------------------------------------------------------------------

// TestEmbeddable_KVPut_NilKVService verifies that a PUT request to the KV
// endpoint returns HTTP 501 Not Implemented rather than panicking when the
// KV service is not configured. This is a hard correctness requirement for
// embeddable deployments.
func TestEmbeddable_KVPut_NilKVService(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodPut, "/kv/mykey", bytes.NewReader([]byte("value")))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("PUT /kv/mykey (nil kvSvc) status = %d, want %d; body: %s",
			rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
}

// TestEmbeddable_KVGet_NilKVService verifies that a GET request to the KV
// endpoint returns HTTP 501 when the KV service is not configured.
func TestEmbeddable_KVGet_NilKVService(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodGet, "/kv/mykey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("GET /kv/mykey (nil kvSvc) status = %d, want %d; body: %s",
			rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
}

// TestEmbeddable_KVDelete_NilKVService verifies that a DELETE request to the
// KV endpoint returns HTTP 501 when the KV service is not configured.
func TestEmbeddable_KVDelete_NilKVService(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodDelete, "/kv/mykey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("DELETE /kv/mykey (nil kvSvc) status = %d, want %d; body: %s",
			rec.Code, http.StatusNotImplemented, rec.Body.String())
	}
}

// TestEmbeddable_KVError_HasKVInMessage verifies that the 501 response body
// contains a typed error message that identifies the "kv" service.
func TestEmbeddable_KVError_HasKVInMessage(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodGet, "/kv/anykey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	// The error message is serialized under the "error" key and includes "kv".
	errMsg, ok := body["error"]
	if !ok {
		t.Fatalf("expected error key in JSON body, got: %v", body)
	}
	if !contains(errMsg, "kv") {
		t.Fatalf("expected 'kv' in error message, got: %s", errMsg)
	}
}

// contains reports whether substr is found within s (avoids importing strings).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Embeddability: admin endpoints return 404 when adminSvc is nil
// ---------------------------------------------------------------------------

// TestEmbeddable_Health_NilAdminService verifies that /health returns 404 (not
// found) when the admin service is not configured. The route is not registered
// when adminSvc is nil, so the server returns 404 rather than panicking.
func TestEmbeddable_Health_NilAdminService(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	// /health is only registered when adminSvc != nil.
	// When adminSvc is nil the route does not exist and dispatch returns 404.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /health (nil adminSvc) status = %d, want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TestEmbeddable_Status_NilAdminService verifies that /status returns 404 when
// the admin service is not configured.
func TestEmbeddable_Status_NilAdminService(t *testing.T) {
	srv := newBarrierOnlyServer(&embeddableBarrierService{})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /status (nil adminSvc) status = %d, want %d",
			rec.Code, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// Embeddability: server lifecycle with nil services
// ---------------------------------------------------------------------------

// TestEmbeddable_ServerStart_NilServices verifies that NewServer with nil KV
// and admin services starts and stops without panicking. This covers the full
// lifecycle of an embeddable deployment that only uses the barrier service.
func TestEmbeddable_ServerStart_NilServices(t *testing.T) {
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(nil, nil, dualStackAddr(t), WithBarrierService(&embeddableBarrierService{}), WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with nil KV/admin services returned error: %v", err)
	}

	addr := srv.Addr()
	if addr == "" || addr == "127.0.0.1:0" {
		t.Fatalf("Addr() after Start() = %q, want resolved port", addr)
	}

	srv.Stop()
}

// TestEmbeddable_ServerStart_AllNilServices verifies that NewServer with all
// nil services (no options) starts and stops without panicking. This is the
// minimum viable embeddable configuration.
func TestEmbeddable_ServerStart_AllNilServices(t *testing.T) {
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(nil, nil, dualStackAddr(t), WithTLS(tlsCfg))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() with all nil services returned error: %v", err)
	}

	srv.Stop()
}
