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
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// isSubClientKey reports whether name is a dotted operationPermissions key
// belonging to a Phase 3 provider sub-client (e.g. "AuditClient.QueryAudit")
// rather than a bare *Transport method name.
func isSubClientKey(name string) bool {
	return strings.Contains(name, ".")
}

// subClientTargetResolvedOperations are sub-client methods authorized
// against a target other than the caller's own tenant -- FactorClient's
// userID-scoped MFA methods, via authorizeUserTarget (factor.go).
var subClientTargetResolvedOperations = map[string]struct{}{
	"FactorClient.Enroll": {},
	"FactorClient.List":   {},
	"FactorClient.Remove": {},
	"FactorClient.Verify": {},
}

// subClientIdentityGatedOperations are sub-client methods requiring an
// authenticated caller but no specific permission.
var subClientIdentityGatedOperations = map[string]struct{}{
	"RBACClient.ListOwnPermissions": {},
}

// subClientPublicOperations are sub-client methods reachable with no
// identity at all:
//
//   - LoginClient/SessionClient resolve through the bearer TOKEN passed as
//     an explicit argument, not an ambient ctx identity -- the token IS the
//     credential, exactly as a network transport's session middleware
//     validates a bearer with no separate RBAC permission. BeginLogin and
//     FinishLogin must be reachable with no identity at all: you cannot be
//     required to already be logged in to log in.
//   - AuditClient.EmitAudit stamps the caller's own tenant and runs no
//     permission check on any transport -- see its own doc comment in
//     audit.go.
//   - RBACClient.Authorize is a policy-evaluation PRIMITIVE deciding about
//     an explicit identity ARGUMENT, never an ambient caller identity read
//     from ctx -- see its own doc comment in rbac.go for why there is no
//     caller to additionally authenticate.
var subClientPublicOperations = map[string]struct{}{
	"LoginClient.ResolveIdentity":   {},
	"LoginClient.BeginLogin":        {},
	"LoginClient.FinishLogin":       {},
	"LoginClient.Logout":            {},
	"SessionClient.ResolveIdentity": {},
	"SessionClient.Revoke":          {},
	"AuditClient.EmitAudit":         {},
	"RBACClient.Authorize":          {},
}

// subClientTypes names every concrete type a "Phase 3 provider" accessor on
// *Transport returns, with the dotted-key prefix operationPermissions and
// the sets above use for it. Extending the coverage/behavioral gates to a
// new sub-client means adding one entry here -- the SQLClient/
// UserAuthFactorClient accessors are not here because they return the
// *Transport itself (see their own doc comments), so their methods are
// already covered by TestTransportClientAuthorizationSurface_CoversEveryMethod.
// VolumePoolClient is the same shape: transport.VolumePoolClient is
// implemented directly on *Transport (volume_pool.go), not a dotted
// sub-client, so its bare method keys (ListPools, GetPool, ...) belong to
// that other test too, not here.
var subClientTypes = []struct {
	prefix string
	typ    reflect.Type
}{
	{"FactorClient.", reflect.TypeOf(&factorClient{})},
	{"RBACClient.", reflect.TypeOf(&rbacClient{})},
	{"AuditClient.", reflect.TypeOf(&auditClient{})},
	{"LoginClient.", reflect.TypeOf(&loginClient{})},
	{"SessionClient.", reflect.TypeOf(&sessionClient{})},
	{"PermissionClient.", reflect.TypeOf(&permissionClient{})},
}

