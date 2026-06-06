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
	"encoding/binary"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
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

	// keyPrefixCertThumbprint is the prefix for the mTLS SPKI-thumbprint
	// secondary index (thumbprint -> user ID).
	keyPrefixCertThumbprint = "__sys:user_idx/cert_spki/"

	// defaultPageSize is used when ListUsersRequest.PageSize is zero.
	defaultPageSize = 100
)

// Compile-time interface check.
var _ UserServicer = (*kvUserStore)(nil)

// kvUserStore implements UserServicer backed by a KVStore with secondary
// indexes for tenant listing and username uniqueness.
//
// User IDs are uint64 Snowflake values assigned at creation time from an
// injected dao.IDGenerator. The ID is encoded as a fixed-width 8-byte
// big-endian value everywhere it appears in a KEY so the lexical key order
// the KVStore scans in is also the numeric ID order; the same 8-byte
// big-endian encoding is stored as index VALUES (decoded with
// binary.BigEndian.Uint64).
//
// Key schema:
//
//	__sys:users/id/{be8(userID)}                          -> User JSON
//	__sys:users/tenant/{tenantID}/{be8(userID)}           -> be8(userID)
//	__sys:user_idx/username/{tenantID}/{username}         -> be8(userID)
//	__sys:user_idx/cert_spki/{thumbprint}                 -> be8(userID)
type kvUserStore struct {
	store        kvstore.KVStore
	idgen        dao.IDGenerator
	quotaChecker quota.Checker // optional; nil skips quota enforcement
}

// NewStore creates a new KVStore-backed UserServicer. The provided kvstore
// and ID generator must not be nil. The ID generator is the coordination-free
// source of uint64 user identifiers (a dao.SnowflakeGenerator seeded with the
// node's Raft ReplicaID); there is no default because a node-unique seed is
// required to keep IDs globally unique across a cluster.
func NewStore(store kvstore.KVStore, idgen dao.IDGenerator) (UserServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	if idgen == nil {
		return nil, &StoreError{Op: "new", Detail: "id generator must not be nil"}
	}
	return &kvUserStore{store: store, idgen: idgen}, nil
}

// WithQuotaChecker returns a new kvUserStore with the given quota.Checker
// applied. Passing nil disables quota enforcement.
func (s *kvUserStore) WithQuotaChecker(checker quota.Checker) *kvUserStore {
	s.quotaChecker = checker
	return s
}

// CreateUser creates a new user with a store-assigned Snowflake ID. It
// validates the request, checks username uniqueness within the tenant via the
// secondary index, and atomically writes the primary record plus both indexes.
func (s *kvUserStore) CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.Username == "" {
		return nil, &ValidationError{Field: "username", Reason: "must not be empty"}
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
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
		ID:        s.idgen.NextID(nil),
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

	idBytes := encodeID(user.ID)

	// Write primary record.
	pk := primaryKey(user.ID)
	if err := s.store.Put(ctx, pk, data); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put primary", Err: err}
	}

	// Write tenant index.
	tenantKey := tenantIndexKey(tenantID, user.ID)
	if err := s.store.Put(ctx, tenantKey, idBytes); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put tenant index", Err: err}
	}

	// Write username index.
	if err := s.store.Put(ctx, usernameKey, idBytes); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put username index", Err: err}
	}

	return user, nil
}

// GetUser retrieves a user by its globally unique ID.
func (s *kvUserStore) GetUser(ctx context.Context, id uint64) (*User, error) {
	if id == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
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
		tenantID = tenant.SystemTenantID
	}

	indexKey := usernameIndexKey(tenantID, username)
	idBytes, err := s.store.Get(ctx, indexKey)
	if err != nil {
		return nil, &StoreError{Op: "get_by_username", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		return nil, &UserNotFoundError{Lookup: tenantID + "/" + username}
	}
	id, decErr := decodeID(idBytes)
	if decErr != nil {
		return nil, &StoreError{Op: "get_by_username", Detail: "decode index value", Err: decErr}
	}

	return s.GetUser(ctx, id)
}

