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
		tenantID = tenant.DefaultTenantID
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
	return &r, nil
}

// GetRoleByName resolves a role name within a tenant via the secondary
// index, then fetches the primary record.
func (s *kvRoleStore) GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error) {
	if name == "" {
		return nil, &ValidationError{Field: "name", Reason: "must not be empty"}
	}
	if tenantID == "" {
		tenantID = tenant.DefaultTenantID
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
		tenantID = tenant.DefaultTenantID
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
func BuiltInRoles(tenantID string) []*Role {
	if tenantID == "" {
		tenantID = tenant.DefaultTenantID
	}

	now := time.Now().UTC()

	return []*Role{
		{
			ID:          deterministicUUID(tenantID, "superadmin"),
			Name:        "superadmin",
			TenantID:    tenantID,
			Permissions: permission.All(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          deterministicUUID(tenantID, "admin"),
			Name:        "admin",
			TenantID:    tenantID,
			Permissions: adminPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          deterministicUUID(tenantID, "operator"),
			Name:        "operator",
			TenantID:    tenantID,
			Permissions: operatorPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          deterministicUUID(tenantID, "readwrite"),
			Name:        "readwrite",
			TenantID:    tenantID,
			Permissions: readwritePermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          deterministicUUID(tenantID, "readonly"),
			Name:        "readonly",
			TenantID:    tenantID,
			Permissions: readonlyPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

// SuperadminRoleID returns the deterministic UUID for the superadmin
// built-in role in the given tenant. This is used by the RBAC authorizer
// to perform a fast bypass check without resolving permissions. An empty
// tenantID defaults to DefaultTenantID to match BuiltInRoles behavior.
func SuperadminRoleID(tenantID string) string {
	if tenantID == "" {
		tenantID = tenant.DefaultTenantID
	}
	return deterministicUUID(tenantID, "superadmin")
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

// adminPermissions returns the permission set for the admin built-in role.
func adminPermissions() []permission.Permission {
	perms := []permission.Permission{
		// Admin service
		permission.AdminHealth, permission.AdminStatus, permission.AdminPeers,
		permission.AdminAddMember, permission.AdminRemoveMember,
		permission.AdminSnapshot, permission.AdminDrain, permission.AdminCapabilities,
		// User management
		permission.UsersCreate, permission.UsersRead, permission.UsersUpdate,
		permission.UsersDelete, permission.UsersList, permission.UsersEnable,
		permission.UsersDisable,
		// Role management
		permission.RolesCreate, permission.RolesRead, permission.RolesUpdate,
		permission.RolesDelete, permission.RolesList, permission.RolesAssign,
		permission.RolesUnassign, permission.PermissionsList,
		// Certificate management
		permission.CertsGenerate, permission.CertsExport, permission.CertsRevoke,
		permission.CertsList,
		// Seal management
		permission.SealBootstrap, permission.SealUnseal, permission.SealSeal,
		permission.SealStatus,
		// Tenant management
		permission.TenantsCreate, permission.TenantsRead, permission.TenantsUpdate,
		permission.TenantsDelete, permission.TenantsList,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// operatorPermissions returns the permission set for the operator built-in role.
func operatorPermissions() []permission.Permission {
	perms := []permission.Permission{
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
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// readwritePermissions returns the permission set for the readwrite built-in role.
func readwritePermissions() []permission.Permission {
	perms := []permission.Permission{
		// KV
		permission.KVPut, permission.KVGet, permission.KVDelete,
		permission.KVList, permission.KVExists, permission.KVRange,
		// MVCC
		permission.MVCCPutWithLease, permission.MVCCGetRevision,
		permission.MVCCGetAtRevision, permission.MVCCRange,
		permission.MVCCCurrentRevision,
		// Transaction
		permission.TransactionExecute,
		// Batch
		permission.BatchExecute,
		// Watch
		permission.WatchWatch, permission.WatchWatchPrefix,
		// Lease
		permission.LeaseGrant, permission.LeaseKeepAlive,
		permission.LeaseTTL, permission.LeaseList,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
}

// readonlyPermissions returns the permission set for the readonly built-in role.
func readonlyPermissions() []permission.Permission {
	perms := []permission.Permission{
		// KV (read-only)
		permission.KVGet, permission.KVList, permission.KVExists, permission.KVRange,
		// MVCC (read-only)
		permission.MVCCGetRevision, permission.MVCCGetAtRevision,
		permission.MVCCRange, permission.MVCCCurrentRevision,
		// Lease (read-only)
		permission.LeaseTTL, permission.LeaseList,
	}
	sort.Slice(perms, func(i, j int) bool { return perms[i] < perms[j] })
	return perms
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
