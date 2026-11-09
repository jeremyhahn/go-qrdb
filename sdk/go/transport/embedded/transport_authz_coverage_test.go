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
	"CreateTenant":                    {},
	"GetTenant":                       {},
	"ListTenants":                     {},
	"GetClusterIsolationPolicy":       {},
	"SetClusterIsolationPolicy":       {},
	"ListDeletedTenants":              {},
	"ReconcileDeletedTenants":         {},
	"DeleteTenant":                    {},
	"UpdateTenant":                    {},
	"UpdateTenantQuotas":              {},
	"UpdateTenantEntitlements":        {},
	"TenantBarrierInit":               {},
	"ReconcileBuiltInRolesAllTenants": {},
	"TenantBarrierSignedInitBegin":    {},
	"TenantBarrierSignedInitComplete": {},
	"BarrierEnableEncryption":         {},
	"TenantBarrierUnseal":             {},
	"TenantBarrierSeal":               {},
	"TenantBarrierStatus":             {},
	"TenantBarrierOfficerRegister":    {},
	"TenantBarrierOfficerList":        {},
	"TenantBarrierRekeyStart":         {},
	"TenantBarrierOfficerRevoke":      {},
	"CreateUser":                      {},
	"GetUser":                         {},
	"GetUserByUsername":               {},
	"GetUserByEmail":                  {},
	"AddCertBinding":                  {},
	"RemoveCertBinding":               {},
	"UpdateUser":                      {},
	"DeleteUser":                      {},
	"ListUsers":                       {},
	"EnableUser":                      {},
	"DisableUser":                     {},
	"SuspendUser":                     {},
	"UnsuspendUser":                   {},
	"AssignRole":                      {},
	"UnassignRole":                    {},
	"CreateRole":                      {},
	"GetRole":                         {},
	"UpdateRole":                      {},
	"DeleteRole":                      {},
	"ListRoles":                       {},
	"GetRoleByName":                   {},
	"ReconcileBuiltInRoles":           {},
	"RevokeCert":                      {},
	"ListCerts":                       {},
	"IssueCert":                       {},
	"EnrollCert":                      {},
	"IssueDelegation":                 {},
	"AddTrustAnchors":                 {},
	"ListTrustAnchors":                {},
	"RemoveTrustAnchor":               {},
	// InviteUser authorizes users:invite against the request's tenant via
	// authorizeTenant (AuthMiddleware.AuthorizeTenant), the same generic call
	// authorizeCertTenant makes for the certificate verbs above.
	"InviteUser": {},
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
}

// publicOperations are *Transport methods reachable with NO identity and NO
// permission at all, matching network parity exactly (owner security review
// item 7):
//
//   - Health: public on every transport (REST's /health is
//     server.publicIf, carrying no permission). Liveness must be checkable
//     with no identity and no barrier state.
//   - The sealed-window bootstrap/unseal verbs
//     (pkg/api/sealedwindow.Table): BarrierInitialize, BarrierUnseal,
//     BarrierInitializeShamir, OfficerRegister, RecoveryOfficerRegister,
//     CeremonyInitialize, CeremonySignCSR. Every protocol transport's
//     boot-time sealed-window listener serves these with NO authentication,
//     because that listener has no identity plane to authenticate against
//     -- an embedded node has no IAM store while sealed either, so the
//     embedder must be able to drive the SAME verbs in-process. Each is
//     gated instead by its own domain credential: the unseal secret /
//     Shamir share set the barrier strategy verifies, the SO/user PINs
//     ProvisionInitializePINs verifies, or the CA's trust-on-first-use
//     registration.
//
// BarrierStatus, CeremonyStatus and Status are NOT here: REST gates all
// three behind an RBAC permission outside the sealed window
// (permission.AdminStatus/SealStatus/CeremonyStatus), and the embedded
// transport's single code path is that regular, authenticated side of the
// gate -- matching pkg/api/sealedwindow's own documented assumption for
// the embedded transport ("it is always the regular, authenticated side").
// Only the verbs with no OTHER path to succeed while sealed are public.
//
// RedeemInvitation joins this set for a different reason than the rest: it
// is not a sealed-window bootstrap verb (it still runs t.gate and refuses
// with *BarrierSealedError while sealed, exactly like every other
// operation), but it carries NO identity and NO permission check at all --
// the invitation token is the only authority, matching REST's public
// POST /invitations/redeem route, gRPC/unix's RegisterPublicGRPCMethods
// entry, and QUIC's RegisterPublic frame for the same verb.
var publicOperations = map[string]struct{}{
	"Health":                  {},
	"BarrierInitialize":       {},
	"BarrierUnseal":           {},
	"BarrierInitializeShamir": {},
	"OfficerRegister":         {},
	"RecoveryOfficerRegister": {},
	"CeremonyInitialize":      {},
	"CeremonySignCSR":         {},
	"RedeemInvitation":        {},
}

// lifecycleOperations are request-shaped methods that are not requests:
// connecting and closing the transport, the generic Request dispatcher
// (which delegates to the typed, individually-authorized method named by
// its "method" argument -- see requestHandlers in transport.go), deriving a
// database-scoped transport (every one of whose requests is gated in turn
// by the SAME registry and helpers -- database_scope.go copies authMW onto
// the clone), and the "Phase 3 provider" accessors (RBACClient,
// FactorClient, LoginClient, SessionClient, AuditClient, PermissionClient,
// SQLClient) that construct and return a sub-client rather than perform an
// operation themselves. The sub-clients those accessors return ARE covered,
// by subClientCoverage below. VolumePoolClient is NOT one of these:
// transport.VolumePoolClient is implemented directly on *Transport
// (volume_pool.go), with no accessor method of that name at all, so its six
// methods (ListPools, GetPool, CreatePool, UpdatePool, DeletePool,
// ListPoolTenants) are bare keys in operationPermissions like any other
// ambient-self operation.
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
// targetResolvedOperations, identityGatedOperations, publicOperations, or
// lifecycleOperations. A method declared in more than one, or in none,
// fails the build -- so a new operation cannot ship without its
// authorization mechanism stated, and this table cannot silently drift out
// of step with the type it describes.
func TestTransportClientAuthorizationSurface_CoversEveryMethod(t *testing.T) {
	typ := reflect.TypeOf(&Transport{})
	sets := []map[string]struct{}{targetResolvedOperations, identityGatedOperations, publicOperations, lifecycleOperations}

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
				declaredIn = append(declaredIn, "a target/identity/public/lifecycle set")
			}
		}
		switch count {
		case 0:
			t.Errorf("(*Transport).%s is declared in none of operationPermissions, "+
				"targetResolvedOperations, identityGatedOperations, publicOperations, or "+
				"lifecycleOperations: a new embedded operation must state how it is "+
				"authorized before it ships", name)
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
	checkStale("publicOperations", publicOperations)
	checkStale("lifecycleOperations", lifecycleOperations)
	for name := range operationPermissions {
		if isSubClientKey(name) {
			// A dotted name (AuditClient.QueryAudit) belongs to a Phase 3
			// provider sub-client, not *Transport itself -- checked against
			// its OWN type by TestSubClientAuthorizationSurface_CoversEveryMethod.
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
