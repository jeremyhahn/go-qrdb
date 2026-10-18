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

//go:build integration

// ca_delegate_cli_test.go proves tenant-issued delegation certificates
// (`qrdb ca delegate`, cmd/qrdb/ca_delegate.go; docs/identity-rbac and
// docs/certificate-authority) on every network transport with the real `qrdb`
// binary. A node accepts a delegation only for the service principal its
// composer names and grants -- node.WithServicePrincipal over
// sdk.RegisterPermissionRoleGrant -- and qrdbd has no such composer, so the
// node under test is an in-process standalone node built the way an embedder
// builds one, serving REST, gRPC, QUIC, MCP and Unix behind mTLS. Its two
// tenants are onboarded in process; everything the feature does is driven
// through the CLI.
package integration_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const (
	// delegationPrincipal is the service principal the composed node accepts
	// delegations for. It holds kv:get and kv:put, and not kv:delete.
	delegationPrincipal = "billing"

	// delegationTenantA delegates; delegationTenantB owns the trust anchor
	// the forged delegations are signed by.
	delegationTenantA = "dlg-a"
	delegationTenantB = "dlg-b"

	// delegationDatabase is tenant A's own database, which every kv call
	// names: the node's configured database belongs to the system tenant.
	delegationDatabase = "dlg-db"

	// delegationSeed is the value every delegated read must find.
	delegationSeed = "seeded-by-tenant-admin"
)

// delegationCredential is a client certificate and its key, on disk.
type delegationCredential struct {
	certPath string
	keyPath  string
}

// startDelegationNode builds the composed node and onboards both tenants. The
// returned multiProtoNode's client identity is tenant A's first admin; the
// second value is tenant B's, and the third holds each tenant's login CA.
func startDelegationNode(t *testing.T) (*multiProtoNode, delegationCredential, delegationKeyB) {
	t.Helper()
	sdk.RegisterPermissionRoleGrant(delegationPrincipal, permission.KVGet, permission.KVPut)

	ca := generateTestCA(t)
	serverKP := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("server TLS certificate: %v", err)
	}

	sockDir, err := os.MkdirTemp("", "qrdb-dlg-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	tcp := allocateTCPPorts(t, 3)
	quic := allocatePorts(t, 1)

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort, cfg.Server.GRPCPort, cfg.MCP.Port = tcp[0], tcp[1], tcp[2]
	cfg.Server.QUICPort = quic[0]
	cfg.Unix.SocketPath = unixSocket
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = true, true, true
	cfg.Protocols.MCP, cfg.Protocols.Unix = true, true
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Path: t.TempDir()}}
	// Revocation is read on every request, so a revoked delegation is
	// refused on its very next use rather than after the cache window.
	cfg.Auth.RevocationCacheTTL = "0s"

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	// Every use of a delegation is recorded in the delegating tenant's trail,
	// and a node with no trail serves none: the trail is the embedder's to
	// open, and to close after the node.
	trail, err := audit.OpenTrailIn(cfg.Raft.DataDir, audit.DefaultTrailFile)
	if err != nil {
		t.Fatalf("OpenTrailIn: %v", err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	keyB := delegationKeyB{
		delegationTenantA: newDelegationCA(t, delegationTenantA+"-login-ca"),
		delegationTenantB: newDelegationCA(t, delegationTenantB+"-login-ca"),
	}
	n, err := node.NewStandaloneNode(context.Background(), cfg,
		[]server.ServerOption{server.WithTLS(tlsCfg), server.WithAuditLogger(trail)},
		node.WithServicePrincipal(delegationPrincipal),
		node.WithCertKeyProviderFactory(keyB),
		node.WithTenantIsolation(),
		node.WithDefaultTenantEntitlements(delegationEntitlements()),
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
	adminA := onboardDelegationTenant(t, n, delegationTenantA)
	grantDelegationDataRole(t, n, delegationTenantA)
	adminB := onboardDelegationTenant(t, n, delegationTenantB)

	return &multiProtoNode{
		cli:        buildCLIBinary(t),
		ca:         ca,
		clientCert: adminA.certPath,
		clientKey:  adminA.keyPath,
		unixSocket: unixSocket,
		restPort:   tcp[0],
		grpcPort:   tcp[1],
		mcpPort:    tcp[2],
		quicPort:   quic[0],
	}, adminB, keyB
}

// onboardDelegationTenant creates a barrier tenant with its first admin and
// returns the admin's certificate, signed by the tenant's own CA, and key.
// delegationEntitlements entitles a tenant to every isolation type on the
// fixture's default volume pool.
func delegationEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes: []string{
			quota.IsolationModeBarrier, quota.IsolationModeVolume, quota.IsolationModeRaft,
		},
		AllowDedicatedRaft:        true,
		AllowDedicatedWAL:         true,
		AllowVolumeStorage:        true,
		AllowSystemManagedBarrier: true,
		AllowedVolumePools:        []string{"default"},
	}
}

