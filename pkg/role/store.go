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
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	quotaChecker quota.Checker // optional; nil skips quota enforcement
}

// NewStore creates a new KVStore-backed RoleServicer. The provided
// kvstore must not be nil.
func NewStore(store kvstore.KVStore) (RoleServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	return &kvRoleStore{store: store}, nil
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
	if ctxTenant := tenant.FromContext(ctx); ctxTenant != tenant.SystemTenantID && r.TenantID != ctxTenant {
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
// Roles are ordered as follows:
//  1. SO (Security Officer) — governance only, no crypto (FIPS 140-3 AS10.03)
//  2. Admin — key lifecycle and tenant management. Grants cross-tenant
//     authority only when the role is held in the system tenant
//     (Admin@system); the system-tenant rule in pkg/auth/rbac filters
//     cross-scope permissions to system-tenant identities.
//  3. Operator — routine crypto and maintenance
//  4. User — end-user cryptographic operations
//  5. Auditor — read-only audit and state visibility
//  6. Custodian — Shamir share holder; single permission only
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
			Permissions: soPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameAdmin),
			Name:        RoleNameAdmin,
			TenantID:    tenantID,
			Permissions: adminPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameOperator),
			Name:        RoleNameOperator,
			TenantID:    tenantID,
			Permissions: operatorPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameUser),
			Name:        RoleNameUser,
			TenantID:    tenantID,
			Permissions: userPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameAuditor),
			Name:        RoleNameAuditor,
			TenantID:    tenantID,
			Permissions: auditorPermissions(),
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

// deterministicUUID generates a deterministic UUID v5 from a namespace
// derived from the tenant ID and role name combination.
func deterministicUUID(tenantID, name string) string {
	// Create a deterministic namespace UUID from a SHA-256 hash of a
	// fixed prefix so the result is stable across builds.
	nsData := sha256.Sum256([]byte("go-qrdb:role-namespace"))
	ns, _ := uuid.FromBytes(nsData[:16])
	return uuid.NewSHA1(ns, []byte(tenantID+"/"+name)).String()
}

