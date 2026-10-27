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

package sdk_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// This file is a consumer of the SDK: it imports sdk/go and the standard
// library only, and puts qrdb's mTLS authentication and authorization in front
// of a router of its own.

const (
	consumerTenant   = "acme"
	consumerUsername = "alice"
	consumerRoute    = "/v1/widgets"
)

// errConsumerUnknownUser is consumerUsers' answer for any account but the one
// it holds.
var errConsumerUnknownUser = errors.New("consumer user store: no such user")

// consumerUsers is the consumer's user store, holding one active account. The
// middleware resolves an authenticated subject by username; every other
// method of the interface is unused here and left to the nil embed.
type consumerUsers struct {
	sdk.UserServicer
	account *sdk.User
}

func (s consumerUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*sdk.User, error) {
	if tenantID != s.account.TenantID || username != s.account.Username {
		return nil, errConsumerUnknownUser
	}
	return s.account, nil
}

// consumerRoles is the consumer's role store. It names the built-in roles, so
// the middleware can decide TenantOwner; the enrolled account holds none.
type consumerRoles struct{}

func (consumerRoles) GetRoleByName(_ context.Context, tenantID, name string) (*sdk.Role, error) {
	return &sdk.Role{ID: "role-" + name, Name: name, TenantID: tenantID, System: true}, nil
}

// consumerAuthorizer admits the enrolled account and records every decision
// it is asked for, so a test can tell a refusal at authentication from one at
// authorization.
type consumerAuthorizer struct {
	calls atomic.Int32
}

func (a *consumerAuthorizer) Authorize(_ context.Context, identity *sdk.Identity, _, _ string) error {
	a.calls.Add(1)
	if identity == nil || identity.Username != consumerUsername {
		return errors.New("consumer authorizer: not the enrolled account")
	}
	return nil
}

// consumerGate builds the SDK middleware over the consumer's stores and wraps
// a handler that reports whether it was reached.
func consumerGate(t *testing.T) (http.Handler, *consumerAuthorizer, *atomic.Bool) {
	t.Helper()
	authorizer := &consumerAuthorizer{}
	users := consumerUsers{account: &sdk.User{
		ID:        7,
		Username:  consumerUsername,
		TenantID:  consumerTenant,
		Lifecycle: sdk.LifecycleActive,
		CertBindings: []sdk.CertBinding{{
			Thumbprint: sdk.ComputeSPKIThumbprint(consumerClientCert(consumerSPKI)),
			Kind:       sdk.CertBindingSPKIThumbprint,
		}},
	}}
	opts := []sdkserver.AuthMiddlewareOption{
		sdkserver.WithAuthMiddlewareAuthenticator(sdk.NewMTLSAuthenticator()),
		sdkserver.WithAuthMiddlewareUserService(users),
		sdkserver.WithAuthMiddlewareRoleResolver(consumerRoles{}),
	}
	mw := sdkserver.NewAuthMiddleware(authorizer, opts...)

	routes := sdkserver.NewRESTRouteRegistry()
	routes.Register(http.MethodGet, consumerRoute, sdk.PermissionUsersRead)

	reached := &atomic.Bool{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		if id := sdk.IdentityFromContext(r.Context()); id == nil || id.Username != consumerUsername {
			t.Errorf("handler identity = %+v, want the enrolled account", id)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mw.RESTMiddleware(routes, handler), authorizer, reached
}

// consumerSPKI and strangerSPKI stand in for two subject public keys: the
// binding check reads a certificate's SPKI bytes, never a key, so the test
// needs no key material.
var (
	consumerSPKI = []byte("consumer subject public key info")
	strangerSPKI = []byte("stranger subject public key info")
)

// consumerClientCert is the leaf a TLS server that verified the client would
// put in the connection state: the tenant named by both its Subject OU and its
// SPIFFE ID SAN, the account by its CN, the key by spki.
func consumerClientCert(spki []byte) *x509.Certificate {
	return &x509.Certificate{
		RawSubjectPublicKeyInfo: spki,
		Subject: pkix.Name{
			CommonName:         consumerUsername,
			OrganizationalUnit: []string{consumerTenant},
		},
		URIs: []*url.URL{{Scheme: "spiffe", Host: consumerTenant, Path: "/user/" + consumerUsername}},
	}
}

// verifiedTLS is the connection state of a handshake whose client presented
// cert and the TLS layer verified it.
func verifiedTLS(cert *x509.Certificate) *tls.ConnectionState {
	return &tls.ConnectionState{
		HandshakeComplete: true,
		PeerCertificates:  []*x509.Certificate{cert},
		VerifiedChains:    [][]*x509.Certificate{{cert}},
	}
}

func TestSDKAuthMiddleware_VerifiedClientCertReachesHandler(t *testing.T) {
	gate, authorizer, reached := consumerGate(t)

	req := httptest.NewRequest(http.MethodGet, consumerRoute, nil)
	req.TLS = verifiedTLS(consumerClientCert(consumerSPKI))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
	}
	if !reached.Load() {
		t.Fatal("handler not reached for a verified client certificate")
	}
	if got := authorizer.calls.Load(); got != 1 {
		t.Fatalf("authorizer calls = %d, want 1", got)
	}
}

func TestSDKAuthMiddleware_NoClientCertIsRefused(t *testing.T) {
	gate, authorizer, reached := consumerGate(t)

	req := httptest.NewRequest(http.MethodGet, consumerRoute, nil)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusUnauthorized)
	}
	if reached.Load() {
		t.Fatal("handler reached without a client certificate")
	}
	if got := authorizer.calls.Load(); got != 0 {
		t.Fatalf("authorizer calls = %d, want 0: the refusal is authentication's", got)
	}
}

func TestSDKAuthMiddleware_UnverifiedClientCertIsRefused(t *testing.T) {
	gate, _, reached := consumerGate(t)

	// A certificate the TLS layer did not verify, with no issuer verifier to
	// verify it here, names no one.
	req := httptest.NewRequest(http.MethodGet, consumerRoute, nil)
	req.TLS = &tls.ConnectionState{
		HandshakeComplete: true,
		PeerCertificates:  []*x509.Certificate{consumerClientCert(consumerSPKI)},
	}
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusUnauthorized)
	}
	if reached.Load() {
		t.Fatal("handler reached with an unverified client certificate")
	}
}

func TestSDKAuthMiddleware_UnenrolledKeyIsRefused(t *testing.T) {
	gate, authorizer, reached := consumerGate(t)

	// A verified certificate naming the account, over a key the account never
	// enrolled, has not identified it.
	req := httptest.NewRequest(http.MethodGet, consumerRoute, nil)
	req.TLS = verifiedTLS(consumerClientCert(strangerSPKI))
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusUnauthorized)
	}
	if reached.Load() {
		t.Fatal("handler reached with a certificate over an unenrolled key")
	}
	if got := authorizer.calls.Load(); got != 0 {
		t.Fatalf("authorizer calls = %d, want 0: the refusal is authentication's", got)
	}
}
