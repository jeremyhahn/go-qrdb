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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

const chainCertSubject = "cert-subject"

// afterMTLS places authenticators after mTLS, the position the hooks tests
// exercise.
func afterMTLS(authenticators ...auth.Authenticator) []auth.AuthenticatorPlacement {
	return placed(auth.ChainAfterMTLS, authenticators...)
}

func placed(pos auth.ChainPosition, authenticators ...auth.Authenticator) []auth.AuthenticatorPlacement {
	out := make([]auth.AuthenticatorPlacement, len(authenticators))
	for i, a := range authenticators {
		out[i] = auth.AuthenticatorPlacement{Position: pos, Authenticator: a}
	}
	return out
}

// chainPeerContext returns a context carrying a verified client certificate
// naming chainCertSubject in the system tenant, and the bearer when non-empty.
func chainPeerContext(t *testing.T, bearer string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: chainCertSubject},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	authtest.SetClientTenant(t, tmpl, tenantid.System, chainCertSubject)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	peer, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	ctx := auth.WithTLSState(context.Background(), &tls.ConnectionState{
		HandshakeComplete: true,
		PeerCertificates:  []*x509.Certificate{peer},
		VerifiedChains:    [][]*x509.Certificate{{peer}},
	})
	if bearer != "" {
		ctx = auth.WithHTTPAuthorization(ctx, bearer)
	}
	return ctx
}

// buildPositioned builds the production middleware with stub at pos. The
// enrolled binding posture lets the unenrolled certificate subject
// authenticate, so which authenticator decided is read off the subject.
func buildPositioned(t *testing.T, pos auth.ChainPosition, stub auth.Authenticator) *middleware.AuthMiddleware {
	t.Helper()
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{},
		MTLSBindingPolicy:   auth.MTLSBindingEnrolled,
		ExtraAuthenticators: placed(pos, stub),
	})
	require.NoError(t, err)
	return mw
}

func TestBuildAuthMiddleware_BeforeMTLSDecidesACertificateRequest(t *testing.T) {
	stub := &stubAuthenticator{token: "Bearer good"}
	mw := buildPositioned(t, auth.ChainBeforeMTLS, stub)

	_, id, err := mw.Authenticate(chainPeerContext(t, "Bearer good"))
	require.NoError(t, err)
	assert.Equal(t, "bearer-subject", id.Subject, "the bearer ahead of mTLS must decide")

	// A request the bearer declines falls through to mTLS.
	_, id, err = mw.Authenticate(chainPeerContext(t, ""))
	require.NoError(t, err)
	assert.Equal(t, chainCertSubject, id.Subject)
	assert.Equal(t, 2, stub.calls, "the authenticator ahead of mTLS runs on every request")
}

func TestBuildAuthMiddleware_AfterMTLSLeavesCertificateRequestsToMTLS(t *testing.T) {
	stub := &stubAuthenticator{token: "Bearer good"}
	mw := buildPositioned(t, auth.ChainAfterMTLS, stub)

	_, id, err := mw.Authenticate(chainPeerContext(t, "Bearer good"))
	require.NoError(t, err)
	assert.Equal(t, chainCertSubject, id.Subject, "mTLS ahead of the bearer must decide")
	assert.Zero(t, stub.calls, "an authenticator after mTLS must not run for a certificate mTLS accepted")

	_, id, err = mw.Authenticate(auth.WithHTTPAuthorization(context.Background(), "Bearer good"))
	require.NoError(t, err)
	assert.Equal(t, "bearer-subject", id.Subject)
}

func TestBuildAuthMiddleware_ReplaceMTLSRemovesTheCertificateDecision(t *testing.T) {
	stub := &stubAuthenticator{token: "Bearer good"}
	mw := buildPositioned(t, auth.ChainReplaceMTLS, stub)

	_, _, err := mw.Authenticate(chainPeerContext(t, ""))
	require.Error(t, err, "a client certificate alone authenticated with mTLS replaced")
	assert.True(t, errors.Is(err, auth.ErrUnauthenticated), "refusal is not unauthenticated: %v", err)

	_, id, err := mw.Authenticate(chainPeerContext(t, "Bearer good"))
	require.NoError(t, err)
	assert.Equal(t, "bearer-subject", id.Subject)
	assert.False(t, mw.IsAuthMethod("nonexistent"))
	assert.True(t, mw.IsAuthMethod("stub-bearer"))
}

