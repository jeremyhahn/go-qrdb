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

package node_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"maps"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	delegatingTenant = "dctx-a"
	otherTenant      = "dctx-b"
)

// loginCA is a tenant's login CA (Key B): the CA a tenant's delegations are
// issued under.
type loginCA struct {
	cert   *x509.Certificate
	signer crypto.Signer
}

// loginCAs is the cakey.ProviderFactory handing each tenant its own login CA.
type loginCAs map[string]*loginCA

var _ cakey.ProviderFactory = loginCAs(nil)

func (k loginCAs) ProviderForTenant(tenantID string) (cakey.Provider, error) {
	ca, ok := k[tenantID]
	if !ok {
		return nil, &cakey.NotInitializedError{}
	}
	return cakey.Adapter{
		SignerFunc:      func(context.Context) (crypto.Signer, error) { return ca.signer, nil },
		CertificateFunc: func(context.Context) (*x509.Certificate, error) { return ca.cert, nil },
	}, nil
}

func (k loginCAs) TenantIDs(context.Context) ([]string, error) {
	return slices.Sorted(maps.Keys(k)), nil
}

func newLoginCA(t *testing.T, cn string) *loginCA {
	t.Helper()
	key, _, err := certtest.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA %s: %v", cn, err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA %s: %v", cn, err)
	}
	return &loginCA{cert: c, signer: key}
}

// sign issues a delegation for g under the login CA, valid from notBefore
// for life: the shape a tenant's own issuance produces, for the cases the
// issuing service refuses to produce (another principal, a lapsed lifetime).
func (ca *loginCA) sign(t *testing.T, g delegation.Grant, notBefore time.Time, life time.Duration) *x509.Certificate {
	t.Helper()
	ext, err := g.Extension()
	if err != nil {
		t.Fatalf("delegation extension: %v", err)
	}
	key, _, err := certtest.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	// The tenant is named twice and the two must agree: the Subject OU and
	// the service SPIFFE ID SAN under the trust domain the node pins.
	uris, err := auth.TenantPrincipalURIs(qrdbconfig.DefaultConfig().Auth.EffectiveClusterTrustDomain(), g.Tenant, spiffe.KindService, g.Principal)
	if err != nil {
		t.Fatalf("delegation SPIFFE ID: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:    serial,
		Subject:         pkix.Name{CommonName: auth.ServicePrincipalSubjectPrefix + g.Principal, OrganizationalUnit: []string{g.Tenant}},
		URIs:            uris,
		NotBefore:       notBefore,
		NotAfter:        notBefore.Add(life),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{ext},
	}, ca.cert, &key.PublicKey, ca.signer)
	if err != nil {
		t.Fatalf("sign delegation: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse delegation: %v", err)
	}
	return c
}

// newDelegatingNode starts a standalone node accepting delegations for
// principal, with two onboarded barrier tenants, each holding its own login
// CA.
func newDelegatingNode(t *testing.T, principal string, opts ...node.NodeOption) (*node.Node, loginCAs) {
	t.Helper()
	n, cas, _ := newDelegatingNodeWithTrail(t, principal, opts...)
	return n, cas
}

