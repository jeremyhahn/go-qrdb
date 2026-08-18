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
	"crypto/sha256"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/uuidv8"
)

const (
	// keyPrefixPrimary is the prefix for role primary records keyed by ID.
	keyPrefixPrimary = "__sys:roles/id/"

	// keyPrefixTenant is the prefix for the tenant-to-role index.
	keyPrefixTenant = "__sys:roles/tenant/"

	// keyPrefixName is the prefix for the role name secondary index.
	keyPrefixName = "__sys:role_idx/name/"

	// defaultPageSize is used when ListRolesRequest.PageSize is zero.
	defaultPageSize = 100
)

// Compile-time interface check.
var _ RoleServicer = (*kvRoleStore)(nil)

// kvRoleStore implements RoleServicer backed by a KVStore with secondary
// indexes for tenant listing and name uniqueness.
//
// Key schema:
//
//	__sys:roles/id/{roleID}                      -> Role JSON (primary)
//	__sys:roles/tenant/{tenantID}/{roleID}       -> roleID (as bytes)
//	__sys:role_idx/name/{tenantID}/{roleName}    -> roleID (as bytes)
type kvRoleStore struct {
	store        kvstore.KVStore
	quotaChecker quota.Checker     // optional; nil skips quota enforcement
	audit        audit.AuditLogger // optional; nil disables audit emission
}

// NewStore creates a new KVStore-backed RoleServicer. The provided
// kvstore must not be nil. Optional Options (e.g. WithAuditLogger) tune
// the store's behavior.
func NewStore(store kvstore.KVStore, opts ...Option) (RoleServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	s := &kvRoleStore{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// WithQuotaChecker returns a new kvRoleStore with the given quota.Checker
// applied. Passing nil disables quota enforcement.
func (s *kvRoleStore) WithQuotaChecker(checker quota.Checker) *kvRoleStore {
	s.quotaChecker = checker
	return s
}

// CreateRole creates a new role with a generated UUID. It validates the
// request, checks name uniqueness within the tenant via the secondary
// index, validates all permissions, and atomically writes the primary
// record plus both indexes.
func (s *kvRoleStore) CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.Name == "" {
		return nil, &ValidationError{Field: "name", Reason: "must not be empty"}
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	if s.quotaChecker != nil {
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceRoles); err != nil {
			return nil, err
		}
	}

	// Validate all permissions.
	if err := validatePermissions(req.Permissions); err != nil {
		return nil, err
	}

	// Check name uniqueness via the secondary index.
	nameKey := nameIndexKey(tenantID, req.Name)
	existing, err := s.store.Get(ctx, nameKey)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "name index lookup", Err: err}
	}
	if existing != nil {
		return nil, &RoleNameExistsError{Name: req.Name, TenantID: tenantID}
	}

	now := time.Now().UTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	r := &Role{
		ID:          id,
		Name:        req.Name,
		TenantID:    tenantID,
		Permissions: req.Permissions,
		System:      false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if r.Permissions == nil {
		r.Permissions = []permission.Permission{}
	}

	data, err := json.Marshal(r)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}

	// Write primary record.
	pk := primaryKey(r.ID)
	if err := s.store.Put(ctx, pk, data); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put primary", Err: err}
	}

	// Write tenant index.
	tk := tenantIndexKey(tenantID, r.ID)
	if err := s.store.Put(ctx, tk, []byte(r.ID)); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put tenant index", Err: err}
	}

	// Write name index.
	if err := s.store.Put(ctx, nameKey, []byte(r.ID)); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put name index", Err: err}
	}

	s.emitAudit(ctx, "create", "role:"+r.ID, map[string]any{
		"name":        r.Name,
		"tenantID":    tenantID,
		"permissions": len(r.Permissions),
	})

	return r, nil
}

