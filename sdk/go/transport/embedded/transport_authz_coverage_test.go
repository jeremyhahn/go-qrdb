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

package embedded

import (
	"reflect"
	"testing"
)

// targetResolvedOperations are transport.Client methods authorized
// individually against a target OTHER than the caller's ambient tenant
// (a named tenant, user, or role, or a certificate's resolved owner) --
// tenant.go, tenant_seal.go, the user/role CRUD in transport_auth.go, and
// the role-assignment gate (role_assignment.go). Each one calls
// AuthMiddleware.AuthorizeTarget, AuthorizeTenantUpdate,
// AuthorizeTenantBarrierInit, AuthorizeTenantBarrierSealedOp,
// AuthorizeCertTenant, AuthorizeCertRevoke, AuthorizeRoleAssignmentWrite, or
// AuthorizeRoleUnassignment directly, rather than through the ambient-self
// operationPermissions table.
var targetResolvedOperations = map[string]struct{}{
	"CreateTenant":                 {},
	"GetTenant":                    {},
	"ListTenants":                  {},
	"ListDeletedTenants":           {},
	"ReconcileDeletedTenants":      {},
	"DeleteTenant":                 {},
	"UpdateTenant":                 {},
	"UpdateTenantQuotas":           {},
	"UpdateTenantEntitlements":     {},
	"TenantBarrierInit":            {},
	"TenantBarrierUnseal":          {},
	"TenantBarrierSeal":            {},
	"TenantBarrierStatus":          {},
	"TenantBarrierOfficerRegister": {},
	"TenantBarrierOfficerList":     {},
	"TenantBarrierRekeyStart":      {},
	"TenantBarrierOfficerRevoke":   {},
	"CreateUser":                   {},
	"GetUser":                      {},
	"GetUserByUsername":            {},
	"GetUserByEmail":               {},
	"AddCertBinding":               {},
	"RemoveCertBinding":            {},
	"UpdateUser":                   {},
	"DeleteUser":                   {},
	"ListUsers":                    {},
	"EnableUser":                   {},
	"DisableUser":                  {},
	"AssignRole":                   {},
	"UnassignRole":                 {},
	"CreateRole":                   {},
	"GetRole":                      {},
	"UpdateRole":                   {},
	"DeleteRole":                   {},
	"ListRoles":                    {},
	"GetRoleByName":                {},
	"ReconcileBuiltInRoles":        {},
	"GenerateCert":                 {},
	"RevokeCert":                   {},
	"ListCerts":                    {},
}

// identityGatedOperations are the per-tenant, per-database, and node-level
// share-claim and quorum handshakes. They carry NO RBAC permission on any
// transport (permission.IdentityGated) -- a Security Officer or custodian
// authenticates through the factor it presents (a certificate, a PIN), not
// a role -- so none belongs in operationPermissions. What each one DOES do
// is mark ctx with the same claimContext provenance (a presented
// certificate, or the in-process marker when there is no connection to
// carry one): see tenant.go, transport_barrier.go, and
// transport_database_holders.go.
var identityGatedOperations = map[string]struct{}{
	"TenantBarrierShareClaimBegin":      {},
	"TenantBarrierShareClaimComplete":   {},
	"TenantBarrierQuorumBegin":          {},
	"TenantBarrierQuorumSubmit":         {},
	"ShareClaimBegin":                   {},
	"ShareClaimComplete":                {},
	"RecoveryShareClaimBegin":           {},
	"RecoveryShareClaimComplete":        {},
	"BarrierQuorumBegin":                {},
	"BarrierQuorumSubmit":               {},
	"DatabaseBarrierShareClaimBegin":    {},
	"DatabaseBarrierShareClaimComplete": {},
	"DatabaseBarrierQuorumBegin":        {},
	"DatabaseBarrierQuorumSubmit":       {},

	// SealStore* has no permission.Permission of its own (see
	// requireIdentity's comment); it is grouped here because, like the
	// claim/quorum handshakes, the floor it enforces is "an authenticated
	// caller", not an RBAC grant.
	"SealStorePut":    {},
	"SealStoreGet":    {},
	"SealStoreDelete": {},
	"SealStoreList":   {},
	"SealStoreReseal": {},
	"SealStoreStatus": {},
}

// lifecycleOperations are request-shaped methods that are not requests:
// connecting and closing the transport, the generic Request dispatcher
// (which delegates to the typed, individually-authorized method named by
// its "method" argument -- see requestHandlers in transport.go), deriving a
// database-scoped transport (every one of whose requests is gated in turn
// by the SAME registry and helpers -- database_scope.go copies authMW onto
// the clone), and the "Phase 3 provider" accessors (RBACClient,
// FactorClient, LoginClient, SessionClient, VolumePoolClient, AuditClient,
// PermissionClient, SQLClient) that construct and return a sub-client
// rather than perform an operation themselves.
//
// The sub-clients those accessors return are NOT covered by this gate: their
// methods (FactorClient.Enroll, RBACClient.Authorize, and so on) live on a
// different concrete type (factorClient, rbacClient, ...), so
// reflect.TypeOf(&Transport{}) never sees them. FactorClient's four methods
// in particular (Enroll/List/Remove/Verify, each keyed by userID) have no
// permission check at all today -- a known, documented gap, not a silent
// one. See the embedded-authorization report for the follow-up.
var lifecycleOperations = map[string]struct{}{
	"Connect":              {},
	"Close":                {},
	"Healthy":              {},
	"Request":              {},
	"WithDatabase":         {},
	"RBACClient":           {},
	"FactorClient":         {},
	"LoginClient":          {},
	"SessionClient":        {},
	"VolumePoolClient":     {},
	"AuditClient":          {},
	"PermissionClient":     {},
	"SQLClient":            {},
	"UserAuthFactorClient": {},
}

