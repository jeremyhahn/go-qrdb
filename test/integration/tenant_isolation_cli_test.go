// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

// tenant_isolation_cli_test.go proves SYSTEM vs TENANT and TENANT vs TENANT
// data isolation end-to-end through the real client CLI binary (cmd/qrdb),
// against every transport (REST, gRPC, QUIC, MCP, Unix), complementing the
// lifecycle coverage in tenant_cli_test.go and the REST/iraftAPI-driven
// isolation_raft_*.go tests (which exercise IsolationVolume/IsolationRaft
// cluster mechanics, not the CLI, and not this file's specific proofs).
//
// There is deliberately no --tenant flag anywhere on the qrdb CLI's
// data-plane commands (kv, database): a caller's tenant is derived SOLELY
// from its mTLS client certificate's OU field (see
// (*testCA).generateClientCertForTenant), so every proof below is driven by
// swapping which client identity a CLI invocation authenticates as, never by
// an override flag -- exactly matching how a real multi-tenant deployment
// works.
//
// Four isolation properties are proven, all through the CLI. (A database
// is identified by its owning tenant AND its name -- see
// testTenantIsolationOverProtocol's dbA/dbB/dbSys comment -- so proofs
// (1)/(2) use per-tenant-owned databases holding the IDENTICAL key, and
// proofs (3)/(4) separately confirm that reusing another tenant's database
// name reaches neither that tenant's catalog entry nor its KV data.)
//
//  1. Cross-tenant KV disjointness: two tenants, each owning their own
//     database, writing the SAME key never observe each other's value.
//  2. System-vs-tenant KV disjointness: the system identity's own database,
//     with the SAME key, holds a third disjoint value.
//  3. A tenant creating a database under another tenant's database name
//     gets a new database of its own: no conflict discloses the name, and
//     the other tenant's database is untouched.
//  4. A tenant cannot read OR write another tenant's (or the system's)
//     database by name at all -- it fails closed to "not found," never
//     revealing whether the name exists under another owner.
//
// A fifth property -- a self-managed IsolationBarrier tenant's IAM data
// (roles/users) is unreachable while that tenant's own barrier is sealed and
// reachable once unsealed -- is proven end-to-end (both directions, all 5
// transports) by
// TestTenantIsolation_SealedBarrierTenantIAM_FailsClosed_PerProtocol.
//
// TestTenantIsolation_SystemSealed_BlocksTenantData_PerProtocol proves the
// complementary direction: sealing the SYSTEM barrier locks tenant data out
// too, on every transport -- there is no sealed-window bypass path that
// would leave a tenant's own namespace reachable while the platform-wide
// barrier is sealed.
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// cliTenantAdminCred is a tenant's first-admin certificate/key pair, stashed
// by createTenant/createTenantWithAdminCert so provisionTenantIdentityFor can
// authenticate AS that tenant's own admin (ScopeSelf) to provision a second,
// test-controlled identity in the SAME tenant -- the system-tenant identity
// used here before can no longer reach into another tenant's IAM at all to do
// it (owner ruling, 2026-10-01: ScopeSelf has no system-tenant lift).
type cliTenantAdminCred struct{ certPath, keyPath string }

var (
	cliTenantAdminCredsMu sync.Mutex
	cliTenantAdminCreds   = map[string]cliTenantAdminCred{}
)

func registerTenantAdminCred(tenantID, certPath, keyPath string) {
	cliTenantAdminCredsMu.Lock()
	defer cliTenantAdminCredsMu.Unlock()
	cliTenantAdminCreds[tenantID] = cliTenantAdminCred{certPath: certPath, keyPath: keyPath}
}

func lookupTenantAdminCred(tenantID string) (cliTenantAdminCred, bool) {
	cliTenantAdminCredsMu.Lock()
	defer cliTenantAdminCredsMu.Unlock()
	c, ok := cliTenantAdminCreds[tenantID]
	return c, ok
}

// createTenant creates a tenant via the qrdb CLI over the given protocol
// using the harness's system-admin identity (node.cliArgsFor), failing the
// test on error. It is the shared setup step for every isolation proof
// below; the tenant lifecycle command itself is exercised in depth by
// tenant_cli_test.go.
func createTenant(t *testing.T, node *multiProtoNode, proto multiProtocol, id, name, isolationType string, autoUnseal bool) {
	t.Helper()
	certPath, keyPath := createTenantWithAdminCert(t, node, proto, id, name, isolationType, autoUnseal, id+"-admin")
	registerTenantAdminCred(id, certPath, keyPath)
}

// createTenantWithAdminCert is createTenant, but keeps the first admin's
// own key and certificate and returns their paths: trust-on-first-init
// admits only that exact certificate (see docs/multi-tenancy, the
// pending-admin record), never any other authenticated identity of the
// tenant, so a test driving the pre-RBAC window must authenticate AS the
// admin it onboarded, not a separately-minted tenant identity.
//
// The admin enrolls by CSR, except that of a self-managed dedicated tenant,
// which brings its own certificate (adminBYOCArgsForTenant): that tenant's CA
// is sealed at create, so only a cluster CA could sign the CSR, and these
// harness nodes serve bring-your-own TLS without one.
func createTenantWithAdminCert(t *testing.T, node *multiProtoNode, proto multiProtocol, id, name, isolationType string, autoUnseal bool, adminUsername string) (certPath, keyPath string) {
	t.Helper()
	args := []string{"tenant", "create", "--id", id, "--name", name, "--isolation-type", isolationType}
	if autoUnseal {
		args = append(args, "--barrier-auto-unseal")
	}
	if selfManagedDedicated(isolationType, autoUnseal) {
		adminArgs, leaf := adminBYOCArgsForTenant(t, id, adminUsername)
		args = append(args, adminArgs...)
		certPath, keyPath = leaf.CertPath, leaf.KeyPath
	} else {
		adminArgs := adminCSRArgsForTenant(t, node, proto, adminUsername)
		// adminCSRArgsForTenant's last element is the CSR path; its key.pem
		// sibling is the matching private key.
		keyPath = filepath.Join(filepath.Dir(adminArgs[len(adminArgs)-1]), "key.pem")
		certDir := t.TempDir()
		args = append(args, adminArgs...)
		args = append(args, "--out", certDir)
		certPath = filepath.Join(certDir, "admin-cert.pem")
	}
	out, err := runCLIProto(t, node, proto, args...)
	if err != nil {
		t.Fatalf("[%s] tenant create %s (isolation=%s): %v\noutput: %s", proto.name, id, isolationType, err, out)
	}
	registerTenantAdminCred(id, certPath, keyPath)
	return certPath, keyPath
}

// provisionTenantIdentity grants a tenant's mTLS identity (CN=admin,
// OU=tenantID; see (*testCA).generateClientCertForTenant) a role with the
// given permissions and returns a client certificate/key pair for that
// identity, bound to it and ready to drive tenant-scoped CLI commands over
// any transport. See provisionTenantIdentityFor.
func provisionTenantIdentity(t *testing.T, node *multiProtoNode, tenantID, roleName string, permissions []string) (certPath, keyPath string) {
	t.Helper()
	certPath, keyPath = node.ca.generateClientCertForTenant(t, tenantID)
	provisionTenantIdentityFor(t, node, tenantID, roleName, permissions, certPath)
	return certPath, keyPath
}

