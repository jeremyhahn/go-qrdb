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

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// TestCADelegatePolicy_CLI_PerProtocol drives a tenant's standing delegation
// policy end to end through the qrdb CLI on every protocol (#1186): the
// tenant writes the policy, its delegation renews itself within it and the
// renewed certificate reads the tenant's own data, and once the tenant deletes
// the policy the next mint is refused.
//
// The first mint is in process (Server.MintDelegation) and no CLI reaches it,
// so the tenant's own `ca delegate` stands in for it; every later mint is the
// delegation's own renewal through `ca delegate`, which is what the policy
// admits and what deleting it stops. A renewal is a `ca delegate` call, gated
// by certs:delegate like any other, so the credential that renews carries it
// in its scope, the policy admits it, and the principal is granted it.
func TestCADelegatePolicy_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node, adminB, _, n := startDelegationNodeWith(t)
	protocols := multiProtocols
	waitForDelegationNode(t, node, protocols)
	policies := delegationPolicyStore(t, n)

	for _, proto := range protocols {
		t.Run(proto.name, func(t *testing.T) {
			admin := node.cliArgsFor(proto)
			key := "delegated/policy/" + proto.name
			if out, err := runQRDB(t, node.cli, admin, delegationKV("put", key, delegationSeed)...); err != nil {
				t.Fatalf("[%s] seed %s: %v\noutput: %s", proto.name, key, err, out)
			}

			// The tenant writes the policy, and lists it back.
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "put",
				"--principal", delegationPrincipal, "--scope", "certs:delegate,kv:get", "--ttl", "15m"); err != nil {
				t.Fatalf("[%s] ca delegate policy put: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "list"); err != nil || !strings.Contains(out, delegationPrincipal) {
				t.Fatalf("[%s] ca delegate policy list does not show %s: %v\noutput: %s", proto.name, delegationPrincipal, err, out)
			}

			// The record itself is out of the transport's reach: a kv put
			// naming it lands in the tenant's own database, never in the
			// CA's keyspace, so it cannot widen the policy (#1187).
			forged := "__sys:cert_delegation_policy/" + delegationTenantA + "/" + delegationPrincipal
			if out, err := runQRDB(t, node.cli, admin, delegationKV("put", forged, `{"scope":["*"]}`)...); err != nil {
				t.Logf("[%s] kv put naming the policy record refused: %s", proto.name, strings.TrimSpace(out))
			}
			stood, found, err := policies.DelegationPolicy(context.Background(), delegationTenantA, delegationPrincipal)
			if err != nil || !found || !slices.Equal(stood.Scope, []string{"certs:delegate", "kv:get"}) {
				t.Fatalf("[%s] the policy after a kv put naming its record: %+v, found %v, %v", proto.name, stood, found, err)
			}

			// Another tenant neither sees nor removes tenant A's policy, and
			// its own delegation to the same principal cannot renew on it.
			connB := tenantCLIArgsForProto(node, proto, adminB.certPath, adminB.keyPath)
			if out, err := runQRDB(t, node.cli, connB, "ca", "delegate", "policy", "list"); err != nil || strings.Contains(out, delegationPrincipal) {
				t.Fatalf("[%s] tenant B's policy list shows tenant A's %s: %v\noutput: %s", proto.name, delegationPrincipal, err, out)
			}
			assertCrossTenantPolicyDeleteNotFound(t, node, connB, proto)
			kept, found, err := policies.DelegationPolicy(context.Background(), delegationTenantA, delegationPrincipal)
			if err != nil || !found || !slices.Equal(kept.Scope, []string{"certs:delegate", "kv:get"}) {
				t.Fatalf("[%s] tenant A's policy after tenant B's delete: %+v, found %v, %v", proto.name, kept, found, err)
			}
			_, ofB := issueDelegationCLI(t, node.cli, connB, proto, delegationPrincipal, "15m", "certs:delegate", "kv:get")
			if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, ofB.certPath, ofB.keyPath), "ca", "delegate",
				"--out", t.TempDir(), "--principal", delegationPrincipal, "--scope", "kv:get"); err == nil {
				t.Fatalf("[%s] tenant B's delegation renewed on tenant A's policy: %s", proto.name, out)
			}

			// The delegation renews itself within the policy, and the
			// renewed certificate makes an in-tenant call.
			_, first := issueDelegationCLI(t, node.cli, admin, proto, delegationPrincipal, "15m", "certs:delegate", "kv:get")
			delegated := tenantCLIArgsForProto(node, proto, first.certPath, first.keyPath)
			_, renewed := issueDelegationCLI(t, node.cli, delegated, proto, delegationPrincipal, "10m", "kv:get")
			if out, err := runQRDB(t, node.cli, tenantCLIArgsForProto(node, proto, renewed.certPath, renewed.keyPath),
				delegationKV("get", key)...); err != nil || !strings.Contains(out, delegationSeed) {
				t.Fatalf("[%s] the renewed delegation's in-tenant kv get: %v\noutput: %s", proto.name, err, out)
			}

			// Beyond the policy's scope, the renewal is refused.
			if out, err := runQRDB(t, node.cli, delegated, "ca", "delegate", "--out", t.TempDir(),
				"--principal", delegationPrincipal, "--scope", "kv:put"); err == nil {
				t.Fatalf("[%s] a renewal wider than the policy was admitted: %s", proto.name, out)
			}

			// Deleting the policy revokes it: the next mint is refused.
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "delete", delegationPrincipal); err != nil {
				t.Fatalf("[%s] ca delegate policy delete: %v\noutput: %s", proto.name, err, out)
			}
			if out, err := runQRDB(t, node.cli, delegated, "ca", "delegate", "--out", t.TempDir(),
				"--principal", delegationPrincipal, "--scope", "kv:get"); err == nil {
				t.Fatalf("[%s] a mint after the policy was deleted was admitted: %s", proto.name, out)
			}
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "list"); err != nil || strings.Contains(out, delegationPrincipal) {
				t.Fatalf("[%s] ca delegate policy list still shows %s after the delete: %v\noutput: %s", proto.name, delegationPrincipal, err, out)
			}
		})
	}
}