// newDelegatingNodeWithTrail is newDelegatingNode that also returns the
// node's audit trail, for tests that assert where a delegated call is
// recorded.
func newDelegatingNodeWithTrail(t *testing.T, principal string, opts ...node.NodeOption) (*node.Node, loginCAs, audit.AuditLogger) {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	// A revocation is read on the very next use, not after a cache window.
	cfg.Auth.RevocationCacheTTL = "0s"

	// Every use of a delegation is recorded in the delegating tenant's trail.
	trail, err := audit.OpenTrailIn(cfg.Raft.DataDir, audit.DefaultTrailFile)
	if err != nil {
		t.Fatalf("OpenTrailIn: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	cas := loginCAs{
		delegatingTenant: newLoginCA(t, delegatingTenant+"-login-ca"),
		otherTenant:      newLoginCA(t, otherTenant+"-login-ca"),
	}
	// The SO PIN store tenant create commits each tenant SO's PIN to, as
	// cmd/qrdbd wires it: strict separation of duties (forced under FIPS
	// 140-3) refuses a tenant without an SO.
	soPINs, err := auth.NewSOAuthenticator(kvstore.NewMemoryKVStore(), audit.NewRingBuffer(64), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("SO PIN store: %v", err)
	}
	n, err := node.NewStandaloneNode(context.Background(), cfg,
		[]server.ServerOption{server.WithAuditLogger(trail), server.WithSOAuthenticator(soPINs)},
		append([]node.NodeOption{node.WithServicePrincipal(principal),
			node.WithCertKeyProviderFactory(cas),
			node.WithTenantIsolation(),
			node.WithDefaultTenantEntitlements(&quota.Entitlements{
				AllowedIsolationModes:     []string{quota.IsolationModeBarrier},
				AllowSystemManagedBarrier: true,
				AllowVolumeStorage:        true,
				AllowedVolumePools:        []string{"default"},
			})}, opts...)...,
	)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	pools, ok := n.BarrierRegistry().VolumeService()
	if !ok {
		t.Fatal("the node attached no volume catalog")
	}
	if _, err := pools.CreatePool(context.Background(), volume.CreatePoolRequest{Name: "default"}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	for _, id := range []string{delegatingTenant, otherTenant} {
		_, csrPEM, err := certtest.NewCSR(id + "-admin")
		if err != nil {
			t.Fatalf("NewCSR: %v", err)
		}
		_, soCSRPEM, err := certtest.NewCSR(id + "-so")
		if err != nil {
			t.Fatalf("NewCSR(SO): %v", err)
		}
		if _, err := n.Services().TenantOnboarding.OnboardTenant(context.Background(), &service.OnboardTenantRequest{
			ID: id, Name: id, IsolationType: tenant.IsolationBarrier, BarrierAutoUnseal: true,
			AdminUsername: id + "-admin", AdminCSRPEM: csrPEM,
			SOUsername: id + "-so", SOCSRPEM: soCSRPEM, SOPIN: []byte(id + "-so-pin-1234"),
		}); err != nil {
			t.Fatalf("OnboardTenant(%s): %v", id, err)
		}
	}
	return n, cas, trail
}

// issueDelegation has the delegating tenant issue a delegation to principal
// through the node's own issuing service, as `qrdb ca delegate` does.
func issueDelegation(t *testing.T, n *node.Node, principal string, scope ...permission.Permission) (*x509.Certificate, string) {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR(principal)
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	strs := make([]string, len(scope))
	for i, p := range scope {
		strs[i] = string(p)
	}
	issued, err := n.Services().CertEnrollment.IssueDelegation(tenant.WithTenantID(context.Background(), delegatingTenant), &service.IssueDelegationRequest{
		Principal: principal, Scope: strs, CSRPEM: csrPEM, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	block, _ := pem.Decode(issued.CertPEM)
	if block == nil {
		t.Fatal("IssueDelegation returned no PEM certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse delegation: %v", err)
	}
	return c, issued.Serial
}

// TestDelegatedContext_ActsInTheDelegatingTenantOnly proves the context
// DelegatedContext returns for a delegation the tenant issued lists roles and
// creates users in that tenant through the embedded client, and is refused
// in another tenant and in the system tenant. The write is user creation, an
// Admin duty: role creation is the tenant SO's own act under strict
// separation of duties (forced under FIPS 140-3), which no delegation holds.
func TestDelegatedContext_ActsInTheDelegatingTenantOnly(t *testing.T) {
	principal := uniquePrincipal("dctx")
	permission.RegisterRoleGrant(principal, permission.RolesList, permission.UsersCreate)
	n, _ := newDelegatingNode(t, principal)
	leaf, _ := issueDelegation(t, n, principal, permission.RolesList, permission.UsersCreate)

	ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("DelegatedContext: %v", err)
	}
	id := auth.IdentityFromContext(ctx)
	if g, ok := auth.DelegationOf(id); !ok || g.Principal != principal || id.TenantID != delegatingTenant {
		t.Fatalf("context binds %+v; want %s delegated by %s", id, principal, delegatingTenant)
	}

	roles := n.Client().Roles()
	if _, err := roles.ListRoles(ctx, &transport.ListRolesRequest{TenantID: delegatingTenant}); err != nil {
		t.Fatalf("ListRoles in the delegating tenant: %v", err)
	}
	users := n.Client().Users()
	created, err := users.CreateUser(ctx, &transport.CreateUserRequest{
		Username: "delegated-user", WithoutEmailLoginKey: true, TenantID: delegatingTenant,
	})
	if err != nil {
		t.Fatalf("CreateUser in the delegating tenant: %v", err)
	}
	if created.Username != "delegated-user" {
		t.Fatalf("CreateUser = %+v", created)
	}

	for _, other := range []string{otherTenant, tenantid.System} {
		if _, err := roles.ListRoles(ctx, &transport.ListRolesRequest{TenantID: other}); err == nil {
			t.Fatalf("ListRoles in %s admitted a delegation from %s", other, delegatingTenant)
		}
		if _, err := users.CreateUser(ctx, &transport.CreateUserRequest{
			Username: "escaped", WithoutEmailLoginKey: true, TenantID: other,
		}); err == nil {
			t.Fatalf("CreateUser in %s admitted a delegation from %s", other, delegatingTenant)
		}
	}
}

// TestDelegatedContext_Refused proves each delegation the node must not honour
// is refused with its typed error and no context: an expired one, a revoked
// one, one naming another principal, one another tenant's login CA signed for
// this tenant, and any at all on a node with no service principal.
func TestDelegatedContext_Refused(t *testing.T) {
	principal := uniquePrincipal("dctx-refused")
	permission.RegisterRoleGrant(principal, permission.RolesList)
	n, cas := newDelegatingNode(t, principal)
	grant := delegation.Grant{Tenant: delegatingTenant, Principal: principal, Scope: []string{string(permission.RolesList)}}

	revoked, serial := issueDelegation(t, n, principal, permission.RolesList)
	if _, err := n.Services().CertProv.RevokeCert(tenant.WithTenantID(context.Background(), delegatingTenant), serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	foreign := grant
	foreign.Principal = uniquePrincipal("dctx-foreign")

	isExpired := func(err error) bool {
		var invalid x509.CertificateInvalidError
		return errors.As(err, &invalid) && invalid.Reason == x509.Expired
	}
	for name, tc := range map[string]struct {
		leaf  *x509.Certificate
		check func(error) bool
	}{
		"expired": {leaf: cas[delegatingTenant].sign(t, grant, time.Now().Add(-2*time.Hour), time.Hour), check: isExpired},
		"revoked": {leaf: revoked, check: func(err error) bool { return errors.Is(err, auth.ErrCertificateRevoked) }},
		"another principal": {leaf: cas[delegatingTenant].sign(t, foreign, time.Now().Add(-time.Minute), time.Hour),
			check: func(err error) bool { return errors.Is(err, auth.ErrForeignDelegationPrincipal) }},
		"another tenant's login CA": {leaf: cas[otherTenant].sign(t, grant, time.Now().Add(-time.Minute), time.Hour),
			check: func(err error) bool {
				var refused *auth.DelegationRefusedError
				var untrusted *clienttrust.IssuerNotTrustedError
				return errors.As(err, &refused) && errors.As(err, &untrusted) && untrusted.TenantID == delegatingTenant
			}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{tc.leaf})
			if ctx != nil || !tc.check(err) {
				t.Fatalf("DelegatedContext = %v, %v (%T); want the typed refusal", ctx, err, err)
			}
		})
	}
}

// TestDelegatedContext_RevokedAfterCreationIsRefused proves a context is not a
// credential cache: a delegation revoked after DelegatedContext bound it is
// refused on the very next embedded request with the typed error the mTLS
// path returns, auth.ErrCertificateRevoked.
func TestDelegatedContext_RevokedAfterCreationIsRefused(t *testing.T) {
	principal := uniquePrincipal("dctx-revoked-later")
	permission.RegisterRoleGrant(principal, permission.RolesList)
	n, _ := newDelegatingNode(t, principal)
	leaf, serial := issueDelegation(t, n, principal, permission.RolesList)

	ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("DelegatedContext: %v", err)
	}
	roles := n.Client().Roles()
	list := &transport.ListRolesRequest{TenantID: delegatingTenant}
	if _, err := roles.ListRoles(ctx, list); err != nil {
		t.Fatalf("ListRoles before the revocation: %v", err)
	}
	if _, err := n.Services().CertProv.RevokeCert(tenant.WithTenantID(context.Background(), delegatingTenant), serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if _, err := roles.ListRoles(ctx, list); !errors.Is(err, auth.ErrCertificateRevoked) {
		t.Fatalf("ListRoles after the revocation = %v (%T); want auth.ErrCertificateRevoked", err, err)
	}
}

// TestDelegatedContext_ExpiredAfterCreationIsRefused drives the node's clock
// (WithClock) past the delegation's NotAfter after the context was bound: the
// next embedded request is refused with the typed lifetime error the mTLS path
// returns, a *auth.DelegationRefusedError over x509.Expired.
func TestDelegatedContext_ExpiredAfterCreationIsRefused(t *testing.T) {
	principal := uniquePrincipal("dctx-expired-later")
	permission.RegisterRoleGrant(principal, permission.RolesList)
	clk := clocktest.NewAt(time.Now())
	n, _ := newDelegatingNode(t, principal, node.WithClock(clk))
	leaf, _ := issueDelegation(t, n, principal, permission.RolesList)
	// The provider stamps NotBefore from its own clock; bring the node's to
	// it so the certificate is inside its lifetime when the context is bound.
	if lag := leaf.NotBefore.Sub(clk.Now()); lag > 0 {
		clk.Advance(lag)
	}

	ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("DelegatedContext: %v", err)
	}
	roles := n.Client().Roles()
	list := &transport.ListRolesRequest{TenantID: delegatingTenant}
	if _, err := roles.ListRoles(ctx, list); err != nil {
		t.Fatalf("ListRoles inside the lifetime: %v", err)
	}
	clk.Advance(leaf.NotAfter.Sub(clk.Now()) + time.Second)

	_, err = roles.ListRoles(ctx, list)
	var refused *auth.DelegationRefusedError
	var invalid x509.CertificateInvalidError
	if !errors.As(err, &refused) || !errors.As(err, &invalid) || invalid.Reason != x509.Expired {
		t.Fatalf("ListRoles past NotAfter = %v (%T); want a *auth.DelegationRefusedError over x509.Expired", err, err)
	}
}

// TestDelegatedContext_NoServicePrincipal proves a node, or a zero Node, with
// no service principal configured refuses every delegation with
// ErrNoServicePrincipal rather than acting in any tenant.
func TestDelegatedContext_NoServicePrincipal(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	leaf := newLoginCA(t, "unused-login-ca").sign(t, delegation.Grant{Tenant: delegatingTenant, Principal: "billing", Scope: []string{string(permission.RolesList)}},
		time.Now().Add(-time.Minute), time.Hour)
	for name, nd := range map[string]*node.Node{"a started node": n, "a zero node": {}} {
		t.Run(name, func(t *testing.T) {
			if ctx, err := nd.DelegatedContext(context.Background(), []*x509.Certificate{leaf}); ctx != nil || !errors.Is(err, node.ErrNoServicePrincipal) {
				t.Fatalf("DelegatedContext = %v, %v; want ErrNoServicePrincipal", ctx, err)
			}
		})
	}
}

// consumerScope is the grant an embedder needs to run its four server-owned
// operations inside a tenant (#1187): role and effective-permission reads
// for one account, role-binding writes for federated login, and the
// tenant barrier's seal and officer list.
var consumerScope = []permission.Permission{
	permission.RolesRead, permission.UsersRead, permission.PermissionsResolve,
	permission.RolesAssign, permission.RolesUnassign,
	permission.TenantBarrierOfficerList, permission.TenantBarrierSeal,
}

// TestMintDelegation_ConsumerOperations runs the four embedder operations
// under a delegation minted from tenant A's standing policy: each succeeds
// in A and is audited there, each is refused in B and in the system tenant,
// no policy means no mint, and deleting the policy and revoking the live
// serial refuses the next call.
// otherTenantReader returns a context acting under a users:read delegation
// tenant B grants the same principal, so a test can name B's accounts
// without stepping outside the delegation model.
func otherTenantReader(t *testing.T, n *node.Node, principal string) context.Context {
	t.Helper()
	if _, err := n.Services().CertEnrollment.PutDelegationPolicy(tenant.WithTenantID(context.Background(), otherTenant),
		&service.DelegationPolicyRequest{Principal: principal, Scope: []string{string(permission.UsersRead)}, TTL: time.Hour}); err != nil {
		t.Fatalf("PutDelegationPolicy in %s: %v", otherTenant, err)
	}
	return mintedContext(t, n, otherTenant, principal)
}

// mintedContext mints a delegation from tenantID's standing policy for
// principal and returns a context acting under it.
func mintedContext(t *testing.T, n *node.Node, tenantID, principal string) context.Context {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR(principal)
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	issued, err := n.MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: tenantID, CSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("MintDelegation in %s: %v", tenantID, err)
	}
	block, _ := pem.Decode(issued.CertPEM)
	if block == nil {
		t.Fatal("MintDelegation returned no PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the minted delegation: %v", err)
	}
	ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("DelegatedContext in %s: %v", tenantID, err)
	}
	return ctx
}

