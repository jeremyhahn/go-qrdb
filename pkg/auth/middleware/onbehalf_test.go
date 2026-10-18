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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// capturingAuthorizer records the identity it was asked to authorize so a test
// can assert the leader authorized AS THE CALLER (not the forwarding node).
type capturingAuthorizer struct {
	err  error
	seen *auth.Identity
}

func (c *capturingAuthorizer) Authorize(_ context.Context, id *auth.Identity, _, _ string) error {
	c.seen = id
	return c.err
}

// obNodeCert builds a self-signed ECDSA node certificate with a node SPIFFE SAN.
func obNodeCert(t *testing.T, nodeID string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	uri := &url.URL{Scheme: "spiffe", Host: "qrdb.cluster", Path: "/node/" + nodeID}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(10),
		Subject:      pkix.Name{CommonName: "qrdb-node"},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsecert: %v", err)
	}
	return cert, key
}

// obUserCert builds a self-signed ECDSA cert WITHOUT a node SAN (a user cert).
func obUserCert(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(11),
		Subject:      pkix.Name{CommonName: "mallory"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsecert: %v", err)
	}
	return cert, key
}

const obNodeID = "127.0.0.1:63001"

// obAliceID is the immutable id of the "alice" account the on-behalf tests
// store, and obAliceSubject the Subject a forwarding node asserts for her.
const obAliceID = 4100

var obAliceSubject = auth.UserSubject(obAliceID)

// buildOnBehalfRequest constructs a PUT /kv/foo request carrying a TLS state
// with peerCert as the verified peer, a body, and the on-behalf headers built
// by signing an assertion (bound to method/path/body) with signKey. The
// assertion's issued/expiry times are stamped at issued.
func buildOnBehalfRequest(t *testing.T, peerCert *x509.Certificate, signKey *ecdsa.PrivateKey, subject, tenantID, forwarderNodeID string, issued time.Time, body []byte, method, target string) *http.Request {
	t.Helper()
	a := forward.NewCallerAssertion(subject, tenantID, method, target, forward.BodyDigest(body), forwarderNodeID, issued, forward.DefaultTTL)
	ab, sb, err := forward.Sign(a, signKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set(forward.HeaderAssertion, ab)
	r.Header.Set(forward.HeaderSignature, sb)
	if peerCert != nil {
		r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{peerCert}}}
	}
	return r
}

// runOnBehalf drives r through RESTMiddleware with a capturing authorizer and a
// user store returning storeUser. It returns the recorded response, whether the
// downstream handler ran, and the identity the handler saw.
func runOnBehalf(t *testing.T, r *http.Request, storeUser *user.User, authz *capturingAuthorizer) (*httptest.ResponseRecorder, bool, *auth.Identity) {
	t.Helper()
	users := &mockUserServicer{user: storeUser}
	m := newTestMiddleware(authz,
		WithAuthenticator(&mockAuthenticator{name: "test-authn", err: auth.ErrNoPeerCertificate}),
		WithUserService(users),
	)

	var handlerRan bool
	var seen *auth.Identity
	var bodyAtHandler []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handlerRan = true
		seen = auth.IdentityFromContext(req.Context())
		bodyAtHandler, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	m.RESTMiddleware(testRESTRoutes(), next).ServeHTTP(rec, r)
	_ = bodyAtHandler
	return rec, handlerRan, seen
}

