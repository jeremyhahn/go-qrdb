// Copyright 2026 Jeremy Hahn
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

package node

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// go-qrdb#575: a composing host resolves a verified certificate's user before
// any tenant is bound, with qrdb's own certificate-to-tenant rule
//
// The certificate is minted by the node's REAL certificate provider. The host
// applies sdk.TenantFromClientCert, binds the tenant exactly as the
// middleware does, and looks the thumbprint up within that tenant through the
// SDK client. No cross-tenant index is involved: the same lookup made from a
// context bound to no tenant does not find a tenant user.
// ---------------------------------------------------------------------------

// mintBoundUser creates username in tenantID, mints its client certificate
// with the node's provider, binds the certificate's SPKI thumbprint, and
// returns the parsed certificate and the user's ID.
func mintBoundUser(ctx context.Context, t *testing.T, n *Node, tenantID, username string) (*x509.Certificate, uint64) {
	t.Helper()
	tctx := sdk.ContextWithTenant(ctx, tenantID)
	users := n.Client().Users()
	u, err := users.CreateUser(tctx, &transport.CreateUserRequest{
		Username: username, TenantID: tenantID, WithoutEmailLoginKey: true,
	})
	require.NoError(t, err, "CreateUser(%s/%s)", tenantID, username)

	prov := n.Services().CertProv
	require.NotNil(t, prov, "a sealed-capable node carries the certificate provider")
	bundle, err := prov.GenerateClientCert(tctx, &cert.GenerateRequest{Username: username, TenantID: tenantID})
	require.NoError(t, err, "GenerateClientCert")
	block, _ := pem.Decode(bundle.CertPEM)
	require.NotNil(t, block, "provider returned no PEM certificate")
	c, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	_, err = users.AddCertBinding(tctx, u.ID, sdk.ComputeSPKIThumbprint(c), 0)
	require.NoError(t, err, "AddCertBinding")
	return c, u.ID
}

// resolve is the composing host's pre-authentication step.
func resolve(ctx context.Context, n *Node, c *x509.Certificate) (*transport.User, error) {
	tenantID, _, err := sdk.TenantFromClientCert(c, testTrustDomain)
	if err != nil {
		return nil, err
	}
	return n.Client().Users().GetUserByCertThumbprint(
		sdk.ContextWithTenant(ctx, sdk.NormalizeTenantID(tenantID)), sdk.ComputeSPKIThumbprint(c))
}

func TestTenantFromClientCert_ResolvesProviderCertWithinItsTenant(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	c, id := mintBoundUser(ctx, t, n, "acme", "alice")

	tenantID, fromSPIFFE, err := sdk.TenantFromClientCert(c, testTrustDomain)
	require.NoError(t, err)
	require.Equal(t, "acme", tenantID, "the provider names the tenant in the certificate")
	require.False(t, fromSPIFFE)

	u, err := resolve(ctx, n, c)
	require.NoError(t, err)
	require.Equal(t, id, u.ID)
	require.Equal(t, "acme", u.TenantID)

	// No tenant bound, no cross-tenant search: the thumbprint alone does not
	// find a tenant user. The rule is what makes the lookup land.
	_, err = n.Client().Users().GetUserByCertThumbprint(ctx, sdk.ComputeSPKIThumbprint(c))
	var nf *transport.UserNotFoundError
	require.True(t, errors.As(err, &nf), "a tenant user must not resolve from the system tenant: %v", err)
}

func TestTenantFromClientCert_SystemTenantCertStillResolves(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	c, id := mintBoundUser(ctx, t, n, tenantid.System, "operator")

	u, err := resolve(ctx, n, c)
	require.NoError(t, err)
	require.Equal(t, id, u.ID)
}

func TestTenantFromClientCert_UnboundOrRemovedCertDoesNotResolve(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	c, id := mintBoundUser(ctx, t, n, "acme", "bob")

	_, err := n.Client().Users().RemoveCertBinding(sdk.ContextWithTenant(ctx, "acme"), id, sdk.ComputeSPKIThumbprint(c))
	require.NoError(t, err, "RemoveCertBinding")
	_, err = resolve(ctx, n, c)
	var nf *transport.UserNotFoundError
	require.True(t, errors.As(err, &nf), "a removed binding must not resolve: %v", err)
}

// testTrustDomain is the pinned SPIFFE trust domain the composing host passes.
// A composer always pins; "" accepts any trust domain and is for tests only.
const testTrustDomain = "db.example.com"

// TestTenantFromClientCert_AmbiguousOrForeignSANsRefusedThroughSDK: the SDK
// surface refuses what the authenticator refuses, with the typed causes
// reachable through the SDK aliases.
func TestTenantFromClientCert_AmbiguousOrForeignSANsRefusedThroughSDK(t *testing.T) {
	uri := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return u
	}
	ambiguous := &x509.Certificate{URIs: []*url.URL{
		uri("spiffe://db.example.com/tenant/acme/user/a"),
		uri("spiffe://db.example.com/tenant/globex/user/a"),
	}}
	_, _, err := sdk.TenantFromClientCert(ambiguous, testTrustDomain)
	var amb *sdk.SpiffeAmbiguousTenantError
	var cte *sdk.ClientCertificateTenantError
	require.True(t, errors.As(err, &amb), "err = %v", err)
	require.True(t, errors.As(err, &cte), "err = %v", err)
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate))

	foreign := &x509.Certificate{URIs: []*url.URL{
		uri("spiffe://db.example.com/tenant/acme/user/a"),
		uri("spiffe://evil.example/tenant/acme/user/a"),
	}}
	_, _, err = sdk.TenantFromClientCert(foreign, testTrustDomain)
	require.True(t, errors.Is(err, sdk.ErrSpiffeTrustDomainMismatch), "err = %v", err)
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate))
}

func TestTenantFromClientCert_NilCertificateFailsClosed(t *testing.T) {
	_, _, err := sdk.TenantFromClientCert(nil, "")
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate), "err = %v", err)
}