// buildTagGatedMethods are *Transport methods that exist only under a build
// tag (qrdb_sql: Query, SQLClient; qrdb_vector's RAG handlers ship
// unconditionally but are listed here too for clarity). The completeness
// loop below only ever sees what THIS build compiled, which is already
// build-tag-safe; this set exists solely so the staleness loop does not
// misreport one of them "no longer a method" when run under a reduced tag
// set (go vet -tags "codec_json,raft" ./..., the leanest declared
// combination) instead of the full one (make -s print-build-tags).
var buildTagGatedMethods = map[string]struct{}{
	"Query":             {},
	"SQLClient":         {},
	"RAGIngestDocument": {},
	"RAGQuery":          {},
	"RAGDeleteDocument": {},
	"RAGListDocuments":  {},
}

// TestTransportClientAuthorizationSurface_CoversEveryMethod is the
// embedded-authorization build gate widened from transport.TenantClient to
// the full embedded Transport: every EXPORTED method of the concrete
// *Transport type, reflected directly (not through transport.Client, which
// omits RAG, embedding, auth-factor, SQL, and the guarded-write KV verbs --
// see the comment on operationPermissions for why those still belong here).
// KV, MVCC, transactions, leases, locks, watch, index, timeseries, vector,
// collections, RAG, embedding, users, roles, certs, credentials, DEK,
// backup, rebalance, databases, SQL, ceremony, the node and
// per-tenant/per-database barriers, and cluster admin are all covered this
// way. Every method must be declared in exactly one of: operationPermissions
// (ambient-self, routed through the SAME pkg/auth/middleware decision and
// pkg/permission entry the equivalent network route uses, where one exists),
// targetResolvedOperations, identityGatedOperations, or
// lifecycleOperations. A method declared in more than one, or in none,
// fails the build -- so a new operation cannot ship without its
// authorization mechanism stated, and this table cannot silently drift out
// of step with the type it describes.
//
// What this gate does NOT reach: the sub-clients the "Phase 3 provider"
// accessors return (FactorClient, RBACClient, LoginClient, SessionClient,
// VolumePoolClient, AuditClient, PermissionClient, SQLClient) live on their
// own concrete types, invisible to reflect.TypeOf(&Transport{}). See
// lifecycleOperations' comment for the one documented gap that leaves open
// (FactorClient's four methods).
func TestTransportClientAuthorizationSurface_CoversEveryMethod(t *testing.T) {
	typ := reflect.TypeOf(&Transport{})
	sets := []map[string]struct{}{targetResolvedOperations, identityGatedOperations, lifecycleOperations}

	seen := make(map[string]bool, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		seen[name] = true
		count := 0
		var declaredIn []string
		if _, ok := operationPermissions[name]; ok {
			count++
			declaredIn = append(declaredIn, "operationPermissions")
		}
		for _, set := range sets {
			if _, ok := set[name]; ok {
				count++
				declaredIn = append(declaredIn, "a target/identity/lifecycle set")
			}
		}
		switch count {
		case 0:
			t.Errorf("(*Transport).%s is declared in none of operationPermissions, "+
				"targetResolvedOperations, identityGatedOperations, or lifecycleOperations: "+
				"a new embedded operation must state how it is authorized before it ships", name)
		case 1:
			// exactly one -- correct.
		default:
			t.Errorf("(*Transport).%s is declared in %d authorization sets (%v): "+
				"it must be declared in exactly one", name, count, declaredIn)
		}
	}

	// And the reverse: no set may name a method the type no longer has (a
	// rename or removal), so every one of these stays an accurate
	// description of the CURRENT type rather than accumulating stale
	// entries. buildTagGatedMethods is exempted -- see its own comment.
	checkStale := func(setName string, set map[string]struct{}) {
		for name := range set {
			if _, gated := buildTagGatedMethods[name]; gated {
				continue
			}
			if !seen[name] {
				t.Errorf("%s declares %q, which is no longer a (*Transport) method", setName, name)
			}
		}
	}
	checkStale("targetResolvedOperations", targetResolvedOperations)
	checkStale("identityGatedOperations", identityGatedOperations)
	checkStale("lifecycleOperations", lifecycleOperations)
	for name := range operationPermissions {
		// A dotted name (AuditClient.QueryAudit) belongs to a Phase 3
		// provider sub-client (transport.AuditProvider), not *Transport
		// itself, so it is never checked against this type's method set.
		if name == "AuditClient.QueryAudit" || name == "AuditClient.VerifyChain" {
			continue
		}
		if _, gated := buildTagGatedMethods[name]; gated {
			continue
		}
		if !seen[name] {
			t.Errorf("operationPermissions declares %q, which is no longer a (*Transport) method", name)
		}
	}
}
