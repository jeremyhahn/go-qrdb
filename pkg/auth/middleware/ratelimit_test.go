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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// ---------------------------------------------------------------------------
// checkRateLimit unit tests
// ---------------------------------------------------------------------------

func TestCheckRateLimit_NoOpWhenNotConfigured(t *testing.T) {
	m := New(&mockAuthorizer{})
	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	if err := m.checkRateLimit(identity, permission.KVPut); err != nil {
		t.Fatalf("want no error when rate limiting is not wired, got %v", err)
	}
}

func TestCheckRateLimit_NoOpWhenLimiterNilButRegistrySet(t *testing.T) {
	policies := ratelimit.NewRegistry()
	policies.Register(permission.KVPut, ratelimit.Policy{RatePerSecond: 1, Burst: 1})
	m := New(&mockAuthorizer{}, WithRateLimitPolicies(policies))
	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	if err := m.checkRateLimit(identity, permission.KVPut); err != nil {
		t.Fatalf("want no error when no limiter is installed, got %v", err)
	}
}

func TestCheckRateLimit_NoOpWhenPermissionHasNoPolicy(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.KVPut, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	m := New(&mockAuthorizer{}, WithRateLimiter(limiter), WithRateLimitPolicies(policies))
	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	// permission.KVGet carries no declared policy, so it must never be
	// throttled no matter how many times it is called.
	for i := 0; i < 10; i++ {
		if err := m.checkRateLimit(identity, permission.KVGet); err != nil {
			t.Fatalf("call %d: want no error for an unlimited permission, got %v", i, err)
		}
	}
}

func TestCheckRateLimit_DeniesOverBurst(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.KVPut, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	m := New(&mockAuthorizer{}, WithRateLimiter(limiter), WithRateLimitPolicies(policies))
	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	if err := m.checkRateLimit(identity, permission.KVPut); err != nil {
		t.Fatalf("want the first request within burst allowed, got %v", err)
	}

	err := m.checkRateLimit(identity, permission.KVPut)
	if err == nil {
		t.Fatal("want the second request to be denied")
	}
	var rl *RateLimitedError
	if !asRateLimited(err, &rl) {
		t.Fatalf("want a *RateLimitedError, got %T: %v", err, err)
	}
	if rl.Permission != permission.KVPut || rl.Subject != "alice" || rl.TenantID != "t1" {
		t.Fatalf("want the error to name the caller and permission, got %+v", rl)
	}
	if rl.RetryAfter <= 0 {
		t.Fatalf("want a positive RetryAfter, got %v", rl.RetryAfter)
	}
}

// ---------------------------------------------------------------------------
// CheckLoginRateLimit unit tests -- the pre-identity seam BeginLogin/
// FinishLogin use (sdk/go/transport/embedded/login.go), which reuses
// checkRateLimit's exact engine and key scheme but is keyed from the
// claimed login principal rather than an auth.Identity the authenticator
// chain already produced.
// ---------------------------------------------------------------------------

func TestCheckLoginRateLimit_NilReceiverIsANoOp(t *testing.T) {
	var m *AuthMiddleware
	if err := m.CheckLoginRateLimit(context.Background(), "acme", "alice", permission.UserAuthFactorsLogin); err != nil {
		t.Fatalf("a nil AuthMiddleware must authorize everything, got %v", err)
	}
}

func TestCheckLoginRateLimit_NoOpWhenNotConfigured(t *testing.T) {
	m := New(&mockAuthorizer{})
	if err := m.CheckLoginRateLimit(context.Background(), "acme", "alice", permission.UserAuthFactorsLogin); err != nil {
		t.Fatalf("want no error when rate limiting is not wired, got %v", err)
	}
}