// TestOnBehalf_ValidSwapsIdentityAndReResolvesRoles verifies the happy path:
// a valid assertion from a cluster node swaps the request identity to the
// asserted caller, re-resolves that caller's roles from the user store (NOT any
// asserted role), and authorizes as the caller.
func TestOnBehalf_ValidSwapsIdentityAndReResolvesRoles(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte(`{"value":"x"}`)
	const aliceID = 42
	r := buildOnBehalfRequest(t, nodeCert, nodeKey, auth.UserSubject(aliceID), "acme", obNodeID, time.Now(), body, http.MethodPut, "/kv/foo")

	storeUser := &user.User{ID: aliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{"db-admin", "kv-writer"}}
	authz := &capturingAuthorizer{}

	rec, ran, seen := runOnBehalf(t, r, storeUser, authz)

	if !ran {
		t.Fatalf("handler did not run; status=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen == nil || seen.Subject != auth.UserSubject(aliceID) || seen.UserID != aliceID || seen.Username != "alice" || seen.TenantID != "acme" {
		t.Fatalf("identity not swapped to caller: %+v", seen)
	}
	if len(seen.Roles) != 2 || seen.Roles[0] != "db-admin" || seen.Roles[1] != "kv-writer" {
		t.Fatalf("roles not re-resolved from store: %+v", seen.Roles)
	}
	if authz.seen == nil || authz.seen.Subject != auth.UserSubject(aliceID) {
		t.Fatalf("authorizer did not see caller identity: %+v", authz.seen)
	}
	if seen.Metadata["auth_source"] != "on_behalf" {
		t.Fatalf("expected on_behalf auth_source, got %q", seen.Metadata["auth_source"])
	}
}

// TestOnBehalf_UserIDSubjectResolvesByIDAcrossRename proves the target node
// re-resolves a forwarded caller by its immutable id: the account was renamed
// after the forwarding node asserted it, and the identity is still that
// account, under its current name.
func TestOnBehalf_UserIDSubjectResolvesByIDAcrossRename(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	const aliceID = 77
	r := buildOnBehalfRequest(t, nodeCert, nodeKey, auth.UserSubject(aliceID), "acme", obNodeID, time.Now(), nil, http.MethodGet, "/kv/foo")
	renamed := &user.User{ID: aliceID, Username: "alice-renamed", TenantID: "acme", Lifecycle: user.LifecycleActive}

	rec, ran, seen := runOnBehalf(t, r, renamed, &capturingAuthorizer{})
	if !ran {
		t.Fatalf("handler did not run; status=%d body=%s", rec.Code, rec.Body.String())
	}
	if seen.Subject != auth.UserSubject(aliceID) || seen.Username != "alice-renamed" {
		t.Fatalf("identity = %+v, want user id %d under its current name", seen, aliceID)
	}
}

// TestOnBehalf_UserIDSubjectNamingNoAccountRefused proves a forwarded user id
// that resolves to no account here is refused, never admitted unenriched
// under a Subject that every downstream reader takes for an account.
func TestOnBehalf_UserIDSubjectNamingNoAccountRefused(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	r := buildOnBehalfRequest(t, nodeCert, nodeKey, auth.UserSubject(99), "acme", obNodeID, time.Now(), nil, http.MethodGet, "/kv/foo")
	other := &user.User{ID: 100, Username: "mallory", TenantID: "acme", Lifecycle: user.LifecycleActive}

	rec, ran, _ := runOnBehalf(t, r, other, &capturingAuthorizer{})
	if ran {
		t.Fatal("a user id naming no account was admitted")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestOnBehalf_AssertedRolesNeverTrusted proves the leader ignores any roles a
// caller might try to smuggle: the store is authoritative. Here the store user
// has NO roles, so even a valid assertion yields an unprivileged identity.
func TestOnBehalf_AssertedRolesNeverTrusted(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte("x")
	r := buildOnBehalfRequest(t, nodeCert, nodeKey, obAliceSubject, "acme", obNodeID, time.Now(), body, http.MethodPut, "/kv/foo")

	storeUser := &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: nil}
	authz := &capturingAuthorizer{}
	_, ran, seen := runOnBehalf(t, r, storeUser, authz)
	if !ran {
		t.Fatal("handler should run; authorizer decides on empty roles")
	}
	if len(seen.Roles) != 0 {
		t.Fatalf("expected no roles from store, got %+v", seen.Roles)
	}
}

// TestOnBehalf_ForgedSignatureRejected: an assertion signed by a key other than
// the presenting node cert's key is rejected; the handler never runs.
func TestOnBehalf_ForgedSignatureRejected(t *testing.T) {
	nodeCert, _ := obNodeCert(t, obNodeID)
	_, otherKey := obNodeCert(t, obNodeID)
	body := []byte("x")
	// Sign with otherKey but present nodeCert.
	r := buildOnBehalfRequest(t, nodeCert, otherKey, obAliceSubject, "acme", obNodeID, time.Now(), body, http.MethodPut, "/kv/foo")

	rec, ran, _ := runOnBehalf(t, r, &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran despite forged signature")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403, got %d", rec.Code)
	}
}

// TestOnBehalf_NonClusterPeerRejected: a user cert (no node SAN) presenting a
// validly-signed assertion is rejected -- only cluster nodes may forward.
func TestOnBehalf_NonClusterPeerRejected(t *testing.T) {
	userC, userKey := obUserCert(t)
	body := []byte("x")
	r := buildOnBehalfRequest(t, userC, userKey, obAliceSubject, "acme", obNodeID, time.Now(), body, http.MethodPut, "/kv/foo")

	rec, ran, _ := runOnBehalf(t, r, &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran despite non-cluster-peer presenter")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403, got %d", rec.Code)
	}
}