func TestBuildAuthMiddleware_RefusesAnInvalidChainPosition(t *testing.T) {
	for _, placements := range [][]auth.AuthenticatorPlacement{
		{{Authenticator: &stubAuthenticator{}}},
		{{Position: auth.ChainReplaceMTLS + 1, Authenticator: &stubAuthenticator{}}},
	} {
		_, err := BuildAuthMiddleware(AuthMiddlewareConfig{
			Enabled:             true,
			RoleService:         adminRoleService(),
			UserService:         &fakeUserService{},
			ExtraAuthenticators: placements,
		})
		var target *auth.InvalidChainPositionError
		require.ErrorAs(t, err, &target)
	}
}

func TestBuildAuthMiddleware_RefusesReplacingMTLSWithNothing(t *testing.T) {
	_, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{},
		ExtraAuthenticators: []auth.AuthenticatorPlacement{{Position: auth.ChainReplaceMTLS}},
	})
	var target *auth.EmptyAuthenticatorChainError
	require.ErrorAs(t, err, &target)
}

// TestBuildAuthMiddleware_PeerCredentialsLastInChain: the peer-credential
// authenticator runs after mTLS and after every embedder placement, so an
// explicit credential wins over the socket's ambient identity.
func TestBuildAuthMiddleware_PeerCredentialsLastInChain(t *testing.T) {
	uid := uint32(1000)
	stub := &stubAuthenticator{token: "Bearer good"}
	// The mapped account exists: a peer credential whose account does not
	// resolve is refused (auth.ErrPeerCredentialAccountMissing).
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{u: &user.User{ID: 11, TenantID: tenantid.System, Username: "pc-user", Lifecycle: user.LifecycleActive}},
		MTLSBindingPolicy:   auth.MTLSBindingEnrolled,
		ExtraAuthenticators: afterMTLS(stub),
		PeerCredentials:     []auth.PeerCredentialMapping{{UID: &uid, TenantID: tenantid.System, Username: "pc-user"}},
	})
	require.NoError(t, err)
	cred := auth.PeerCredential{UID: uid, GID: 1000, PID: 1}

	_, id, err := mw.Authenticate(auth.WithPeerCredential(chainPeerContext(t, ""), cred))
	require.NoError(t, err)
	assert.Equal(t, chainCertSubject, id.Subject, "a certificate must win over the peer credential")

	_, id, err = mw.Authenticate(auth.WithPeerCredential(auth.WithHTTPAuthorization(context.Background(), "Bearer good"), cred))
	require.NoError(t, err)
	assert.Equal(t, "bearer-subject", id.Subject, "an embedder bearer must win over the peer credential")

	_, id, err = mw.Authenticate(auth.WithPeerCredential(context.Background(), cred))
	require.NoError(t, err)
	assert.Equal(t, "1000", id.Metadata["peer_uid"], "the peer credential alone must authenticate")
}

// revocationFailingAuthenticator fails every request with a revocation-lookup
// failure, the error the mTLS authenticator returns when the revocation store
// cannot be read (retryable, 503 / UNAVAILABLE).
type revocationFailingAuthenticator struct{}

func (revocationFailingAuthenticator) Name() string { return "revocation-failing" }

func (revocationFailingAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return nil, &auth.RevocationCheckError{Err: errors.New("kv read failed")}
}

// TestBuildAuthMiddleware_PeerCredentialDeclineKeepsRevocationCheckError pins
// that the peer-credential authenticator wiring appends last declines without
// masking an earlier method's error: on a TCP request (no peer credential) a
// revocation-lookup failure ahead of it is still the answer, not
// auth.ErrUnauthenticated.
func TestBuildAuthMiddleware_PeerCredentialDeclineKeepsRevocationCheckError(t *testing.T) {
	uid := uint32(1000)
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{},
		MTLSBindingPolicy:   auth.MTLSBindingEnrolled,
		ExtraAuthenticators: afterMTLS(revocationFailingAuthenticator{}),
		PeerCredentials:     []auth.PeerCredentialMapping{{UID: &uid, TenantID: tenantid.System, Username: "pc-user"}},
	})
	require.NoError(t, err)

	_, _, err = mw.Authenticate(context.Background())
	var rce *auth.RevocationCheckError
	require.ErrorAs(t, err, &rce, "the peer-credential decline must not replace the revocation-lookup failure")
}

// TestBuildAuthMiddleware_NoPeerCredentialsNoAuthenticator: with no mapping
// configured a peer credential authenticates nobody.
func TestBuildAuthMiddleware_NoPeerCredentialsNoAuthenticator(t *testing.T) {
	mw := buildPositioned(t, auth.ChainAfterMTLS, &stubAuthenticator{token: "Bearer good"})
	_, _, err := mw.Authenticate(auth.WithPeerCredential(context.Background(), auth.PeerCredential{UID: 1000}))
	require.Error(t, err)
}