// provisionTenantIdentityFor creates the tenant's admin user with a role
// carrying permissions and binds the certificate at certPath to it, for a
// caller that already presents that certificate. It uses the harness's
// system-admin identity (mtlsHTTPClient presents the bound bootstrap admin,
// CN=admin,OU=system) to create the role and user via the REST admin API --
// this is setup plumbing (mirrors tenant_data_root_cli_test.go's
// provisionTenantCLIIdentity), not the feature under test.
func provisionTenantIdentityFor(t *testing.T, node *multiProtoNode, tenantID, roleName string, permissions []string, certPath string) {
	t.Helper()
	// Authenticate as the tenant's OWN first admin, registered by
	// createTenant/createTenantWithAdminCert. The system admin can no
	// longer provision records inside another tenant at all (owner ruling,
	// 2026-10-01), so it is used only as a fallback for a tenant this file
	// did not create itself (there is none today, but a missing
	// registration must not panic).
	var adminClient *http.Client
	if cred, ok := lookupTenantAdminCred(tenantID); ok {
		adminClient = mtlsHTTPClientFromFiles(t, node.ca, cred.certPath, cred.keyPath, 15*time.Second)
		adminClient = seatTenantProvisioner(t, node.ca, node.restPort, tenantID, adminClient, false)
	} else {
		adminClient = mtlsHTTPClient(t, node.ca, 15*time.Second)
	}

	roleBody, roleStatus := iraftAPIPost(t, adminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", node.restPort),
		map[string]interface{}{
			"name":        roleName,
			"tenant_id":   tenantID,
			"permissions": permissions,
		},
	)
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("provisionTenantIdentity: create role for tenant %s: status %d, body: %s", tenantID, roleStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
		t.Fatalf("provisionTenantIdentity: decode role id: %v body=%s", err, roleBody)
	}

	createBoundUser(t, adminClient, node.restPort,
		map[string]interface{}{"username": "admin", "email": "admin@" + tenantID + ".example.com", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
		certPath)
}

// awaitTenantAdminID waits until tenantID's own admin, authenticating as
// adminClient, is accepted, and returns that admin's user id. A
// system-managed tenant's admin account is completed just after the create
// answers (docs/multi-tenancy/api.md), and until it exists the admin's
// certificate is refused: 401 under the default binding policy, 403
// otherwise. Only that refusal is waited out; any other status fails.
func awaitTenantAdminID(t *testing.T, restPort int, tenantID string, adminClient *http.Client) string {
	t.Helper()
	return awaitTenantAccountID(t, restPort, tenantID, tenantID+"-admin", adminClient)
}

// tenantAdminCompletionWindow is how long awaitTenantAccountID waits for an
// account to authenticate. The server completes a new tenant's admin account
// after the create answers, retrying for up to
// bootstrap.DefaultAdminCompletionBudget (qrdbd does not override it); the
// window is that budget plus one lookup at the 15s client timeout the callers
// use, so a completion the server still has budget for is never cut short.
const tenantAdminCompletionWindow = bootstrap.DefaultAdminCompletionBudget + 15*time.Second

// awaitTenantAccountID looks up username in tenantID as client, polling until
// client authenticates and is authorized, and returns the account's id.
func awaitTenantAccountID(t *testing.T, restPort int, tenantID, username string, client *http.Client) string {
	t.Helper()
	lookupURL := fmt.Sprintf("https://127.0.0.1:%d/users/by-username/%s?tenantID=%s", restPort, username, tenantID)
	var lookupBody []byte
	var lookupStatus int
	pollUntil(tenantAdminCompletionWindow, 200*time.Millisecond, func() bool {
		lookupBody, lookupStatus = iraftAPIGet(t, client, lookupURL)
		return lookupStatus != http.StatusUnauthorized && lookupStatus != http.StatusForbidden
	})
	if lookupStatus != http.StatusOK {
		t.Fatalf("awaitTenantAccountID: look up %s in tenant %s: status %d, body: %s", username, tenantID, lookupStatus, lookupBody)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(lookupBody, &resp); err != nil || resp.ID == "" {
		t.Fatalf("awaitTenantAccountID: decode id of %s: %v body=%s", username, err, lookupBody)
	}
	return resp.ID
}

// seatTenantProvisioner seats the account a test's tenant setup runs as, and
// returns that account's client. The tenant's own first Admin (adminClient)
// creates a second account in tenantID holding the built-in Admin and
// Operator roles, bound to a fresh certificate (CN=<tenantID>-provisioner-<n>,
// OU=tenantID; the sequence number keeps a second seat in the same tenant
// distinct), and, when withSO, then assigns it the built-in SO role too.
//
// The grant rule (go-qrdb#1278) is why the setup does not run as the first
// Admin itself: a role is written only by a principal that holds every
// permission in it, and the Admin holds no kv:* data verbs, so the data roles
// the tests mint need an account that also holds Operator. No principal binds
// a role to itself past its holdings, so the Admin cannot take Operator or SO
// for itself; assigning a built-in role to another account is the duty split
// as designed. SO is assigned after the certificate is bound because the
// certificate target guard refuses a key binding for an SO account.
var provisionerSeq atomic.Uint64

func seatTenantProvisioner(t *testing.T, ca *testCA, restPort int, tenantID string, adminClient *http.Client, withSO bool) *http.Client {
	t.Helper()
	provisioner, _ := seatTenantProvisionerKeyPair(t, ca, restPort, tenantID, adminClient, withSO)
	return provisioner
}

// seatTenantProvisionerKeyPair is seatTenantProvisioner returning also the
// provisioner's certificate and key, for a caller that drives the CLI as it.
func seatTenantProvisionerKeyPair(t *testing.T, ca *testCA, restPort int, tenantID string, adminClient *http.Client, withSO bool) (*http.Client, *qrdbdharness.KeyPair) {
	t.Helper()
	awaitTenantAdminID(t, restPort, tenantID, adminClient)
	username := fmt.Sprintf("%s-provisioner-%d", tenantID, provisionerSeq.Add(1))
	kp := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(username, tenantID))
	createBoundUser(t, adminClient, restPort, map[string]any{
		"username":  username,
		"email":     username + "@" + tenantID + ".example.com",
		"tenant_id": tenantID,
		"role_ids": []string{
			role.SystemRoleID(tenantID, role.RoleNameAdmin),
			role.SystemRoleID(tenantID, role.RoleNameOperator),
		},
	}, kp.CertPath)
	provisioner := mtlsHTTPClientFromFiles(t, ca, kp.CertPath, kp.KeyPath, 15*time.Second)
	id := awaitTenantAccountID(t, restPort, tenantID, username, provisioner)
	if withSO {
		body, status := iraftAPIPost(t, adminClient,
			fmt.Sprintf("https://127.0.0.1:%d/roles/assign", restPort),
			map[string]interface{}{
				"user_id": id,
				"role_id": role.SystemRoleID(tenantID, role.RoleNameSO),
			},
		)
		if status != http.StatusOK {
			t.Fatalf("seatTenantProvisioner: assign SO to %s: status %d, body: %s", username, status, body)
		}
	}
	return provisioner, kp
}

// tenantCLIArgsForProto returns the root-level qrdb CLI flags that select
// the given protocol against node, authenticated as the identity named by
// certPath/keyPath instead of the harness's default system-admin identity
// (mirrors (*multiProtoNode).cliArgsFor with a swapped client certificate).
func tenantCLIArgsForProto(node *multiProtoNode, proto multiProtocol, certPath, keyPath string) []string {
	return []string{
		"--server", node.serverURLFor(proto),
		"--ca-file", node.ca.CertPath,
		"--cert-file", certPath,
		"--key-file", keyPath,
		"--timeout", "20s",
	}
}

// dataPlanePermissions is the permission set every tenant identity below
// needs to create a database and do KV I/O, PLUS admin:health.
//
// admin:health is required for a non-obvious reason discovered empirically
// by this file: the SDK's gRPC/QUIC/Unix Transport.Connect implementations
// perform an authenticated Health RPC before any other call succeeds
// (pkg/auth/middleware/mapping.go maps the Health method to
// permission.AdminHealth on every transport), so a tenant identity that
// lacks admin:health cannot even OPEN a connection over those three
// transports, regardless of what data-plane permissions it holds. REST and
// MCP do not gate Health this way. admin:health is registered at
// ScopeSelf (not tenant-only or system-only), so granting it to a
// tenant-local role is safe and does not cross any tenant boundary -- but
// every prior tenant-identity fixture in this test suite
// (tenant_data_root_cli_test.go, enable_encryption_cli_test.go) only ever
// drove REST, so this coupling had never been exercised multi-protocol
// before. Worth a product-level UX review (least-privilege data-plane
// roles arguably should not need an admin permission merely to connect),
// but it is applied uniformly across gRPC/QUIC/Unix (verified -- not a
// parity gap), so this file works around it in its own fixture rather than
// changing product code.
var dataPlanePermissions = []string{
	"databases:create", "databases:create_raft", "databases:create_volume", "databases:read", "databases:list",
	"kv:put", "kv:get",
	"admin:health",
}

// TestTenantIsolation_KVAndIAM_PerProtocol proves properties (1)-(4) from
// the file doc comment, against every transport sharing one multiProtoNode.
func TestTenantIsolation_KVAndIAM_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantIsolationOverProtocol(t, node, proto)
		})
	}
}

func testTenantIsolationOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA := "iso-a-" + suffix
	tenantB := "iso-b-" + suffix
	// Every database this test creates uses the IDENTICAL key "greeting" --
	// that collision is the whole point of proof (1)/(2) below -- but each
	// db NAME is namespaced by owning identity (dbA/dbB/dbSys) AND by
	// protocol so the five protocol subtests (sharing one node) never
	// collide with EACH OTHER across iterations.
	//
	// A database is identified by its owning tenant AND its name: the
	// catalog record is __sys:dbcatalog/<tenant>/<name> and the manager's
	// open map is keyed by the (tenant, name) pair (pkg/database/manager.go
	// dbKey). Every tenant-scoped lookup (GetForTenant, and the KV service
	// resolved from it) resolves the caller's own database and fails closed
	// to "not found" for a name the caller does not own. So no route reveals
	// another tenant's database names (docs/databases/architecture.md): a
	// second tenant creating a name another tenant already uses gets a
	// database of its OWN, not a conflict that would confirm the name
	// exists. The isolation proof is that the IDENTICAL key inside each
	// database is never visible cross-tenant, PLUS the direct proofs (3)/(4)
	// that a tenant reusing another tenant's database name reaches only its
	// own database, never the other tenant's data.
	dbA := "appdata-a-" + suffix
	dbB := "appdata-b-" + suffix
	dbSys := "appdata-sys-" + suffix
	const key = "greeting"

	createTenant(t, node, proto, tenantA, "Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "Tenant B", "prefix", false)

	certA, keyA := provisionTenantIdentity(t, node, tenantA, "db-admin", dataPlanePermissions)
	certB, keyB := provisionTenantIdentity(t, node, tenantB, "db-admin", dataPlanePermissions)

	connA := tenantCLIArgsForProto(node, proto, certA, keyA)
	connB := tenantCLIArgsForProto(node, proto, certB, keyB)

	if out, err := runQRDB(t, node.cli, connA, "database", "create", dbA, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant A database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbB, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant B database create: %v\noutput: %s", proto.name, err, out)
	}

	valueA := "tenant-a-secret-" + suffix
	valueB := "tenant-b-secret-" + suffix

	if out, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "put", key, valueA); err != nil {
		t.Fatalf("[%s] tenant A kv put: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "put", key, valueB); err != nil {
		t.Fatalf("[%s] tenant B kv put: %v\noutput: %s", proto.name, err, out)
	}

	// PROOF (1): the identical key resolves to each tenant's OWN value in
	// its OWN database -- neither identity's write is visible to the other.
	outA, err := runQRDB(t, node.cli, connA, "--database", dbA, "kv", "get", key)
	if err != nil || strings.TrimSpace(outA) != valueA {
		t.Fatalf("[%s] tenant A kv get %s = %q (err=%v), want %q -- cross-tenant leak or isolation break", proto.name, key, outA, err, valueA)
	}
	outB, err := runQRDB(t, node.cli, connB, "--database", dbB, "kv", "get", key)
	if err != nil || strings.TrimSpace(outB) != valueB {
		t.Fatalf("[%s] tenant B kv get %s = %q (err=%v), want %q -- cross-tenant leak or isolation break", proto.name, key, outB, err, valueB)
	}

	// PROOF (2): the system identity's own database, with the SAME key,
	// holds a third disjoint value.
	sysValue := "system-scope-secret-" + suffix
	if out, err := runCLIProto(t, node, proto, "database", "create", dbSys, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] system database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "--database", dbSys, "kv", "put", key, sysValue); err != nil {
		t.Fatalf("[%s] system kv put: %v\noutput: %s", proto.name, err, out)
	}
	outSys, err := runCLIProto(t, node, proto, "--database", dbSys, "kv", "get", key)
	if err != nil || strings.TrimSpace(outSys) != sysValue {
		t.Fatalf("[%s] system kv get %s = %q (err=%v), want %q", proto.name, key, outSys, err, sysValue)
	}

	// PROOF (3): a tenant reusing another tenant's database name creates a
	// database it owns -- the create is neither refused (a conflict would
	// disclose that tenant A uses the name) nor attached to tenant A's
	// database. Reading the shared key from it right away finds nothing:
	// it is a new, empty database, not tenant A's.
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbA, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant B creating its own database named %q (tenant A's name): %v\noutput: %s", proto.name, dbA, err, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "--database", dbA, "kv", "get", key); err == nil || strings.Contains(out, valueA) {
		t.Fatalf("[%s] tenant B reading its own new database %q = %q (err=%v), want not found -- it resolved tenant A's database", proto.name, dbA, out, err)
	}

	// PROOF (4): a tenant cannot read OR write another tenant's (or the
	// system's) database by name -- every name resolves within the caller's
	// own tenant, failing closed to "not found" for a name it does not own
	// (never leaking the other tenant's data, and not distinguishing
	// "exists but forbidden" from "does not exist"). Tenant B's write under
	// tenant A's name lands in tenant B's own database.
	if out, err := runQRDB(t, node.cli, connA, "--database", dbSys, "kv", "get", key); err == nil {
		t.Fatalf("[%s] tenant A reading the system database %q should fail, got: %s", proto.name, dbSys, out)
	}
	const hostile = "hostile-overwrite"
	if out, err := runQRDB(t, node.cli, connB, "--database", dbA, "kv", "put", key, hostile); err != nil {
		t.Fatalf("[%s] tenant B writing to its own database %q: %v\noutput: %s", proto.name, dbA, err, out)
	}
	outB, err = runQRDB(t, node.cli, connB, "--database", dbA, "kv", "get", key)
	if err != nil || strings.TrimSpace(outB) != hostile {
		t.Fatalf("[%s] tenant B kv get %s from its own database %q = %q (err=%v), want %q", proto.name, key, dbA, outB, err, hostile)
	}
	// Confirm tenant B's write never reached tenant A's database.
	outA, err = runQRDB(t, node.cli, connA, "--database", dbA, "kv", "get", key)
	if err != nil || strings.TrimSpace(outA) != valueA {
		t.Fatalf("[%s] tenant A kv get %s after tenant B wrote under the same database name = %q (err=%v), want unchanged %q", proto.name, key, outA, err, valueA)
	}

	// System-scope and tenant-scope data-plane commands are proven still
	// fully live at the end of every proof above -- the sysValue/valueA
	// reads embedded in proofs (2)/(4) already exercised this.
	//
	// The FIFTH property -- a self-managed IsolationBarrier tenant's IAM
	// (users/roles) is unreachable, even to the SYSTEM admin, while that
	// tenant's own barrier is sealed, and reachable once unsealed -- is
	// proven end-to-end (both directions, all 5 transports) by
	// TestTenantIsolation_SealedBarrierTenantIAM_FailsClosed_PerProtocol
	// below. It requires a volume-pool-provisioned node
	// (startTenantEntitlementNode) because registerDedicatedBarrier always
	// calls the barrier registry's *Pooled* constructor, so it cannot share
	// this file's plain multiProtoNode and lives in its own test.
}

