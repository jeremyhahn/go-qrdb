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
	"context"
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
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
		err:   &audit.GateError{Err: &audit.WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}},
		class: AuthFailureAuditUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
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
		name:  "TenantLookupError -- the certificate's tenant could not be read",
		err:   &auth.TenantLookupError{TenantID: "acme", Err: errors.New("store down")},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		name:  "UnknownTenantError -- the certificate names no created tenant",
		err:   &auth.CertificateTenantError{Err: &auth.UnknownTenantError{TenantID: "ghost"}},
		class: AuthFailureUnauthenticated, http: http.StatusUnauthorized, grpc: codes.Unauthenticated,
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
	{
		// go-qrdb strict-isolation phase 0 review finding B1: the RBAC
		// role-resolution failure -- the primary store failed or timed out,
		// and the StaleRead fallback either is not configured or (not the
		// system tenant it is confined to) was never consulted -- must be
		// retryable, not a silent 401/403 for a correctly enrolled user.
		name:  "rbac.ResolveError -- a transient role-resolution failure",
		err:   &rbac.ResolveError{Err: context.DeadlineExceeded},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		// Review finding BL1: context.Canceled -- the caller's own context
		// ending mid-resolve -- is the other context-based transient cause
		// resolveErrorIsTransient recognises, alongside DeadlineExceeded.
		name:  "rbac.ResolveError -- the caller's context was canceled mid-resolve",
		err:   &rbac.ResolveError{Err: context.Canceled},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		// Review finding BL2: resolveErrorIsTransient no longer matches
		// role.StoreError/tenant.StoreError by type or by Op/Detail string --
		// it asks the cause chain whether it declares itself transient
		// (transientMarker), a capability only the PRODUCER can set (its
		// field is unexported). fakeTransientCauseError pins that interface check
		// in isolation, decoupled from either producer package; the real
		// producers are pinned end-to-end by
		// TestResolveErrorIsTransient_ProducerDeclaresTransience below, built
		// over the real role.NewStore and the real tenant service/resolver.
		name:  "rbac.ResolveError -- the cause declares itself transient",
		err:   &rbac.ResolveError{Err: &fakeTransientCauseError{msg: "read index not ready", transient: true}},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		// Review finding X1's re-review item 5: a barrier-isolated tenant's
		// role store resolved to a SEALED barrier (routingRoleService.storeFor
		// -> resolver.Resolve). Retryable once the tenant is unsealed, exactly
		// as the equivalent condition is for every other data-plane read
		// (pkg/api/errclass's dominant tenant.ErrTenantSealed rule).
		name:  "rbac.ResolveError -- the tenant's role store barrier is sealed",
		err:   &rbac.ResolveError{Err: tenant.ErrTenantSealed},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		// The same seal met one step later: the barrier sealed AFTER Resolve
		// returned a store and BEFORE that store's own Get/Put ran.
		name:  "rbac.ResolveError -- the barrier sealed between resolve and use",
		err:   &rbac.ResolveError{Err: seal.ErrSealed},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		// A node re-sealed at runtime: go-quicraft refuses the role read
		// with its own sentinel, which is not seal.ErrSealed.
		name:  "rbac.ResolveError -- the node barrier re-sealed under the role read",
		err:   &rbac.ResolveError{Err: &role.StoreError{Op: "get", Detail: "primary lookup", Err: seal.ErrBarrierSealed}},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
	{
		name:  "BarrierSealedError -- the node barrier re-sealed under the user lookup",
		err:   &BarrierSealedError{Err: seal.ErrBarrierSealed},
		class: AuthFailureUnavailable, http: http.StatusServiceUnavailable, grpc: codes.Unavailable, retry: true,
	},
}

// fakeTransientCauseError is a minimal error implementing the same ReadTransient()
// bool capability role.StoreError and tenant.StoreError do (transientMarker),
// so resolveErrorIsTransient's interface check can be pinned directly
// without needing either producer package's real, unexported field.
type fakeTransientCauseError struct {
	msg       string
	transient bool
}

func (f *fakeTransientCauseError) Error() string       { return f.msg }
func (f *fakeTransientCauseError) ReadTransient() bool { return f.transient }

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
		AuthFailureUnknown:          "unknown",
		AuthFailureUnauthenticated:  "unauthenticated",
		AuthFailureUnauthorized:     "unauthorized",
		AuthFailureUnavailable:      "unavailable",
		AuthFailureRateLimited:      "rate-limited",
		AuthFailureAuditUnavailable: "audit-unavailable",
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

