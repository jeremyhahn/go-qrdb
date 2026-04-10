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

package user

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// keyPrefixPrimary is the prefix for user primary records keyed by ID.
	keyPrefixPrimary = "__sys:users/id/"

	// keyPrefixTenant is the prefix for the tenant-to-user index.
	keyPrefixTenant = "__sys:users/tenant/"

	// keyPrefixUsername is the prefix for the username secondary index.
	keyPrefixUsername = "__sys:user_idx/username/"

	// defaultPageSize is used when ListUsersRequest.PageSize is zero.
	defaultPageSize = 100
)

// Compile-time interface check.
var _ UserServicer = (*kvUserStore)(nil)

// kvUserStore implements UserServicer backed by a KVStore with secondary
// indexes for tenant listing and username uniqueness.
//
// Key schema:
//
//	__sys:users/id/{userID}                             -> User JSON
//	__sys:users/tenant/{tenantID}/{userID}              -> userID (as bytes)
//	__sys:user_idx/username/{tenantID}/{username}        -> userID (as bytes)
type kvUserStore struct {
	store        kvstore.KVStore
	quotaChecker quota.Checker // optional; nil skips quota enforcement
}

// NewStore creates a new KVStore-backed UserServicer. The provided
// kvstore must not be nil.
func NewStore(store kvstore.KVStore) (UserServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	return &kvUserStore{store: store}, nil
}

// WithQuotaChecker returns a new kvUserStore with the given quota.Checker
// applied. Passing nil disables quota enforcement.
func (s *kvUserStore) WithQuotaChecker(checker quota.Checker) *kvUserStore {
	s.quotaChecker = checker
	return s
}

// CreateUser creates a new user with a generated UUID. It validates the
// request, checks username uniqueness within the tenant via the secondary
// index, and atomically writes the primary record plus both indexes.
func (s *kvUserStore) CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.Username == "" {
		return nil, &ValidationError{Field: "username", Reason: "must not be empty"}
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.DefaultTenantID
	}

	if s.quotaChecker != nil {
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceUsers); err != nil {
			return nil, err
		}
	}

	// Check username uniqueness via the secondary index.
	usernameKey := usernameIndexKey(tenantID, req.Username)
	existing, err := s.store.Get(ctx, usernameKey)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "username index lookup", Err: err}
	}
	if existing != nil {
		return nil, &UsernameExistsError{Username: req.Username, TenantID: tenantID}
	}

	now := time.Now().UTC()
	user := &User{
		ID:        uuid.New().String(),
		Username:  req.Username,
		TenantID:  tenantID,
		RoleIDs:   req.RoleIDs,
		Enabled:   true,
		System:    false,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if user.RoleIDs == nil {
		user.RoleIDs = []string{}
	}

	data, err := json.Marshal(user)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}

	// Write primary record.
	pk := primaryKey(user.ID)
	if err := s.store.Put(ctx, pk, data); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put primary", Err: err}
	}

	// Write tenant index.
	tenantKey := tenantIndexKey(tenantID, user.ID)
	if err := s.store.Put(ctx, tenantKey, []byte(user.ID)); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put tenant index", Err: err}
	}

	// Write username index.
	if err := s.store.Put(ctx, usernameKey, []byte(user.ID)); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put username index", Err: err}
	}

	return user, nil
}

// GetUser retrieves a user by its globally unique ID.
func (s *kvUserStore) GetUser(ctx context.Context, id string) (*User, error) {
	if id == "" {
		return nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	data, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return nil, &StoreError{Op: "get", Detail: "primary lookup", Err: err}
	}
	if data == nil {
		return nil, &UserNotFoundError{ID: id}
	}

	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return nil, &StoreError{Op: "get", Detail: "unmarshal", Err: err}
	}
	return &user, nil
}

