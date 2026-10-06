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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

// stubAwaitingProbe reports a switchable awaiting-admin-credential state.
type stubAwaitingProbe struct{ awaiting atomic.Bool }

func (p *stubAwaitingProbe) AwaitingAdminCredential() bool { return p.awaiting.Load() }

// markingHandler records that it served the request and answers with code.
func markingHandler(served *string, name string, code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*served = name
		w.WriteHeader(code)
	})
}

func TestAwaitingAdminCredentialMiddleware_Routing(t *testing.T) {
	probe := &stubAwaitingProbe{}
	cases := []struct {
		name     string
		probe    awaitingAdminCredentialProbe
		awaiting bool
		method   string
		path     string
		served   string
		code     int
	}{
		{"nil probe passes to authenticated chain", nil, false, http.MethodGet, "/v1/kv/a", "authenticated", http.StatusOK},
		{"not awaiting passes to authenticated chain", probe, false, http.MethodGet, "/v1/kv/a", "authenticated", http.StatusOK},
		{"awaiting serves allowlisted status on raw router", probe, true, http.MethodGet, "/v1/ceremony/status", "raw", http.StatusAccepted},
		{"awaiting serves allowlisted initialize on raw router", probe, true, http.MethodPost, "/v1/ceremony/initialize", "raw", http.StatusAccepted},
		{"awaiting refuses everything else", probe, true, http.MethodGet, "/v1/kv/a", "", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe.awaiting.Store(tc.awaiting)
			var served string
			raw := markingHandler(&served, "raw", http.StatusAccepted)
			h := AwaitingAdminCredentialMiddleware(tc.probe, raw)(markingHandler(&served, "authenticated", http.StatusOK))

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if served != tc.served || rr.Code != tc.code {
				t.Fatalf("served %q status %d, want %q %d", served, rr.Code, tc.served, tc.code)
			}
		})
	}
}

func TestAwaitingAdminCredentialMiddleware_RefusalShape(t *testing.T) {
	probe := &stubAwaitingProbe{}
	probe.awaiting.Store(true)
	h := AwaitingAdminCredentialMiddleware(probe, http.NotFoundHandler())(http.NotFoundHandler())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/v1/kv/a", strings.NewReader("v")))
	if rr.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q, want 5", rr.Header().Get("Retry-After"))
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body is not JSON: %v (%q)", err, rr.Body.String())
	}
	if body.Code != "AWAITING_ADMIN_CREDENTIAL" || body.Error == "" {
		t.Fatalf("refusal body = %+v", body)
	}
}

// TestNewServer_AwaitingAdminCredentialProbeGatesRouter proves the option is
// wired into the server's real handler chain, not merely stored.
func TestNewServer_AwaitingAdminCredentialProbeGatesRouter(t *testing.T) {
	probe := &stubAwaitingProbe{}
	probe.awaiting.Store(true)
	svc := &volumetest.Service{T: t}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0",
		WithVolumeService(svc), WithAwaitingAdminCredentialProbe(probe))

	rr := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/volume-pools", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while awaiting admin credential", rr.Code)
	}
}

// TestNewServer_MaxBodyBytesCapsRequestBody drives a body-reading handler
// through the full chain: a body under WithMaxBodyBytes reaches the service,
// one over it is refused before the service sees it.
func TestNewServer_MaxBodyBytesCapsRequestBody(t *testing.T) {
	var created int
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		created++
		return &volume.Pool{Name: req.Name, Provider: "dir", Overcommit: 1}, nil
	}}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0",
		WithVolumeService(svc), WithMaxBodyBytes(64))

	small := `{"name":"fast"}`
	rr := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/volume-pools", strings.NewReader(small)))
	if rr.Code != http.StatusCreated || created != 1 {
		t.Fatalf("small body: status %d created %d, want 201 1: %s", rr.Code, created, rr.Body.String())
	}

	large := `{"name":"fast","description":"` + strings.Repeat("x", 128) + `"}`
	rr = httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/volume-pools", strings.NewReader(large)))
	if rr.Code != http.StatusBadRequest || created != 1 {
		t.Fatalf("oversized body: status %d created %d, want 400 1", rr.Code, created)
	}
}

func TestResolveMaxBodyBytes(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{0, DefaultMaxBodyBytes},
		{-1, DefaultMaxBodyBytes},
		{4096, 4096},
	} {
		if got := resolveMaxBodyBytes(tc.in); got != tc.want {
			t.Errorf("resolveMaxBodyBytes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