// TestSubClientAuthorizationSurface_CoversEveryMethod is
// TestTransportClientAuthorizationSurface_CoversEveryMethod's counterpart
// for the "Phase 3 provider" sub-clients (FactorClient, RBACClient,
// AuditClient, LoginClient, SessionClient, VolumePoolClient,
// PermissionClient), which live on their own concrete types and so are
// invisible to reflect.TypeOf(&Transport{}). Every exported method of each
// must be declared, under its "Type.Method" dotted key, in exactly one of
// operationPermissions, subClientTargetResolvedOperations,
// subClientIdentityGatedOperations, or subClientPublicOperations.
func TestSubClientAuthorizationSurface_CoversEveryMethod(t *testing.T) {
	sets := []map[string]struct{}{subClientTargetResolvedOperations, subClientIdentityGatedOperations, subClientPublicOperations}

	seenKeys := make(map[string]bool)
	for _, sc := range subClientTypes {
		for i := 0; i < sc.typ.NumMethod(); i++ {
			m := sc.typ.Method(i)
			key := sc.prefix + m.Name
			seenKeys[key] = true
			count := 0
			var declaredIn []string
			if _, ok := operationPermissions[key]; ok {
				count++
				declaredIn = append(declaredIn, "operationPermissions")
			}
			for _, set := range sets {
				if _, ok := set[key]; ok {
					count++
					declaredIn = append(declaredIn, "a target/identity/public set")
				}
			}
			switch count {
			case 0:
				t.Errorf("(%s) is declared in none of operationPermissions, "+
					"subClientTargetResolvedOperations, subClientIdentityGatedOperations, or "+
					"subClientPublicOperations: a new sub-client operation must state how it "+
					"is authorized before it ships", key)
			case 1:
				// exactly one -- correct.
			default:
				t.Errorf("(%s) is declared in %d authorization sets (%v): it must be "+
					"declared in exactly one", key, count, declaredIn)
			}
		}
	}

	// Reverse: every declared dotted key must name a REAL method on the
	// type its prefix claims -- "the registry key matches its method
	// name". A typo (AuditClient.QueryAudi) or a renamed/removed method
	// would otherwise sit in a declaration set forever, silently never
	// enforced because the real method it meant to name is undeclared.
	checkKeysResolve := func(setName string, set map[string]struct{}) {
		for key := range set {
			if !seenKeys[key] {
				t.Errorf("%s declares %q, which does not match any current sub-client "+
					"method (wrong prefix, a typo, or a renamed/removed method)", setName, key)
			}
		}
	}
	checkKeysResolve("subClientTargetResolvedOperations", subClientTargetResolvedOperations)
	checkKeysResolve("subClientIdentityGatedOperations", subClientIdentityGatedOperations)
	checkKeysResolve("subClientPublicOperations", subClientPublicOperations)
	for key := range operationPermissions {
		if !isSubClientKey(key) {
			continue
		}
		if !seenKeys[key] {
			t.Errorf("operationPermissions declares %q, which does not match any current "+
				"sub-client method (wrong prefix, a typo, or a renamed/removed method)", key)
		}
	}
}

// ---------------------------------------------------------------------------
// The BEHAVIORAL gate: enforcement, not declaration.
//
// The two coverage tests above (this file's and
// transport_authz_coverage_test.go's) only prove that every method STATES a
// mechanism. They pass identically whether the method actually CALLS
// authorize()/gate() or merely has a comment claiming it does -- exactly
// the gap a reviewer would call "checking declaration, not enforcement".
//
// This test closes it for the mechanically uniform class: every bare or
// dotted key in operationPermissions. Each one is invoked for REAL, through
// reflection, against a REAL AuthMiddleware backed by REAL RBAC
// (authtest.BuiltInRoles -- never a stub authorizer, which could assert the
// defect was correct and stay green), with an identity holding NO
// permissions at all, and the call MUST be refused with the actual RBAC
// decision (*rbac.PermissionDeniedError, wrapping auth.ErrUnauthorized) --
// not merely "some non-nil error", which an unrelated nil-pointer panic or
// a not-implemented stub would also produce. Deleting the authorize() call
// from any one of these operations turns its case red.
//
// It is deliberately scoped to operationPermissions rather than EVERY
// declared operation. That set is where authorize()/t.authorize runs as the
// FIRST substantive statement after gate(), in every single case (verified
// by inspection when this test was written, and enforced going forward by
// authorize()'s fail-closed unknown-key behavior -- see its own doc
// comment), so a zero-valued synthetic call reaches the SAME check a real
// one would, for every key, with no bespoke per-method setup. The
// target-resolved and identity-gated operations (tenant/user/role CRUD,
// FactorClient, share-claim/quorum) need real service mocks and seeded
// records to behave meaningfully and already have dedicated behavioral
// tests proving denial with real roles: tenant_authz_realroles_test.go,
// probe_kv_users_roles_test.go, role_assignment_test.go,
// cert_confinement_test.go.
func TestAmbientOperations_DenyANoPermissionIdentity(t *testing.T) {
	roles := authtest.BuiltInRoles(t, "acme", tenantid.System)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))
	tr := newExtTransport(t, WithAuthMiddleware(mw))

	// An authenticated identity holding no role at all: ResolvePermissions
	// of an empty role set is empty, so every permission check denies it,
	// regardless of the permission's scope (ScopeSelf, ScopeCross,
	// ScopeTenantOnly all require a GRANT the identity does not have).
	noPerm := ctxFor(&auth.Identity{Subject: "nobody", TenantID: "acme", Roles: nil})

	for key := range operationPermissions {
		if _, gated := buildTagGatedMethods[key]; gated && !isSubClientKey(key) {
			if _, ok := reflect.TypeOf(tr).MethodByName(key); !ok {
				continue // not compiled into this build; see buildTagGatedMethods' comment.
			}
		}
		t.Run(key, func(t *testing.T) {
			recv, name := resolveReceiver(t, tr, key)
			err := callZeroArgs(t, recv, name, noPerm)
			if err == nil {
				t.Fatalf("%s: succeeded for an identity holding no permissions at all", key)
			}
			var denied *rbac.PermissionDeniedError
			if !errors.As(err, &denied) && !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("%s: err = %v (%T); want the RBAC permission denial "+
					"(*rbac.PermissionDeniedError / auth.ErrUnauthorized), not an unrelated "+
					"failure -- authorize() may not be running first", key, err, err)
			}
		})
	}
}

