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

// This file carries NO build constraint, and every helper it uses is defined
// in a file that carries none either (onbehalf_test.go, middleware_test.go,
// rest_routes_helper_test.go). See go-qrdb#178.

package middleware

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// obRevocationChecker answers every question from a script.
type obRevocationChecker struct {
	revoked bool
	err     error

	calls int
}

func (o *obRevocationChecker) IsCertRevoked(_ context.Context, _ *x509.Certificate) (bool, error) {
	o.calls++
	return o.revoked, o.err
}

// obUnavailableError carries the marker method that means "no answer exists
// here", standing in for pkg/cert.RevocationStateUnavailableError.
type obUnavailableError struct{}

func (e *obUnavailableError) Error() string                    { return "revocation state unavailable" }
func (e *obUnavailableError) RevocationStateUnavailable() bool { return true }

// runOnBehalfWithRevocation is runOnBehalf plus a revocation checker on the
// middleware.
func runOnBehalfWithRevocation(t *testing.T, r *http.Request, storeUser *user.User, rc auth.RevocationChecker) (int, bool) {
	t.Helper()
	m := New(&capturingAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}),
		WithUserService(&mockUserServicer{user: storeUser}),
		WithRevocationChecker(rc),
	)

	var handlerRan bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	m.RESTMiddleware(testRESTRoutes(), next).ServeHTTP(rec, r)
	return rec.Code, handlerRan
}

// obRevocationRequest builds a well-formed forwarded write from a node whose
// certificate is cert.
func obRevocationRequest(t *testing.T) *http.Request {
	t.Helper()
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	return buildOnBehalfRequest(t, nodeCert, nodeKey,
		"alice", "acme", obNodeID, time.Now(), []byte(`{"value":"v"}`),
		http.MethodPut, "/kv/foo")
}

func obStoreUser() *user.User {
	return &user.User{Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}
}

// TestOnBehalf_RevokedForwarderIsRefused closes a bypass the authenticator's
// own check does not cover: authenticateOnBehalf never runs the authenticator
// chain, so without a gate of its own a REVOKED cluster node keeps forwarding
// writes on behalf of any user it names.
func TestOnBehalf_RevokedForwarderIsRefused(t *testing.T) {
	rc := &obRevocationChecker{revoked: true}

	code, ran := runOnBehalfWithRevocation(t, obRevocationRequest(t), obStoreUser(), rc)

	if ran {
		t.Fatal("the handler ran for a forwarded write from a revoked node")
	}
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", code, http.StatusUnauthorized)
	}
	if rc.calls != 1 {
		t.Fatalf("revocation checker consulted %d times, want 1", rc.calls)
	}
}

func TestOnBehalf_UnrevokedForwarderIsAdmitted(t *testing.T) {
	rc := &obRevocationChecker{}

	code, ran := runOnBehalfWithRevocation(t, obRevocationRequest(t), obStoreUser(), rc)

	if !ran {
		t.Fatalf("the handler did not run for a good forwarded write (status %d)", code)
	}
	if rc.calls != 1 {
		t.Fatalf("revocation checker consulted %d times, want 1", rc.calls)
	}
}

// A forwarding node whose revocation status could not be read is refused, and
// the refusal is retryable rather than a credential rejection — the same
// policy the authenticator applies, because both call auth.EnforceRevocation.
func TestOnBehalf_LookupFailureFailsClosed(t *testing.T) {
	rc := &obRevocationChecker{err: errors.New("kv read failed")}

	code, ran := runOnBehalfWithRevocation(t, obRevocationRequest(t), obStoreUser(), rc)

	if ran {
		t.Fatal("the handler ran for a forwarder of unknown revocation status")
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", code, http.StatusServiceUnavailable)
	}
}

