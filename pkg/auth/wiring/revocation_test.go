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

package wiring

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Test barrier
// ---------------------------------------------------------------------------

// testBarrier is a real AES-GCM seal.EncryptionBarrier, so the certificate
// authority under test performs genuine wrap/unwrap of its CA key rather than
// running against a pass-through.
type testBarrier struct {
	key    []byte
	sealed atomic.Bool
}

func newTestBarrier(t *testing.T) *testBarrier {
	t.Helper()
	key := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, key)
	require.NoError(t, err)
	return &testBarrier{key: key}
}

func (b *testBarrier) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func (b *testBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, &cert.BarrierSealedError{}
	}
	gcm, err := b.aead()
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *testBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if b.sealed.Load() {
		return nil, &cert.BarrierSealedError{}
	}
	gcm, err := b.aead()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *testBarrier) IsSealed() bool { return b.sealed.Load() }

// ---------------------------------------------------------------------------
// End-to-end: issue -> authenticate -> revoke -> refuse
// ---------------------------------------------------------------------------

// revocationFixture holds a real certificate authority and a client
// certificate it issued.
type revocationFixture struct {
	provider *cert.DefaultCertProvider
	barrier  *testBarrier
	serial   string
	peer     *x509.Certificate
}

func newRevocationFixture(t *testing.T, username, tenantID string) *revocationFixture {
	t.Helper()
	barrier := newTestBarrier(t)
	provider := cert.NewDefaultProvider(kvstore.NewMemoryKVStore(), barrier)
	ctx := context.Background()
	require.NoError(t, provider.Initialize(ctx))

	bundle, err := provider.GenerateClientCert(ctx, &cert.GenerateRequest{
		Username: username,
		TenantID: tenantID,
	})
	require.NoError(t, err)

	block, _ := pem.Decode(bundle.CertPEM)
	require.NotNil(t, block)
	peer, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	return &revocationFixture{provider: provider, barrier: barrier, serial: bundle.Serial, peer: peer}
}

// request builds an authenticated GET /status carrying the fixture's
// certificate as a verified mTLS peer.
func (f *revocationFixture) request() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{f.peer},
		VerifiedChains:   [][]*x509.Certificate{{f.peer}},
	}
	return req
}

// buildRevocationMiddleware wires the production auth stack against the
// fixture's certificate authority.
func buildRevocationMiddleware(t *testing.T, f *revocationFixture, username, tenantID string, ttl time.Duration) *middleware.AuthMiddleware {
	t.Helper()
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{u: &user.User{
			Username:  username,
			TenantID:  tenantID,
			RoleIDs:   []string{testAdminRoleID},
			Lifecycle: user.LifecycleActive,
		}},
		CertProvider:       f.provider,
		RevocationCacheTTL: ttl,
	})
	require.NoError(t, err)
	require.NotNil(t, mw)
	return mw
}

