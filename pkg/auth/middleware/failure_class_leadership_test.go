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

// An auth fallback that fails because its shard is electing a leader is
// retryable on every transport (go-qrdb#1280).
//
// No build constraint: same reasoning as failure_class_test.go, and every
// helper used here is unconstrained.

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// livenessCheckError stands in for a consumer's own typed fallback error, the
// "platformtoken: liveness check failed" layer of the observed chain.
type livenessCheckError struct{ err error }

func (e *livenessCheckError) Error() string {
	return "platformtoken: liveness check failed: " + e.err.Error()
}
func (e *livenessCheckError) Unwrap() error { return e.err }

// leadershipTransientCauses is every member of service.LeadershipTransient:
// the not-leader error the service layer returns, and each quicraft sentinel.
func leadershipTransientCauses() map[string]error {
	return map[string]error{
		"service.NotLeaderError":         &service.NotLeaderError{ShardID: 1},
		"quicraft.ErrTimeout":            quicraft.ErrTimeout,
		"quicraft.ErrAborted":            quicraft.ErrAborted,
		"quicraft.ErrNotLeader":          quicraft.ErrNotLeader,
		"quicraft.ErrShardNotReady":      quicraft.ErrShardNotReady,
		"quicraft.ErrReadIndexNotReady":  quicraft.ErrReadIndexNotReady,
		"quicraft.ErrTransferInProgress": quicraft.ErrTransferInProgress,
	}
}

// observedChain wraps cause the way consumer go-quicbaas's REST fallback
// returned it: "platformtoken: liveness check failed: tenant: get: <cause>".
func observedChain(cause error) error {
	return &livenessCheckError{err: &tenant.StoreError{Op: "get", Err: cause}}
}

func assertLeadershipUnavailable(t *testing.T, err error) {
	t.Helper()
	if got := ClassifyAuthError(err); got != AuthFailureUnavailable {
		t.Fatalf("ClassifyAuthError(%v) = %v, want AuthFailureUnavailable", err, got)
	}
	if got := HTTPStatus(err, http.StatusUnauthorized); got != http.StatusServiceUnavailable {
		t.Errorf("HTTPStatus = %d, want 503", got)
	}
	if got := mapHTTPAuthError(err); got != http.StatusServiceUnavailable {
		t.Errorf("mapHTTPAuthError = %d, want 503", got)
	}
	if got := status.Code(GRPCError(err, codes.Unauthenticated)); got != codes.Unavailable {
		t.Errorf("GRPCError = %v, want Unavailable", got)
	}
	if got := status.Code(mapGRPCAuthError(err)); got != codes.Unavailable {
		t.Errorf("mapGRPCAuthError = %v, want Unavailable", got)
	}
}

// TestClassifyAuthError_LeadershipTransientIsUnavailable: each leadership
// transient, bare, in the observed wrapper chain, and behind the chain's own
// AuthChainError, is retryable -- never the 401 fallback.
func TestClassifyAuthError_LeadershipTransientIsUnavailable(t *testing.T) {
	for name, cause := range leadershipTransientCauses() {
		t.Run(name, func(t *testing.T) {
			t.Run("bare", func(t *testing.T) { assertLeadershipUnavailable(t, cause) })
			t.Run("observed chain", func(t *testing.T) { assertLeadershipUnavailable(t, observedChain(cause)) })
			t.Run("AuthChainError", func(t *testing.T) {
				assertLeadershipUnavailable(t, &AuthChainError{Err: observedChain(cause)})
			})
		})
	}
}

// TestClassifyAuthError_LeadershipTransientBesideAuthError: a chain carrying
// an *auth.AuthError beside the election is still retryable. An election is
// never a credential failure, so the credential verdict found in the same
// chain is not the answer.
func TestClassifyAuthError_LeadershipTransientBesideAuthError(t *testing.T) {
	for name, cause := range leadershipTransientCauses() {
		for _, authErr := range []*auth.AuthError{auth.ErrUnauthenticated, auth.ErrUnauthorized} {
			t.Run(name+"/"+authErr.Code, func(t *testing.T) {
				assertLeadershipUnavailable(t, &livenessCheckError{err: errors.Join(authErr, observedChain(cause))})
			})
		}
	}
}

// TestClassifyAuthError_ObservedNotLeaderText pins the issue's exact text, so
// the test is known to model the chain that answered 401.
func TestClassifyAuthError_ObservedNotLeaderText(t *testing.T) {
	err := observedChain(&service.NotLeaderError{ShardID: 1})
	const want = "platformtoken: liveness check failed: tenant: get: service: not leader for shard 1, leader unknown"
	if err.Error() != want {
		t.Fatalf("chain text = %q, want %q", err.Error(), want)
	}
	assertLeadershipUnavailable(t, err)
}

// TestRESTMiddleware_FallbackOnLeaderlessShardIs503: the first authenticator
// finds no credential of its kind, the fallback fails on a leaderless shard,
// and the REST middleware answers 503 rather than asking the client to
// re-authenticate.
func TestRESTMiddleware_FallbackOnLeaderlessShardIs503(t *testing.T) {
	primary := &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}
	fallback := &mockAuthenticator{name: "platformtoken", err: observedChain(&service.NotLeaderError{ShardID: 1})}
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(primary), WithAuthenticator(fallback))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be called")
	})
	rec := httptest.NewRecorder()
	m.RESTMiddleware(testRESTRoutes(), next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if fallback.calls.Load() != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls.Load())
	}
}

// TestRESTMiddleware_FallbackCredentialRefusalStays401: the new rule leaves a
// fallback's genuine credential refusal alone.
func TestRESTMiddleware_FallbackCredentialRefusalStays401(t *testing.T) {
	primary := &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}
	fallback := &mockAuthenticator{name: "platformtoken", err: &livenessCheckError{err: errors.New("token expired")}}
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(primary), WithAuthenticator(fallback))

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be called")
	})
	rec := httptest.NewRecorder()
	m.RESTMiddleware(testRESTRoutes(), next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
