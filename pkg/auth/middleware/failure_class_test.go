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

// Transport-neutral classification of auth failures.
//
// No build constraint: same reasoning as audit_failure_test.go -- these pin a
// security-relevant mapping that ships in every build, and every helper they
// use is unconstrained.

package middleware

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// classCases is the one table every assertion below reads, so the HTTP code,
// the gRPC code and the class itself cannot be stated inconsistently.
var classCases = []struct {
	name  string
	err   error
	class AuthFailureClass
	http  int
	grpc  codes.Code
	retry bool
}{
	{
		name:  "AuditError -- the node could not RECORD the decision",
		err:   &AuditError{Err: &audit.WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		name:  "StoreUnavailableError",
		err:   &StoreUnavailableError{Err: errors.New("raft not ready")},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		name:  "RevocationCheckError",
		err:   &auth.RevocationCheckError{Err: errors.New("store down")},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		name:  "rbac.PermissionDeniedError -- the ordinary denial",
		err:   &rbac.PermissionDeniedError{Subject: "alice", Resource: "kv", Action: "put"},
		class: AuthFailureUnauthorized, http: http.StatusForbidden, grpc: codes.PermissionDenied,
	},
	{
		name:  "auth.ErrUnauthorized",
		err:   auth.ErrUnauthorized,
		class: AuthFailureUnauthorized, http: http.StatusForbidden, grpc: codes.PermissionDenied,
	},
	{
		name:  "UserDisabledError",
		err:   &UserDisabledError{Username: "bob", TenantID: "t1", Lifecycle: "suspended"},
		class: AuthFailureUnauthorized, http: http.StatusForbidden, grpc: codes.PermissionDenied,
	},
	{
		name:  "AuthMethodNotPermittedError",
		err:   &AuthMethodNotPermittedError{Username: "bob", Method: "mtls", Permitted: []string{"webauthn"}},
		class: AuthFailureUnauthorized, http: http.StatusForbidden, grpc: codes.PermissionDenied,
	},
	{
		name:  "auth.ErrUnauthenticated",
		err:   auth.ErrUnauthenticated,
		class: AuthFailureUnauthenticated, http: http.StatusUnauthorized, grpc: codes.Unauthenticated,
	},
	{
		name:  "AuthChainError",
		err:   &AuthChainError{Err: errors.New("no peer cert")},
		class: AuthFailureUnauthenticated, http: http.StatusUnauthorized, grpc: codes.Unauthenticated,
	},
	{
		name:  "RateLimitedError",
		err:   &RateLimitedError{Permission: permission.KVPut, Subject: "a", TenantID: "t", RetryAfter: time.Second},
		class: AuthFailureRateLimited, http: http.StatusTooManyRequests, grpc: codes.ResourceExhausted, retry: true,
	},
}

func TestClassifyAuthError(t *testing.T) {
	for _, tc := range classCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthError(tc.err); got != tc.class {
				t.Fatalf("ClassifyAuthError = %v, want %v", got, tc.class)
			}
		})
	}
}

// TestClassifyAuthError_UnknownAndNil: an error this package does not
// recognise must NOT be guessed at, because the right guess differs by call
// site. Nil likewise -- a mapper reached with nil has a caller bug that must
// not be papered over.
func TestClassifyAuthError_UnknownAndNil(t *testing.T) {
	if got := ClassifyAuthError(errors.New("something else")); got != AuthFailureUnknown {
		t.Errorf("unrecognised error classified as %v, want unknown", got)
	}
	if got := ClassifyAuthError(nil); got != AuthFailureUnknown {
		t.Errorf("nil classified as %v, want unknown", got)
	}
}

func TestAuthFailureClass_Retryable(t *testing.T) {
	for _, tc := range classCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthError(tc.err).Retryable(); got != tc.retry {
				t.Errorf("Retryable() = %v, want %v", got, tc.retry)
			}
		})
	}
	if AuthFailureUnknown.Retryable() {
		t.Error("an unclassified failure must not be advertised as retryable")
	}
}

func TestAuthFailureClass_String(t *testing.T) {
	want := map[AuthFailureClass]string{
		AuthFailureUnknown:         "unknown",
		AuthFailureUnauthenticated: "unauthenticated",
		AuthFailureUnauthorized:    "unauthorized",
		AuthFailureUnavailable:     "unavailable",
		AuthFailureRateLimited:     "rate-limited",
	}
	for class, name := range want {
		if got := class.String(); got != name {
			t.Errorf("String() = %q, want %q", got, name)
		}
	}
}