// TestRevocation_EndToEnd is the verification the issue asks for: revoke a
// certificate, then assert that an authenticated request presenting it is
// rejected. Before enforcement existed this test's second half failed — the
// request succeeded and `qrdb certs list` showed revoked: true.
func TestRevocation_EndToEnd(t *testing.T) {
	const username, tenantID = "alice", "system"
	f := newRevocationFixture(t, username, tenantID)
	// No caching: the two requests below are two real lookups, so this test
	// measures enforcement rather than cache timing.
	mw := buildRevocationMiddleware(t, f, username, tenantID, 0)

	var reached atomic.Int64
	handler := mw.RESTMiddleware(statusRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	require.Equal(t, http.StatusOK, rec.Code, "a valid certificate must authenticate")
	require.Equal(t, int64(1), reached.Load())

	require.NoError(t, f.provider.RevokeCert(context.Background(), f.serial))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a revoked certificate must not authenticate")
	assert.Equal(t, int64(1), reached.Load(),
		"the handler was reached with a revoked certificate")
	assert.Contains(t, rec.Body.String(), "revoked")
}

// TestRevocation_TakesEffectWithinTheConfiguredWindow states the security
// meaning of the TTL: it is the window during which a revoked certificate
// still authenticates on a node that had already seen it.
func TestRevocation_TakesEffectWithinTheConfiguredWindow(t *testing.T) {
	const username, tenantID = "alice", "system"
	f := newRevocationFixture(t, username, tenantID)
	// A TTL this short has elapsed by the time the next request runs, without
	// the test sleeping.
	mw := buildRevocationMiddleware(t, f, username, tenantID, time.Nanosecond)

	handler := mw.RESTMiddleware(statusRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	require.Equal(t, http.StatusOK, rec.Code)

	require.NoError(t, f.provider.RevokeCert(context.Background(), f.serial))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"the cached not-revoked answer outlived its TTL")
}

// TestRevocation_SealedBarrierDoesNotLockOutTheOperator pins the one stated
// fail-open. Refusing every mTLS request while the barrier is sealed would
// make the barrier unsealable, because the unseal endpoints are themselves
// reached over mTLS.
func TestRevocation_SealedBarrierDoesNotLockOutTheOperator(t *testing.T) {
	const username, tenantID = "alice", "system"
	f := newRevocationFixture(t, username, tenantID)
	mw := buildRevocationMiddleware(t, f, username, tenantID, 0)

	// Revoke first, THEN seal: this proves the request is admitted because the
	// state is unreadable, not because there was nothing to find.
	require.NoError(t, f.provider.RevokeCert(context.Background(), f.serial))
	f.barrier.sealed.Store(true)

	handler := mw.RESTMiddleware(statusRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	assert.Equal(t, http.StatusOK, rec.Code,
		"a sealed barrier must not lock every mTLS caller out of the unseal path")

	// And the moment the barrier opens, the revocation takes effect.
	f.barrier.sealed.Store(false)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"the revocation did not take effect once the barrier was unsealed")
}

// TestRevocation_LookupFailureIsRetryableNotDenied pins the fail-closed half:
// a certificate of unknown status is refused, and the refusal is 503 so a
// legitimate caller is told to retry rather than that its credential is bad.
func TestRevocation_LookupFailureIsRetryableNotDenied(t *testing.T) {
	const username, tenantID = "alice", "system"
	f := newRevocationFixture(t, username, tenantID)

	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{u: &user.User{
			Username:  username,
			TenantID:  tenantID,
			RoleIDs:   []string{testAdminRoleID},
			Lifecycle: user.LifecycleActive,
		}},
		CertProvider:       failingRevocationProvider{CertProvider: f.provider},
		RevocationCacheTTL: 0,
	})
	require.NoError(t, err)

	handler := mw.RESTMiddleware(statusRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the handler was reached with a certificate of unknown revocation status")
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// failingRevocationProvider answers every revocation lookup with a store
// failure while delegating everything else.
type failingRevocationProvider struct {
	cert.CertProvider
}

func (failingRevocationProvider) IsRevoked(context.Context, string) (bool, error) {
	return false, &cert.StoreError{Op: "get", Detail: "revoked", Err: io.ErrUnexpectedEOF}
}

// TestRevocationChecker_NilProviderYieldsNilInterface guards the typed-nil
// trap: a nil *cert.RevocationCache stored in an interface is NOT a nil
// interface, so the authenticator's `revocation != nil` check would pass and
// every request would call through a nil receiver — enforcement reported as on
// and answering "not revoked" to everything.
//
// The comparison is a bare `!= nil` on purpose. testify's assert.Nil uses
// reflection and reports a typed nil pointer as nil, so it passes on exactly
// the value this test exists to reject (measured: it did not catch the
// mutation that removed the guard).
func TestRevocationChecker_NilProviderYieldsNilInterface(t *testing.T) {
	if rc := revocationChecker(nil, time.Minute); rc != nil {
		t.Fatalf("revocationChecker(nil, …) = %#v, want a nil interface", rc)
	}
}

func TestRevocationChecker_ProviderYieldsChecker(t *testing.T) {
	f := newRevocationFixture(t, "alice", "system")
	rc := revocationChecker(f.provider, 5*time.Second)
	require.NotNil(t, rc)

	revoked, err := rc.IsCertRevoked(context.Background(), f.peer)
	require.NoError(t, err)
	assert.False(t, revoked)
}

// statusRoutes is the smallest route registry that admits the authenticated
// GET /status used by these tests.
func statusRoutes() *middleware.RESTRouteRegistry {
	rr := middleware.NewRESTRouteRegistry()
	rr.Register(http.MethodGet, "/status", permission.AdminStatus)
	return rr
}

// ---------------------------------------------------------------------------
// The on-behalf forwarding path is gated too
// ---------------------------------------------------------------------------

// obNodeCertificate returns a self-signed cluster-node certificate carrying a
// node SPIFFE SAN, plus its key and the serial in the CA's canonical format.
//
// It is self-signed rather than CA-issued because revocation here is
// DENY-BY-SERIAL: the marker is written for any serial, so a node certificate
// this CA never issued is still revocable — which is the case cluster
// operators actually have.
func obNodeCertificate(t *testing.T, nodeID string) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial := big.NewInt(0x5eed)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "qrdb-node"},
		URIs:         []*url.URL{{Scheme: "spiffe", Host: "qrdb.cluster", Path: "/node/" + nodeID}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	parsed, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return parsed, key, cert.FormatSerial(serial)
}

