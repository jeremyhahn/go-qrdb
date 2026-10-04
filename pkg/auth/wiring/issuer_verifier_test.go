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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// These tests close go-qrdb#560 review item B2-1 and B2-2 at the wiring
// seam: BuildAuthMiddleware.cfg.IssuerVerifier must reach BOTH the mTLS
// authenticator (auth.WithIssuerVerifier) and the on-behalf forwarding path
// (middleware.WithIssuerVerifier) from a single config field, since the
// on-behalf path bypasses the authenticator chain entirely and does not
// inherit the mTLS authenticator's own check.
//
// The old issuerTenantBinding(ceremony.CeremonyCA, cert.CertProvider) helper
// this file used to test is gone: constructing the auth.IssuerVerifier is
// now the composer's job (in production, *clienttrust.Pool, which is also
// the object widening the listener's TLS ClientCAs -- "the pool and the
// binding are the same object", go-qrdb#560 review B2-2), and
// BuildAuthMiddleware only has to forward whatever it is given to both
// consumers. That forwarding is what these tests pin.

// wiringFakeIssuerVerifier implements auth.IssuerVerifier. It plays the role
// *clienttrust.Pool plays in production: ONE object answers both
// IsClusterCA and VerifyIssuer, so Key B trust and the issuer-tenant binding
// can never drift apart here either.
type wiringFakeIssuerVerifier struct {
	clusterCA *x509.Certificate
	registry  map[string]*x509.Certificate
}

var errWiringIssuerNotTrusted = errors.New("wiring test: issuer not trusted")

func (f *wiringFakeIssuerVerifier) IsClusterCA(_ context.Context, issuer *x509.Certificate) bool {
	if f.clusterCA == nil || issuer == nil {
		return false
	}
	return bytes.Equal(f.clusterCA.Raw, issuer.Raw)
}

// VerifyPeerChain is unexercised by this file's tests: this fake is only
// used to confirm BuildAuthMiddleware WIRES the verifier through, not to
// drive the authenticator's own chain-resolution fallback, so it errors
// unconditionally rather than fabricating a chain a test here never asks
// for.
func (f *wiringFakeIssuerVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	return nil, errors.New("wiringFakeIssuerVerifier: VerifyPeerChain not implemented")
}

func (f *wiringFakeIssuerVerifier) VerifyIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error {
	if f.IsClusterCA(ctx, issuer) {
		return nil
	}
	registered, ok := f.registry[tenantID]
	if !ok || issuer == nil || registered.Raw == nil || issuer.Raw == nil || !bytes.Equal(registered.Raw, issuer.Raw) {
		return errWiringIssuerNotTrusted
	}
	return nil
}

var _ auth.IssuerVerifier = (*wiringFakeIssuerVerifier)(nil)

// genWiringCA generates a self-signed CA certificate for these tests.
func genWiringCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return ca, key
}

// genWiringNodeLeaf issues a leaf under ca/caKey carrying a node-scoped
// SPIFFE SAN -- the exact shape forward.ClusterPeer accepts.
func genWiringNodeLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, nodeID string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "qrdb-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{{Scheme: "spiffe", Host: "qrdb.cluster", Path: "/node/" + nodeID}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return leaf, key
}

// issuerVerifierOBRoutes registers the forwarded write's route.
func issuerVerifierOBRoutes() *middleware.RESTRouteRegistry {
	rr := middleware.NewRESTRouteRegistry()
	rr.Register(http.MethodPut, "/kv/*", permission.KVPut)
	return rr
}

// issuerVerifierForwardedWrite builds a well-formed forwarded write signed
// by nodeKey, presenting nodeCert as verified against issuerCert.
func issuerVerifierForwardedWrite(t *testing.T, nodeCert *x509.Certificate, nodeKey *ecdsa.PrivateKey, issuerCert *x509.Certificate, nodeID string) *http.Request {
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
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{nodeCert, issuerCert}}}
	return r
}