// iamPermissions is the permission set a tenant-local admin identity needs to
// drive users/roles management through the CLI, plus admin:health (required to
// OPEN a gRPC/QUIC/Unix connection at all -- see dataPlanePermissions for the
// detailed rationale). Every permission here is ScopeSelf, so granting them to
// a tenant-local role confines the identity to its OWN tenant -- the whole
// point of the cross-tenant proof below is that these self-scoped grants must
// NOT let the identity reach another tenant's IAM store by supplying a foreign
// --tenant-id, because the routing layer reconciles the request tenant against
// the caller's identity independently of RBAC.
var iamPermissions = []string{
	"users:create", "users:read", "users:list",
	"roles:create", "roles:read", "roles:list",
	"admin:health",
}

// TestTenantIsolation_CrossTenantIAM_FailsClosed_PerProtocol proves the
// CRITICAL cross-tenant IAM property: ANY caller -- a tenant-local admin
// (identity tenant A) or the SYSTEM admin -- that supplies a FOREIGN
// --tenant-id (tenant B) on an IAM command is refused FORBIDDEN on EVERY
// transport -- it can neither enumerate, read, nor write tenant B's
// users/roles -- while the identical command scoped to its OWN tenant
// succeeds. The system tenant has no special cross-tenant reach any more
// (owner ruling, 2026-10-01: "strict tenant isolation" removes the
// `ScopeSelf` system-tenant lift unconditionally, under every isolation
// policy); this test used to prove the SYSTEM admin's reach was the one
// EXCEPTION, and now proves there is none.
//
// The --tenant-id flag lands in the request BODY (req.TenantID), NOT the
// mTLS-derived context tenant, so transport RBAC (which authorizes the
// caller's OWN tenant for the ScopeSelf users:*/roles:* grants) admits the
// call; the IAM routing layer (pkg/user, pkg/role) then independently
// reconciles the request tenant against the caller's identity via
// tenant.EffectiveTenant and rejects ANY mismatch -- the caller's own tenant
// included no exception -- with tenant.CrossTenantDeniedError ->
// 403 / codes.PermissionDenied. This is the fix for the client-supplied-tenant
// IAM leak: the effective tenant is NEVER sourced from the request for ANY
// caller.
//
// Both A and B are plain "prefix" tenants sharing the system store, so before
// the ORIGINAL fix (confining non-system callers) tenant A's admin could read
// tenant B's records straight out of the shared store by prefix -- the
// assertions below therefore also confirm no tenant-B record name leaks into
// the refused output, for every caller including the system admin.
func TestTenantIsolation_CrossTenantIAM_FailsClosed_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCrossTenantIAMOverProtocol(t, node, proto)
		})
	}
}

func testCrossTenantIAMOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA := "iam-a-" + suffix
	tenantB := "iam-b-" + suffix

	createTenant(t, node, proto, tenantA, "IAM Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "IAM Tenant B", "prefix", false)

	// Provision an admin identity in EACH tenant. Tenant A's identity is the
	// attacker; tenant B's provisioning seeds a role ("victim-role") and a
	// user ("admin") in tenant B so a cross-tenant enumeration WOULD have
	// something to leak if the fix regressed.
	victimRole := "victim-role-" + suffix
	certA, keyA := provisionTenantIdentity(t, node, tenantA, "iam-admin-"+suffix, iamPermissions)
	provisionTenantIdentity(t, node, tenantB, victimRole, iamPermissions)

	connA := tenantCLIArgsForProto(node, proto, certA, keyA)

	// Direction 1 (fail closed): tenant A's admin targeting tenant B via
	// --tenant-id is FORBIDDEN on every IAM op -- enumerate, read, and write.
	assertCrossTenantIAMForbidden(t, node, proto, connA, victimRole,
		"users", "list", "--tenant-id", tenantB)
	assertCrossTenantIAMForbidden(t, node, proto, connA, victimRole,
		"roles", "list", "--tenant-id", tenantB)
	assertCrossTenantIAMForbidden(t, node, proto, connA, victimRole,
		"users", "create", "--without-email-login-key", "--username", "intruder-"+suffix, "--tenant-id", tenantB)
	assertCrossTenantIAMForbidden(t, node, proto, connA, victimRole,
		"roles", "create", "--name", "intruder-role-"+suffix, "--permissions", "kv:get", "--tenant-id", tenantB)
	assertCrossTenantIAMForbidden(t, node, proto, connA, victimRole,
		"roles", "get-by-name", victimRole, "--tenant-id", tenantB)

	// Direction 2 (own tenant succeeds): the identical enumerate command scoped
	// to tenant A's OWN tenant works -- proving the refusal above is a
	// cross-tenant denial, not a blanket loss of the IAM permission.
	if out, err := runQRDB(t, node.cli, connA, "users", "list", "--tenant-id", tenantA); err != nil {
		t.Fatalf("[%s] tenant A admin listing its OWN tenant users must succeed: %v\noutput: %s", proto.name, err, out)
	}
	// With NO --tenant-id the request tenant is empty; a non-system caller
	// resolves it to its identity tenant (A), so this too must succeed -- and
	// must NOT silently fall through to the system tenant.
	if out, err := runQRDB(t, node.cli, connA, "roles", "list"); err != nil {
		t.Fatalf("[%s] tenant A admin listing roles with no --tenant-id must succeed (scoped to A): %v\noutput: %s", proto.name, err, out)
	}

	// Direction 3 (system is no longer cross-tenant authoritative): the
	// SAME command tenant A's admin was refused is now ALSO refused for the
	// system admin -- the ScopeSelf system-tenant lift was removed
	// unconditionally (owner ruling, 2026-10-01: "strict tenant
	// isolation"), so the system tenant is confined to its own tenant like
	// any other, with no built-in cross-tenant IAM reach left. Nothing
	// replaces it yet (tenant_identity:read/write is not wired to any
	// transport), so this is a hard refusal, not a narrower one.
	assertCrossTenantIAMForbidden(t, node, proto, node.cliArgsFor(proto), victimRole,
		"roles", "list", "--tenant-id", tenantB)
}