func TestCheckLoginRateLimit_DeniesOverBurst(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.UserAuthFactorsLogin, ratelimit.Policy{RatePerSecond: 1, Burst: 1})
	m := New(&mockAuthorizer{}, WithRateLimiter(limiter), WithRateLimitPolicies(policies))

	if err := m.CheckLoginRateLimit(context.Background(), "acme", "alice", permission.UserAuthFactorsLogin); err != nil {
		t.Fatalf("want the first attempt within burst allowed, got %v", err)
	}
	err := m.CheckLoginRateLimit(context.Background(), "acme", "alice", permission.UserAuthFactorsLogin)
	var rl *RateLimitedError
	if !asRateLimited(err, &rl) {
		t.Fatalf("want a *RateLimitedError, got %T: %v", err, err)
	}
	if rl.Subject != "alice" || rl.TenantID != "acme" {
		t.Fatalf("want the error to name the claimed principal, got %+v", rl)
	}
}

func TestCheckLoginRateLimit_DistinctPrincipalsAreIndependent(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.UserAuthFactorsLogin, ratelimit.Policy{RatePerSecond: 1, Burst: 1})
	m := New(&mockAuthorizer{}, WithRateLimiter(limiter), WithRateLimitPolicies(policies))

	if err := m.CheckLoginRateLimit(context.Background(), "acme", "alice", permission.UserAuthFactorsLogin); err != nil {
		t.Fatalf("alice's first attempt: %v", err)
	}
	if err := m.CheckLoginRateLimit(context.Background(), "acme", "bob", permission.UserAuthFactorsLogin); err != nil {
		t.Fatalf("bob's own bucket must be untouched by alice's attempt, got %v", err)
	}
	if err := m.CheckLoginRateLimit(context.Background(), "globex", "alice", permission.UserAuthFactorsLogin); err != nil {
		t.Fatalf("alice in a different tenant must be a different bucket, got %v", err)
	}
}

func TestCheckRateLimit_CallersAreIndependent(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.KVPut, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	m := New(&mockAuthorizer{}, WithRateLimiter(limiter), WithRateLimitPolicies(policies))
	alice := &auth.Identity{Subject: "alice", TenantID: "t1"}
	bob := &auth.Identity{Subject: "bob", TenantID: "t1"}

	if err := m.checkRateLimit(alice, permission.KVPut); err != nil {
		t.Fatalf("want alice's first request allowed, got %v", err)
	}
	if err := m.checkRateLimit(alice, permission.KVPut); err == nil {
		t.Fatal("want alice's second request denied")
	}
	// Bob's bucket must be untouched by alice's exhaustion, and the SAME
	// subject in a DIFFERENT tenant must not share alice's bucket either.
	if err := m.checkRateLimit(bob, permission.KVPut); err != nil {
		t.Fatalf("want bob's first request allowed, got %v", err)
	}
	aliceOtherTenant := &auth.Identity{Subject: "alice", TenantID: "t2"}
	if err := m.checkRateLimit(aliceOtherTenant, permission.KVPut); err != nil {
		t.Fatalf("want alice-in-t2's first request allowed despite alice-in-t1 being exhausted, got %v", err)
	}
}

// asRateLimited is errors.As spelled out so the test file needs no "errors"
// import solely for this one assertion.
func asRateLimited(err error, target **RateLimitedError) bool {
	rl, ok := err.(*RateLimitedError)
	if !ok {
		return false
	}
	*target = rl
	return true
}

// ---------------------------------------------------------------------------
// authorize() integration: rate limiting runs before RBAC
// ---------------------------------------------------------------------------

func TestAuthorize_RateLimitRunsBeforeAuthorizer(t *testing.T) {
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.AdminStatus, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	authz := &mockAuthorizer{}
	m := New(authz, WithRateLimiter(limiter), WithRateLimitPolicies(policies))
	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	if err := m.authorize(context.Background(), identity, permission.AdminStatus); err != nil {
		t.Fatalf("want the first call allowed, got %v", err)
	}
	if err := m.authorize(context.Background(), identity, permission.AdminStatus); err == nil {
		t.Fatal("want the second call denied by the rate limiter")
	}
	// The RBAC authorizer's Authorize was never reached on the denied call:
	// it saw exactly the one request the limiter let through.
	if got := authz.calls.Load(); got != 1 {
		t.Fatalf("want the RBAC authorizer called exactly once, got %d", got)
	}
}