// TestOnBehalf_ExpiredRejected: an assertion issued far in the past (beyond TTL)
// is rejected.
func TestOnBehalf_ExpiredRejected(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte("x")
	issued := time.Now().Add(-forward.DefaultTTL - time.Minute)
	r := buildOnBehalfRequest(t, nodeCert, nodeKey, obAliceSubject, "acme", obNodeID, issued, body, http.MethodPut, "/kv/foo")

	rec, ran, _ := runOnBehalf(t, r, &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran despite expired assertion")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403, got %d", rec.Code)
	}
}

// TestOnBehalf_BodySwapRejected: a captured assertion replayed against a
// different body is rejected by the body-digest binding.
func TestOnBehalf_BodySwapRejected(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	origBody := []byte("original")
	// Build the request with the assertion bound to origBody...
	r := buildOnBehalfRequest(t, nodeCert, nodeKey, obAliceSubject, "acme", obNodeID, time.Now(), origBody, http.MethodPut, "/kv/foo")
	// ...but replace the body the leader actually receives.
	tampered := []byte("tampered")
	r.Body = io.NopCloser(bytes.NewReader(tampered))
	r.ContentLength = int64(len(tampered))

	rec, ran, _ := runOnBehalf(t, r, &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran despite body swap")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403, got %d", rec.Code)
	}
}

// TestOnBehalf_PathSwapRejected: an assertion bound to one path replayed on a
// different path is rejected.
func TestOnBehalf_PathSwapRejected(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte("x")
	// Sign an assertion bound to /kv/other, then submit it on /kv/foo.
	a := forward.NewCallerAssertion(obAliceSubject, "acme", http.MethodPut, "/kv/other", forward.BodyDigest(body), obNodeID, time.Now(), forward.DefaultTTL)
	ab, sb, err := forward.Sign(a, nodeKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	r := httptest.NewRequest(http.MethodPut, "/kv/foo", bytes.NewReader(body))
	r.Header.Set(forward.HeaderAssertion, ab)
	r.Header.Set(forward.HeaderSignature, sb)
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{nodeCert}}}

	rec, ran, _ := runOnBehalf(t, r, &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran despite path swap")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403, got %d", rec.Code)
	}
}

// TestOnBehalf_NoPeerCertRejected: on-behalf headers with no verified peer
// certificate are rejected (never honored over a non-mTLS connection).
func TestOnBehalf_NoPeerCertRejected(t *testing.T) {
	_, nodeKey := obNodeCert(t, obNodeID)
	body := []byte("x")
	r := buildOnBehalfRequest(t, nil, nodeKey, obAliceSubject, "acme", obNodeID, time.Now(), body, http.MethodPut, "/kv/foo")

	rec, ran, _ := runOnBehalf(t, r, &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran despite missing peer cert")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401/403, got %d", rec.Code)
	}
}

// TestOnBehalf_AbsentHeadersUseNormalAuth verifies that a request WITHOUT the
// on-behalf headers falls through to the normal authenticator chain unchanged.
func TestOnBehalf_AbsentHeadersUseNormalAuth(t *testing.T) {
	users := &mockUserServicer{user: &user.User{Username: "node", TenantID: "system", Lifecycle: user.LifecycleActive}}
	m := newTestMiddleware(&capturingAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: &auth.Identity{Subject: "node", TenantID: "system"}}),
		WithUserService(users),
	)
	var ran bool
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ran = true
		w.WriteHeader(http.StatusNoContent)
	})
	r := httptest.NewRequest(http.MethodPut, "/kv/foo", bytes.NewReader([]byte("x")))
	rec := httptest.NewRecorder()
	m.RESTMiddleware(testRESTRoutes(), next).ServeHTTP(rec, r)
	if !ran {
		t.Fatalf("normal-auth request did not reach handler; status=%d", rec.Code)
	}
}
