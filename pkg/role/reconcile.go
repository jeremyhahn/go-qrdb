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

package role

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Reconciliation report
// ---------------------------------------------------------------------------

// RoleReconcileResult records what reconciliation changed for ONE built-in
// role. A result is only present in a report when the role changed, so a
// report with no results is the idempotent case.
type RoleReconcileResult struct {
	// RoleID is the identifier of the role as persisted. For a role that
	// reconciliation created it is the canonical deterministic id; for one
	// that already existed it is whatever id that record carries, which on a
	// cluster seeded before the UUIDv5 migration is the legacy identifier.
	RoleID string `json:"roleID"`

	// Name is the built-in role's name.
	Name string `json:"name"`

	// Created is true when the role was absent from the tenant and
	// reconciliation seeded it. Added then holds its whole permission set.
	Created bool `json:"created"`

	// Added lists the permissions the build's definition grants that the
	// stored record did not carry. Sorted.
	Added []permission.Permission `json:"added,omitempty"`

	// Removed lists the permissions the stored record carried that the
	// build's definition does not grant. Sorted. A removed permission is
	// either retired (no longer registered at all) or simply no longer placed
	// on this role.
	Removed []permission.Permission `json:"removed,omitempty"`
}

// Changed reports whether this result records any change.
func (r RoleReconcileResult) Changed() bool {
	return r.Created || len(r.Added) > 0 || len(r.Removed) > 0
}

// ReconcileReport is the outcome of ReconcileBuiltInRoles for one tenant.
type ReconcileReport struct {
	// TenantID is the tenant whose built-in roles were reconciled.
	TenantID string `json:"tenantID"`

	// Roles holds one result per built-in role that CHANGED, ordered as
	// BuiltInRoles orders them. An unchanged role contributes nothing, so an
	// empty slice means the tenant was already in agreement with the build.
	Roles []RoleReconcileResult `json:"roles,omitempty"`
}

// Changed reports whether reconciliation wrote anything.
func (r *ReconcileReport) Changed() bool {
	return r != nil && len(r.Roles) > 0
}

// ---------------------------------------------------------------------------
// Retired-permission drift, visible on read
// ---------------------------------------------------------------------------

// RetiredPermissionError reports that a stored role carries permissions the
// build's permission registry no longer defines.
//
// It is a SIGNAL, not a failure: no read path returns it, because a listing
// that failed on drift would hide exactly the roles an operator needs to see
// in order to repair them. It is produced on demand by Role.RetiredPermissionError
// so a consumer can classify the drift with one errors.As instead of
// re-deriving the comparison against the registry itself.
type RetiredPermissionError struct {
	// RoleID is the drifted role's identifier.
	RoleID string

	// Name is the drifted role's name.
	Name string

	// TenantID is the tenant holding the drifted role.
	TenantID string

	// Permissions are the stored permissions the registry no longer defines,
	// sorted.
	Permissions []permission.Permission
}

// Error returns a human-readable description of the retired-permission drift.
func (e *RetiredPermissionError) Error() string {
	msg := "role: " + e.Name + " in tenant " + e.TenantID +
		" carries " + strconv.Itoa(len(e.Permissions)) +
		" permission(s) this build no longer defines"
	for i, p := range e.Permissions {
		if i == 0 {
			msg += ": "
		} else {
			msg += ", "
		}
		msg += string(p)
	}
	return msg
}

// RetiredPermissions returns the members of perms that the global permission
// registry no longer defines, sorted. Whole-segment wildcards (e.g. "keys:*")
// are never retired: the RBAC authorizer expands them against concrete
// checks at authorization time, so there is no registry entry for one to lose.
//
// It reads the registry at CALL time, exactly as BuiltInRoles does, so a
// permission a higher layer contributed after this package's init() counts as
// defined rather than as drift.
func RetiredPermissions(perms []permission.Permission) []permission.Permission {
	retired := permission.Unregistered(perms)
	sortPermissions(retired)
	return retired
}

// RetiredPermissionError returns a *RetiredPermissionError describing this
// role's retired permissions, or nil when the role carries none.
//
// The role must have been read through a store, which is what populates
// RetiredPermissions; calling it on a hand-built value reports no drift.
func (r *Role) RetiredPermissionError() error {
	if r == nil || len(r.RetiredPermissions) == 0 {
		return nil
	}
	return &RetiredPermissionError{
		RoleID:      r.ID,
		Name:        r.Name,
		TenantID:    r.TenantID,
		Permissions: append([]permission.Permission(nil), r.RetiredPermissions...),
	}
}