// A sealed barrier admits, for the same reason it does on the authenticator
// path: there is no revocation state to read, and refusing would make the
// barrier unsealable.
func TestOnBehalf_UnavailableStateIsAdmitted(t *testing.T) {
	rc := &obRevocationChecker{err: &obUnavailableError{}}

	code, ran := runOnBehalfWithRevocation(t, obRevocationRequest(t), obStoreUser(), rc)

	if !ran {
		t.Fatalf("the handler did not run while revocation state was unreadable (status %d)", code)
	}
}

// No checker wired keeps the pre-existing behaviour, so turning enforcement on
// is a wiring decision rather than an implicit default.
func TestOnBehalf_NoCheckerDoesNotEnforce(t *testing.T) {
	code, ran := runOnBehalfWithRevocation(t, obRevocationRequest(t), obStoreUser(), nil)

	if !ran {
		t.Fatalf("the handler did not run with no revocation checker (status %d)", code)
	}
}

// ---------------------------------------------------------------------------
// auth.EnforceRevocation is the single definition both paths apply
// ---------------------------------------------------------------------------

func TestEnforceRevocation_Outcomes(t *testing.T) {
	ctx := context.Background()
	peer := &x509.Certificate{}

	if err := auth.EnforceRevocation(ctx, nil, peer); err != nil {
		t.Errorf("nil checker = %v, want nil (enforcement off)", err)
	}
	if err := auth.EnforceRevocation(ctx, &obRevocationChecker{}, peer); err != nil {
		t.Errorf("not revoked = %v, want nil", err)
	}
	if err := auth.EnforceRevocation(ctx, &obRevocationChecker{revoked: true}, peer); !errors.Is(err, auth.ErrCertificateRevoked) {
		t.Errorf("revoked = %v, want ErrCertificateRevoked", err)
	}
	boom := errors.New("boom")
	err := auth.EnforceRevocation(ctx, &obRevocationChecker{err: boom}, peer)
	var checkErr *auth.RevocationCheckError
	if !errors.As(err, &checkErr) || !errors.Is(err, boom) {
		t.Errorf("lookup failure = %v, want *RevocationCheckError wrapping the cause", err)
	}
	if err := auth.EnforceRevocation(ctx, &obRevocationChecker{err: &obUnavailableError{}}, peer); err != nil {
		t.Errorf("unavailable state = %v, want nil (admitted)", err)
	}
}

// ---------------------------------------------------------------------------
// The checker is reachable by authorization sites that never run this
// middleware
// ---------------------------------------------------------------------------

// TestRevocationChecker_ReturnsTheWiredChecker verifies the accessor hands
// back the SAME checker instance the option installed, not an equivalent one.
//
// Identity is the property under test. The value of sharing it with the
// cluster key plane is the shared bounded-TTL CACHE; a checker that compared
// equal but cached separately would satisfy every behavioural assertion and
// still give that plane its own revocation window.
func TestRevocationChecker_ReturnsTheWiredChecker(t *testing.T) {
	rc := &obRevocationChecker{}
	m := New(nil, WithRevocationChecker(rc))

	if got := m.RevocationChecker(); got != auth.RevocationChecker(rc) {
		t.Fatalf("RevocationChecker() = %#v, want the installed checker %#v", got, rc)
	}
}

// TestRevocationChecker_UnwiredAndNilMiddlewareAnswerNil covers the two ways a
// reusing plane can end up with no enforcement: no checker was wired, and no
// middleware exists at all (--auth-enabled=false). Both must be a nil
// INTERFACE, since the reusing plane's own guard is a `!= nil` test.
func TestRevocationChecker_UnwiredAndNilMiddlewareAnswerNil(t *testing.T) {
	if rc := New(nil).RevocationChecker(); rc != nil {
		t.Errorf("unwired middleware: RevocationChecker() = %#v, want nil", rc)
	}

	var absent *AuthMiddleware
	if rc := absent.RevocationChecker(); rc != nil {
		t.Errorf("nil middleware: RevocationChecker() = %#v, want nil", rc)
	}
}