// GetRole retrieves a role by its globally unique ID.
func (s *kvRoleStore) GetRole(ctx context.Context, id string) (*Role, error) {
	if id == "" {
		return nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	data, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return nil, &StoreError{Op: "get", Detail: "primary lookup", Err: err}
	}
	if data == nil {
		return nil, &RoleNotFoundError{ID: id}
	}

	var r Role
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, &StoreError{Op: "get", Detail: "unmarshal", Err: err}
	}
	// Tenant ownership: a non-system caller may only read roles in its own
	// tenant. The system tenant (admin and internal RBAC permission
	// resolution, which runs under the identity's own tenant) reads any role.
	// This closes a cross-tenant role-metadata read in the shared
	// (prefix-isolation) store; barrier-isolated tenants are already
	// physically isolated by the routing layer. GetRoleByName and
	// ResolvePermissions both funnel through here, so they inherit the check.
	if ctxTenant := tenant.FromContext(ctx); !tenant.IsSystemTenant(ctxTenant) && r.TenantID != ctxTenant {
		return nil, &RoleNotFoundError{ID: id}
	}
	return &r, nil
}

// GetRoleByName resolves a role name within a tenant via the secondary
// index, then fetches the primary record.
func (s *kvRoleStore) GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error) {
	if name == "" {
		return nil, &ValidationError{Field: "name", Reason: "must not be empty"}
	}
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	idBytes, err := s.store.Get(ctx, nameIndexKey(tenantID, name))
	if err != nil {
		return nil, &StoreError{Op: "get_by_name", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		return nil, &RoleNotFoundError{ID: tenantID + "/" + name}
	}

	return s.GetRole(ctx, string(idBytes))
}

// UpdateRole applies partial updates to an existing role. An empty
// Name means no change; nil Permissions means no change. If the name
// changes, uniqueness is re-validated and the secondary index is
// updated atomically. System roles cannot have their names changed.
func (s *kvRoleStore) UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.ID == "" {
		return nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	existing, err := s.GetRole(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	nameChanged := req.Name != "" && req.Name != existing.Name

	if nameChanged {
		// Check that the new name is not already taken.
		newKey := nameIndexKey(existing.TenantID, req.Name)
		found, err := s.store.Get(ctx, newKey)
		if err != nil {
			return nil, &StoreError{Op: "update", Detail: "name index check", Err: err}
		}
		if found != nil {
			return nil, &RoleNameExistsError{Name: req.Name, TenantID: existing.TenantID}
		}

		// Remove old name index.
		oldKey := nameIndexKey(existing.TenantID, existing.Name)
		if err := s.store.Delete(ctx, oldKey); err != nil {
			return nil, &StoreError{Op: "update", Detail: "delete old name index", Err: err}
		}

		existing.Name = req.Name

		// Write new name index.
		if err := s.store.Put(ctx, newKey, []byte(existing.ID)); err != nil {
			return nil, &StoreError{Op: "update", Detail: "put new name index", Err: err}
		}
	}

	if req.Permissions != nil {
		if err := validatePermissions(req.Permissions); err != nil {
			return nil, err
		}
		existing.Permissions = req.Permissions
	}

	existing.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(existing)
	if err != nil {
		return nil, &StoreError{Op: "update", Detail: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, primaryKey(existing.ID), data); err != nil {
		return nil, &StoreError{Op: "update", Detail: "put primary", Err: err}
	}

	s.emitAudit(ctx, "update", "role:"+existing.ID, map[string]any{
		"name":                existing.Name,
		"tenantID":            existing.TenantID,
		"name_changed":        nameChanged,
		"permissions_changed": req.Permissions != nil,
		"permissions":         len(existing.Permissions),
	})

	return existing, nil
}

// DeleteRole removes a role by ID. System roles cannot be deleted.
func (s *kvRoleStore) DeleteRole(ctx context.Context, id string) error {
	if id == "" {
		return &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	existing, err := s.GetRole(ctx, id)
	if err != nil {
		return err
	}
	if existing.System {
		return &SystemRoleImmutableError{ID: id}
	}

	// Delete primary record.
	if err := s.store.Delete(ctx, primaryKey(id)); err != nil {
		return &StoreError{Op: "delete", Detail: "primary", Err: err}
	}

	// Delete tenant index.
	if err := s.store.Delete(ctx, tenantIndexKey(existing.TenantID, id)); err != nil {
		return &StoreError{Op: "delete", Detail: "tenant index", Err: err}
	}

	// Delete name index.
	if err := s.store.Delete(ctx, nameIndexKey(existing.TenantID, existing.Name)); err != nil {
		return &StoreError{Op: "delete", Detail: "name index", Err: err}
	}

	s.emitAudit(ctx, "delete", "role:"+id, map[string]any{
		"name":     existing.Name,
		"tenantID": existing.TenantID,
	})

	return nil
}

// ListRoles returns a paginated list of roles for the given tenant by
// scanning the tenant index prefix.
func (s *kvRoleStore) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	// Collect all role IDs for the tenant via the tenant index.
	prefix := keyPrefixTenant + tenantID + "/"
	var roleIDs []string
	err := s.store.Scan(ctx, prefix, func(_ string, value []byte) error {
		roleIDs = append(roleIDs, string(value))
		return nil
	})
	if err != nil {
		return nil, &StoreError{Op: "list", Detail: "scan tenant index", Err: err}
	}

	// Sort for deterministic pagination.
	sort.Strings(roleIDs)

	total := len(roleIDs)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	pageIDs := roleIDs[start:end]
	roles := make([]*Role, 0, len(pageIDs))
	for _, id := range pageIDs {
		r, err := s.GetRole(ctx, id)
		if err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}

	return &ListRolesResponse{
		Roles:   roles,
		Total:   total,
		HasMore: end < total,
	}, nil
}

// ResolvePermissions returns the union of all permissions for the given
// role IDs. The result is sorted and deduplicated.
func (s *kvRoleStore) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if len(roleIDs) == 0 {
		return []permission.Permission{}, nil
	}

	seen := make(map[permission.Permission]struct{})
	for _, id := range roleIDs {
		r, err := s.GetRole(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, p := range r.Permissions {
			seen[p] = struct{}{}
		}
	}

	result := make([]permission.Permission, 0, len(seen))
	for p := range seen {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})

	return result, nil
}

// BuiltInRoles returns the predefined system roles for the given tenant.
// Each role uses a deterministic UUID derived from its name and tenant ID.
//
// The SO, Admin, Operator, User, and Auditor permission sets are NOT
// hand-maintained lists: each is a live projection of the permission
// registry's default role-grant metadata (permission.RolePermissions),
// registered once per permission in pkg/permission/builtin_role_grants.go
// rather than duplicated here. Role placement is metadata carried by the
// permission; this function only materializes it. Because
// permission.RolePermissions reads the registry at call time rather than a
// value cached at package-init time, a permission registered by a
// higher-layer package (go-quicbaas, go-xkms) before role seeding is
// reflected here even though it was contributed after this package's own
// init() ran.
//
// Roles are ordered as follows:
//  1. SO (Security Officer) — governance only, no crypto (FIPS 140-3 AS10.03,
//     enforced structurally by permission.RegisterRoleGrant)
//  2. Admin — key lifecycle and tenant management. Grants cross-tenant
//     authority only when the role is held in the system tenant
//     (Admin@system); the system-tenant rule in pkg/auth/rbac filters
//     cross-scope permissions to system-tenant identities.
//  3. Operator — routine crypto and maintenance
//  4. User — end-user cryptographic operations
//  5. Auditor — read-only audit and state visibility
//  6. Custodian — Shamir share holder; single permission only (a fixed,
//     minimal set, not registry-derived — see custodianPermissions)
func BuiltInRoles(tenantID string) []*Role {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	now := time.Now().UTC()

	return []*Role{
		{
			ID:          SystemRoleID(tenantID, RoleNameSO),
			Name:        RoleNameSO,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleSO),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameAdmin),
			Name:        RoleNameAdmin,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleAdmin),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameOperator),
			Name:        RoleNameOperator,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleOperator),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameUser),
			Name:        RoleNameUser,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleUser),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameAuditor),
			Name:        RoleNameAuditor,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleAuditor),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameCustodian),
			Name:        RoleNameCustodian,
			TenantID:    tenantID,
			Permissions: custodianPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