// ---------------------------------------------------------------------------
// Reconciliation
// ---------------------------------------------------------------------------

// ReconcileBuiltInRoles brings the tenant's STORED built-in roles back into
// agreement with BuiltInRoles — the build's live projection of the permission
// registry — and is the ONE sanctioned writer to a system role's permissions.
//
// UpdateRole refuses to re-permission a system role and DeleteRole refuses to
// remove one, deliberately: an operator must not be able to widen `auditor` or
// re-permission `custodian` by hand. That invariant assumed no caller ever
// needed to rewrite a built-in's grants. The assumption expired the first time
// a permission was renamed: roles are copied out of BuiltInRoles when a tenant
// is provisioned and never touched again, so a tenant created before the rename
// keeps the retired verbs forever and never gains the live ones, and every
// write path that could repair it is closed (go-qrdb#354, go-quicbaas#573).
//
// This method is not a relaxation of that invariant, because its target is not
// arbitrary: it is BuiltInRoles itself — by construction the definition the
// guard exists to protect. The caller chooses only WHICH tenant to converge,
// never what to converge it to.
//
// Semantics:
//
//   - A built-in that is ABSENT from the tenant is seeded from its canonical
//     definition, reported with Created and its whole permission set in Added.
//   - A built-in whose stored permission SET differs from the build's has its
//     stored permissions replaced, reported as Added and Removed.
//   - A built-in already in agreement is left completely alone — no write, no
//     UpdatedAt bump, no audit record. A second call therefore performs zero
//     writes and returns an empty report.
//   - A role whose name is not a built-in name is never read and never
//     written. Custom roles, including ones an operator created to work around
//     the drift this repairs, survive reconciliation untouched.
//
// TIMING IS PART OF THE CONTRACT. BuiltInRoles resolves its permission sets
// through the global registry at call time, and modules contribute permissions
// when they register — so a composer must call this AFTER module registration,
// or it will converge every tenant onto a definition missing the modules' own
// verbs and then report success.
func (s *kvRoleStore) ReconcileBuiltInRoles(ctx context.Context, tenantID string) (*ReconcileReport, error) {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	report := &ReconcileReport{TenantID: tenantID}

	for _, canonical := range reconcilableRoles(tenantID) {
		result, err := s.reconcileRole(ctx, tenantID, canonical)
		if err != nil {
			return nil, err
		}
		if result != nil {
			report.Roles = append(report.Roles, *result)
		}
	}

	return report, nil
}

// reconcilableRoles returns the roles ReconcileBuiltInRoles considers, in
// report order: the six canonical built-ins, then Owner.
//
// Owner is included because it drifts for the same reason — its grant is
// permission.All(), so every permission a build adds or retires changes it —
// and IsSystemRoleName covers it, which means UpdateRole refuses to repair it
// exactly as it refuses the other six. It is NOT created when absent (see
// reconcileRole): Owner is provisioned only under the standard RBAC posture,
// so seeding it into a tenant that does not hold it would hand a strict
// separation-of-duties deployment a superuser role reconciliation was never
// asked to create.
func reconcilableRoles(tenantID string) []*Role {
	roles := BuiltInRoles(tenantID)
	return append(roles, OwnerRole(tenantID))
}

// reconcileRole converges ONE built-in role and returns the result, or nil
// when the stored role already agreed with the build.
func (s *kvRoleStore) reconcileRole(ctx context.Context, tenantID string, canonical *Role) (*RoleReconcileResult, error) {
	stored, err := s.GetRoleByName(ctx, tenantID, canonical.Name)
	if err != nil {
		if !isRoleNotFound(err) {
			return nil, err
		}
		return s.seedMissingRole(ctx, tenantID, canonical)
	}

	added, removed := permissionSetDiff(stored.Permissions, canonical.Permissions)
	if len(added) == 0 && len(removed) == 0 {
		// IDEMPOTENCY. The write is skipped entirely rather than performed
		// with an equal value: a no-op Put would still bump UpdatedAt, emit an
		// audit record, and replicate a Raft entry on every boot of every node.
		return nil, nil
	}

	stored.Permissions = append([]permission.Permission(nil), canonical.Permissions...)
	sortPermissions(stored.Permissions)
	stored.UpdatedAt = time.Now().UTC()

	if err := s.putRole(ctx, stored, "reconcile"); err != nil {
		return nil, err
	}

	s.emitAudit(ctx, "reconcile", "role:"+stored.ID, map[string]any{
		"name":        stored.Name,
		"tenantID":    tenantID,
		"added":       permissionStrings(added),
		"removed":     permissionStrings(removed),
		"permissions": len(stored.Permissions),
	})

	return &RoleReconcileResult{
		RoleID:  stored.ID,
		Name:    stored.Name,
		Added:   added,
		Removed: removed,
	}, nil
}