// resolveReceiver returns the reflect.Value to call key's method on, and
// the bare method name: tr itself for a bare key, or a freshly constructed
// sub-client wrapping tr for a dotted one (matching the accessor each
// "Phase 3 provider" method on *Transport returns).
func resolveReceiver(t *testing.T, tr *Transport, key string) (reflect.Value, string) {
	t.Helper()
	prefix, name, dotted := strings.Cut(key, ".")
	if !dotted {
		return reflect.ValueOf(tr), prefix
	}
	switch prefix + "." {
	case "AuditClient.":
		return reflect.ValueOf(&auditClient{transport: tr}), name
	case "RBACClient.":
		return reflect.ValueOf(&rbacClient{transport: tr}), name
	case "PermissionClient.":
		return reflect.ValueOf(&permissionClient{transport: tr}), name
	default:
		t.Fatalf("resolveReceiver: %q has no known sub-client prefix", key)
		return reflect.Value{}, ""
	}
}

// callZeroArgs invokes recv's method named name with ctx as the first
// argument and the zero value for every other parameter (and none of the
// variadic tail, when the method is variadic), and returns the LAST return
// value if it is an error. Every method this file calls returns (T, error)
// or just error, by this package's own convention (CLAUDE.md: "error as
// the last return value"), so the last result is always checked.
//
// A panic (a nil-pointer dereference reached before the authorization
// check, for instance) is NOT swallowed: it fails the test with the same
// message a benign-looking nil would have hidden, because the whole point
// of this gate is that an authorization check must run before anything
// that could panic on zero-valued input.
func callZeroArgs(t *testing.T, recv reflect.Value, name string, ctx context.Context) error {
	t.Helper()
	method := recv.MethodByName(name)
	if !method.IsValid() {
		t.Fatalf("callZeroArgs: %s has no method %q", recv.Type(), name)
	}
	mt := method.Type()
	fixedIn := mt.NumIn()
	if mt.IsVariadic() {
		fixedIn--
	}
	args := make([]reflect.Value, fixedIn)
	for i := 0; i < fixedIn; i++ {
		if i == 0 {
			args[i] = reflect.ValueOf(ctx)
			continue
		}
		args[i] = reflect.Zero(mt.In(i))
	}
	results := method.Call(args)
	if len(results) == 0 {
		return nil
	}
	last := results[len(results)-1]
	if last.IsNil() {
		return nil
	}
	err, ok := last.Interface().(error)
	if !ok {
		t.Fatalf("callZeroArgs: %s.%s's last return value is not an error", recv.Type(), name)
	}
	return err
}

// TestOperationPermissions_UnknownKeyFailsClosed pins authorize()'s fail
// behavior directly: an unregistered method key, with an AuthMiddleware
// installed, is refused with *UnregisteredOperationError rather than
// admitted. This is the production floor the two coverage tests above
// cannot see for themselves -- they prove no CURRENT method calls authorize
// with an unregistered key, not what happens if one ever did.
func TestOperationPermissions_UnknownKeyFailsClosed(t *testing.T) {
	roles := authtest.BuiltInRoles(t, tenantid.System)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))
	tr := newExtTransport(t, WithAuthMiddleware(mw))

	systemAdmin := ctxFor(authtest.AdminIdentity(tenantid.System, "root"))
	err := tr.authorize(systemAdmin, "NotARealRegisteredOperation")
	var unreg *UnregisteredOperationError
	if !errors.As(err, &unreg) {
		t.Fatalf("err = %v (%T); want *UnregisteredOperationError", err, err)
	}
	if unreg.Method != "NotARealRegisteredOperation" {
		t.Errorf("Method = %q, want the key passed in", unreg.Method)
	}
}

// TestOperationPermissions_UnknownKeyAdmitsWithNoMiddleware pins the OTHER
// half: with no AuthMiddleware configured, RBAC is opt-in and an
// unregistered key is admitted exactly like a registered one would be --
// matching a network transport run with none configured.
func TestOperationPermissions_UnknownKeyAdmitsWithNoMiddleware(t *testing.T) {
	tr := newExtTransport(t)
	if err := tr.authorize(context.Background(), "NotARealRegisteredOperation"); err != nil {
		t.Fatalf("err = %v, want nil (no AuthMiddleware configured)", err)
	}
}