// UserByCertThumbprint resolves a user by an mTLS certificate's SPKI thumbprint
// via the secondary index. This is the mTLS-auth lookup: given a presented
// client cert, compute user.ComputeSPKIThumbprint and resolve the bound user.
// Returns UserNotFoundError when no user is bound to the thumbprint.
func (s *kvUserStore) UserByCertThumbprint(ctx context.Context, thumbprint string) (*User, error) {
	if thumbprint == "" {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must not be empty"}
	}
	tenantID := tenant.FromContext(ctx)
	idBytes, err := s.store.Get(ctx, certThumbprintIndexKey(tenantID, thumbprint))
	if err != nil {
		return nil, &StoreError{Op: "get_by_cert_thumbprint", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		// Dual-read fallback: resolve via the legacy un-scoped global key so
		// deployments predating tenant-scoping keep working. The resolved
		// user's binding is still validated below, so this cannot resolve a
		// thumbprint to a user in a foreign tenant.
		idBytes, err = s.store.Get(ctx, legacyCertThumbprintIndexKey(thumbprint))
		if err != nil {
			return nil, &StoreError{Op: "get_by_cert_thumbprint", Detail: "legacy index lookup", Err: err}
		}
	}
	if idBytes == nil {
		return nil, &UserNotFoundError{Lookup: "cert_spki/" + tenantID + "/" + thumbprint}
	}
	id, decErr := decodeID(idBytes)
	if decErr != nil {
		return nil, &StoreError{Op: "get_by_cert_thumbprint", Detail: "decode index value", Err: decErr}
	}
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	// Enforce binding validity on the resolution path: the index entry may be
	// stale (the binding was removed) or the binding may have expired. Only a
	// currently-valid, non-expired binding resolves the identity — otherwise a
	// decommissioned or expired credential could be replayed.
	if !u.HasCertBinding(thumbprint) {
		return nil, &UserNotFoundError{Lookup: "cert_spki/" + thumbprint}
	}
	return u, nil
}

// AddCertBinding binds an mTLS certificate's SPKI thumbprint to the user and
// refreshes the secondary index so UserByCertThumbprint resolves it. A
// notAfterUnix > 0 sets the binding's expiry; otherwise the binding never
// expires. Returns the updated user.
func (s *kvUserStore) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error) {
	if thumbprint == "" {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must not be empty"}
	}
	// Only canonical SPKI thumbprints (base64url of 32 bytes) may be stored, so
	// the identity index cannot be polluted with arbitrary or non-canonical
	// strings that a canonical-encoding lookup would never match.
	if !IsValidSPKIThumbprint(thumbprint) {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must be a base64url-encoded SHA-256 SPKI thumbprint"}
	}

	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	// A thumbprint resolves to exactly one identity within a tenant. Reject
	// binding a thumbprint that is already bound to a different user in the
	// same tenant — silently overwriting it would let a caller hijack another
	// user's mTLS identity. The index is tenant-scoped, so the conflict check
	// is performed under the user's own tenant.
	existingID, err := s.store.Get(ctx, certThumbprintIndexKey(u.TenantID, thumbprint))
	if err != nil {
		return nil, &StoreError{Op: "add_cert_binding", Detail: "index lookup", Err: err}
	}
	if existingID != nil {
		boundID, decErr := decodeID(existingID)
		if decErr != nil {
			return nil, &StoreError{Op: "add_cert_binding", Detail: "decode index value", Err: decErr}
		}
		if boundID != userID {
			return nil, &CertBindingConflictError{
				Thumbprint:   thumbprint,
				BoundUserID:  boundID,
				TargetUserID: userID,
			}
		}
	}
	b := CertBinding{Thumbprint: thumbprint, Kind: CertBindingSPKIThumbprint}
	if notAfterUnix > 0 {
		b.NotAfter = time.Unix(notAfterUnix, 0).UTC()
	}
	u.AddCertBinding(b)
	return s.UpdateUser(ctx, &UpdateUserRequest{ID: userID, CertBindings: u.CertBindings})
}