// seedMissingRole creates a built-in that the tenant does not hold, through
// the ordinary CreateRole path so the name index, tenant index and identity
// claim are all written by the code that owns them.
//
// Owner is deliberately not seeded — see reconcilableRoles.
func (s *kvRoleStore) seedMissingRole(ctx context.Context, tenantID string, canonical *Role) (*RoleReconcileResult, error) {
	if canonical.Name == RoleNameOwner {
		return nil, nil
	}

	created, err := s.CreateRole(ctx, &CreateRoleRequest{
		ID:          canonical.ID,
		Name:        canonical.Name,
		TenantID:    tenantID,
		Permissions: canonical.Permissions,
	})
	if err != nil {
		return nil, err
	}

	added := append([]permission.Permission(nil), canonical.Permissions...)
	sortPermissions(added)

	return &RoleReconcileResult{
		RoleID:  created.ID,
		Name:    created.Name,
		Created: true,
		Added:   added,
	}, nil
}

// putRole writes a role's primary record. It is the reconcile path's write:
// UpdateRole cannot serve, because its system-role guard is precisely what
// this operation is sanctioned to bypass, and reconciliation never touches a
// role's name, so no index maintenance is required.
func (s *kvRoleStore) putRole(ctx context.Context, r *Role, op string) error {
	data, err := marshalRole(r)
	if err != nil {
		return &StoreError{Op: op, Detail: "marshal", Err: err}
	}
	if err := s.store.Put(ctx, primaryKey(r.ID), data); err != nil {
		return &StoreError{Op: op, Detail: "put primary", Err: err}
	}
	return nil
}

// ReconcileBuiltInRoles routes to the tenant's own store. The tenant is the
// EFFECTIVE one reconciled from the caller's authenticated identity and the
// requested tenant, so a normal tenant caller can converge only its own
// tenant's built-ins (*tenant.CrossTenantDeniedError otherwise).
func (r *routingRoleService) ReconcileBuiltInRoles(ctx context.Context, tenantID string) (*ReconcileReport, error) {
	eff, err := tenant.EffectiveTenant(ctx, tenantID, "reconcile built-in roles")
	if err != nil {
		return nil, err
	}
	svc, err := r.storeFor(ctx, eff)
	if err != nil {
		return nil, err
	}
	return svc.ReconcileBuiltInRoles(ctx, eff)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// permissionSetDiff compares two permission lists as SETS and returns the
// members of want that have are absent from have (added) and the members of
// have that are absent from want (removed), each sorted. Order carries no
// meaning in a grant and a repeated permission grants nothing extra, which is
// the same reading permissionSetsEqual takes.
func permissionSetDiff(have, want []permission.Permission) (added, removed []permission.Permission) {
	haveSet := make(map[permission.Permission]struct{}, len(have))
	for _, p := range have {
		haveSet[p] = struct{}{}
	}
	wantSet := make(map[permission.Permission]struct{}, len(want))
	for _, p := range want {
		wantSet[p] = struct{}{}
	}

	for p := range wantSet {
		if _, ok := haveSet[p]; !ok {
			added = append(added, p)
		}
	}
	for p := range haveSet {
		if _, ok := wantSet[p]; !ok {
			removed = append(removed, p)
		}
	}

	sortPermissions(added)
	sortPermissions(removed)
	return added, removed
}

// sortPermissions orders a permission slice lexically so a report and a
// stored grant are both deterministic.
func sortPermissions(perms []permission.Permission) {
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
}

// permissionStrings renders a permission slice for an audit record.
func permissionStrings(perms []permission.Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}
	return out
}

// isRoleNotFound reports whether err is the store's not-found answer.
func isRoleNotFound(err error) bool {
	var notFound *RoleNotFoundError
	return errors.As(err, &notFound)
}
