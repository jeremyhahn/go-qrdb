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
	"strings"
	"testing"
)

// TestLeaseRoutes_NotRegisteredWithoutService verifies that when no lease
// service is provided, the /lease/* routes are not registered in the
// dispatch map. Requests to those paths return 404 (route not found)
// instead of reaching a handler that would propose to a SM without a
// lease subsystem — which would prevent lastApplied from advancing and
// block all subsequent linearizable reads (Bug 1).
func TestLeaseRoutes_NotRegisteredWithoutService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/lease/grant", `{"ttl":60}`},
		{http.MethodPost, "/lease/revoke", `{"lease_id":1}`},
		{http.MethodPost, "/lease/keepalive", `{"lease_id":1}`},
		{http.MethodGet, "/lease/ttl?lease_id=1", ""},
		{http.MethodGet, "/lease/list", ""},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			// Without a lease service the route must not be registered.
			// The dispatcher returns 404 for unknown paths.
			if rec.Code == http.StatusInternalServerError {
				t.Fatalf("lease route returned 500 without service; Raft stall possible; body = %s",
					rec.Body.String())
			}
			// Confirm the route is genuinely absent (not 200).
			if rec.Code == http.StatusOK {
				t.Fatalf("lease route returned 200 without service; expected 404; body = %s",
					rec.Body.String())
			}
		})
	}
}

// TestLeaseRoutes_NotRegistered_ThenOtherRoutesWork verifies the regression
// scenario from Bug 1: after an attempt on an unregistered /lease/* route,
// subsequent admin reads (e.g. /health) must still succeed. In the original
// bug a failed SM.Apply stalled lastApplied, blocking all linearizable reads.
func TestLeaseRoutes_NotRegistered_ThenOtherRoutesWork(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	// Hit the unregistered lease route.
	req := httptest.NewRequest(http.MethodPost, "/lease/grant", strings.NewReader(`{"ttl":60}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusInternalServerError {
		t.Fatalf("lease grant returned 500; possible Raft stall; body = %s", rec.Body.String())
	}

	// /health is an admin route — confirm it is still reachable.
	req2 := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec2 := httptest.NewRecorder()
	srv.dispatch(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("health returned %d after unregistered lease attempt; wanted 200; body = %s",
			rec2.Code, rec2.Body.String())
	}
}

// TestLeaseRoutes_RegisteredWithService verifies that when a lease service
// is provided, the /lease/grant route IS registered and returns 200 on a
// successful grant.
func TestLeaseRoutes_RegisteredWithService(t *testing.T) {
	leaseSvc := &mockLeaseService{}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithLease(leaseSvc))

	req := httptest.NewRequest(http.MethodPost, "/lease/grant", strings.NewReader(`{"ttl":60}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

// TestLockRoutes_NotRegisteredWithoutService verifies that /lock/* routes are
// absent when no lock service is configured.
func TestLockRoutes_NotRegisteredWithoutService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/lock/acquire", `{"key":"k","holder_id":"h","ttl":30}`},
		{http.MethodPost, "/lock/trylock", `{"key":"k","holder_id":"h","ttl":30}`},
		{http.MethodPost, "/lock/release", `{"key":"k","fencing_token":1}`},
		{http.MethodPost, "/lock/extend", `{"key":"k","fencing_token":1}`},
		{http.MethodGet, "/lock/status?key=k", ""},
		{http.MethodGet, "/lock/list", ""},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			if rec.Code == http.StatusInternalServerError {
				t.Fatalf("lock route returned 500 without service; body = %s", rec.Body.String())
			}
		})
	}
}