// TestCADelegatePolicy_CLI_TenantDeleteReclaims_PerProtocol: a deleted
// shared tenant's standing policies, which sit in the system's store rather
// than a home store of its own, go with it, so a delegation it issued can no
// longer renew itself, and no record is left for a later tenant of the same
// name to inherit (#1187).
func TestCADelegatePolicy_CLI_TenantDeleteReclaims_PerProtocol(t *testing.T) {
	t.Parallel()
	protocols := multiProtocols
	gone := make([]string, 0, len(protocols))
	for _, proto := range protocols {
		gone = append(gone, "dlg-gone-"+proto.name)
	}
	node, _, _, n := startDelegationNodeWith(t, gone...)
	creds := make(map[string]delegationCredential, len(gone))
	for _, id := range gone {
		creds[id] = onboardDelegationTenantIn(t, n, id, tenant.IsolationPrefix)
		grantDelegationDataRole(t, n, id)
	}
	waitForDelegationNode(t, node, protocols)
	policies := delegationPolicyStore(t, n)

	for i, proto := range protocols {
		t.Run(proto.name, func(t *testing.T) {
			id := gone[i]
			admin := tenantCLIArgsForProto(node, proto, creds[id].certPath, creds[id].keyPath)
			if out, err := runQRDB(t, node.cli, admin, "ca", "delegate", "policy", "put",
				"--principal", delegationPrincipal, "--scope", "certs:delegate,kv:get", "--ttl", "15m"); err != nil {
				t.Fatalf("[%s] ca delegate policy put: %v\noutput: %s", proto.name, err, out)
			}
			_, first := issueDelegationCLI(t, node.cli, admin, proto, delegationPrincipal, "15m", "certs:delegate", "kv:get")
			delegated := tenantCLIArgsForProto(node, proto, first.certPath, first.keyPath)
			issueDelegationCLI(t, node.cli, delegated, proto, delegationPrincipal, "10m", "kv:get")

			if out, err := runQRDB(t, node.cli, admin, "tenant", "delete", id); err != nil {
				t.Fatalf("[%s] tenant delete %s: %v\noutput: %s", proto.name, id, err, out)
			}
			if out, err := runQRDB(t, node.cli, delegated, "ca", "delegate", "--out", t.TempDir(),
				"--principal", delegationPrincipal, "--scope", "kv:get"); err == nil {
				t.Fatalf("[%s] a renewal for deleted tenant %s was admitted: %s", proto.name, id, out)
			}
			var left []string
			if !pollUntil(time.Minute, 50*time.Millisecond, func() bool {
				held, err := policies.ListDelegationPolicies(context.Background(), id)
				if err != nil {
					t.Fatalf("[%s] ListDelegationPolicies(%s): %v", proto.name, id, err)
				}
				left = left[:0]
				for _, p := range held {
					left = append(left, p.Principal)
				}
				return len(left) == 0
			}) {
				t.Fatalf("[%s] deleted tenant %s still holds policies for %v", proto.name, id, left)
			}
		})
	}
}

// consumerCLIScope is the grant an embedder needs for its four server-owned
// operations in a tenant (#1187): role and effective-permission reads for
// one account, role-binding writes, and the tenant barrier's officer list
// and seal.
var consumerCLIScope = []permission.Permission{
	permission.RolesRead, permission.UsersRead, permission.PermissionsResolve,
	permission.RolesAssign, permission.RolesUnassign,
	permission.TenantBarrierOfficerList, permission.TenantBarrierSeal,
}