// assertCrossTenantIAMForbidden runs an IAM CLI command as a non-system tenant
// identity targeting a FOREIGN tenant and fails the test unless it is refused
// with a cross-tenant permission denial (never a barrier-sealed error, and
// never leaking the target tenant's seeded record name).
func assertCrossTenantIAMForbidden(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, victimRole string, args ...string) {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, args...)
	if err == nil {
		t.Fatalf("[%s] %v as a foreign-tenant caller must be FORBIDDEN, got success: %s", proto.name, args, out)
	}
	lower := strings.ToLower(out)
	// The refusal must be the cross-tenant denial (CrossTenantDeniedError text
	// "not permitted ... in tenant", surfaced as 403 / PermissionDenied), not a
	// barrier-sealed or not-found artifact.
	forbidden := strings.Contains(lower, "not permitted") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "forbidden")
	if !forbidden {
		t.Fatalf("[%s] %v must be refused as a cross-tenant permission denial, got: %s", proto.name, args, out)
	}
	if strings.Contains(lower, "seal") {
		t.Fatalf("[%s] %v was refused by the barrier, not cross-tenant authorization: %s", proto.name, args, out)
	}
	// Anti-leak: the foreign tenant's seeded role name must NOT appear.
	if victimRole != "" && strings.Contains(out, victimRole) {
		t.Fatalf("[%s] %v leaked foreign tenant record %q into the refused output: %s", proto.name, args, victimRole, out)
	}
}

// TestTenantIsolation_SealedBarrierTenantIAM_FailsClosed_PerProtocol proves
// the fifth isolation property: a self-managed IsolationBarrier tenant's IAM
// data (roles/users) fails closed while that tenant's own barrier is SEALED,
// on EVERY transport (REST/gRPC/QUIC/MCP/Unix), driven through the real qrdb
// CLI binary.
//
// The daemon serves role/user management through the StoreResolver-routed
// RoutingService (cmd/qrdbd/start.go bootstrapUserRoleStores ->
// server.WithUserService/WithRoleService), so a barrier-isolated tenant's IAM
// resolves to its own TenantBarrier-backed store. When that barrier is sealed
// the resolver returns tenant.ErrTenantSealed and every transport maps it to a
// fail-closed status (REST 503, gRPC/QUIC codes.Unavailable, MCP/Unix error)
// rather than silently serving the operation from the shared system store.
//
// Both directions are asserted with the IDENTICAL command and the IDENTICAL
// (system-admin, cross-tenant-authoritative) caller, differing ONLY in the
// target tenant's seal state:
//
//   - a self-managed barrier tenant starts SEALED (registerDedicatedBarrier
//     leaves it uninitialized): `roles create --tenant-id <sealed>` must be
//     REFUSED, and the refusal must be the barrier failing closed (the error
//     text carries "sealed"), NOT an RBAC denial.
//   - a system-managed (--barrier-auto-unseal) barrier tenant starts UNSEALED:
//     the same `roles create --tenant-id <open>` must SUCCEED.
//
// Because the caller and command are identical, the sealed refusal cannot be
// an authorization artifact -- the unsealed tenant proves RBAC admits the
// operation, so only the barrier distinguishes the two outcomes. System-tenant
// IAM (no --tenant-id, always the shared prefix store) is proven live too.
func TestTenantIsolation_SealedBarrierTenantIAM_FailsClosed_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testSealedDedicatedTenantIAMOverProtocol(t, node, proto, "barrier")
		})
	}
}

// TestTenantIsolation_SealedVolumeRaftTenantIAM_FailsClosed_PerProtocol proves
// the SAME fail-closed IAM property for the dedicated-DataRoot isolation modes
// (IsolationVolume and IsolationRaft) that
// TestTenantIsolation_SealedBarrierTenantIAM_FailsClosed_PerProtocol proves for
// IsolationBarrier.
//
// A volume/raft tenant owns a dedicated on-disk DataRoot whose IAM (users/roles)
// lives in a per-tenant barrier at {DataRoot}/barrier, registered in the SAME
// BarrierRegistry the StoreResolver consults (RegisterVolumeTenant). Before this
// change the resolver only routed IsolationBarrier tenants through their barrier;
// volume/raft IAM fell through to the unsealed SHARED store, so a sealed
// volume/raft tenant's users/roles were reachable while its data was sealed --
// the gap this test guards against. Now a self-managed (SEALED) volume/raft
// tenant's IAM must fail closed with tenant.ErrTenantSealed on EVERY transport,
// and a system-managed (UNSEALED) one must succeed with the identical command
// and caller -- so the seal state, not RBAC, is what distinguishes the outcomes.
func TestTenantIsolation_SealedVolumeRaftTenantIAM_FailsClosed_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementNode(t)

	for _, isolationType := range []string{"volume", "raft"} {
		isolationType := isolationType
		t.Run(isolationType, func(t *testing.T) {
			for _, proto := range multiProtocols {
				proto := proto
				t.Run(proto.name, func(t *testing.T) {
					testSealedDedicatedTenantIAMOverProtocol(t, node, proto, isolationType)
				})
			}
		})
	}
}