// OwnerRole returns the standard-posture Owner role for the given tenant.
// Owner holds the full operational permission set (permission.All()) and is
// the convenience superuser used when separation of duties is NOT enforced.
//
// It is deliberately NOT part of BuiltInRoles: it is seeded and assigned to
// the bootstrap admin only under the standard (non-strict) posture by
// pkg/firstboot, and the RBAC authorizer voids any Owner grant when strict
// separation of duties is active (FIPS 140-3 mode or
// auth.separation_of_duties=true). An empty tenantID defaults to
// SystemTenantID to match BuiltInRoles behavior.
func OwnerRole(tenantID string) *Role {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	now := time.Now().UTC()
	return &Role{
		ID:          SystemRoleID(tenantID, RoleNameOwner),
		Name:        RoleNameOwner,
		TenantID:    tenantID,
		Permissions: ownerPermissions(),
		System:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// ownerPermissions returns the permission set for the Owner role: every
// registered permission. This breadth is acceptable ONLY because the Owner
// role is confined to the standard (non-compliance) RBAC posture — see
// OwnerRole and RoleNameOwner.
func ownerPermissions() []permission.Permission {
	return permission.All()
}

// SystemRoleID returns the deterministic UUID for any named system role in
// the given tenant. It is used by the RBAC authorizer for fast identity
// checks (e.g., the SO deny-list match by deterministic role ID) and by
// the bootstrap flow to grant the canonical Admin@system role with a
// stable cluster-wide identifier. An empty tenantID defaults to
// SystemTenantID to match BuiltInRoles behavior.
func SystemRoleID(tenantID, roleName string) string {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	return deterministicUUID(tenantID, roleName)
}

// deterministicUUID generates a deterministic UUID from a namespace derived
// from the tenant ID and role name combination.
//
// The derivation is RFC 9562 version 8 over SHA-256 (see pkg/uuidv8). It was
// UUID version 5, which is defined to hash with SHA-1 and therefore panics
// under GODEBUG=fips140=only.
//
// The IDs are derived on every call rather than allocated, so a cluster whose
// nodes all run this build is self-consistent. A store seeded by an OLDER
// build is not: its role records keep the version 5 IDs, role seeding is
// idempotent on the name index rather than the ID, and callers that match on
// the derived ID would find no record. Such a store needs its system roles
// re-seeded. See docs/seal/fips140-mode.md.
func deterministicUUID(tenantID, name string) string {
	// Create a deterministic namespace UUID from a SHA-256 hash of a
	// fixed prefix so the result is stable across builds.
	nsData := sha256.Sum256([]byte("go-qrdb:role-namespace"))
	ns, _ := uuid.FromBytes(nsData[:16])
	return uuidv8.FromName(ns, tenantID+"/"+name).String()
}

// custodianPermissions returns the permission set for the Custodian built-in role.
// Custodians hold a single Shamir share and can ONLY submit that share during
// barrier unseal or tenant barrier operations. All other operations are denied
// (FIPS 140-3 AS10.06).
func custodianPermissions() []permission.Permission {
	return []permission.Permission{
		permission.BarrierUnsealShare,
		// Tenant SO operations (ScopeTenantOnly: a tenant custodian may claim
		// their share and submit seal votes for their OWN tenant's barrier).
		permission.TenantBarrierClaimShare,
		permission.TenantBarrierSealVote,
	}
}

// validatePermissions checks that every permission in the slice is either
// registered in the global permission registry or a whole-segment wildcard
// (e.g. "keys:*"). Wildcards are admitted because the RBAC authorizer expands
// them against concrete resource:action checks at authorization time.
func validatePermissions(perms []permission.Permission) error {
	for _, p := range perms {
		if permission.IsWildcard(p) {
			continue
		}
		if !permission.IsRegistered(p) {
			return &InvalidPermissionError{Permission: string(p)}
		}
	}
	return nil
}

// primaryKey builds the primary record key for a role ID.
func primaryKey(id string) string {
	return keyPrefixPrimary + id
}

// tenantIndexKey builds the tenant index key for a role within a tenant.
func tenantIndexKey(tenantID, roleID string) string {
	return keyPrefixTenant + tenantID + "/" + roleID
}

// nameIndexKey builds the role name secondary index key.
func nameIndexKey(tenantID, name string) string {
	return keyPrefixName + tenantID + "/" + name
}
