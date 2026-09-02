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
// Four isolation properties are proven, all through the CLI. (Database
// NAMES turn out to be a single flat, globally-unique catalog resource with
// exactly one OWNING tenant recorded at create time -- verified empirically,
// see testTenantIsolationOverProtocol's dbA/dbB/dbSys comment -- so proofs
// (1)/(2) use per-tenant-owned databases holding the IDENTICAL key, and
// proofs (3)/(4) separately confirm neither the catalog nor the KV data
// plane can be name-squatted or read/written cross-tenant at all.)
//
//  1. Cross-tenant KV disjointness: two tenants, each owning their own
//     database, writing the SAME key never observe each other's value.
//  2. System-vs-tenant KV disjointness: the system identity's own database,
//     with the SAME key, holds a third disjoint value.
//  3. A tenant cannot name-squat another tenant's (or the system's)
//     already-owned database name.
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
	"strings"
	"testing"
	"time"
)

// createTenant creates a tenant via the qrdb CLI over the given protocol
// using the harness's system-admin identity (node.cliArgsFor), failing the
// test on error. It is the shared setup step for every isolation proof
// below; the tenant lifecycle command itself is exercised in depth by
// tenant_cli_test.go.
func createTenant(t *testing.T, node *multiProtoNode, proto multiProtocol, id, name, isolationType string, autoUnseal bool) {
	t.Helper()
	args := []string{"tenant", "create", "--id", id, "--name", name, "--isolation-type", isolationType}
	if autoUnseal {
		args = append(args, "--barrier-auto-unseal")
	}
	if out, err := runCLIProto(t, node, proto, args...); err != nil {
		t.Fatalf("[%s] tenant create %s (isolation=%s): %v\noutput: %s", proto.name, id, isolationType, err, out)
	}
}

