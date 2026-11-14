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
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// testHandler is a simple handler that returns 200 OK for all requests.
var testHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestCORSMiddleware_NoOriginHeader(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         3600,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers when Origin is absent")
	}
}

func TestCORSMiddleware_WildcardOrigin(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         3600,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected Access-Control-Allow-Origin *, got %q", got)
	}
}

func TestCORSMiddleware_SpecificOrigin_Allowed(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         7200,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected origin echo, got %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("expected Vary: Origin, got %q", got)
	}
}

func TestCORSMiddleware_SpecificOrigin_Denied(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         3600,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers for denied origin")
	}
}

func TestCORSMiddleware_PreflightWildcard(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         3600,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodOptions, "/kv/mykey", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected *, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("expected Allow-Methods header")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Fatalf("expected headers, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "3600" {
		t.Fatalf("expected max-age 3600, got %q", got)
	}
}

func TestCORSMiddleware_PreflightSpecificOrigin(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         7200,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodOptions, "/kv/mykey", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("expected origin echo, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "7200" {
		t.Fatalf("expected max-age 7200, got %q", got)
	}
}

func TestCORSMiddleware_AllowCredentials(t *testing.T) {
	for _, creds := range []bool{true, false} {
		for _, method := range []string{http.MethodOptions, http.MethodGet} {
			t.Run(fmt.Sprintf("credentials=%v/%s", creds, method), func(t *testing.T) {
				cfg := CORSConfig{
					AllowedOrigins:   []string{"https://app.example.com"},
					AllowedHeaders:   []string{"Content-Type"},
					MaxAge:           60,
					AllowCredentials: creds,
				}
				handler := corsMiddleware(cfg, testHandler)

				req := httptest.NewRequest(method, "/kv/mykey", nil)
				req.Header.Set("Origin", "https://app.example.com")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				want := ""
				if creds {
					want = "true"
				}
				if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != want {
					t.Fatalf("Access-Control-Allow-Credentials = %q, want %q", got, want)
				}
				if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
					t.Fatalf("Access-Control-Allow-Origin = %q, want the origin echoed", got)
				}
			})
		}
	}
}

func TestCORSMiddleware_AllowCredentials_DeniedOrigin(t *testing.T) {
	cfg := CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowCredentials: true}
	handler := corsMiddleware(cfg, testHandler)
	for _, method := range []string{http.MethodOptions, http.MethodGet} {
		req := httptest.NewRequest(method, "/kv/mykey", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Fatalf("%s: Access-Control-Allow-Credentials = %q for an unlisted origin, want none", method, got)
		}
	}
}

func TestCORSMiddleware_PreflightDeniedOrigin(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         3600,
	}
	handler := corsMiddleware(cfg, testHandler)

	req := httptest.NewRequest(http.MethodOptions, "/kv/mykey", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Denied origin passes through to next handler (no preflight response).
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (pass-through), got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers for denied origin")
	}
}

func TestCORSMiddleware_MultipleOrigins(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://a.example.com", "https://b.example.com"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         3600,
	}
	handler := corsMiddleware(cfg, testHandler)

	// First allowed origin.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://a.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://a.example.com" {
		t.Fatalf("expected a.example.com, got %q", got)
	}

	// Second allowed origin.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://b.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://b.example.com" {
		t.Fatalf("expected b.example.com, got %q", got)
	}

	// Unknown origin.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://c.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("expected no CORS headers for unknown origin")
	}
}

func TestWithCORS_Option(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         3600,
	}
	s := &Server{}
	opt := WithCORS(cfg)
	opt(s)
	if s.corsConfig == nil {
		t.Fatal("expected corsConfig to be set")
	}
	if s.corsConfig.MaxAge != 3600 {
		t.Fatalf("expected MaxAge 3600, got %d", s.corsConfig.MaxAge)
	}
}

func TestWithCORS_IntegrationWithServer(t *testing.T) {
	kvSvc := &mockKVService{}
	adminSvc := &mockAdminService{}
	corsConfig := CORSConfig{
		AllowedOrigins: []string{"*"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         3600,
	}
	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(kvSvc, adminSvc, dualStackAddr(t), WithCORS(corsConfig), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// Make a CORS preflight request over TLS (the transport is TLS-only).
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
	req, _ := http.NewRequest(http.MethodOptions, "https://"+srv.Addr()+"/health", nil)
	req.Header.Set("Origin", "https://browser.example.com")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected *, got %q", got)
	}
}

func TestWithCORS_ServerPreflightListedOriginOnly(t *testing.T) {
	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, dualStackAddr(t), WithTLS(tlsCfg), WithCORS(CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedHeaders: []string{"Authorization"},
		MaxAge:         600,
	}))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
	defer client.CloseIdleConnections()

	for origin, want := range map[string]string{
		"https://app.example.com":  "https://app.example.com",
		"https://evil.example.com": "",
	} {
		req, _ := http.NewRequest(http.MethodOptions, "https://"+srv.Addr()+"/health", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %s: Access-Control-Allow-Origin = %q, want %q", origin, got, want)
		}
	}
}