// testSealedDedicatedTenantIAMOverProtocol drives the fail-closed IAM proof for
// any dedicated-isolation mode (barrier/volume/raft): all three register a
// per-tenant barrier in the StoreResolver's registry, so their IAM routing and
// sealed-fail-closed semantics are identical. The self-managed tenant starts
// SEALED and every IAM op against it must be refused by the barrier (never RBAC);
// the system-managed tenant starts UNSEALED and the identical op must succeed.
func testSealedDedicatedTenantIAMOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol, isolationType string) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%s-%d", isolationType, proto.name, time.Now().UnixNano())
	sealedID := "sealed-iam-" + suffix
	openID := "open-iam-" + suffix

	// A self-managed dedicated-isolation tenant: its per-tenant barrier is
	// registered SEALED and left uninitialized.
	createTenant(t, node, proto, sealedID, "Sealed IAM Tenant", isolationType, false)
	// A system-managed (auto-unseal) dedicated-isolation tenant: its barrier is
	// initialized immediately and starts UNSEALED.
	createTenant(t, node, proto, openID, "Open IAM Tenant", isolationType, true)

	// Direction 1 (fail closed): every IAM op targeting the sealed tenant must
	// be refused, and the refusal must be the barrier failing closed -- the
	// error carries tenant.ErrTenantSealed's "barrier is sealed" text, never an
	// RBAC "permission denied". The system admin is no longer cross-tenant
	// authoritative over ScopeSelf data/IAM (owner ruling, 2026-10-01), so this
	// must authenticate as the SEALED tenant's own pending admin -- its
	// identity resolves to its own tenant, finalizeIdentity binds ctx to that
	// tenant, and the lookup that establishes the identity is itself what the
	// sealed barrier refuses.
	sealedCred, ok := lookupTenantAdminCred(sealedID)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for sealed tenant %s", proto.name, sealedID)
	}
	sealedConn := tenantCLIArgsForProto(node, proto, sealedCred.certPath, sealedCred.keyPath)
	assertSealedIAMRefused(t, node, proto, sealedConn, "roles", "create",
		"--name", "role-"+suffix, "--permissions", "users:read", "--tenant-id", sealedID)
	assertSealedIAMRefused(t, node, proto, sealedConn, "users", "create", "--without-email-login-key",
		"--username", "user-"+suffix, "--tenant-id", sealedID)
	assertSealedIAMRefused(t, node, proto, sealedConn, "roles", "list", "--tenant-id", sealedID)

	// System-tenant IAM stays fully live: a create with no --tenant-id targets
	// the shared system store, which is never barrier-sealed.
	if out, err := runCLIProto(t, node, proto, "roles", "create",
		"--name", "sys-role-"+suffix, "--permissions", "kv:get"); err != nil {
		t.Fatalf("[%s] system-tenant roles create must still work while tenant %s is sealed: %v\noutput: %s", proto.name, sealedID, err, out)
	}

	// Direction 2 (succeeds when unsealed): the OPEN tenant's own admin,
	// authenticating as itself, resolves to that tenant's own unsealed
	// barrier-backed store and succeeds. Its built-in Admin role holds
	// roles:create, users:create and roles:list in its own tenant, so it
	// needs no other role; the wait only outlasts the admin account's
	// completion after the tenant create.
	openCred, ok := lookupTenantAdminCred(openID)
	if !ok {
		t.Fatalf("[%s] no registered admin credential for open tenant %s", proto.name, openID)
	}
	openConn := tenantCLIArgsForProto(node, proto, openCred.certPath, openCred.keyPath)
	openClient := mtlsHTTPClientFromFiles(t, node.ca, openCred.certPath, openCred.keyPath, 15*time.Second)
	awaitTenantAdminID(t, node.restPort, openID, openClient)

	// users:read, which the tenant Admin holds: under the grant rule
	// (go-qrdb#1278) it writes no role past its own holdings.
	openRole := "role-open-" + suffix
	if out, err := runQRDB(t, node.cli, openConn, "roles", "create",
		"--name", openRole, "--permissions", "users:read", "--tenant-id", openID); err != nil {
		t.Fatalf("[%s] roles create against UNSEALED barrier tenant %s must succeed: %v\noutput: %s", proto.name, openID, err, out)
	}
	if out, err := runQRDB(t, node.cli, openConn, "users", "create", "--without-email-login-key",
		"--username", "user-open-"+suffix, "--tenant-id", openID); err != nil {
		t.Fatalf("[%s] users create against UNSEALED barrier tenant %s must succeed: %v\noutput: %s", proto.name, openID, err, out)
	}
	out, err := runQRDB(t, node.cli, openConn, "roles", "list", "--tenant-id", openID)
	if err != nil {
		t.Fatalf("[%s] roles list against UNSEALED barrier tenant %s must succeed: %v\noutput: %s", proto.name, openID, err, out)
	}
	if !strings.Contains(out, openRole) {
		t.Fatalf("[%s] roles list for unsealed tenant %s should contain %q, got: %s", proto.name, openID, openRole, out)
	}
}

// assertSealedIAMRefused runs an IAM CLI command against a sealed barrier
// tenant over the given protocol and fails the test unless it is refused with
// the barrier's fail-closed signal (tenant.ErrTenantSealed's "sealed" text),
// disambiguating a barrier refusal from an RBAC "permission denied".
func assertSealedIAMRefused(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, args ...string) {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, args...)
	if err == nil {
		t.Fatalf("[%s] %v against a SEALED barrier tenant must fail closed, got success: %s", proto.name, args, out)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "seal") {
		t.Fatalf("[%s] %v against a SEALED barrier tenant must be refused by the barrier (error should mention 'sealed'), got: %s", proto.name, args, out)
	}
	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "unauthorized") {
		t.Fatalf("[%s] %v was refused by RBAC, not the barrier -- the fail-closed proof requires a barrier refusal: %s", proto.name, args, out)
	}
}

// TestTenantIsolation_SystemSealed_BlocksTenantData_PerProtocol proves the
// complementary direction to the documented-but-unenforced IsolationBarrier
// property noted above: sealing the SYSTEM barrier locks tenant data out
// too, on EVERY transport. There must be no
// sealed-window bypass path that leaves a tenant's own (independently
// created, unsealed-by-default "prefix") namespace reachable while the
// platform-wide barrier is sealed -- every transport's sealed-window gate
// (pkg/api/{grpc,quic,mcp,unix,rest}) is built from the same canonical
// transport-neutral allowlist in pkg/api/sealedwindow, and KV is not on it.
//
// This test seals the node's system barrier, which is destructive (every
// subsequent request that isn't on the sealed-window allowlist is refused),
// so it uses its OWN dedicated multiProtoNode rather than sharing one with
// any other test.
func TestTenantIsolation_SystemSealed_BlocksTenantData_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	const tenantID = "iso-seal-victim"
	const dbName = "appdata"
	const key = "greeting"
	const value = "should-become-unreachable-once-sealed"

	// Use the "rest" protocol purely as the setup transport (data plane and
	// the barrier seal itself are proven per-protocol in the loop below).
	setupProto := multiProtocols[0]

	createTenant(t, node, setupProto, tenantID, "Seal Victim Tenant", "prefix", false)
	certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "db-admin", dataPlanePermissions)
	tenantConn := tenantCLIArgsForProto(node, setupProto, certPath, keyPath)

	if out, err := runQRDB(t, node.cli, tenantConn, "database", "create", dbName, "--backend", "pebble"); err != nil {
		t.Fatalf("tenant database create: %v\noutput: %s", err, out)
	}
	if out, err := runQRDB(t, node.cli, tenantConn, "--database", dbName, "kv", "put", key, value); err != nil {
		t.Fatalf("tenant kv put: %v\noutput: %s", err, out)
	}
	if out, err := runQRDB(t, node.cli, tenantConn, "--database", dbName, "kv", "get", key); err != nil || strings.TrimSpace(out) != value {
		t.Fatalf("tenant kv get (pre-seal) = %q (err=%v), want %q", out, err, value)
	}

	// Seal the SYSTEM barrier as the system admin identity.
	sysConn := node.cliArgsFor(setupProto)
	if out, err := runQRDB(t, node.cli, sysConn, "barrier", "seal", "-y", "--so-pin", harnessSOPIN); err != nil {
		t.Fatalf("barrier seal: %v\noutput: %s", err, out)
	}

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			connArgsTenant := tenantCLIArgsForProto(node, proto, certPath, keyPath)
			out, err := runQRDB(t, node.cli, connArgsTenant, "--database", dbName, "kv", "get", key)
			if err == nil {
				t.Fatalf("[%s] tenant kv get should fail while the SYSTEM barrier is sealed (no sealed-window bypass may exist), got: %s", proto.name, out)
			}
		})
	}
}

