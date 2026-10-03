// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleMintJoinToken
// ---------------------------------------------------------------------------

func TestHandleMintJoinToken_NotConfigured(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens", strings.NewReader(`{"node_id":"node-1"}`))
	rec := httptest.NewRecorder()
	s.handleMintJoinToken(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleMintJoinToken_BadBody(t *testing.T) {
	admin := newDefaultAdminSvc()
	admin.mintJoinTokenFn = func(context.Context, string, time.Duration) (string, time.Time, error) {
		return "tok", time.Now(), nil
	}
	s := NewServer(&mockKVService{}, admin, ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleMintJoinToken(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleMintJoinToken_Success(t *testing.T) {
	admin := newDefaultAdminSvc()
	wantExpiry := time.Now().Add(time.Hour).UTC()
	var gotNodeID string
	var gotTTL time.Duration
	admin.mintJoinTokenFn = func(_ context.Context, nodeID string, ttl time.Duration) (string, time.Time, error) {
		gotNodeID, gotTTL = nodeID, ttl
		return "s3cr3t-token", wantExpiry, nil
	}
	s := NewServer(&mockKVService{}, admin, ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens",
		strings.NewReader(`{"node_id":"node-1","ttl_seconds":3600}`))
	rec := httptest.NewRecorder()
	s.handleMintJoinToken(rec, req)
	assertStatus(t, rec, http.StatusCreated)

	var resp mintJoinTokenResponse
	decodeJSON(t, rec, &resp)
	if resp.Token != "s3cr3t-token" {
		t.Errorf("token = %q, want s3cr3t-token", resp.Token)
	}
	if !resp.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("expires_at = %v, want %v", resp.ExpiresAt, wantExpiry)
	}
	if gotNodeID != "node-1" {
		t.Errorf("minter received node_id = %q, want node-1", gotNodeID)
	}
	if gotTTL != time.Hour {
		t.Errorf("minter received ttl = %v, want 1h", gotTTL)
	}
}

// TestHandleMintJoinToken_SetsCacheControlNoStore proves the response
// carrying the single-use token is never cacheable: an intermediary or
// browser cache holding onto it would be a second place it could leak
// from.
func TestHandleMintJoinToken_SetsCacheControlNoStore(t *testing.T) {
	admin := newDefaultAdminSvc()
	admin.mintJoinTokenFn = func(context.Context, string, time.Duration) (string, time.Time, error) {
		return "s3cr3t-token", time.Now().Add(time.Hour), nil
	}
	s := NewServer(&mockKVService{}, admin, ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens", strings.NewReader(`{"node_id":"node-1"}`))
	rec := httptest.NewRecorder()
	s.handleMintJoinToken(rec, req)
	assertStatus(t, rec, http.StatusCreated)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

// TestHandleMintJoinToken_TTLOverflowRefused proves a ttl_seconds value
// large enough to overflow the seconds-to-duration conversion is refused
// with a typed error BEFORE ever reaching the minter -- not silently
// wrapped into a small or negative duration.
func TestHandleMintJoinToken_TTLOverflowRefused(t *testing.T) {
	admin := newDefaultAdminSvc()
	called := false
	admin.mintJoinTokenFn = func(context.Context, string, time.Duration) (string, time.Time, error) {
		called = true
		return "tok", time.Now(), nil
	}
	s := NewServer(&mockKVService{}, admin, ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens",
		strings.NewReader(`{"node_id":"node-1","ttl_seconds":9223372036854775807}`))
	rec := httptest.NewRecorder()
	s.handleMintJoinToken(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
	if called {
		t.Error("minter was called with an overflowing ttl_seconds; the guard did not refuse it first")
	}
}

func TestHandleMintJoinToken_ServiceErrorMapsToClass(t *testing.T) {
	admin := newDefaultAdminSvc()
	admin.mintJoinTokenFn = func(context.Context, string, time.Duration) (string, time.Time, error) {
		return "", time.Time{}, &clusterenroll.JoinTokenTTLError{TTL: time.Second}
	}
	s := NewServer(&mockKVService{}, admin, ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens", strings.NewReader(`{"node_id":"node-1"}`))
	rec := httptest.NewRecorder()
	s.handleMintJoinToken(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

// TestHandleMintJoinToken_Registered proves the route is wired on
// registerRoutes with permission.ClusterJoinToken, end to end through
// s.dispatch rather than calling the handler directly.
func TestHandleMintJoinToken_Registered(t *testing.T) {
	admin := newDefaultAdminSvc()
	admin.mintJoinTokenFn = func(context.Context, string, time.Duration) (string, time.Time, error) {
		return "tok", time.Now().Add(time.Hour), nil
	}
	s := NewServer(&mockKVService{}, admin, ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/join-tokens", strings.NewReader(`{"node_id":"node-1"}`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusCreated)
}

var _ service.ClusterJoinTokenMinter = (*mockAdminService)(nil)
