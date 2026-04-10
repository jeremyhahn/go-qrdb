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

// Composable server tests: verify that NewServer(nil, nil, addr) doesn't panic
// and that optional services produce the correct HTTP status codes.
//
// All tests use httptest.NewRecorder for in-process dispatch; no network I/O
// is performed and no host-system resources are modified.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// NewServer(nil, nil) construction
// ---------------------------------------------------------------------------

// TestNilServices_Construction verifies that NewServer with both nil KV and
// nil admin services does not panic and returns a usable *Server.
func TestNilServices_Construction(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	if srv == nil {
		t.Fatal("NewServer(nil, nil, addr) returned nil")
	}
}

// ---------------------------------------------------------------------------
// /health returns 404 when adminSvc is nil
// ---------------------------------------------------------------------------

// TestNilServices_Health_ReturnsNotFound verifies that GET /health returns 404
// when the admin service is nil. The route is only registered when adminSvc is
// provided, so calling it with nil adminSvc results in a not-found response.
func TestNilServices_Health_ReturnsNotFound(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /health (nil adminSvc) = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// KV PUT returns 501 when kvSvc is nil
// ---------------------------------------------------------------------------

// TestNilServices_KVPut_ReturnsNotImplemented verifies that a PUT to any KV
// path returns HTTP 501 Not Implemented when the KV service is nil.
func TestNilServices_KVPut_ReturnsNotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPut, "/kv/testkey", bytes.NewReader([]byte("value")))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("PUT /kv/testkey (nil kvSvc) = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

// TestNilServices_KVPut_ErrorBodyContainsServiceName verifies that the 501
// response for a nil KV service produces a JSON error body whose message
// identifies the "kv" service.
func TestNilServices_KVPut_ErrorBodyContainsServiceName(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodPut, "/kv/testkey", bytes.NewReader([]byte("value")))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	// Error is serialized as {"error": "rest: service not implemented: kv"}.
	errMsg, ok := body["error"]
	if !ok {
		t.Fatalf("expected 'error' key in JSON body, got: %v", body)
	}
	if !kvMessageContainsKV(errMsg) {
		t.Fatalf("expected 'kv' in error message, got: %s", errMsg)
	}
}

// kvMessageContainsKV reports whether the error message string contains "kv".
func kvMessageContainsKV(s string) bool {
	for i := 0; i <= len(s)-2; i++ {
		if s[i:i+2] == "kv" {
			return true
		}
	}
	return false
}

// TestNilServices_KVDelete_ReturnsNotImplemented verifies that DELETE /kv/{key}
// returns 501 when the KV service is nil.
func TestNilServices_KVDelete_ReturnsNotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodDelete, "/kv/testkey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("DELETE /kv/testkey (nil kvSvc) = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

// TestNilServices_KVGet_ReturnsNotImplemented verifies that GET /kv/{key}
// returns 501 when the KV service is nil.
func TestNilServices_KVGet_ReturnsNotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/kv/testkey", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("GET /kv/testkey (nil kvSvc) = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

// ---------------------------------------------------------------------------
// Barrier endpoint works when barrierSvc is provided but kvSvc is nil
// ---------------------------------------------------------------------------

// TestNilServices_Barrier_WorksWithoutKV verifies that the barrier status
// endpoint returns 200 OK even when the KV service is nil. This confirms
// composable service injection: consumers can mix barrier + no-KV.
func TestNilServices_Barrier_WorksWithoutKV(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithBarrierService(&embeddableBarrierService{}),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/barrier/status (nil kvSvc, non-nil barrierSvc) = %d, want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestNilServices_BarrierInitialize_WorksWithoutKV verifies that barrier
// initialization returns 204 No Content even when the KV service is nil.
func TestNilServices_BarrierInitialize_WorksWithoutKV(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithBarrierService(&embeddableBarrierService{}),
	)

	body, _ := json.Marshal(map[string]string{"secret": "test-secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /v1/barrier/initialize (nil kvSvc) = %d, want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Unregistered route returns 404 for any nil-service server
// ---------------------------------------------------------------------------

// TestNilServices_UnknownRoute_Returns404 verifies that an unknown path
// returns HTTP 404 and does not panic.
func TestNilServices_UnknownRoute_Returns404(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/nonexistent/path", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nonexistent/path = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestNilServices_BarrierRouteNotRegisteredWhenNil verifies that barrier
// routes return 404 (not panic) when no barrier service is provided.
func TestNilServices_BarrierRouteNotRegisteredWhenNil(t *testing.T) {
	// No WithBarrierService option provided.
	srv := NewServer(nil, nil, "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/barrier/status (nil barrierSvc) = %d, want %d",
			rec.Code, http.StatusNotFound)
	}
}