func TestMintDelegation_ConsumerOperations(t *testing.T) {
	principal := uniquePrincipal("consumer")
	permission.RegisterRoleGrant(principal, consumerScope...)
	n, _, trail := newDelegatingNodeWithTrail(t, principal)
	adminCtx := tenant.WithTenantID(context.Background(), delegatingTenant)

	_, csrPEM, err := certtest.NewCSR(principal)
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	var notFound *service.DelegationPolicyNotFoundError
	if _, err := n.MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant, CSRPEM: csrPEM}); !errors.As(err, &notFound) {
		t.Fatalf("MintDelegation with no policy = %v; want *service.DelegationPolicyNotFoundError", err)
	}

	scope := make([]string, 0, len(consumerScope))
	for _, p := range consumerScope {
		scope = append(scope, string(p))
	}
	if _, err := n.Services().CertEnrollment.PutDelegationPolicy(adminCtx,
		&service.DelegationPolicyRequest{Principal: principal, Scope: scope, TTL: time.Hour}); err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	issued, err := n.MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant, CSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("MintDelegation: %v", err)
	}
	block, _ := pem.Decode(issued.CertPEM)
	if block == nil {
		t.Fatal("MintDelegation returned no PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the minted delegation: %v", err)
	}
	ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("DelegatedContext: %v", err)
	}
	sealCtx := mintedContext(t, n, delegatingTenant, principal)

	roles, users := n.Client().Roles(), n.Client().Users()
	account := delegatingTenant + "-admin"

	// 1. Role and effective-permission reads for one account.
	user, err := users.GetUserByUsername(ctx, delegatingTenant, account)
	if err != nil {
		t.Fatalf("GetUserByUsername in %s: %v", delegatingTenant, err)
	}
	userRole, err := roles.GetRoleByName(ctx, &transport.GetRoleByNameRequest{TenantID: delegatingTenant, Name: role.RoleNameUser})
	if err != nil {
		t.Fatalf("GetRoleByName(%s) in %s: %v", role.RoleNameUser, delegatingTenant, err)
	}
	resolved, err := roles.ResolvePermissions(ctx, &transport.ResolvePermissionsRequest{RoleIDs: []string{userRole.ID}})
	if err != nil || len(resolved.Permissions) == 0 {
		t.Fatalf("ResolvePermissions(%s) = %+v, %v; want the role's permissions", role.RoleNameUser, resolved, err)
	}

	// 2. Role-binding writes for federated login.
	auditor, err := roles.GetRoleByName(ctx, &transport.GetRoleByNameRequest{TenantID: delegatingTenant, Name: role.RoleNameAuditor})
	if err != nil {
		t.Fatalf("GetRoleByName(%s): %v", role.RoleNameAuditor, err)
	}
	if err := roles.AssignRole(ctx, user.ID, auditor.ID); err != nil {
		t.Fatalf("AssignRole in %s: %v", delegatingTenant, err)
	}
	if err := roles.UnassignRole(ctx, user.ID, auditor.ID); err != nil {
		t.Fatalf("UnassignRole in %s: %v", delegatingTenant, err)
	}

	// 3. The tenant barrier's officer list and seal.
	if _, err := n.Client().Tenants().TenantBarrierOfficerList(ctx, delegatingTenant); err != nil {
		t.Fatalf("TenantBarrierOfficerList in %s: %v", delegatingTenant, err)
	}

	// Every operation is refused outside the delegating tenant.
	for _, other := range []string{otherTenant, tenantid.System} {
		if _, err := users.GetUserByUsername(ctx, other, other+"-admin"); err == nil {
			t.Errorf("GetUserByUsername in %s admitted a delegation from %s", other, delegatingTenant)
		}
		if _, err := roles.GetRoleByName(ctx, &transport.GetRoleByNameRequest{TenantID: other, Name: role.RoleNameAuditor}); err == nil {
			t.Errorf("GetRoleByName in %s admitted a delegation from %s", other, delegatingTenant)
		}
		if _, err := n.Client().Tenants().TenantBarrierOfficerList(ctx, other); err == nil {
			t.Errorf("TenantBarrierOfficerList in %s admitted a delegation from %s", other, delegatingTenant)
		}
		if err := n.Client().Tenants().TenantBarrierSeal(ctx, &transport.TenantBarrierSealRequest{TenantID: other}); err == nil {
			t.Errorf("TenantBarrierSeal in %s admitted a delegation from %s", other, delegatingTenant)
		}
	}
	otherUser, err := users.GetUserByUsername(otherTenantReader(t, n, principal), otherTenant, otherTenant+"-admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(%s) under its own delegation: %v", otherTenant, err)
	}
	if err := roles.AssignRole(ctx, otherUser.ID, auditor.ID); err == nil {
		t.Errorf("AssignRole on a %s account admitted a delegation from %s", otherTenant, delegatingTenant)
	}
	if err := roles.UnassignRole(ctx, otherUser.ID, otherUser.RoleIDs[0]); err == nil {
		t.Errorf("UnassignRole on a %s account admitted a delegation from %s", otherTenant, delegatingTenant)
	}

	// Deleting the policy stops any further mint; the serial already out
	// stays live until it is revoked.
	if err := n.Services().CertEnrollment.DeleteDelegationPolicy(adminCtx, delegatingTenant, principal); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if _, err := n.MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant, CSRPEM: csrPEM}); !errors.As(err, &notFound) {
		t.Fatalf("MintDelegation after the policy delete = %v; want *service.DelegationPolicyNotFoundError", err)
	}

	// Revoking the live serial refuses the next call.
	if _, err := n.Services().CertProv.RevokeCert(adminCtx, issued.Serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
	if _, err := n.Client().Tenants().TenantBarrierOfficerList(ctx, delegatingTenant); !errors.Is(err, auth.ErrCertificateRevoked) {
		t.Fatalf("TenantBarrierOfficerList after the revocation = %v; want auth.ErrCertificateRevoked", err)
	}

	// A second delegation minted while the policy stood is still live:
	// the seal runs under it.
	if err := n.Client().Tenants().TenantBarrierSeal(sealCtx, &transport.TenantBarrierSealRequest{TenantID: delegatingTenant}); err != nil {
		t.Fatalf("TenantBarrierSeal in %s: %v", delegatingTenant, err)
	}

	// Each allowed call is recorded under the delegating tenant.
	for _, p := range consumerScope {
		page, err := trail.Query(context.Background(), audit.Filter{TenantID: delegatingTenant, Action: "auth.authorize", Resource: string(p), Outcome: "success"})
		if err != nil {
			t.Fatalf("Query(%s): %v", p, err)
		}
		if len(page.Events) == 0 {
			t.Errorf("no %s authorization recorded under %s", p, delegatingTenant)
		}
	}
}