// dbBarrierControlPermissions is the permission set an OVER-privileged tenant
// admin needs to reach the per-database barrier control surface (seal, unseal,
// barrier-status) PLUS create/list its own databases and open a connection.
// No BUILT-IN role grants the databases:seal/unseal/barrier_status verbs to a
// tenant, so this fixture deliberately mints a custom role that DOES -- the
// whole point of the cross-tenant proof below is that even a tenant holding
// these permissions is refused by the SERVER-SIDE ownership check when it
// targets a database owned by another tenant (the SDK-side verifyOwnership
// check is bypassable over raw gRPC/REST, so the guarantee must hold at the
// service layer). admin:health is required to OPEN a gRPC/QUIC/Unix connection
// (see dataPlanePermissions for the rationale).
var dbBarrierControlPermissions = []string{
	"databases:create", "databases:create_raft", "databases:create_volume", "databases:read", "databases:list",
	"databases:seal", "databases:unseal", "databases:barrier_status",
	"admin:health",
}

// TestTenantIsolation_CrossTenantDatabaseBarrier_FailsClosed_PerProtocol proves
// the per-database barrier control surface fails closed cross-tenant on EVERY
// transport: a tenant-local admin that HOLDS databases:seal/unseal/
// barrier_status cannot seal, unseal, or read the barrier status of a database
// owned by ANOTHER tenant, addressing it by its global catalog name. It gets
// the same "not found" a name nobody holds gets, so it cannot tell which names
// other tenants use. The identical command run by the OWNING tenant, and by
// the SYSTEM admin, is NOT refused -- proving the refusal is the server-side
// ownership check and not a blanket loss of the permission.
//
// Both tenants are plain "prefix" tenants sharing the system store, so the
// target database carries no dedicated barrier; the ownership check is
// evaluated BEFORE the barrier manager is consulted, so a non-owner never
// learns whether the database or its barrier exists, while the owner/system
// caller passes the ownership gate and reaches the barrier layer.
func TestTenantIsolation_CrossTenantDatabaseBarrier_FailsClosed_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testCrossTenantDatabaseBarrierOverProtocol(t, node, proto)
		})
	}
}

func testCrossTenantDatabaseBarrierOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	tenantA := "dbc-a-" + suffix // attacker
	tenantB := "dbc-b-" + suffix // victim/owner
	dbB := "b-controlled-" + suffix

	createTenant(t, node, proto, tenantA, "DB Control Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "DB Control Tenant B", "prefix", false)

	certA, keyA := provisionTenantIdentity(t, node, tenantA, "dbc-admin-a-"+suffix, dbBarrierControlPermissions)
	certB, keyB := provisionTenantIdentity(t, node, tenantB, "dbc-admin-b-"+suffix, dbBarrierControlPermissions)
	connA := tenantCLIArgsForProto(node, proto, certA, keyA)
	connB := tenantCLIArgsForProto(node, proto, certB, keyB)

	// Tenant B owns the database.
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbB, "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant B database create: %v\noutput: %s", proto.name, err, out)
	}

	// Direction 1 (fail closed): tenant A -- though it HOLDS the barrier-control
	// permissions -- is refused an access denial on every per-database barrier
	// control verb against tenant B's database.
	assertCrossTenantDatabaseControlForbidden(t, node, proto, connA,
		"database", "barrier-status", dbB)
	assertCrossTenantDatabaseControlForbidden(t, node, proto, connA,
		"database", "seal", dbB)
	assertCrossTenantDatabaseControlForbidden(t, node, proto, connA,
		"database", "unseal", dbB, "--secret", "irrelevant")

	// Direction 2 (owner passes the ownership gate): the OWNING tenant running
	// the identical command is NOT refused as an authorization/cross-tenant
	// denial -- it clears the ownership check and reaches the barrier layer.
	assertNotAuthorizationDenied(t, node, proto, connB,
		"database", "barrier-status", dbB)

	// Direction 3 (system is authoritative): the SYSTEM admin targeting tenant
	// B's database is likewise not an authorization denial.
	assertNotAuthorizationDenied(t, node, proto, node.cliArgsFor(proto),
		"database", "barrier-status", dbB)
}

// assertCrossTenantDatabaseControlForbidden runs a per-database barrier control
// command as a non-owning tenant and fails the test unless it is refused with
// the same "not found" a missing database gets (an access denial would tell
// the caller the name is held by another tenant), and never a bare
// barrier-not-configured artifact that would mean the ownership gate was
// skipped.
func assertCrossTenantDatabaseControlForbidden(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, args ...string) {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, args...)
	if err == nil {
		t.Fatalf("[%s] %v as a non-owning tenant must be refused, got success: %s", proto.name, args, out)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "not found") {
		t.Fatalf("[%s] %v must be refused as not found, like a missing database, got: %s", proto.name, args, out)
	}
	if strings.Contains(lower, "access denied") || strings.Contains(lower, "forbidden") {
		t.Fatalf("[%s] %v revealed that another tenant holds the name: %s", proto.name, args, out)
	}
	// The refusal must be the ownership denial, not the barrier reporting it is
	// unconfigured (which would mean the guard ran AFTER the barrier manager
	// check and leaked configuration state to a non-owner).
	if strings.Contains(lower, "not configured") {
		t.Fatalf("[%s] %v leaked barrier-not-configured to a non-owner (ownership gate skipped): %s", proto.name, args, out)
	}
}

// assertNotAuthorizationDenied runs a command that is EXPECTED to pass the
// tenant-ownership gate (owner or system caller) and fails the test only if the
// outcome is an authorization/cross-tenant denial. A non-authorization error
// (e.g. the plain database carries no barrier to report) is acceptable: the
// point is that the ownership guard admitted the caller and the request reached
// the barrier layer, unlike the cross-tenant refusal above.
func assertNotAuthorizationDenied(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, args ...string) {
	t.Helper()
	out, _ := runQRDB(t, node.cli, conn, args...)
	lower := strings.ToLower(out)
	if strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "forbidden") {
		t.Fatalf("[%s] %v for an authorized (owner/system) caller must NOT be an authorization denial, got: %s", proto.name, args, out)
	}
}

// mvccCompactPermissions grants a tenant identity what the compaction proof
// below drives: its own database, KV writes and revision reads, and
// mvcc:compact. Compaction is per database (v0.33.0): mvcc:compact is the
// owning tenant's right over its own database's history, ScopeSelfStrict, so
// holding it never reaches another tenant's database or the system's.
var mvccCompactPermissions = append(append([]string{}, dataPlanePermissions...), "kv:range", "mvcc:range", "mvcc:current_revision", "mvcc:compact")

// TestTenantIsolation_MVCCCompact_OwnDatabaseOnly_PerProtocol proves on every
// transport that a tenant holding mvcc:compact compacts its OWN database and
// nothing else. Naming another tenant's database or the system's is refused
// (ownership fails closed to "not found"), and after the tenant compacts its
// own database, the other tenant's and the system's history at their old
// revisions still reads back -- the compaction never lifted onto a keyspace
// the tenant does not own -- while the tenant's own old revision is gone.
func TestTenantIsolation_MVCCCompact_OwnDatabaseOnly_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testOwnDatabaseCompactionOverProtocol(t, node, proto)
		})
	}
}

