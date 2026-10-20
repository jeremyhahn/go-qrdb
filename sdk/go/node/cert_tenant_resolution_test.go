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
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
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
	bundle, err := certtest.Generate(tctx, prov, &certtest.Request{Username: username, TenantID: tenantID})
	require.NoError(t, err, "certtest.Generate")
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
	tenantID, err := sdk.TenantFromClientCert(c, testTrustDomain)
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

	tenantID, err := sdk.TenantFromClientCert(c, testTrustDomain)
	require.NoError(t, err)
	require.Equal(t, "acme", tenantID, "the provider names the tenant in the certificate")
	require.Equal(t, []string{"acme"}, c.Subject.OrganizationalUnit)
	require.Len(t, c.URIs, 1, "the provider names the tenant in exactly one SPIFFE ID SAN")
	require.Equal(t, "spiffe://acme/user/alice", c.URIs[0].String(),
		"the SAN agrees with the OU under the node's trust domain")

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

// testTrustDomain is the pinned SPIFFE trust domain the composing host passes:
// the node's own, which its certificate provider names in every tenant SAN. The
// fixture configures none, so it is the default cluster trust domain. A
// composer always pins; "" accepts any trust domain and is for tests only.
const testTrustDomain = qrdbconfig.DefaultClusterTrustDomain

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
		uri("spiffe://acme/user/a"),
		uri("spiffe://globex/user/a"),
	}}
	_, err := sdk.TenantFromClientCert(ambiguous, testTrustDomain)
	var amb *sdk.SpiffeAmbiguousTenantError
	var cte *sdk.ClientCertificateTenantError
	require.True(t, errors.As(err, &amb), "err = %v", err)
	require.True(t, errors.As(err, &cte), "err = %v", err)
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate))

	foreign := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"acme"}},
		URIs:    []*url.URL{uri("spiffe://evil.example/user/a")},
	}
	_, err = sdk.TenantFromClientCert(foreign, testTrustDomain)
	var mismatch *sdk.ClientCertificateTenantNameMismatchError
	require.True(t, errors.As(err, &mismatch), "err = %v", err)
	require.Equal(t, "evil.example", mismatch.SPIFFETenant)
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate))

	orphan := &x509.Certificate{URIs: []*url.URL{uri("spiffe://evil.example/user/a")}}
	_, err = sdk.TenantFromClientCert(orphan, testTrustDomain)
	require.True(t, errors.Is(err, sdk.ErrSpiffeTrustDomainMismatch), "err = %v", err)
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate))
}

// The SDK's OU refusals are pkg/auth's: an alias, not a copy, so errors.Is and
// errors.As agree on both sides of the boundary.
var (
	_ error                         = sdk.ErrNoClientCertificateTenantOU
	_ *auth.TenantNameMismatchError = (*sdk.ClientCertificateTenantNameMismatchError)(nil)
)

// TestTenantFromClientCert_OUAndSANMustAgreeThroughSDK (go-qrdb#777): the
// tenant is named only when the Subject OU and the SPIFFE ID SAN are both
// present and agree; each refusal carries its typed cause through the SDK
// aliases and matches ErrInvalidClientCertificate.
func TestTenantFromClientCert_OUAndSANMustAgreeThroughSDK(t *testing.T) {
	require.Same(t, auth.ErrNoTenantOU, sdk.ErrNoClientCertificateTenantOU,
		"the SDK sentinel is pkg/auth's, not a copy")
	san, err := url.Parse("spiffe://acme/user/alice")
	require.NoError(t, err)

	ouOnly := &x509.Certificate{Subject: pkix.Name{OrganizationalUnit: []string{"acme"}}}
	_, err = sdk.TenantFromClientCert(ouOnly, testTrustDomain)
	require.ErrorIs(t, err, sdk.ErrSpiffeNoTenantID, "an OU alone names no tenant")
	require.ErrorIs(t, err, sdk.ErrInvalidClientCertificate)

	sanOnly := &x509.Certificate{URIs: []*url.URL{san}}
	_, err = sdk.TenantFromClientCert(sanOnly, testTrustDomain)
	require.ErrorIs(t, err, sdk.ErrSpiffeTrustDomainMismatch,
		"a SPIFFE ID alone names no tenant: with no OU it must be under the cluster trust domain")
	require.ErrorIs(t, err, sdk.ErrInvalidClientCertificate)

	clusterSAN, err := url.Parse("spiffe://" + testTrustDomain + "/svc/backup")
	require.NoError(t, err)
	_, err = sdk.TenantFromClientCert(&x509.Certificate{URIs: []*url.URL{clusterSAN}}, testTrustDomain)
	require.ErrorIs(t, err, sdk.ErrNoClientCertificateTenantOU, "a cluster principal names no tenant")
	require.ErrorIs(t, err, sdk.ErrInvalidClientCertificate)

	clusterOU := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{testTrustDomain}},
		URIs:    []*url.URL{clusterSAN},
	}
	_, err = sdk.TenantFromClientCert(clusterOU, testTrustDomain)
	require.ErrorIs(t, err, sdk.ErrClientCertificateTenantIsClusterTrustDomain,
		"no tenant may be the cluster trust domain")

	disagree := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"globex"}},
		URIs:    []*url.URL{san},
	}
	_, err = sdk.TenantFromClientCert(disagree, testTrustDomain)
	var mm *sdk.ClientCertificateTenantNameMismatchError
	require.ErrorAs(t, err, &mm, "an OU naming another tenant is refused")
	require.Equal(t, "globex", mm.OU)
	require.Equal(t, "acme", mm.SPIFFETenant)
	require.ErrorIs(t, err, sdk.ErrInvalidClientCertificate)

	agree := &x509.Certificate{
		Subject: pkix.Name{OrganizationalUnit: []string{"acme"}},
		URIs:    []*url.URL{san},
	}
	tenantID, err := sdk.TenantFromClientCert(agree, testTrustDomain)
	require.NoError(t, err)
	require.Equal(t, "acme", tenantID)
}

func TestTenantFromClientCert_NilCertificateFailsClosed(t *testing.T) {
	_, err := sdk.TenantFromClientCert(nil, "")
	require.True(t, errors.Is(err, sdk.ErrInvalidClientCertificate), "err = %v", err)
}