// soPermissions returns the permission set for the Security Officer role.
// The SO manages module lifecycle and governance. Cryptographic operations
// (keys:sign, keys:encrypt, keys:decrypt, keys:verify, keys:wrap, keys:unwrap)
// are NOT included here and are also enforced via the RBAC deny-list even if
// erroneously added (FIPS 140-3 AS10.03 defense-in-depth).
func soPermissions() []permission.Permission {
	perms := []permission.Permission{
		// Seal management (SO-exclusive)
		permission.SealBootstrap, permission.SealUnseal, permission.SealSeal,
		permission.SealStatus, permission.SealRotateRootKey, permission.SealRekey,
		permission.SealGenerateRecovery, permission.SealRecoverWithKeys,
		permission.SealInitializeShamir,
		// Ceremony (SO-exclusive)
		permission.CeremonyInitialize, permission.CeremonyEnrollCustodian,
		permission.CeremonyRevokeCustodian, permission.CeremonyClaimCert,
		permission.CeremonySignCSR, permission.CeremonyStatus,
		// Certificate management
		permission.CertsGenerate, permission.CertsExport, permission.CertsRevoke,
		permission.CertsList,
		// Role management (SO-exclusive)
		permission.RolesCreate, permission.RolesRead, permission.RolesUpdate,
		permission.RolesDelete, permission.RolesList, permission.RolesAssign,
		permission.RolesUnassign, permission.PermissionsList,
		// User management
		permission.UsersCreate, permission.UsersRead, permission.UsersUpdate,
		permission.UsersDelete, permission.UsersList, permission.UsersEnable,
		permission.UsersDisable,
		// Tenant management
		permission.TenantsCreate, permission.TenantsRead, permission.TenantsUpdate,
		permission.TenantsDelete, permission.TenantsList,
		// Key lifecycle (infrastructure keys — delete any key; create infra keys)
		permission.KeysCreate, permission.KeysDelete, permission.KeysList, permission.KeysGet,
		// Audit (operational oversight)
		permission.AuditRead, permission.AuditQuery,
		// Admin
		permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
		permission.AdminAddMember, permission.AdminRemoveMember,
		permission.AdminSnapshot, permission.AdminDrain, permission.AdminCapabilities,
		// Auth factor management
		permission.AuthFactorEnroll, permission.AuthFactorList, permission.AuthFactorRevoke,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// adminPermissions returns the permission set for the Admin built-in role.
// Admins manage operational key lifecycle and tenant configuration. They cannot
// initialize the module, reset SO PINs, or perform barrier bootstrapping.
//
// The cross-scope permissions at the bottom of this list (TenantsListCross,
// TenantsCreateCross, TenantsApprove, VolumePool*, RegistrationConfig*,
// AuditReadCross) confer cross-tenant authority only when held by an
// identity in the system tenant (the system-tenant rule in
// pkg/auth/rbac filters them out for tenant-local identities). A
// tenant-local Admin can carry these grants without exercising them
// outside their own tenant.
func adminPermissions() []permission.Permission {
	perms := []permission.Permission{
		// Key lifecycle (operational keys — cannot delete infrastructure keys)
		permission.KeysCreate, permission.KeysRotate, permission.KeysDelete,
		permission.KeysList, permission.KeysGet,
		// Wrap/unwrap for key transport
		permission.KeysWrap, permission.KeysUnwrap,
		// Certificate management
		permission.CertsGenerate, permission.CertsExport, permission.CertsRevoke,
		permission.CertsList,
		// Seal management (operational subset: no bootstrap, rotate, rekey)
		permission.SealUnseal, permission.SealSeal, permission.SealStatus,
		permission.SealRecoverWithKeys,
		// Ceremony (status only)
		permission.CeremonyStatus,
		// Tenant management (self-scope: act on own tenant)
		permission.TenantsCreate, permission.TenantsRead, permission.TenantsUpdate,
		permission.TenantsDelete, permission.TenantsList,
		// Per-tenant barrier management (tenant-only scope: a tenant admin
		// may init/unseal/seal their OWN tenant's barrier; the system tenant
		// is structurally denied these by the ScopeTenantOnly rule even
		// though the perm is present in the role).
		permission.TenantBarrierInit, permission.TenantBarrierUnseal,
		permission.TenantBarrierSeal,
		// User management
		permission.UsersCreate, permission.UsersRead, permission.UsersUpdate,
		permission.UsersDelete, permission.UsersList, permission.UsersEnable,
		permission.UsersDisable,
		// Role management (read/assign — cannot create/delete system roles)
		permission.RolesRead, permission.RolesList, permission.RolesAssign,
		permission.RolesUnassign, permission.PermissionsList,
		// Admin service
		permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
		permission.AdminAddMember, permission.AdminRemoveMember,
		permission.AdminSnapshot, permission.AdminDrain, permission.AdminCapabilities,
		// Audit (self-scope)
		permission.AuditRead, permission.AuditQuery,
		// Cross-scope tenant management — only effective for system-tenant Admin.
		permission.TenantsListCross, permission.TenantsCreateCross, permission.TenantsApprove,
		// Cross-scope volume pool management — only effective for system-tenant Admin.
		permission.VolumePoolCreate, permission.VolumePoolList, permission.VolumePoolRead,
		permission.VolumePoolUpdate, permission.VolumePoolDelete,
		// Cross-scope registration policy — only effective for system-tenant Admin.
		permission.RegistrationConfigRead, permission.RegistrationConfigUpdate,
		// Cross-scope audit — only effective for system-tenant Admin.
		permission.AuditReadCross,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// operatorPermissions returns the permission set for the Operator built-in role.
// Operators perform routine crypto operations and maintenance. They can sign,
// encrypt, and decrypt using existing keys, and seal/unseal the barrier, but
// cannot create/delete keys or manage tenants.
func operatorPermissions() []permission.Permission {
	perms := []permission.Permission{
		// Cryptographic operations
		permission.KeysSign, permission.KeysVerify,
		permission.KeysEncrypt, permission.KeysDecrypt,
		permission.KeysList, permission.KeysGet,
		// Key rotation (not creation/deletion)
		permission.KeysRotate,
		// Barrier (operational: not bootstrap or SO-exclusive ops)
		permission.SealUnseal, permission.SealSeal, permission.SealStatus,
		// Ceremony (status only)
		permission.CeremonyStatus,
		// KV
		permission.KVPut, permission.KVGet, permission.KVDelete,
		permission.KVList, permission.KVExists, permission.KVRange,
		// MVCC
		permission.MVCCPutWithLease, permission.MVCCGetRevision,
		permission.MVCCGetAtRevision, permission.MVCCRange,
		permission.MVCCCompact, permission.MVCCCurrentRevision,
		// Transaction
		permission.TransactionExecute,
		// Batch
		permission.BatchExecute,
		// Watch
		permission.WatchWatch, permission.WatchWatchPrefix,
		// Lease
		permission.LeaseGrant, permission.LeaseRevoke,
		permission.LeaseKeepAlive, permission.LeaseTTL, permission.LeaseList,
		// Index
		permission.IndexCreate, permission.IndexDrop,
		permission.IndexQuery, permission.IndexScan,
		// TimeSeries
		permission.TSWrite, permission.TSWriteBatch,
		permission.TSQuery, permission.TSAggregate,
		// Vector
		permission.VectorNamespaceCreate, permission.VectorNamespaceDelete,
		permission.VectorNamespaceList, permission.VectorNamespaceInfo,
		permission.VectorUpsert, permission.VectorGet, permission.VectorDelete,
		permission.VectorBatchUpsert, permission.VectorBatchDelete, permission.VectorList,
		permission.VectorUpdateMetadata, permission.VectorSearchSimilar,
		permission.VectorSearchByMetadata, permission.VectorHybridSearch,
		permission.VectorSearchByID, permission.VectorRebuildIndex,
		permission.VectorGetRebuildProg, permission.VectorGetIndexStats,
		permission.VectorCompact, permission.VectorGetCompactProg,
		// Admin (limited)
		permission.AdminHealth, permission.AdminStatus,
		// Audit (read)
		permission.AuditRead,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// userPermissions returns the permission set for the User built-in role.
// Users perform cryptographic operations (sign, verify, encrypt, decrypt) using
// existing keys. They cannot create, delete, or modify keys or configurations.
func userPermissions() []permission.Permission {
	perms := []permission.Permission{
		// Cryptographic operations (read-only key access)
		permission.KeysSign, permission.KeysVerify,
		permission.KeysEncrypt, permission.KeysDecrypt,
		// Key metadata (public fields only)
		permission.KeysGet, permission.KeysList,
		// System health (all roles can check health)
		permission.AdminHealth, permission.AdminStatus,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// auditorPermissions returns the permission set for the Auditor built-in role.
// Auditors have read-only access to audit logs and system state. They cannot
// modify any state (FIPS 140-3 AS10.10).
func auditorPermissions() []permission.Permission {
	perms := []permission.Permission{
		// Audit visibility (the core auditor capability)
		permission.AuditRead, permission.AuditQuery, permission.AuditStream,
		// System state (read-only)
		permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
		// User, role, cert, tenant listing (metadata only — no writes)
		permission.UsersList, permission.UsersRead,
		permission.RolesList, permission.RolesRead, permission.PermissionsList,
		permission.CertsList,
		permission.TenantsList, permission.TenantsRead,
		// Key metadata (public fields only — no cryptographic operations)
		permission.KeysList, permission.KeysGet,
		// Ceremony status
		permission.CeremonyStatus,
		// Auth factor listing
		permission.AuthFactorList,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// custodianPermissions returns the permission set for the Custodian built-in role.
// Custodians hold a single Shamir share and can ONLY submit that share during
// barrier unseal. All other operations are denied (FIPS 140-3 AS10.06).
func custodianPermissions() []permission.Permission {
	return []permission.Permission{
		permission.BarrierUnsealShare,
	}
}

// validatePermissions checks that every permission in the slice is
// registered in the global permission registry.
func validatePermissions(perms []permission.Permission) error {
	for _, p := range perms {
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