func onboardDelegationTenant(t *testing.T, n *node.Node, tenantID string) delegationCredential {
	t.Helper()
	keyPEM, csrPEM, err := certtest.NewCSR(tenantID + "-admin")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	res, err := n.Services().TenantOnboarding.OnboardTenant(context.Background(), &service.OnboardTenantRequest{
		ID:                tenantID,
		Name:              tenantID,
		IsolationType:     tenant.IsolationBarrier,
		BarrierAutoUnseal: true,
		AdminUsername:     tenantID + "-admin",
		AdminCSRPEM:       csrPEM,
	})
	if err != nil {
		t.Fatalf("OnboardTenant(%s): %v", tenantID, err)
	}
	if res.Admin.AwaitsBarrierInit {
		t.Fatalf("OnboardTenant(%s): the admin awaits barrier init; an auto-unsealed tenant's barrier serves at creation", tenantID)
	}
	awaitDelegationAdmin(t, n, tenantID)
	return writeDelegationCredential(t, res.Admin.CertPEM, keyPEM)
}

// delegationAdminDeadline bounds awaitDelegationAdmin. The completion it waits
// for retries on its own bound of up to 2 minutes; on an idle node its first
// attempt succeeds within milliseconds.
const delegationAdminDeadline = 30 * time.Second

// awaitDelegationAdmin waits for the account of tenantID's first admin. A
// system-managed tenant's admin account is completed just after the create
// answers, not inside it (docs/multi-tenancy/api.md), so until then the
// account is absent and the admin's certificate is refused. Only that absence
// is waited out; any other error fails.
func awaitDelegationAdmin(t *testing.T, n *node.Node, tenantID string) {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	var err error
	if !pollUntil(delegationAdminDeadline, 50*time.Millisecond, func() bool {
		_, err = n.Services().User.GetUserByUsername(ctx, tenantID, tenantID+"-admin")
		if err == nil {
			return true
		}
		var absent *user.UserNotFoundError
		if !errors.As(err, &absent) {
			t.Fatalf("GetUserByUsername(%s): %v", tenantID, err)
		}
		return false
	}) {
		t.Fatalf("the admin account of %s was not completed within %s: %v", tenantID, delegationAdminDeadline, err)
	}
}

// grantDelegationDataRole gives tenantID's first admin a role holding the
// data-plane permissions the test seeds and reads with. The built-in Admin
// role a tenant's first admin is onboarded with carries none: data access is
// a role the tenant grants, and the qrdbd-backed suites grant it the same
// way, through the tenant's own IAM.
func grantDelegationDataRole(t *testing.T, n *node.Node, tenantID string) {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	svc := n.Services()
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "data-owner",
		TenantID: tenantID,
		Permissions: []permission.Permission{
			permission.DatabasesCreate, permission.DatabasesList, permission.DatabasesRead,
			permission.KVPut, permission.KVGet, permission.KVDelete, permission.KVExists,
		},
	})
	if err != nil {
		t.Fatalf("CreateRole(%s): %v", tenantID, err)
	}
	admin, err := svc.User.GetUserByUsername(ctx, tenantID, tenantID+"-admin")
	if err != nil {
		t.Fatalf("GetUserByUsername(%s): %v", tenantID, err)
	}
	if _, err := svc.User.UpdateUser(ctx, &user.UpdateUserRequest{
		ID: admin.ID, RoleIDs: append(slices.Clone(admin.RoleIDs), r.ID),
	}); err != nil {
		t.Fatalf("UpdateUser(%s): %v", tenantID, err)
	}
}

