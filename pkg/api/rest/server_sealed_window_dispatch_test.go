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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// dispatchAuthenticator refuses every caller and counts how often it was
// asked: a request it sees is a request that reached authentication, and so
// the user store behind it.
type dispatchAuthenticator struct{ calls int }

func (a *dispatchAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	a.calls++
	return nil, auth.ErrUnauthenticated
}

func (a *dispatchAuthenticator) Name() string { return "counting" }

// dispatchDenyAuthorizer is never reached: dispatchAuthenticator admits no
// one.
type dispatchDenyAuthorizer struct{}

func (dispatchDenyAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return auth.ErrUnauthorized
}

// TestSealedWindowDispatch_REST: a regular REST listener serves a sealed
// node exactly as the boot-time listener does -- the sealed-window routes
// anonymously, everything else refused 503 SEALED, neither authenticated --
// and authenticates every request once the node barrier is unsealed, a
// cluster-held node included.
func TestSealedWindowDispatch_REST(t *testing.T) {
	tests := []struct {
		name                string
		nodeSealed, barrier bool
		method, path        string
		want                int
		sealedEnvelope      bool
		served              bool
		authentications     int
		body                string
	}{
		{name: "sealed, unseal", nodeSealed: true, barrier: true, method: http.MethodPost, path: "/v1/barrier/unseal", want: http.StatusNoContent, served: true},
		{name: "sealed, data route", nodeSealed: true, barrier: true, method: http.MethodGet, path: "/v1/kv/foo", want: http.StatusServiceUnavailable, sealedEnvelope: true},
		{name: "sealed, barrier status", nodeSealed: true, barrier: true, method: http.MethodGet, path: "/v1/barrier/status", want: http.StatusOK, served: true, body: `"initialized":true`},
		{name: "cluster-held, barrier status", nodeSealed: true, method: http.MethodGet, path: "/v1/barrier/status", want: http.StatusServiceUnavailable, sealedEnvelope: true},
		{name: "sealed, health", nodeSealed: true, barrier: true, method: http.MethodGet, path: "/v1/health", want: http.StatusServiceUnavailable, sealedEnvelope: true},
		{name: "sealed, authenticated-only route", nodeSealed: true, barrier: true, method: http.MethodPost, path: "/v1/barrier/cluster-unseal", want: http.StatusServiceUnavailable, sealedEnvelope: true},
		{name: "unsealed", method: http.MethodPost, path: "/v1/barrier/unseal", want: http.StatusUnauthorized, authentications: 1},
		{name: "cluster-held", nodeSealed: true, method: http.MethodPost, path: "/v1/barrier/cluster-unseal", want: http.StatusUnauthorized, authentications: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn := &dispatchAuthenticator{}
			var served, anonymous bool
			barrier := &mockBarrierService{
				unsealFn: func(ctx context.Context, _ []byte) error {
					served, anonymous = true, sealedwindow.Anonymous(ctx)
					return nil
				},
				statusFn: func(ctx context.Context) (*service.BarrierStatusInfo, error) {
					served, anonymous = true, sealedwindow.Anonymous(ctx)
					return &service.BarrierStatusInfo{Sealed: true, Initialized: true}, nil
				},
				clusterUnsealFn: func(ctx context.Context) (*clusterseal.Report, error) {
					served, anonymous = true, sealedwindow.Anonymous(ctx)
					return &clusterseal.Report{}, nil
				},
			}
			srv := NewServer(nil, nil, "127.0.0.1:0",
				WithBarrierService(barrier),
				WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithAuthenticator(authn))),
				WithSealedWindowProbe(newFakeSealed(tt.nodeSealed)),
				WithSealedWindowDispatch(newFakeSealed(tt.barrier)))

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{"secret":"cw=="}`))
			req.Header.Set("Content-Type", "application/json")
			srv.server.Handler.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tt.want, rr.Body.String())
			}
			if got := strings.Contains(rr.Body.String(), `"code":"SEALED"`); got != tt.sealedEnvelope {
				t.Fatalf("SEALED envelope = %v, want %v (body %q)", got, tt.sealedEnvelope, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.body) {
				t.Fatalf("body %q, want it to contain %q", rr.Body.String(), tt.body)
			}
			if served != tt.served || anonymous != tt.served {
				t.Fatalf("served %v anonymous %v, want %v", served, anonymous, tt.served)
			}
			if authn.calls != tt.authentications {
				t.Fatalf("authentications = %d, want %d", authn.calls, tt.authentications)
			}
		})
	}
}

// TestSealedWindowDispatch_FollowsTheBarrier: the dispatch samples the
// barrier on every request, so a re-sealed listener leaves authentication
// and one that unseals returns to it.
func TestSealedWindowDispatch_FollowsTheBarrier(t *testing.T) {
	authn := &dispatchAuthenticator{}
	node, barrier := newFakeSealed(false), newFakeSealed(false)
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithBarrierService(&mockBarrierService{}),
		WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithAuthenticator(authn))),
		WithSealedWindowProbe(node),
		WithSealedWindowDispatch(barrier))
	send := func() int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", strings.NewReader(`{"secret":"cw=="}`))
		req.Header.Set("Content-Type", "application/json")
		srv.server.Handler.ServeHTTP(rr, req)
		return rr.Code
	}
	for _, sealed := range []bool{false, true, false} {
		node.sealed.Store(sealed)
		barrier.sealed.Store(sealed)
		want := map[bool]int{true: http.StatusNoContent, false: http.StatusUnauthorized}[sealed]
		if got := send(); got != want {
			t.Fatalf("sealed %v: status %d, want %d", sealed, got, want)
		}
	}
	if authn.calls != 2 {
		t.Fatalf("authentications = %d, want 2 (the unsealed requests)", authn.calls)
	}
}