// UpdateUser applies partial updates to an existing user. An empty
// Username means no change; a nil RoleIDs means no change. If the
// username changes, uniqueness is re-validated and the secondary index
// is updated atomically.
func (s *kvUserStore) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.ID == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	existing, err := s.GetUser(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	idBytes := encodeID(existing.ID)

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
		if err := s.store.Put(ctx, newKey, idBytes); err != nil {
			return nil, &StoreError{Op: "update", Detail: "put new username index", Err: err}
		}
	}

	if req.RoleIDs != nil {
		existing.RoleIDs = req.RoleIDs
	}
	if req.RoleBindings != nil {
		existing.RoleBindings = req.RoleBindings
	}
	if req.Attributes != nil {
		existing.Attributes = req.Attributes
	}
	if req.Factors != nil {
		existing.Factors = req.Factors
	}
	if req.CertBindings != nil {
		// Refresh the SPKI-thumbprint secondary index: drop the old entries and
		// add the new ones so UserByCertThumbprint stays consistent.
		for _, b := range existing.CertBindings {
			if err := s.store.Delete(ctx, certThumbprintIndexKey(existing.TenantID, b.Thumbprint)); err != nil {
				return nil, &StoreError{Op: "update", Detail: "delete old cert index", Err: err}
			}
			// Also clear any legacy un-scoped entry so a rewrite fully migrates
			// the binding off the global key.
			if err := s.store.Delete(ctx, legacyCertThumbprintIndexKey(b.Thumbprint)); err != nil {
				return nil, &StoreError{Op: "update", Detail: "delete legacy cert index", Err: err}
			}
		}
		existing.CertBindings = req.CertBindings
		for _, b := range req.CertBindings {
			if err := s.store.Put(ctx, certThumbprintIndexKey(existing.TenantID, b.Thumbprint), idBytes); err != nil {
				return nil, &StoreError{Op: "update", Detail: "put cert index", Err: err}
			}
		}
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
func (s *kvUserStore) DeleteUser(ctx context.Context, id uint64) error {
	if id == 0 {
		return &ValidationError{Field: "id", Reason: "must not be zero"}
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

	// Delete any mTLS SPKI-thumbprint index entries.
	for _, b := range existing.CertBindings {
		if err := s.store.Delete(ctx, certThumbprintIndexKey(existing.TenantID, b.Thumbprint)); err != nil {
			return &StoreError{Op: "delete", Detail: "cert index", Err: err}
		}
		if err := s.store.Delete(ctx, legacyCertThumbprintIndexKey(b.Thumbprint)); err != nil {
			return &StoreError{Op: "delete", Detail: "legacy cert index", Err: err}
		}
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

	// Collect all user IDs for the tenant via the tenant index.
	prefix := keyPrefixTenant + tenantID + "/"
	var userIDs []uint64
	var scanErr error
	err := s.store.Scan(ctx, prefix, func(_ string, value []byte) error {
		id, decErr := decodeID(value)
		if decErr != nil {
			scanErr = decErr
			return decErr
		}
		userIDs = append(userIDs, id)
		return nil
	})
	if err != nil {
		if scanErr != nil {
			return nil, &StoreError{Op: "list", Detail: "decode tenant index value", Err: scanErr}
		}
		return nil, &StoreError{Op: "list", Detail: "scan tenant index", Err: err}
	}

	// Sort numerically for deterministic pagination.
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })

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
func (s *kvUserStore) EnableUser(ctx context.Context, id uint64) error {
	if id == 0 {
		return &ValidationError{Field: "id", Reason: "must not be zero"}
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
func (s *kvUserStore) DisableUser(ctx context.Context, id uint64) error {
	if id == 0 {
		return &ValidationError{Field: "id", Reason: "must not be zero"}
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

// encodeID returns the fixed-width 8-byte big-endian encoding of a user ID.
// Big-endian preserves numeric order under the KVStore's lexical key ordering.
func encodeID(id uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], id)
	return b[:]
}

// idStringKey returns the canonical key-segment encoding of a user ID: the
// fixed-width 8-byte big-endian bytes as a string. It is used inside KEYS so
// scan order is numeric.
func idStringKey(id uint64) string {
	b := encodeID(id)
	return string(b)
}

// decodeID decodes an 8-byte big-endian user-id index value. It returns an
// IDEncodingError when the value is not exactly 8 bytes.
func decodeID(b []byte) (uint64, error) {
	if len(b) != 8 {
		return 0, &IDEncodingError{Length: len(b)}
	}
	return binary.BigEndian.Uint64(b), nil
}

// IDEncodingError is returned when a stored user-id index value does not have
// the expected fixed-width 8-byte big-endian form.
type IDEncodingError struct {
	Length int
}

// Error returns a human-readable description of the encoding failure.
func (e *IDEncodingError) Error() string {
	return "user: malformed id index value: expected 8 bytes, got " + strconv.Itoa(e.Length)
}

// primaryKey builds the primary record key for a user ID.
func primaryKey(id uint64) string {
	return keyPrefixPrimary + idStringKey(id)
}

// tenantIndexKey builds the tenant index key for a user within a tenant.
func tenantIndexKey(tenantID string, userID uint64) string {
	return keyPrefixTenant + tenantID + "/" + idStringKey(userID)
}

// usernameIndexKey builds the username secondary index key.
func usernameIndexKey(tenantID, username string) string {
	return keyPrefixUsername + tenantID + "/" + username
}

// certThumbprintIndexKey builds the tenant-scoped mTLS SPKI-thumbprint
// secondary index key. Scoping by tenant prevents a thumbprint collision
// across tenants from resolving to the wrong identity: the same physical
// certificate (or a deliberately-crafted SPKI collision) bound in two tenants
// no longer shares one global index slot.
func certThumbprintIndexKey(tenantID, thumbprint string) string {
	return keyPrefixCertThumbprint + tenantID + "/" + thumbprint
}

// legacyCertThumbprintIndexKey builds the pre-tenant-scoping global index key.
// It is consulted only on the read path as a dual-read fallback so existing
// deployments keep resolving until the index is rewritten under the
// tenant-scoped key. New writes always use certThumbprintIndexKey.
func legacyCertThumbprintIndexKey(thumbprint string) string {
	return keyPrefixCertThumbprint + thumbprint
}