// TestClassifyAuthError_ResolveErrorDefinitiveCausesStayUnknown pins every
// exclusion the B1/X1 allow-list carries: an *rbac.ResolveError wrapping a
// DEFINITIVE cause from the primary store -- a deleted or never-existent
// role, a malformed request, or a resolver contract violation -- is not the
// cold-node condition resolveErrorIsTransient exists to make retryable, so
// each must keep falling through unclassified exactly as it did before the
// B1 rule was added. Checked against every mapper this package exposes, not
// just ClassifyAuthError: a classCases row cannot carry these, because
// mapHTTPAuthError/mapGRPCAuthError's OWN pre-refactor defaults (401,
// Unauthenticated) differ from the generic HTTPStatus/GRPCError callers'
// chosen fallback, and classCases assumes one answer serves every mapper.
func TestClassifyAuthError_ResolveErrorDefinitiveCausesStayUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "RoleNotFoundError -- a deleted or never-existent role",
			err:  &rbac.ResolveError{Err: &role.RoleNotFoundError{ID: "deleted-role"}},
		},
		{
			name: "ValidationError -- a malformed request",
			err:  &rbac.ResolveError{Err: &role.ValidationError{Field: "id", Reason: "must not be empty"}},
		},
		{
			name: "role.InvalidResolverResultError -- a resolver contract violation",
			err:  &rbac.ResolveError{Err: &role.InvalidResolverResultError{Name: "so", TenantID: "acme"}},
		},
		{
			// Review finding BL2: the cause chain can implement transientMarker
			// and still answer false -- a corrupt stored record or a decrypt/
			// authentication refusal, which role.newStoreError and
			// tenant.newStoreError both decide at construction (see
			// TestResolveErrorIsTransient_ProducerDeclaresTransience for the
			// real producers). This pins the interface check's false branch in
			// isolation.
			name: "the cause declares itself NOT transient",
			err:  &rbac.ResolveError{Err: &fakeTransientCauseError{msg: "invalid character", transient: false}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthError(tc.err); got != AuthFailureUnknown {
				t.Fatalf("classified as %v, want AuthFailureUnknown", got)
			}
			if got := HTTPStatus(tc.err, http.StatusTeapot); got != http.StatusTeapot {
				t.Errorf("HTTPStatus = %d, want the caller's fallback 418 (unrecognised)", got)
			}
			if got := status.Code(GRPCError(tc.err, codes.Aborted)); got != codes.Aborted {
				t.Errorf("GRPCError = %v, want the caller's fallback Aborted (unrecognised)", got)
			}
			if got := mapHTTPAuthError(tc.err); got != http.StatusUnauthorized {
				t.Errorf("mapHTTPAuthError = %d, want 401 (the pre-refactor default for an unrecognised error)", got)
			}
			if got := status.Code(mapGRPCAuthError(tc.err)); got != codes.Unauthenticated {
				t.Errorf("mapGRPCAuthError = %v, want Unauthenticated (the pre-refactor default)", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// go-qrdb#460 -- AuthFailureUnavailable and AuthFailureAuditUnavailable are
// two distinct classes that answer the wire identically.
// ---------------------------------------------------------------------------

// TestAuthFailureAuditUnavailable_IsDistinctFromUnavailable is the structural
// half of the fix. Before this class existed, "could not record the
// decision" (*audit.GateError) and "the tenant barrier is sealed"
// (*StoreUnavailableError wrapping tenant.ErrTenantSealed) were the SAME
// class, and the only thing keeping a control-flow branch from conflating
// them was a comment and isAuditFailure's concrete-type check
// (barrier_bootstrap.go). The classes must never compare equal, and each
// error must classify into exactly the one it means -- never the other.
func TestAuthFailureAuditUnavailable_IsDistinctFromUnavailable(t *testing.T) {
	if AuthFailureAuditUnavailable == AuthFailureUnavailable {
		t.Fatal("AuthFailureAuditUnavailable must not equal AuthFailureUnavailable")
	}

	gateErr := &audit.GateError{Err: errors.New("disk full")}
	if got := ClassifyAuthError(gateErr); got != AuthFailureAuditUnavailable {
		t.Errorf("*audit.GateError classified as %v, want AuthFailureAuditUnavailable", got)
	}
	if got := ClassifyAuthError(gateErr); got == AuthFailureUnavailable {
		t.Error("*audit.GateError must NOT classify as the wide AuthFailureUnavailable")
	}

	sealed := &StoreUnavailableError{Err: errors.New("tenant sealed")}
	if got := ClassifyAuthError(sealed); got != AuthFailureUnavailable {
		t.Errorf("*StoreUnavailableError classified as %v, want AuthFailureUnavailable", got)
	}
	if got := ClassifyAuthError(sealed); got == AuthFailureAuditUnavailable {
		t.Error("*StoreUnavailableError must NOT classify as AuthFailureAuditUnavailable -- it is the sealed-window signal, not an audit failure")
	}
}

// TestAuthFailureAuditUnavailable_MapsIdenticallyToUnavailable is the
// transport half: splitting the class must change no wire answer. Every
// transport is meant to treat the two classes exactly alike -- 503 /
// Unavailable, retryable -- so a caller deciding HOW TO ANSWER THE WIRE never
// needs to know the split happened.
func TestAuthFailureAuditUnavailable_MapsIdenticallyToUnavailable(t *testing.T) {
	if httpStatusByClass[AuthFailureAuditUnavailable] != httpStatusByClass[AuthFailureUnavailable] {
		t.Errorf("HTTP status diverged: audit-unavailable=%d unavailable=%d",
			httpStatusByClass[AuthFailureAuditUnavailable], httpStatusByClass[AuthFailureUnavailable])
	}
	if grpcCodeByClass[AuthFailureAuditUnavailable] != grpcCodeByClass[AuthFailureUnavailable] {
		t.Errorf("gRPC code diverged: audit-unavailable=%v unavailable=%v",
			grpcCodeByClass[AuthFailureAuditUnavailable], grpcCodeByClass[AuthFailureUnavailable])
	}
	if AuthFailureAuditUnavailable.Retryable() != AuthFailureUnavailable.Retryable() {
		t.Error("Retryable() diverged between the two classes")
	}
}

// TestAuthFailureAuditUnavailable_OuterGateErrorWinsOverInnerCause is the
// regression for a classifier-order bug: errors.As walks err's ENTIRE unwrap
// chain, not just its outermost layer, and *audit.GateError.Err is the audit
// SINK's own failure -- a value this package does not control, because the
// sink is pluggable. Checking auth.AuthError or StoreUnavailableError before
// audit.GateError would let a sink whose write error happens to be, or wrap,
// one of those types win the coincidental INNER match and misclassify a
// could-not-record failure as a credential or store problem -- exactly the
// case isAuditFailure and the SO-PIN gate must never see. audit.GateError
// must always win when it is the error actually returned, regardless of what
// its Err field holds.
func TestAuthFailureAuditUnavailable_OuterGateErrorWinsOverInnerCause(t *testing.T) {
	wrappingAuthError := &audit.GateError{
		Component: "authorize",
		Err:       &auth.AuthError{Code: "unauthorized", Message: "sink returned an AuthError"},
	}
	if got := ClassifyAuthError(wrappingAuthError); got != AuthFailureAuditUnavailable {
		t.Errorf("*audit.GateError wrapping *auth.AuthError classified as %v, want AuthFailureAuditUnavailable", got)
	}

	wrappingStoreUnavailable := &audit.GateError{
		Component: "authorize",
		Err:       &StoreUnavailableError{Err: errors.New("sink returned a StoreUnavailableError")},
	}
	if got := ClassifyAuthError(wrappingStoreUnavailable); got != AuthFailureAuditUnavailable {
		t.Errorf("*audit.GateError wrapping *StoreUnavailableError classified as %v, want AuthFailureAuditUnavailable", got)
	}

	// The control-flow guard itself must agree, not just the class.
	if !isAuditFailure(wrappingAuthError) {
		t.Error("isAuditFailure must recognise a GateError even when it wraps an AuthError")
	}
	if !isAuditFailure(wrappingStoreUnavailable) {
		t.Error("isAuditFailure must recognise a GateError even when it wraps a StoreUnavailableError")
	}
}