// buildIssuerVerifierMiddleware wires BuildAuthMiddleware with the given
// verifier and nothing else security-relevant switched on.
func buildIssuerVerifierMiddleware(t *testing.T, verifier auth.IssuerVerifier) *middleware.AuthMiddleware {
	t.Helper()
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
		IssuerVerifier: verifier,
	})
	require.NoError(t, err)
	require.NotNil(t, mw)
	return mw
}

// TestIssuerVerifier_OnBehalf_KeyBSignedNodeLeafRefused is the B2-1 proof: a
// node-SAN leaf signed by a tenant's Key B CA -- never the cluster CA --
// must not be honored as an on-behalf cluster-forwarding assertion, even
// though forward.ClusterPeer's SPIFFE-SHAPE check alone would have accepted
// it. Before the fix this request succeeded.
func TestIssuerVerifier_OnBehalf_KeyBSignedNodeLeafRefused(t *testing.T) {
	const nodeID = "127.0.0.1:63001"
	clusterCA, _ := genWiringCA(t, "cluster-ca")
	keyBCA, keyBKey := genWiringCA(t, "tenant-key-b-ca")
	nodeLeaf, nodeKey := genWiringNodeLeaf(t, keyBCA, keyBKey, nodeID)

	verifier := &wiringFakeIssuerVerifier{clusterCA: clusterCA}
	mw := buildIssuerVerifierMiddleware(t, verifier)

	handler := mw.RESTMiddleware(issuerVerifierOBRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the handler was reached for an on-behalf assertion forwarded by a Key B-signed node leaf")
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, issuerVerifierForwardedWrite(t, nodeLeaf, nodeKey, keyBCA, nodeID))
	// auth.AuthError{Code: "unauthorized"} classifies as AuthFailureUnauthorized
	// (403), the same status the pre-existing forward.ClusterPeer refusal two
	// lines above this check uses -- see failure_class.go.
	require.Equal(t, http.StatusForbidden, rec.Code,
		"a node leaf signed by a tenant Key B CA must not be honored as an on-behalf assertion")
}

// TestIssuerVerifier_OnBehalf_ClusterCASignedNodeLeafAccepted is the
// companion green case: the same request, with the node leaf signed by the
// cluster CA instead, must be accepted.
func TestIssuerVerifier_OnBehalf_ClusterCASignedNodeLeafAccepted(t *testing.T) {
	const nodeID = "127.0.0.1:63001"
	clusterCA, clusterKey := genWiringCA(t, "cluster-ca")
	nodeLeaf, nodeKey := genWiringNodeLeaf(t, clusterCA, clusterKey, nodeID)

	verifier := &wiringFakeIssuerVerifier{clusterCA: clusterCA}
	mw := buildIssuerVerifierMiddleware(t, verifier)

	var reached bool
	handler := mw.RESTMiddleware(issuerVerifierOBRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, issuerVerifierForwardedWrite(t, nodeLeaf, nodeKey, clusterCA, nodeID))
	require.Equal(t, http.StatusNoContent, rec.Code,
		"a node leaf signed by the cluster CA must be honored as an on-behalf assertion")
	require.True(t, reached)
}

// TestIssuerVerifier_OnBehalf_UnwiredVerifierDoesNotRefuse pins the
// unenforced posture: with no IssuerVerifier configured (the zero value),
// the on-behalf path's behavior is unchanged -- a SPIFFE-shaped node leaf
// from ANY issuer is honored, exactly as before B2-1 existed. This is NOT
// the production posture (a composer that wires Key B trust must also wire
// the verifier), but it documents that BuildAuthMiddleware never enables
// this check on its own.
func TestIssuerVerifier_OnBehalf_UnwiredVerifierDoesNotRefuse(t *testing.T) {
	const nodeID = "127.0.0.1:63001"
	keyBCA, keyBKey := genWiringCA(t, "tenant-key-b-ca")
	nodeLeaf, nodeKey := genWiringNodeLeaf(t, keyBCA, keyBKey, nodeID)

	mw := buildIssuerVerifierMiddleware(t, nil)

	var reached bool
	handler := mw.RESTMiddleware(issuerVerifierOBRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, issuerVerifierForwardedWrite(t, nodeLeaf, nodeKey, keyBCA, nodeID))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, reached)
}