// cliJSONID decodes the "id" of a qrdb --output json reply.
func cliJSONID(t *testing.T, what, out string) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.ID == "" {
		t.Fatalf("%s: no id in the reply: %v\noutput: %s", what, err, out)
	}
	return v.ID
}

// TestCADelegatePolicy_CLI_ConsumerOperations_PerProtocol drives the four
// embedder operations through the qrdb CLI under a delegation minted on
// tenant A's standing policy, on every transport: each is allowed in A,
// refused in B, and refused once the policy is deleted and the serial
// revoked. Sealing is terminal for A, so each transport gets its own node.
func TestCADelegatePolicy_CLI_ConsumerOperations_PerProtocol(t *testing.T) {
	t.Parallel()
	permission.RegisterRoleGrant(delegationPrincipal, consumerCLIScope...)
	scope := []string{string(permission.CertsDelegate)}
	for _, p := range consumerCLIScope {
		scope = append(scope, string(p))
	}

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node, adminB, _, _ := startDelegationNodeWith(t)
			waitForDelegationNode(t, node, []multiProtocol{proto})
			admin := node.cliArgsFor(proto)
			connB := tenantCLIArgsForProto(node, proto, adminB.certPath, adminB.keyPath)
			run := func(conn []string, args ...string) (string, error) {
				return runQRDB(t, node.cli, conn, args...)
			}
			must := func(what string, conn []string, args ...string) string {
				out, err := run(conn, args...)
				if err != nil {
					t.Fatalf("[%s] %s: %v\noutput: %s", proto.name, what, err, out)
				}
				return out
			}

			// Tenant A grants the consumer scope; a bootstrap delegation
			// mints the working ones on that policy.
			must("ca delegate policy put", admin, "ca", "delegate", "policy", "put",
				"--principal", delegationPrincipal, "--scope", strings.Join(scope, ","), "--ttl", "15m")
			_, boot := issueDelegationCLI(t, node.cli, admin, proto, delegationPrincipal, "15m", string(permission.CertsDelegate))
			bootConn := tenantCLIArgsForProto(node, proto, boot.certPath, boot.keyPath)
			issued, work := issueDelegationCLI(t, node.cli, bootConn, proto, delegationPrincipal, "10m", scope[1:]...)
			_, sealer := issueDelegationCLI(t, node.cli, bootConn, proto, delegationPrincipal, "10m", string(permission.TenantBarrierSeal))
			delegated := tenantCLIArgsForProto(node, proto, work.certPath, work.keyPath)

			account := cliJSONID(t, "users create in A", must("users create in A", admin, "--output", "json",
				"users", "create", "--without-email-login-key", "--username", "consumer-a-"+proto.name, "--tenant-id", delegationTenantA))
			accountB := cliJSONID(t, "users create in B", must("users create in B", connB, "--output", "json",
				"users", "create", "--without-email-login-key", "--username", "consumer-b-"+proto.name, "--tenant-id", delegationTenantB))

			// 1. Role and effective-permission reads for one account.
			must("users get", delegated, "users", "get", account)
			userRole := cliJSONID(t, "roles get-by-name user", must("roles get-by-name user", delegated, "--output", "json",
				"roles", "get-by-name", "user", "--tenant-id", delegationTenantA))
			must("roles resolve-permissions", delegated, "roles", "resolve-permissions", userRole)

			// 2. Role-binding writes for federated login.
			auditor := cliJSONID(t, "roles get-by-name auditor", must("roles get-by-name auditor", delegated, "--output", "json",
				"roles", "get-by-name", "auditor", "--tenant-id", delegationTenantA))
			must("roles assign", delegated, "roles", "assign", "--user", account, "--role", auditor)
			must("roles unassign", delegated, "roles", "unassign", "--user", account, "--role", auditor)

			// 3. The tenant barrier's officer list.
			must("tenant barrier-officer list", delegated, "tenant", "barrier-officer", "list", "--tenant-id", delegationTenantA)

			// Every operation is refused in tenant B.
			for what, args := range map[string][]string{
				"users get":                   {"users", "get", accountB},
				"roles get-by-name":           {"roles", "get-by-name", "auditor", "--tenant-id", delegationTenantB},
				"roles assign":                {"roles", "assign", "--user", accountB, "--role", auditor},
				"roles unassign":              {"roles", "unassign", "--user", accountB, "--role", auditor},
				"tenant barrier-officer list": {"tenant", "barrier-officer", "list", "--tenant-id", delegationTenantB},
				"tenant barrier-seal":         {"tenant", "barrier-seal", "--id", delegationTenantB},
			} {
				if out, err := run(delegated, args...); err == nil {
					t.Errorf("[%s] %s in %s admitted a delegation from %s: %s", proto.name, what, delegationTenantB, delegationTenantA, out)
				}
			}

			// Without the grant, in A itself: the bootstrap delegation holds
			// only certs:delegate, and each operation is refused on
			// authorization, not on any other failure.
			for what, args := range map[string][]string{
				"users get":                   {"users", "get", account},
				"roles get-by-name":           {"roles", "get-by-name", "auditor", "--tenant-id", delegationTenantA},
				"roles resolve-permissions":   {"roles", "resolve-permissions", userRole},
				"roles assign":                {"roles", "assign", "--user", account, "--role", auditor},
				"roles unassign":              {"roles", "unassign", "--user", account, "--role", auditor},
				"tenant barrier-officer list": {"tenant", "barrier-officer", "list", "--tenant-id", delegationTenantA},
				"tenant barrier-seal":         {"tenant", "barrier-seal", "--id", delegationTenantA},
			} {
				out, err := run(bootConn, args...)
				assertCLIAuthorizationRefused(t, proto.name, what+" without the grant", out, err)
			}

			// Deleting the policy stops the mint; revoking the serial
			// refuses the next call.
			must("ca delegate policy delete", admin, "ca", "delegate", "policy", "delete", delegationPrincipal)
			if out, err := run(bootConn, "ca", "delegate", "--out", t.TempDir(),
				"--principal", delegationPrincipal, "--scope", string(permission.UsersRead)); err == nil {
				t.Fatalf("[%s] a mint after the policy was deleted was admitted: %s", proto.name, out)
			}
			must("ca revoke", admin, "ca", "revoke", "--serial", issued.Serial)
			if out, err := run(delegated, "tenant", "barrier-officer", "list", "--tenant-id", delegationTenantA); err == nil {
				t.Fatalf("[%s] a revoked delegation listed the officers: %s", proto.name, out)
			}

			// The seal, under a delegation minted while the policy stood.
			must("tenant barrier-seal", tenantCLIArgsForProto(node, proto, sealer.certPath, sealer.keyPath),
				"tenant", "barrier-seal", "--id", delegationTenantA)
		})
	}
}

