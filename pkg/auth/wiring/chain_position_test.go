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
