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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// servicePrincipalClaims are the identities an authenticator could hand the
// chain that claim the service principal: the node's own construction, the
// reserved subject alone, and the metadata marker alone (with an ordinary
// subject and an empty value -- presence is the claim).
func servicePrincipalClaims() map[string]*auth.Identity {
	return map[string]*auth.Identity{
		"constructed": auth.NewServicePrincipal("xkms", tenantid.System, []string{"kv:read"}),
		"subject":     {Subject: auth.ServicePrincipalSubjectPrefix + "xkms", TenantID: tenantid.System},
		"marker":      {Subject: "alice", TenantID: "acme", Metadata: map[string]string{auth.ServicePrincipalMetadataKey: ""}},
	}
}

// claimantMiddleware is a middleware whose only authenticator returns claim,
// with a user store that would resolve any subject to an active account
// holding roles -- so a refusal is the service-principal rule's, never a
// missing account's.
// claimantUserID is the immutable id of the account claimantMiddleware stores.
const claimantUserID = 4242

func claimantMiddleware(claim *auth.Identity) *AuthMiddleware {
	stored := &user.User{ID: claimantUserID, Username: claim.Subject, TenantID: claim.TenantID, Lifecycle: user.LifecycleActive, RoleIDs: []string{"admin-role"}}
	return New(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "host-bearer", identity: claim}),
		WithUserService(&mockUserServicer{user: stored}))
}

// assertServicePrincipalRefusal checks err is the typed refusal, classified as
// unauthenticated on every transport.
func assertServicePrincipalRefusal(t *testing.T, err error) {
	t.Helper()
	var refused *ServicePrincipalPresentedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v (%T), want *ServicePrincipalPresentedError", err, err)
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v does not match auth.ErrUnauthenticated", err)
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnauthenticated {
		t.Fatalf("ClassifyAuthError = %v, want %v", got, AuthFailureUnauthenticated)
	}
}

// TestAuthenticate_ServicePrincipalClaimIsRefused covers the chain entry
// points the MCP, SQL (pgsql, mysql), etcd and QUIC servers call: whatever
// credential produced the claim, the chain refuses it and binds no identity.
func TestAuthenticate_ServicePrincipalClaimIsRefused(t *testing.T) {
	for name, claim := range servicePrincipalClaims() {
		t.Run(name, func(t *testing.T) {
			m := claimantMiddleware(claim)

			ctx, id, err := m.Authenticate(context.Background())
			assertServicePrincipalRefusal(t, err)
			if id != nil || auth.IdentityFromContext(ctx) != nil {
				t.Fatalf("refused claim still bound an identity: %+v", id)
			}

			qctx, err := m.AuthenticateQUIC(context.Background())
			assertServicePrincipalRefusal(t, err)
			if auth.IdentityFromContext(qctx) != nil {
				t.Fatal("QUIC: refused claim still bound an identity")
			}
		})
	}
}

// TestAuthenticate_OrdinaryIdentityIsNotAServicePrincipal is the control: a
// subject that merely resembles the namespace is admitted.
func TestAuthenticate_OrdinaryIdentityIsNotAServicePrincipal(t *testing.T) {
	for _, subject := range []string{"qrdb-service", "service:xkms", "xkms"} {
		m := claimantMiddleware(&auth.Identity{Subject: subject, TenantID: "acme"})
		_, id, err := m.Authenticate(context.Background())
		if err != nil || id == nil || id.Subject != auth.UserSubject(claimantUserID) || id.Username != subject {
			t.Fatalf("%q: Authenticate = (%+v, %v), want the identity admitted as user id %d", subject, id, err, claimantUserID)
		}
	}
}

// TestRESTMiddleware_ServicePrincipalClaimIs401: the REST and HTTP/3 surface
// answers 401 and never reaches the handler.
func TestRESTMiddleware_ServicePrincipalClaimIs401(t *testing.T) {
	for name, claim := range servicePrincipalClaims() {
		t.Run(name, func(t *testing.T) {
			ran := false
			h := claimantMiddleware(claim).RESTMiddleware(testRESTRoutes(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
			if rec.Code != http.StatusUnauthorized || ran {
				t.Fatalf("status = %d, handler ran = %v; want 401 and no handler", rec.Code, ran)
			}
		})
	}
}

// TestUnaryInterceptor_ServicePrincipalClaimIsUnauthenticated: the gRPC and
// Unix surface answers Unauthenticated and never reaches the handler.
func TestUnaryInterceptor_ServicePrincipalClaimIsUnauthenticated(t *testing.T) {
	for name, claim := range servicePrincipalClaims() {
		t.Run(name, func(t *testing.T) {
			ran := false
			handler := func(context.Context, any) (any, error) { ran = true; return nil, nil }
			info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.KVService/Get"}
			_, err := claimantMiddleware(claim).UnaryInterceptor()(context.Background(), nil, info, handler)
			if status.Code(err) != codes.Unauthenticated || ran {
				t.Fatalf("err = %v, handler ran = %v; want Unauthenticated and no handler", err, ran)
			}
		})
	}
}

// TestOnBehalf_ServicePrincipalAssertionIsRefused: a cluster peer's signed
// assertion naming the principal is refused on REST and QUIC, even from a
// verified cluster node and with a user record of that name in the store.
func TestOnBehalf_ServicePrincipalAssertionIsRefused(t *testing.T) {
	subject := auth.ServicePrincipalSubjectPrefix + "xkms"
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	stored := &user.User{Username: subject, TenantID: tenantid.System, Lifecycle: user.LifecycleActive, RoleIDs: []string{"admin-role"}}

	r := buildOnBehalfRequest(t, nodeCert, nodeKey, subject, tenantid.System, obNodeID, time.Now(), nil, http.MethodGet, "/status")
	rec, ran, _ := runOnBehalf(t, r, stored, &capturingAuthorizer{})
	if rec.Code != http.StatusUnauthorized || ran {
		t.Fatalf("REST on-behalf: status = %d, handler ran = %v; want 401 and no handler", rec.Code, ran)
	}

	payload := []byte("kv get payload")
	a := forward.RPCBinding("quic", "kv.Get", payload).Assertion(subject, tenantid.System, obNodeID, time.Now())
	ab, sb, err := forward.Sign(a, nodeKey)
	if err != nil {
		t.Fatal(err)
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{nodeCert}, VerifiedChains: [][]*x509.Certificate{{nodeCert}}}
	m := New(&capturingAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}),
		WithUserService(&mockUserServicer{user: stored}))
	_, err = m.AuthenticateQUICOnBehalf(auth.WithTLSState(context.Background(), state), "kv.Get", payload, ab, sb)
	assertServicePrincipalRefusal(t, err)
}

// TestServicePrincipalPresentedError_Message names the refused subject.
func TestServicePrincipalPresentedError_Message(t *testing.T) {
	err := &ServicePrincipalPresentedError{Subject: "qrdb-service:xkms"}
	want := `auth/middleware: identity "qrdb-service:xkms" claims a service principal, which no network transport accepts`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("does not unwrap to auth.ErrUnauthenticated")
	}
}