// provisionTenantIdentity grants a tenant's mTLS identity (CN=admin,
// OU=tenantID; see (*testCA).generateClientCertForTenant) a role with the
// given permissions and returns a client certificate/key pair for that
// identity, ready to drive tenant-scoped CLI commands over any transport.
// It uses the harness's system-admin identity (mtlsHTTPClient mints
// CN=admin,OU=system) to create the role and user via the REST admin API --
// this is setup plumbing (mirrors tenant_data_root_cli_test.go's
// provisionTenantCLIIdentity), not the feature under test.
func provisionTenantIdentity(t *testing.T, node *multiProtoNode, tenantID, roleName string, permissions []string) (certPath, keyPath string) {
	t.Helper()
	sysClient := mtlsHTTPClient(t, node.ca, 15*time.Second)

	roleBody, roleStatus := iraftAPIPost(t, sysClient,
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

	if _, uStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/users", node.restPort),
		map[string]interface{}{"username": "admin", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
	); uStatus != http.StatusCreated && uStatus != http.StatusOK {
		t.Fatalf("provisionTenantIdentity: create user for tenant %s: status %d", tenantID, uStatus)
	}

	return node.ca.generateClientCertForTenant(t, tenantID)
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
	"databases:create", "databases:read", "databases:list",
	"kv:put", "kv:get",
	"admin:health",
}

// TestTenantIsolation_KVAndIAM_PerProtocol proves properties (1)-(4) from
// the file doc comment, against every transport sharing one multiProtoNode.
func TestTenantIsolation_KVAndIAM_PerProtocol(t *testing.T) {
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
	// A database's NAME is a single flat, system-wide catalog key
	// (pkg/database/manager.go's m.databases map), but every database has
	// exactly one OWNING tenant recorded at create time
	// (pkg/database/manager_tenant.go db.TenantID()), and every
	// tenant-scoped lookup (GetForTenant, and the KV service resolved from
	// it) fails closed to "not found" for a database it does not own --
	// verified empirically against a live server: a second tenant calling
	// `database create` on an ALREADY-OWNED name gets a real 409 conflict
	// (no per-tenant namespace at the catalog layer), and attempting `kv`
	// I/O against a database another tenant created gets "database not
	// found" (deliberately indistinguishable from "does not exist", so a
	// tenant cannot even enumerate another tenant's database names by
	// probing for 403-vs-404). So EACH tenant below creates and owns its
	// OWN uniquely-named database; the isolation proof is that the
	// IDENTICAL key inside each is never visible cross-tenant, PLUS the
	// two direct-denial proofs (3)/(4) that a tenant cannot even name-squat
	// or read/write another tenant's database at all.
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

	if out, err := runQRDB(t, node.cli, connA, "database", "create", dbA, "--engine", "kv", "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] tenant A database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbB, "--engine", "kv", "--backend", "pebble"); err != nil {
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
	if out, err := runCLIProto(t, node, proto, "database", "create", dbSys, "--engine", "kv", "--backend", "pebble"); err != nil {
		t.Fatalf("[%s] system database create: %v\noutput: %s", proto.name, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "--database", dbSys, "kv", "put", key, sysValue); err != nil {
		t.Fatalf("[%s] system kv put: %v\noutput: %s", proto.name, err, out)
	}
	outSys, err := runCLIProto(t, node, proto, "--database", dbSys, "kv", "get", key)
	if err != nil || strings.TrimSpace(outSys) != sysValue {
		t.Fatalf("[%s] system kv get %s = %q (err=%v), want %q", proto.name, key, outSys, err, sysValue)
	}

	// PROOF (3): a tenant cannot name-squat another tenant's (or the
	// system's) already-owned database name -- the catalog's global
	// uniqueness constraint is enforced regardless of which identity is
	// asking, so a tenant can never "claim" a name it doesn't already own
	// out from under its rightful owner.
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbA, "--engine", "kv", "--backend", "pebble"); err == nil {
		t.Fatalf("[%s] tenant B re-creating tenant A's database name %q should be rejected as already-owned, got: %s", proto.name, dbA, out)
	}

	// PROOF (4): a tenant cannot read OR write another tenant's (or the
	// system's) database by name at all -- ownership is checked before any
	// KV dispatch, failing closed to "not found" (never leaking the other
	// tenant's data, and not distinguishing "exists but forbidden" from
	// "does not exist").
	if out, err := runQRDB(t, node.cli, connB, "--database", dbA, "kv", "get", key); err == nil {
		t.Fatalf("[%s] tenant B reading tenant A's database %q should fail, got: %s", proto.name, dbA, out)
	}
	if out, err := runQRDB(t, node.cli, connA, "--database", dbSys, "kv", "get", key); err == nil {
		t.Fatalf("[%s] tenant A reading the system database %q should fail, got: %s", proto.name, dbSys, out)
	}
	if out, err := runQRDB(t, node.cli, connB, "--database", dbA, "kv", "put", key, "hostile-overwrite"); err == nil {
		t.Fatalf("[%s] tenant B writing to tenant A's database %q should fail, got: %s", proto.name, dbA, out)
	}
	// Confirm the hostile write attempt truly had no effect.
	outA, err = runQRDB(t, node.cli, connA, "--database", dbA, "kv", "get", key)
	if err != nil || strings.TrimSpace(outA) != valueA {
		t.Fatalf("[%s] tenant A kv get %s after tenant B's rejected hostile write = %q (err=%v), want unchanged %q", proto.name, key, outA, err, valueA)
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
// CRITICAL cross-tenant IAM property: a tenant-local admin (identity tenant A)
// that supplies a FOREIGN --tenant-id (tenant B) on an IAM command is refused
// FORBIDDEN on EVERY transport -- it can neither enumerate, read, nor write
// tenant B's users/roles -- while the identical command scoped to its OWN
// tenant succeeds, and a SYSTEM admin may legitimately target tenant B.
//
// The --tenant-id flag lands in the request BODY (req.TenantID), NOT the
// mTLS-derived context tenant, so transport RBAC (which authorizes the
// caller's OWN tenant for the ScopeSelf users:*/roles:* grants) admits the
// call; the IAM routing layer (pkg/user, pkg/role) then independently
// reconciles the request tenant against the caller's identity and rejects the
// mismatch with tenant.CrossTenantDeniedError -> 403 / codes.PermissionDenied.
// This is the fix for the client-supplied-tenant IAM leak: the effective
// tenant is NEVER sourced from the request for a non-system caller.
//
// Both A and B are plain "prefix" tenants sharing the system store, so before
// the fix tenant A's admin could read tenant B's records straight out of the
// shared store by prefix -- the assertions below therefore also confirm no
// tenant-B record name leaks into the refused output.
func TestTenantIsolation_CrossTenantIAM_FailsClosed_PerProtocol(t *testing.T) {
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

	// Direction 3 (system admin is cross-tenant authoritative): the SAME
	// command that tenant A was refused succeeds for the system admin, whose
	// identity IS the system tenant -- confirming the legitimate cross-tenant
	// admin path is preserved.
	out, err := runCLIProto(t, node, proto, "roles", "list", "--tenant-id", tenantB)
	if err != nil {
		t.Fatalf("[%s] system admin listing tenant B roles must succeed: %v\noutput: %s", proto.name, err, out)
	}
	if !strings.Contains(out, victimRole) {
		t.Fatalf("[%s] system admin roles list for tenant B should contain %q, got: %s", proto.name, victimRole, out)
	}
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
	// RBAC "permission denied". The system admin is cross-tenant authoritative,
	// so this is not an authz outcome.
	assertSealedIAMRefused(t, node, proto, "roles", "create",
		"--name", "role-"+suffix, "--permissions", "kv:get", "--tenant-id", sealedID)
	assertSealedIAMRefused(t, node, proto, "users", "create", "--without-email-login-key",
		"--username", "user-"+suffix, "--tenant-id", sealedID)
	assertSealedIAMRefused(t, node, proto, "roles", "list", "--tenant-id", sealedID)

	// System-tenant IAM stays fully live: a create with no --tenant-id targets
	// the shared system store, which is never barrier-sealed.
	if out, err := runCLIProto(t, node, proto, "roles", "create",
		"--name", "sys-role-"+suffix, "--permissions", "kv:get"); err != nil {
		t.Fatalf("[%s] system-tenant roles create must still work while tenant %s is sealed: %v\noutput: %s", proto.name, sealedID, err, out)
	}

	// Direction 2 (succeeds when unsealed): the IDENTICAL command and caller
	// against the UNSEALED barrier tenant resolve to that tenant's own
	// barrier-backed store and succeed.
	openRole := "role-open-" + suffix
	if out, err := runCLIProto(t, node, proto, "roles", "create",
		"--name", openRole, "--permissions", "kv:get", "--tenant-id", openID); err != nil {
		t.Fatalf("[%s] roles create against UNSEALED barrier tenant %s must succeed: %v\noutput: %s", proto.name, openID, err, out)
	}
	if out, err := runCLIProto(t, node, proto, "users", "create", "--without-email-login-key",
		"--username", "user-open-"+suffix, "--tenant-id", openID); err != nil {
		t.Fatalf("[%s] users create against UNSEALED barrier tenant %s must succeed: %v\noutput: %s", proto.name, openID, err, out)
	}
	out, err := runCLIProto(t, node, proto, "roles", "list", "--tenant-id", openID)
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
func assertSealedIAMRefused(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) {
	t.Helper()
	out, err := runCLIProto(t, node, proto, args...)
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

	if out, err := runQRDB(t, node.cli, tenantConn, "database", "create", dbName, "--engine", "kv", "--backend", "pebble"); err != nil {
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
	if out, err := runQRDB(t, node.cli, sysConn, "barrier", "seal", "-y"); err != nil {
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
	"databases:create", "databases:read", "databases:list",
	"databases:seal", "databases:unseal", "databases:barrier_status",
	"admin:health",
}

// TestTenantIsolation_CrossTenantDatabaseBarrier_FailsClosed_PerProtocol proves
// the per-database barrier control surface fails closed cross-tenant on EVERY
// transport: a tenant-local admin that HOLDS databases:seal/unseal/
// barrier_status cannot seal, unseal, or read the barrier status of a database
// owned by ANOTHER tenant, addressing it by its global catalog name. The
// identical command run by the OWNING tenant, and by the SYSTEM admin, is NOT
// refused as an authorization denial -- proving the refusal is the server-side
// ownership check (database.TenantAccessDeniedError -> 403 / PermissionDenied)
// and not a blanket loss of the permission.
//
// Both tenants are plain "prefix" tenants sharing the system store, so the
// target database carries no dedicated barrier; the ownership check is
// evaluated BEFORE the barrier manager is consulted, so a non-owner is refused
// with an access denial and never learns whether the database or its barrier
// exists, while the owner/system caller passes the ownership gate and reaches
// the barrier layer (a non-authorization outcome).
func TestTenantIsolation_CrossTenantDatabaseBarrier_FailsClosed_PerProtocol(t *testing.T) {
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
	if out, err := runQRDB(t, node.cli, connB, "database", "create", dbB, "--engine", "kv", "--backend", "pebble"); err != nil {
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
// command as a non-owning tenant and fails the test unless it is refused as a
// cross-tenant access denial (never a "not found" that would leak whether the
// database exists under another owner, and never a bare barrier-not-configured
// artifact that would mean the ownership gate was skipped).
func assertCrossTenantDatabaseControlForbidden(t *testing.T, node *multiProtoNode, proto multiProtocol, conn []string, args ...string) {
	t.Helper()
	out, err := runQRDB(t, node.cli, conn, args...)
	if err == nil {
		t.Fatalf("[%s] %v as a non-owning tenant must be FORBIDDEN, got success: %s", proto.name, args, out)
	}
	lower := strings.ToLower(out)
	forbidden := strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "forbidden")
	if !forbidden {
		t.Fatalf("[%s] %v must be refused as a cross-tenant access denial, got: %s", proto.name, args, out)
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

// mvccCompactPermissions grants a tenant identity the store-global
// mvcc:compact verb (plus the connect permission). mvcc:compact is registered
// ScopeCross, so even a tenant that HOLDS it must be denied at the RBAC
// authorizer -- only the system tenant may compact.
var mvccCompactPermissions = []string{
	"mvcc:compact",
	"admin:health",
}

// TestTenantIsolation_MVCCCompact_SystemOnly_PerProtocol proves that MVCC
// compaction -- which discards a store-GLOBAL revision counter shared by every
// prefix-isolated tenant, destroying other tenants' time-travel history -- is
// reserved for the system tenant on EVERY transport. A tenant-local identity
// that HOLDS mvcc:compact is refused a permission denial (the ScopeCross authz
// gate), while the SYSTEM admin's identical compaction succeeds.
func TestTenantIsolation_MVCCCompact_SystemOnly_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			suffix := fmt.Sprintf("%s-%d", proto.name, time.Now().UnixNano())
			tenantID := "cmp-" + suffix
			createTenant(t, node, proto, tenantID, "Compact Tenant", "prefix", false)
			certPath, keyPath := provisionTenantIdentity(t, node, tenantID, "cmp-admin-"+suffix, mvccCompactPermissions)
			tenantConn := tenantCLIArgsForProto(node, proto, certPath, keyPath)

			// Tenant caller: refused a permission denial by the ScopeCross gate.
			out, err := runQRDB(t, node.cli, tenantConn, "kv", "compact", "--revision", "1")
			if err == nil {
				t.Fatalf("[%s] tenant mvcc compact must be FORBIDDEN (mvcc:compact is system-only), got success: %s", proto.name, out)
			}
			// The denial wording is normalized differently per transport (gRPC/
			// unix surface the RBAC "insufficient permissions"/"unauthorized"
			// text; MCP/REST/QUIC carry the fuller "cross-scope permission
			// requires system tenant"), so accept any authorization-denial
			// marker -- the point proven is that it is refused as a permission
			// denial, never silently compacted.
			lower := strings.ToLower(out)
			if !strings.Contains(lower, "permission denied") &&
				!strings.Contains(lower, "permissiondenied") &&
				!strings.Contains(lower, "not permitted") &&
				!strings.Contains(lower, "forbidden") &&
				!strings.Contains(lower, "unauthorized") &&
				!strings.Contains(lower, "insufficient permission") &&
				!strings.Contains(lower, "system tenant") {
				t.Fatalf("[%s] tenant mvcc compact must be refused as a permission denial, got: %s", proto.name, out)
			}

			// System caller: the identical compaction is admitted. Advance the
			// revision with a write first so compacting to revision 1 is valid.
			sysConn := node.cliArgsFor(proto)
			if out, err := runQRDB(t, node.cli, sysConn, "kv", "put", "cmp-seed-"+suffix, "v"); err != nil {
				t.Fatalf("[%s] system kv put (seed revision): %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, sysConn, "kv", "compact", "--revision", "1"); err != nil {
				t.Fatalf("[%s] system mvcc compact must succeed (legitimate admin path): %v\noutput: %s", proto.name, err, out)
			}
		})
	}
}