// writeDelegationCredential writes certPEM and keyPEM under a new directory.
func writeDelegationCredential(t *testing.T, certPEM, keyPEM []byte) delegationCredential {
	t.Helper()
	dir := t.TempDir()
	cred := delegationCredential{certPath: filepath.Join(dir, "cert.pem"), keyPath: filepath.Join(dir, "key.pem")}
	if err := os.WriteFile(cred.certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(cred.keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return cred
}

// parseDelegationCert parses the one certificate in certPEM.
func parseDelegationCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatalf("no PEM block in %q", certPEM)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return c
}

// issueDelegationCLI runs "ca delegate --csr" over conn and returns what it
// printed as JSON, with the certificate and the CSR's key on disk.
func issueDelegationCLI(t *testing.T, cli string, conn []string, proto multiProtocol, principal, ttl string, scope ...string) (transport.IssuedDelegation, delegationCredential) {
	t.Helper()
	keyPEM, csrPEM, err := certtest.NewCSR("ignored-by-the-ca")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	csrPath := filepath.Join(t.TempDir(), "csr.pem")
	if err := os.WriteFile(csrPath, csrPEM, 0o600); err != nil {
		t.Fatalf("write CSR: %v", err)
	}
	out, err := runQRDB(t, cli, conn, "--output", "json", "ca", "delegate",
		"--principal", principal, "--scope", strings.Join(scope, ","), "--csr", csrPath, "--ttl", ttl)
	if err != nil {
		t.Fatalf("[%s] ca delegate --principal %s --scope %v: %v\noutput: %s", proto.name, principal, scope, err, out)
	}
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("[%s] ca delegate printed no JSON: %s", proto.name, out)
	}
	var issued transport.IssuedDelegation
	if err := json.Unmarshal([]byte(out[jsonStart:]), &issued); err != nil {
		t.Fatalf("[%s] decode ca delegate JSON: %v\noutput: %s", proto.name, err, out)
	}
	return issued, writeDelegationCredential(t, []byte(issued.CertPEM), keyPEM)
}

// delegationKV is the kv subcommand args against tenant A's own database.
func delegationKV(args ...string) []string {
	return append([]string{"--database", delegationDatabase, "kv"}, args...)
}

// waitForDelegationNode waits until every transport serves tenant A's admin,
// then has that admin create the tenant's database, which every kv call names.
func waitForDelegationNode(t *testing.T, node *multiProtoNode, protocols []multiProtocol) {
	t.Helper()
	for _, proto := range protocols {
		deadline := time.Now().Add(60 * time.Second)
		for {
			out, err := runCLIProto(t, node, proto, "database", "list")
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("[%s] the transport did not serve tenant %s's admin: %v\noutput: %s", proto.name, delegationTenantA, err, out)
			}
		}
	}
	if out, err := runCLIProto(t, node, protocols[0], "database", "create", delegationDatabase, "--backend", "memory"); err != nil {
		t.Fatalf("tenant %s creating database %s: %v\noutput: %s", delegationTenantA, delegationDatabase, err, out)
	}
}

// assertDelegationRefused presents cred over proto and requires the read of
// key -- which the delegation's scope covers -- to fail without the value.
func assertDelegationRefused(t *testing.T, node *multiProtoNode, proto multiProtocol, cred delegationCredential, key, what string) {
	t.Helper()
	out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, cred.certPath, cred.keyPath), delegationKV("get", key)...)
	if err == nil || strings.Contains(out, delegationSeed) {
		t.Fatalf("[%s] %s: kv get was admitted, want a refusal\noutput: %s", proto.name, what, out)
	}
	t.Logf("[%s] %s refused: %s", proto.name, what, strings.TrimSpace(out))
}

// delegationCA is a CA whose key the test holds. One serves as each tenant's
// login CA (Key B, through delegationKeyB), so the test can sign what that
// tenant's own CA would, including what the CA's issuance path refuses to.
// Another is registered as tenant B's trust anchor: the node verifies the
// chains it signs, but it is not tenant A's login CA, so a delegation it signs
// naming tenant A must be refused.
type delegationCA struct {
	cert     *x509.Certificate
	signer   crypto.Signer
	certPath string
}

// newDelegationCA creates a CA named cn and writes its certificate to disk.
func newDelegationCA(t *testing.T, cn string) *delegationCA {
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
		MaxPathLen:            0,
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
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write CA %s: %v", cn, err)
	}
	return &delegationCA{cert: c, signer: key, certPath: path}
}