// obForwardedWrite builds a well-formed forwarded write signed by nodeKey and
// presenting nodeCert as the verified mTLS peer.
func obForwardedWrite(t *testing.T, nodeCert *x509.Certificate, nodeKey *ecdsa.PrivateKey, nodeID string) *http.Request {
	t.Helper()
	const method, target = http.MethodPut, "/kv/foo"
	body := []byte(`{"value":"v"}`)

	a := forward.NewCallerAssertion("alice", "system", method, target,
		forward.BodyDigest(body), nodeID, time.Now(), forward.DefaultTTL)
	ab, sb, err := forward.Sign(a, nodeKey)
	require.NoError(t, err)

	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set(forward.HeaderAssertion, ab)
	r.Header.Set(forward.HeaderSignature, sb)
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{nodeCert}}}
	return r
}

// obRoutes registers the forwarded write's route.
func obRoutes() *middleware.RESTRouteRegistry {
	rr := middleware.NewRESTRouteRegistry()
	rr.Register(http.MethodPut, "/kv/*", permission.KVPut)
	return rr
}

// TestRevocation_OnBehalfForwardingIsGatedByProductionWiring asserts that
// BuildAuthMiddleware gates BOTH paths.
//
// authenticateOnBehalf never runs the authenticator chain, so it does not
// inherit the mTLS authenticator's revocation check. Wiring the checker into
// only the authenticator leaves a revoked cluster node forwarding writes on
// behalf of any user it names — and every authenticator-path test still
// passes. This test is what makes that wiring load-bearing.
func TestRevocation_OnBehalfForwardingIsGatedByProductionWiring(t *testing.T) {
	const nodeID = "127.0.0.1:63001"
	f := newRevocationFixture(t, "alice", "system")
	nodeCert, nodeKey, nodeSerial := obNodeCertificate(t, nodeID)

	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled: true,
		RoleService: &fakeRoleService{perms: map[string][]permission.Permission{
			testAdminRoleID: {permission.KVPut},
		}},
		UserService: &fakeUserService{u: &user.User{
			Username:  "alice",
			TenantID:  "system",
			RoleIDs:   []string{testAdminRoleID},
			Lifecycle: user.LifecycleActive,
		}},
		CertProvider:       f.provider,
		RevocationCacheTTL: 0,
	})
	require.NoError(t, err)

	var reached atomic.Int64
	handler := mw.RESTMiddleware(obRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, obForwardedWrite(t, nodeCert, nodeKey, nodeID))
	require.Equal(t, http.StatusNoContent, rec.Code, "a good forwarded write must be accepted")
	require.Equal(t, int64(1), reached.Load())

	// Deny-by-serial: this node holds no record for the node certificate, and
	// the revocation takes effect anyway. That is the whole point of retaining
	// it — a cluster operator revokes a peer-issued certificate.
	require.NoError(t, f.provider.RevokeCert(context.Background(), nodeSerial))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, obForwardedWrite(t, nodeCert, nodeKey, nodeID))
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"a revoked cluster node must not be able to forward")
	assert.Equal(t, int64(1), reached.Load(),
		"the handler ran for a forwarded write from a revoked node")
}