func TestAuthorize_RateLimitDoesNotApplyWithNilIdentity(t *testing.T) {
	// IdentityGated with a nil identity must still return ErrUnauthenticated
	// rather than panicking on a nil-identity rate-limit key.
	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.IdentityGated, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	m := New(&mockAuthorizer{}, WithRateLimiter(limiter), WithRateLimitPolicies(policies))
	if err := m.authorize(context.Background(), nil, permission.IdentityGated); err != auth.ErrUnauthenticated {
		t.Fatalf("want auth.ErrUnauthenticated, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// REST: 429 + Retry-After
// ---------------------------------------------------------------------------

func TestRESTMiddleware_RateLimited(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}

	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	policies.Register(permission.AdminStatus, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	m := New(authz, WithAuthenticator(authn), WithRateLimiter(limiter), WithRateLimitPolicies(policies))

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	// First request: within burst, admitted.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("want 200 for the first request, got %d", rec1.Code)
	}

	// Second request: over burst, refused with 429 and Retry-After.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 for the second request, got %d", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Error("want a Retry-After header on a 429 response")
	}
	var errResp restErrorResponse
	if err := json.NewDecoder(rec2.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Error("want a non-empty error message")
	}
}

func TestRESTMiddleware_NotRateLimitedWhenNotConfigured(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.RESTMiddleware(testRESTRoutes(), next)

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: want 200 with rate limiting unconfigured, got %d", i, rec.Code)
		}
	}
}

func TestMapHTTPAuthError_RateLimited(t *testing.T) {
	err := &RateLimitedError{Permission: permission.KVPut, Subject: "alice", TenantID: "t1", RetryAfter: time.Second}
	if got := mapHTTPAuthError(err); got != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", got)
	}
}

func TestSetRetryAfterHeader(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"rate limited, sub-second rounds up to 1", &RateLimitedError{RetryAfter: 400 * time.Millisecond}, "1"},
		{"rate limited, whole seconds", &RateLimitedError{RetryAfter: 3 * time.Second}, "3"},
		{"not rate limited", auth.ErrUnauthenticated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			setRetryAfterHeader(rec, tc.err)
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Fatalf("want Retry-After %q, got %q", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// gRPC: ResourceExhausted
// ---------------------------------------------------------------------------

func TestMapGRPCAuthError_RateLimited(t *testing.T) {
	err := &RateLimitedError{Permission: permission.KVPut, Subject: "alice", TenantID: "t1", RetryAfter: time.Second}
	got := mapGRPCAuthError(err)
	st, ok := status.FromError(got)
	if !ok {
		t.Fatalf("want a gRPC status error, got %v", got)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Fatalf("want codes.ResourceExhausted, got %v", st.Code())
	}
}

func TestUnaryInterceptor_RateLimited(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}

	limiter := ratelimit.NewTokenBucketLimiter()
	t.Cleanup(func() { _ = limiter.Close() })
	policies := ratelimit.NewRegistry()
	// /qrdb.v1.QRDBService/Get resolves to permission.KVGet via the compiled
	// proto descriptor's (qrdb.v1.permission) option (see
	// TestLookupGRPCFromDescriptor_DefaultDenyRegression for the mapping this
	// relies on), so no test-only registry stub is needed to exercise the
	// real interceptor.
	policies.Register(permission.KVGet, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	m := New(authz, WithAuthenticator(authn), WithRateLimiter(limiter), WithRateLimitPolicies(policies))

	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Get"}
	handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
		return "ok", nil
	}

	interceptor := m.UnaryInterceptor()

	if _, err := interceptor(context.Background(), "req", info, handler); err != nil {
		t.Fatalf("want the first call allowed, got %v", err)
	}

	_, err := interceptor(context.Background(), "req", info, handler)
	if err == nil {
		t.Fatal("want the second call denied")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("want codes.ResourceExhausted, got %v", err)
	}
}