// sign issues a delegation certificate carrying g, valid from a minute ago
// for life, shaped exactly as a tenant's own CA shapes one, and writes it with
// its key to disk.
func (f *delegationCA) sign(t *testing.T, g delegation.Grant, life time.Duration) delegationCredential {
	t.Helper()
	ext, err := g.Extension()
	if err != nil {
		t.Fatalf("delegation extension: %v", err)
	}
	key, keyPEM, err := certtest.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	notBefore := time.Now().Add(-time.Minute)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber:    serial,
		Subject:         pkix.Name{CommonName: auth.ServicePrincipalSubjectPrefix + g.Principal, OrganizationalUnit: []string{g.Tenant}},
		NotBefore:       notBefore,
		NotAfter:        notBefore.Add(life),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{ext},
	}, f.cert, &key.PublicKey, f.signer)
	if err != nil {
		t.Fatalf("sign delegation: %v", err)
	}
	return writeDelegationCredential(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM)
}

// delegationKeyB is the node's Key B seam (node.WithCertKeyProviderFactory):
// each tenant's login CA is a delegationCA the test holds, and a tenant it
// does not name has no Key B.
type delegationKeyB map[string]*delegationCA

var _ cakey.ProviderFactory = delegationKeyB(nil)

// ProviderForTenant returns tenantID's own CA.
func (k delegationKeyB) ProviderForTenant(tenantID string) (cakey.Provider, error) {
	ca, ok := k[tenantID]
	if !ok {
		return nil, &cakey.NotInitializedError{}
	}
	return cakey.Adapter{
		SignerFunc:      func(context.Context) (crypto.Signer, error) { return ca.signer, nil },
		CertificateFunc: func(context.Context) (*x509.Certificate, error) { return ca.cert, nil },
	}, nil
}

// TenantIDs returns the tenants k holds a CA for.
func (k delegationKeyB) TenantIDs(context.Context) ([]string, error) {
	return slices.Sorted(maps.Keys(k)), nil
}

// expiringDelegation is a short-lived delegation issued before the
// per-transport subtests run, so its lifetime runs out while they do.
type expiringDelegation struct {
	cred     delegationCredential
	notAfter time.Time
}

// expiringDelegationTTL is the lifetime of an expiringDelegation: long enough
// to be admitted on the read that follows its issue, short enough to have run
// out by the end of its transport's subtest.
const expiringDelegationTTL = "10s"