// compactionHistory is one identity's database with a key written twice: the
// first value, and the store revision it was written at.
type compactionHistory struct {
	conn     []string
	db       string
	first    string
	firstRev int64
}

func testOwnDatabaseCompactionOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
	const key = "history"

	tenantA, tenantB := "cmp-a-"+suffix, "cmp-b-"+suffix
	createTenant(t, node, proto, tenantA, "Compact Tenant A", "prefix", false)
	createTenant(t, node, proto, tenantB, "Compact Tenant B", "prefix", false)
	certA, keyA := provisionTenantIdentity(t, node, tenantA, "cmp-admin", mvccCompactPermissions)
	certB, keyB := provisionTenantIdentity(t, node, tenantB, "cmp-admin", mvccCompactPermissions)

	write := func(conn []string, db, owner string) compactionHistory {
		t.Helper()
		if out, err := runQRDB(t, node.cli, conn, "database", "create", db, "--backend", "pebble"); err != nil {
			t.Fatalf("[%s] %s database create %s: %v\noutput: %s", proto.name, owner, db, err, out)
		}
		h := compactionHistory{conn: conn, db: db, first: owner + "-first-" + suffix}
		if out, err := runQRDB(t, node.cli, conn, "--database", db, "kv", "put", key, h.first); err != nil {
			t.Fatalf("[%s] %s kv put: %v\noutput: %s", proto.name, owner, err, out)
		}
		h.firstRev = compactionRevision(t, node, proto, conn, db)
		if out, err := runQRDB(t, node.cli, conn, "--database", db, "kv", "put", key, owner+"-second-"+suffix); err != nil {
			t.Fatalf("[%s] %s kv put (second): %v\noutput: %s", proto.name, owner, err, out)
		}
		return h
	}
	a := write(tenantCLIArgsForProto(node, proto, certA, keyA), "cmp-db-a-"+suffix, "tenant-a")
	b := write(tenantCLIArgsForProto(node, proto, certB, keyB), "cmp-db-b-"+suffix, "tenant-b")
	sys := write(node.cliArgsFor(proto), "cmp-db-sys-"+suffix, "system")

	// The history a database owner reads at its first revision.
	readsFirst := func(h compactionHistory) (string, error) {
		return runQRDB(t, node.cli, h.conn, "--database", h.db, "kv", "range", key, "--revision", strconv.FormatInt(h.firstRev, 10))
	}
	assertIntact := func(h compactionHistory, after string) {
		t.Helper()
		out, err := readsFirst(h)
		if err != nil || !strings.Contains(out, h.first) {
			t.Fatalf("[%s] %s: database %s at revision %d = %q (err=%v), want %q -- another identity's compaction reached it",
				proto.name, after, h.db, h.firstRev, out, err, h.first)
		}
	}

	revision := strconv.FormatInt(compactionRevision(t, node, proto, a.conn, a.db), 10)

	// Tenant A names a database it does not own: refused, nothing compacted.
	for _, other := range []compactionHistory{b, sys} {
		if out, err := runQRDB(t, node.cli, a.conn, "--database", other.db, "kv", "compact", "--revision", revision); err == nil {
			t.Fatalf("[%s] tenant A compacting %s, which it does not own, must be refused, got: %s", proto.name, other.db, out)
		}
	}
	assertIntact(b, "after tenant A's refused compaction")
	assertIntact(sys, "after tenant A's refused compaction")

	// Tenant A compacts its own database: admitted, and its own first
	// revision is gone.
	if out, err := runQRDB(t, node.cli, a.conn, "--database", a.db, "kv", "compact", "--revision", revision); err != nil {
		t.Fatalf("[%s] tenant A compacting its own database must succeed: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := readsFirst(a); err == nil && strings.Contains(out, a.first) {
		t.Fatalf("[%s] tenant A still reads revision %d of its own compacted database: %s", proto.name, a.firstRev, out)
	}

	// Nothing else was compacted.
	assertIntact(b, "after tenant A compacted its own database")
	assertIntact(sys, "after tenant A compacted its own database")
}

// compactionRevision returns the current store revision as conn reads it
// through database db.
func compactionRevision(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, db string) int64 {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, "--output", "json", "--database", db, "kv", "revision")
	if err != nil {
		t.Fatalf("[%s] kv revision on %s: %v\noutput: %s", proto.name, db, err, out)
	}
	var resp struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &resp); err != nil || resp.Revision == 0 {
		t.Fatalf("[%s] kv revision on %s: decode %q: %v", proto.name, db, out, err)
	}
	return resp.Revision
}

// TestTenantIsolation_IsolationPolicyVerbs_RefusedUntilActive_PerProtocol
// proves every isolation-policy verb is refused, with the strict-isolation
// not-active refusal, on every protocol until the feature is active, and that
// a refusal changes nothing: the tenant record reads back identically and the
// floor is still unreadable through the gated verb.
func TestTenantIsolation_IsolationPolicyVerbs_RefusedUntilActive_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testIsolationPolicyVerbsRefusedOverProtocol(t, node, proto)
		})
	}
}

func testIsolationPolicyVerbsRefusedOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()

	tenantID := fmt.Sprintf("isopol-%s-%d", proto.name, time.Now().UnixNano())
	createTenant(t, node, proto, tenantID, "Isolation Policy Tenant", "prefix", false)
	sys := node.cliArgsFor(proto)

	before, err := runQRDB(t, node.cli, sys, "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get before: %v\noutput: %s", proto.name, err, before)
	}

	refusals := [][]string{
		{"cluster-isolation-policy", "get"},
		{"cluster-isolation-policy", "set", "strict"},
		{"tenant", "update", tenantID, "--isolation-policy", "strict"},
	}
	for _, args := range refusals {
		out, err := runQRDB(t, node.cli, sys, args...)
		if err == nil {
			t.Fatalf("[%s] qrdb %s succeeded before %s is active\noutput: %s",
				proto.name, strings.Join(args, " "), tenant.StrictIsolation, out)
		}
		if !strings.Contains(out, strconv.Quote(string(tenant.StrictIsolation))) || !strings.Contains(out, "is not active") {
			t.Fatalf("[%s] qrdb %s: want the %s not-active refusal\noutput: %s",
				proto.name, strings.Join(args, " "), tenant.StrictIsolation, out)
		}
	}

	after, err := runQRDB(t, node.cli, sys, "tenant", "get", tenantID)
	if err != nil {
		t.Fatalf("[%s] tenant get after: %v\noutput: %s", proto.name, err, after)
	}
	if after != before {
		t.Fatalf("[%s] a refused isolation-policy verb changed the tenant\nbefore:\n%s\nafter:\n%s", proto.name, before, after)
	}
	// The floor is still behind the gate: the read is refused exactly as
	// before, so no refused set reached it.
	if out, err := runQRDB(t, node.cli, sys, "cluster-isolation-policy", "get"); err == nil || !strings.Contains(out, "is not active") {
		t.Fatalf("[%s] floor read after the refusals = %v\noutput: %s", proto.name, err, out)
	}
}