// ---------------------------------------------------------------------------
// The mappers
// ---------------------------------------------------------------------------

func TestHTTPStatus(t *testing.T) {
	for _, tc := range classCases {
		t.Run(tc.name, func(t *testing.T) {
			// The fallback must be irrelevant for a recognised error, so it is
			// deliberately a value no case expects.
			if got := HTTPStatus(tc.err, http.StatusTeapot); got != tc.http {
				t.Errorf("HTTPStatus = %d, want %d", got, tc.http)
			}
		})
	}
}

// TestHTTPStatus_FallbackIsTheCallersChoice is the assertion that keeps this
// refactor from being a silent behaviour change. The REST auth middleware
// wants 401 for an unrecognised failure; a handler-level gate that runs AFTER
// authentication wants 403. One hard-coded default would have been wrong for
// one of them.
func TestHTTPStatus_FallbackIsTheCallersChoice(t *testing.T) {
	unknown := errors.New("something this package does not know")
	if got := HTTPStatus(unknown, http.StatusUnauthorized); got != http.StatusUnauthorized {
		t.Errorf("middleware fallback: got %d, want 401", got)
	}
	if got := HTTPStatus(unknown, http.StatusForbidden); got != http.StatusForbidden {
		t.Errorf("gate fallback: got %d, want 403", got)
	}
}

func TestGRPCError(t *testing.T) {
	for _, tc := range classCases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := status.FromError(GRPCError(tc.err, codes.Aborted))
			if !ok {
				t.Fatalf("expected a gRPC status")
			}
			if st.Code() != tc.grpc {
				t.Errorf("code = %v, want %v", st.Code(), tc.grpc)
			}
		})
	}
}

func TestGRPCError_FallbackIsTheCallersChoice(t *testing.T) {
	unknown := errors.New("something this package does not know")
	for _, fallback := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied} {
		st, _ := status.FromError(GRPCError(unknown, fallback))
		if st.Code() != fallback {
			t.Errorf("got %v, want the caller's fallback %v", st.Code(), fallback)
		}
	}
}

func TestGRPCError_NilIsNil(t *testing.T) {
	if err := GRPCError(nil, codes.PermissionDenied); err != nil {
		t.Fatalf("GRPCError(nil) = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// The existing mappers must be UNCHANGED for everything they already handled
// ---------------------------------------------------------------------------

// TestMapHTTPAuthError_UnchangedByRefactor. mapHTTPAuthError is now a thin
// wrapper over HTTPStatus; these assert it still answers exactly what its
// hand-written errors.As chain did, including the 401 default.
func TestMapHTTPAuthError_UnchangedByRefactor(t *testing.T) {
	for _, tc := range classCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapHTTPAuthError(tc.err); got != tc.http {
				t.Errorf("mapHTTPAuthError = %d, want %d", got, tc.http)
			}
		})
	}
	if got := mapHTTPAuthError(errors.New("unknown")); got != http.StatusUnauthorized {
		t.Errorf("default = %d, want 401 (the pre-refactor default)", got)
	}
}

func TestMapGRPCAuthError_UnchangedByRefactor(t *testing.T) {
	for _, tc := range classCases {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := status.FromError(mapGRPCAuthError(tc.err))
			if st.Code() != tc.grpc {
				t.Errorf("mapGRPCAuthError = %v, want %v", st.Code(), tc.grpc)
			}
		})
	}
	st, _ := status.FromError(mapGRPCAuthError(errors.New("unknown")))
	if st.Code() != codes.Unauthenticated {
		t.Errorf("default = %v, want Unauthenticated (the pre-refactor default)", st.Code())
	}
}

// TestAuthErrorWithUnknownCodeFallsThrough pins a subtlety the hand-written
// chain had and this table must keep: an *auth.AuthError carrying a code
// neither branch names is NOT classified by the first rule -- it falls through
// to the rules below it, and out to the caller's fallback.
func TestAuthErrorWithUnknownCodeFallsThrough(t *testing.T) {
	err := &auth.AuthError{Code: "some_future_code", Message: "x"}
	if got := ClassifyAuthError(err); got != AuthFailureUnknown {
		t.Fatalf("classified as %v; an unrecognised AuthError code must fall through", got)
	}
	if got := HTTPStatus(err, http.StatusForbidden); got != http.StatusForbidden {
		t.Errorf("HTTPStatus = %d, want the caller's fallback 403", got)
	}
}