// GetUserByUsername resolves a username within a tenant via the secondary
// index, then fetches the primary record.
func (s *kvUserStore) GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error) {
	if username == "" {
		return nil, &ValidationError{Field: "username", Reason: "must not be empty"}
	}
	if tenantID == "" {
		tenantID = tenant.DefaultTenantID
	}

	indexKey := usernameIndexKey(tenantID, username)
	idBytes, err := s.store.Get(ctx, indexKey)
	if err != nil {
		return nil, &StoreError{Op: "get_by_username", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		return nil, &UserNotFoundError{ID: tenantID + "/" + username}
	}

	return s.GetUser(ctx, string(idBytes))
}

// UpdateUser applies partial updates to an existing user. An empty
// Username means no change; a nil RoleIDs means no change. If the
// username changes, uniqueness is re-validated and the secondary index
// is updated atomically.
func (s *kvUserStore) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.ID == "" {
		return nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	existing, err := s.GetUser(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	usernameChanged := req.Username != "" && req.Username != existing.Username

	if usernameChanged {
		// Check that the new username is not already taken.
		newKey := usernameIndexKey(existing.TenantID, req.Username)
		found, err := s.store.Get(ctx, newKey)
		if err != nil {
			return nil, &StoreError{Op: "update", Detail: "username index check", Err: err}
		}
		if found != nil {
			return nil, &UsernameExistsError{Username: req.Username, TenantID: existing.TenantID}
		}

		// Remove old username index.
		oldKey := usernameIndexKey(existing.TenantID, existing.Username)
		if err := s.store.Delete(ctx, oldKey); err != nil {
			return nil, &StoreError{Op: "update", Detail: "delete old username index", Err: err}
		}

		existing.Username = req.Username

		// Write new username index.
		if err := s.store.Put(ctx, newKey, []byte(existing.ID)); err != nil {
			return nil, &StoreError{Op: "update", Detail: "put new username index", Err: err}
		}
	}

	if req.RoleIDs != nil {
		existing.RoleIDs = req.RoleIDs
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

// DeleteUser removes a user by ID. System users cannot be deleted.
func (s *kvUserStore) DeleteUser(ctx context.Context, id string) error {
	if id == "" {
		return &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	existing, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if existing.System {
		return &SystemUserImmutableError{ID: id}
	}

	// Delete primary record.
	if err := s.store.Delete(ctx, primaryKey(id)); err != nil {
		return &StoreError{Op: "delete", Detail: "primary", Err: err}
	}

	// Delete tenant index.
	if err := s.store.Delete(ctx, tenantIndexKey(existing.TenantID, id)); err != nil {
		return &StoreError{Op: "delete", Detail: "tenant index", Err: err}
	}

	// Delete username index.
	if err := s.store.Delete(ctx, usernameIndexKey(existing.TenantID, existing.Username)); err != nil {
		return &StoreError{Op: "delete", Detail: "username index", Err: err}
	}

	return nil
}

// ListUsers returns a paginated list of users for the given tenant by
// scanning the tenant index prefix.
func (s *kvUserStore) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
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

	// Collect all user IDs for the tenant via the tenant index.
	prefix := keyPrefixTenant + tenantID + "/"
	var userIDs []string
	err := s.store.Scan(ctx, prefix, func(_ string, value []byte) error {
		userIDs = append(userIDs, string(value))
		return nil
	})
	if err != nil {
		return nil, &StoreError{Op: "list", Detail: "scan tenant index", Err: err}
	}

	// Sort for deterministic pagination.
	sort.Strings(userIDs)

	total := len(userIDs)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	pageIDs := userIDs[start:end]
	users := make([]*User, 0, len(pageIDs))
	for _, id := range pageIDs {
		user, err := s.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return &ListUsersResponse{
		Users:   users,
		Total:   total,
		HasMore: end < total,
	}, nil
}

// EnableUser activates a user account by setting Enabled to true.
func (s *kvUserStore) EnableUser(ctx context.Context, id string) error {
	if id == "" {
		return &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	user, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}

	user.Enabled = true
	user.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(user)
	if err != nil {
		return &StoreError{Op: "enable", Detail: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, primaryKey(id), data); err != nil {
		return &StoreError{Op: "enable", Detail: "put", Err: err}
	}
	return nil
}

// DisableUser deactivates a user account by setting Enabled to false.
func (s *kvUserStore) DisableUser(ctx context.Context, id string) error {
	if id == "" {
		return &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	user, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}

	user.Enabled = false
	user.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(user)
	if err != nil {
		return &StoreError{Op: "disable", Detail: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, primaryKey(id), data); err != nil {
		return &StoreError{Op: "disable", Detail: "put", Err: err}
	}
	return nil
}

// primaryKey builds the primary record key for a user ID.
func primaryKey(id string) string {
	return keyPrefixPrimary + id
}

// tenantIndexKey builds the tenant index key for a user within a tenant.
func tenantIndexKey(tenantID, userID string) string {
	return keyPrefixTenant + tenantID + "/" + userID
}

// usernameIndexKey builds the username secondary index key.
func usernameIndexKey(tenantID, username string) string {
	return keyPrefixUsername + tenantID + "/" + username
}