// TestCADelegate_CLI_PerProtocol proves, on every network transport, that a
// delegation certificate a tenant issues through `qrdb ca delegate` is its own
// CA's, names the principal and scope in a critical extension, and
// authenticates the service principal inside that tenant holding the scope
// intersected with the principal's own grants, reading and writing the
// tenant's own database -- and that it is refused when it names another
// principal, when another tenant's CA signed it, on another tenant's data,
// once it is revoked, once it has expired, and when the tenant's own CA
// signed it for longer than delegation.MaxLifetime. It also proves the tenant
// cannot issue one for a built-in role, beyond the maximum lifetime, or for a
// permission the node does not know.
func TestCADelegate_CLI_PerProtocol(t *testing.T) {
	node, adminB, keyB := startDelegationNode(t)
	protocols := multiProtocols
	waitForDelegationNode(t, node, protocols)

	// Tenant B registers a CA the test holds the key of as its own trust
	// anchor, through its own admin.
	forger := newDelegationCA(t, delegationTenantB+"-anchor")
	connB := tenantCLIArgsForProto(node, protocols[0], adminB.certPath, adminB.keyPath)
	if out, err := runQRDB(t, node.cli, connB, "tenant", "trust-anchors", "add",
		"--tenant-id", delegationTenantB, "--file", forger.certPath); err != nil {
		t.Fatalf("tenant B registering its trust anchor: %v\noutput: %s", err, out)
	}

	const expiringKey = "delegated/expiring"
	if out, err := runCLIProto(t, node, protocols[0], delegationKV("put", expiringKey, delegationSeed)...); err != nil {
		t.Fatalf("seed %s: %v\noutput: %s", expiringKey, err, out)
	}
	expiring := make(map[string]expiringDelegation, len(protocols))
	for _, proto := range protocols {
		// The --out form: the CLI generates the key and writes both halves.
		dir := t.TempDir()
		if out, err := runCLIProto(t, node, proto, "ca", "delegate",
			"--principal", delegationPrincipal, "--scope", "kv:get", "--ttl", expiringDelegationTTL, "--out", dir); err != nil {
			t.Fatalf("[%s] ca delegate --out: %v\noutput: %s", proto.name, err, out)
		}
		certPEM, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
		if err != nil {
			t.Fatalf("[%s] read the delegation ca delegate --out wrote: %v", proto.name, err)
		}
		e := expiringDelegation{
			cred:     delegationCredential{certPath: filepath.Join(dir, "cert.pem"), keyPath: filepath.Join(dir, "key.pem")},
			notAfter: parseDelegationCert(t, certPEM).NotAfter,
		}
		out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, e.cred.certPath, e.cred.keyPath), delegationKV("get", expiringKey)...)
		if err != nil || !strings.Contains(out, delegationSeed) {
			t.Fatalf("[%s] a delegation issued %s before it expires was not admitted (now %s, not after %s): %v\noutput: %s",
				proto.name, expiringDelegationTTL, time.Now().UTC().Format(time.RFC3339), e.notAfter.UTC().Format(time.RFC3339), err, out)
		}
		expiring[proto.name] = e
	}

	for _, proto := range protocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			admin := node.cliArgsFor(proto)
			key := "delegated/" + proto.name
			if out, err := runQRDB(t, node.cli, admin, delegationKV("put", key, delegationSeed)...); err != nil {
				t.Fatalf("[%s] seed %s: %v\noutput: %s", proto.name, key, err, out)
			}

			// Tenant A's own CA issues it: the principal and scope in a
			// critical extension, for the asked lifetime.
			before := time.Now()
			issued, cred := issueDelegationCLI(t, node.cli, admin, proto, delegationPrincipal, "15m", "kv:get", "kv:delete")
			wantScope := []string{"kv:get", "kv:delete"}
			if issued.TenantID != delegationTenantA || issued.Principal != delegationPrincipal || !slices.Equal(issued.Scope, wantScope) || issued.Serial == "" {
				t.Fatalf("[%s] issued %+v; want tenant %s, principal %s, scope %v and a serial", proto.name, issued, delegationTenantA, delegationPrincipal, wantScope)
			}
			if issued.NotAfter.Before(before) || issued.NotAfter.After(before.Add(15*time.Minute+time.Minute)) {
				t.Fatalf("[%s] not after %s; want within 15m of %s", proto.name, issued.NotAfter, before)
			}
			leaf := parseDelegationCert(t, []byte(issued.CertPEM))
			g, found, err := delegation.FromCertificate(leaf)
			if !found || err != nil || g.Tenant != delegationTenantA || g.Principal != delegationPrincipal || !slices.Equal(g.Scope, wantScope) {
				t.Fatalf("[%s] certificate grant = %+v (found %v, err %v)", proto.name, g, found, err)
			}
			critical := slices.ContainsFunc(leaf.Extensions, func(e pkix.Extension) bool {
				return e.Id.Equal(delegation.OID) && e.Critical
			})
			if !critical {
				t.Fatalf("[%s] the delegation extension is missing or not critical", proto.name)
			}

			// Its scope intersected with the principal's grants: kv:get is
			// both and is admitted; kv:put is the principal's but was not
			// delegated; kv:delete was delegated but is not the principal's.
			delegated := tenantCLIArgsForProto(node, proto, cred.certPath, cred.keyPath)
			if out, err := runQRDB(t, node.cli, delegated, delegationKV("get", key)...); err != nil || !strings.Contains(out, delegationSeed) {
				t.Fatalf("[%s] delegated kv get inside the scope: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, delegated, delegationKV("put", key, "overwritten")...); err == nil {
				t.Fatalf("[%s] delegated kv put outside the scope was admitted: %s", proto.name, out)
			}
			if out, err := runQRDB(t, node.cli, delegated, delegationKV("del", key)...); err == nil {
				t.Fatalf("[%s] delegated kv del beyond the principal's grants was admitted: %s", proto.name, out)
			}
			// Delegated a write, it writes the tenant's own database.
			_, writer := issueDelegationCLI(t, node.cli, admin, proto, delegationPrincipal, "15m", "kv:get", "kv:put")
			writtenKey, written := key+"/written", "written-by-the-delegation"
			if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, writer.certPath, writer.keyPath),
				delegationKV("put", writtenKey, written)...); err != nil {
				t.Fatalf("[%s] delegated kv put inside the scope: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, admin, delegationKV("get", writtenKey)...); err != nil || !strings.Contains(out, written) {
				t.Fatalf("[%s] the tenant does not read back the delegated write: %v\noutput: %s", proto.name, err, out)
			}

			// Tenant B's own delegation to the same principal, on tenant A's
			// data: the tenant is the certificate's, so it reaches only B's.
			_, ofB := issueDelegationCLI(t, node.cli, tenantCLIArgsForProto(node, proto, adminB.certPath, adminB.keyPath),
				proto, delegationPrincipal, "15m", "kv:get", "kv:put")
			assertDelegationRefused(t, node, proto, ofB, key, "tenant B's delegation on tenant A's database")
			if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, ofB.certPath, ofB.keyPath),
				delegationKV("put", key, "overwritten-by-tenant-b")...); err == nil {
				t.Fatalf("[%s] tenant B's delegation wrote tenant A's database: %s", proto.name, out)
			}

			if out, err := runQRDB(t, node.cli, admin, delegationKV("get", key)...); err != nil || !strings.Contains(out, delegationSeed) {
				t.Fatalf("[%s] the refused writes changed %s: %v\noutput: %s", proto.name, key, err, out)
			}

			// Naming a principal this node does not accept delegations for.
			_, other := issueDelegationCLI(t, node.cli, admin, proto, "reporting", "15m", "kv:get")
			assertDelegationRefused(t, node, proto, other, key, "a delegation naming another principal")

			// Signed by tenant B's CA, naming tenant A.
			grant := delegation.Grant{Tenant: delegationTenantA, Principal: delegationPrincipal, Scope: []string{"kv:get"}}
			forged := forger.sign(t, grant, 15*time.Minute)
			assertDelegationRefused(t, node, proto, forged, key, "a delegation signed by another tenant's CA")

			// Signed by tenant A's own CA past the maximum lifetime, which its
			// issuance refuses to do: the verifier refuses it whatever the CA
			// wrote. The same certificate at exactly the maximum is admitted,
			// so the lifetime is the only reason.
			caA := keyB[delegationTenantA]
			atMax := caA.sign(t, grant, delegation.MaxLifetime)
			if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, atMax.certPath, atMax.keyPath),
				delegationKV("get", key)...); err != nil || !strings.Contains(out, delegationSeed) {
				t.Fatalf("[%s] a delegation its tenant's CA signed for exactly %s was not admitted: %v\noutput: %s", proto.name, delegation.MaxLifetime, err, out)
			}
			assertDelegationRefused(t, node, proto, caA.sign(t, grant, delegation.MaxLifetime+time.Second), key,
				"a delegation its tenant's CA signed for longer than the maximum lifetime")

			// Delegations the tenant may not issue at all.
			for _, refused := range []struct {
				what string
				args []string
			}{
				{"a built-in role", []string{"--principal", permission.RoleAdmin, "--scope", "kv:get"}},
				{"beyond the maximum lifetime", []string{"--principal", delegationPrincipal, "--scope", "kv:get", "--ttl", "25h"}},
				{"an unknown permission", []string{"--principal", delegationPrincipal, "--scope", "kv:levitate"}},
			} {
				args := append([]string{"ca", "delegate", "--out", t.TempDir()}, refused.args...)
				if out, err := runQRDB(t, node.cli, admin, args...); err == nil {
					t.Fatalf("[%s] ca delegate for %s succeeded, want a refusal: %s", proto.name, refused.what, out)
				}
			}

			// Revoked by its tenant: refused on its next use.
			if out, err := runQRDB(t, node.cli, admin, "ca", "revoke", "--serial", issued.Serial); err != nil {
				t.Fatalf("[%s] ca revoke --serial %s: %v\noutput: %s", proto.name, issued.Serial, err, out)
			}
			assertDelegationRefused(t, node, proto, cred, key, "a revoked delegation")

			// Expired: the short-lived delegation issued before the subtests.
			e := expiring[proto.name]
			conn := tenantCLIArgsForProto(node, proto, e.cred.certPath, e.cred.keyPath)
			deadline := e.notAfter.Add(30 * time.Second)
			for {
				out, err := runQRDB(t, node.cli, conn, delegationKV("get", expiringKey)...)
				done := time.Now()
				if err != nil {
					if done.Before(e.notAfter) {
						t.Fatalf("[%s] the short-lived delegation was refused before it expired at %s: %s", proto.name, e.notAfter, out)
					}
					t.Logf("[%s] an expired delegation refused: %s", proto.name, strings.TrimSpace(out))
					break
				}
				if done.After(deadline) {
					t.Fatalf("[%s] a delegation that expired at %s is still admitted at %s", proto.name, e.notAfter, done)
				}
			}
		})
	}
}
