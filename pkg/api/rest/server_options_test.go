//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

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
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// ---------------------------------------------------------------------------
// Error type Error() methods
// ---------------------------------------------------------------------------

func TestTenantNotFoundError_Error(t *testing.T) {
	err := &TenantNotFoundError{TenantID: "acme"}
	want := "service: tenant not found: acme"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestDatabaseNotFoundError_Error(t *testing.T) {
	err := &DatabaseNotFoundError{TenantID: "acme", Database: "mydb"}
	want := "service: database not found: mydb (tenant: acme)"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestBarrierSealedError_Error(t *testing.T) {
	err := &BarrierSealedError{TenantID: "acme"}
	want := "service: barrier sealed for tenant: acme"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestValidationError_Error(t *testing.T) {
	err := &ValidationError{Field: "key"}
	want := "rest: validation error: key"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Server options: WithKVService, WithAdminService, WithWebUI, WithAuditLogger,
//                 WithAuthFactorService, WithCustodianService, WithSOAuthenticator,
//                 WithServiceRegistry
// ---------------------------------------------------------------------------

func TestWithKVService_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithKVService(nil))
	if srv.kvSvc != nil {
		t.Error("expected kvSvc to be nil")
	}
}

func TestWithAdminService_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithAdminService(nil))
	if srv.adminSvc != nil {
		t.Error("expected adminSvc to be nil")
	}
}

func TestWithWebUI_Applied(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := NewServer(nil, nil, ":0", WithWebUI(h))
	if srv.webUI == nil {
		t.Error("expected webUI to be set")
	}
}

func TestWithAuditLogger_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithAuditLogger(nil))
	if srv.auditLogger != nil {
		t.Error("expected auditLogger to be nil")
	}
}

func TestWithAuthFactorService_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithAuthFactorService(nil))
	if srv.authFactorSvc != nil {
		t.Error("expected authFactorSvc to be nil")
	}
}

func TestWithSOAuthenticator_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithSOAuthenticator(nil))
	if srv.soPINVerifier != nil {
		t.Error("expected soPINVerifier to be nil")
	}
}

func TestWithServiceRegistry_Applied(t *testing.T) {
	srv := NewServer(nil, nil, ":0", WithServiceRegistry(nil))
	if srv.serviceRegistry != nil {
		t.Error("expected serviceRegistry to be nil")
	}
}

// ---------------------------------------------------------------------------
// NewSPAHandler / ServeHTTP
// ---------------------------------------------------------------------------

func TestNewSPAHandler_ReturnsNonNil(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html/>")},
	}
	handler := NewSPAHandler(fsys)
	if handler == nil {
		t.Fatal("NewSPAHandler returned nil")
	}
}

func TestSPAHandler_APIPathReturns404(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html/>")},
	}
	handler := NewSPAHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/v1/kv/key", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for API path, got %d", rec.Code)
	}
}

func TestSPAHandler_StaticAssetNotFound(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html/>")},
	}
	handler := NewSPAHandler(fsys)

	// Path with extension that doesn't exist in FS — should return 404.
	req := httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing asset, got %d", rec.Code)
	}
}

func TestSPAHandler_ServesExistingAsset(t *testing.T) {
	cssContent := []byte("body{color:red}")
	fsys := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html/>")},
		"app.css":    &fstest.MapFile{Data: cssContent},
	}
	handler := NewSPAHandler(fsys)

	req := httptest.NewRequest(http.MethodGet, "/app.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for existing asset, got %d", rec.Code)
	}
}