// assertCLIAuthorizationRefused fails unless a CLI call failed with the
// typed permission refusal every transport renders for it.
func assertCLIAuthorizationRefused(t *testing.T, proto, what, out string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("[%s] %s succeeded: %s", proto, what, out)
		return
	}
	lower := strings.ToLower(out + " " + err.Error())
	for _, marker := range []string{"permission denied", "permissiondenied", "forbidden", "403"} {
		if strings.Contains(lower, marker) {
			return
		}
	}
	t.Errorf("[%s] %s failed, but not on authorization: %v\noutput: %s", proto, what, err, out)
}

// assertCrossTenantPolicyDeleteNotFound: tenant B deleting tenant A's
// policy fails exactly as B deleting a policy no tenant holds does, a
// not-found refusal with a non-zero exit, so B learns nothing of A's.
func assertCrossTenantPolicyDeleteNotFound(t *testing.T, node *multiProtoNode, connB []string, proto multiProtocol) {
	t.Helper()
	const missing = "dlg-never-held"
	refusal := func(principal string) string {
		out, err := runQRDB(t, node.cli, connB, "ca", "delegate", "policy", "delete", principal)
		if err == nil {
			t.Fatalf("[%s] tenant B's policy delete of %s succeeded, want not found\noutput: %s", proto.name, principal, out)
		}
		return strings.ReplaceAll(strings.TrimSpace(out), principal, "<principal>")
	}
	cross, absent := refusal(delegationPrincipal), refusal(missing)
	// REST carries the not-found class as its 404; every other transport
	// as the SDK's key-not-found error.
	notFound := "key not found"
	if proto.name == "rest" {
		notFound = "(status 404)"
	}
	if !strings.Contains(absent, notFound) ||
		!strings.Contains(absent, "no delegation policy stands for <principal> in tenant "+delegationTenantB) {
		t.Fatalf("[%s] tenant B's delete of a missing policy: want a not-found refusal, got: %s", proto.name, absent)
	}
	if cross != absent {
		t.Fatalf("[%s] tenant B's delete of tenant A's policy is told apart from a missing one:\ncross-tenant: %s\nmissing:      %s", proto.name, cross, absent)
	}
}

// delegationPolicyStore is the node's CA, read directly: the record a
// transport must not be able to reach.
func delegationPolicyStore(t *testing.T, n *node.Node) cert.DelegationPolicyStore {
	t.Helper()
	policies, ok := n.Services().CertProv.(cert.DelegationPolicyStore)
	if !ok {
		t.Fatal("the node's CA keeps no delegation policies")
	}
	return policies
}
