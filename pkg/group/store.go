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

package group

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	// keyPrefixPrimary is the prefix for primary group records.
	keyPrefixPrimary = "__sys:groups/id/"

	// keyPrefixTenant is the prefix for the per-tenant group index.
	keyPrefixTenant = "__sys:groups/tenant/"

	// keyPrefixName is the prefix for the per-tenant name index.
	keyPrefixName = "__sys:group_idx/name/"

	// defaultPageSize is the page size used when the caller supplies none.
	defaultPageSize = 100

	// maxNameLength bounds a group name.
	maxNameLength = 256
)

var _ GroupServicer = (*kvGroupStore)(nil)

// Option configures a group store.
type Option func(*kvGroupStore)

// WithRoleLookup sets the role resolver CreateGroup and UpdateGroup check
// every role against. Without it the store refuses any non-empty RoleIDs.
func WithRoleLookup(roles RoleLookup) Option {
	return func(s *kvGroupStore) { s.roles = roles }
}

// WithMemberRevoker sets the revoker DeleteGroup strips members with.
// Without it DeleteGroup refuses to delete, so no member record is left
// naming a group that no longer exists.
func WithMemberRevoker(rv MemberRevoker) Option {
	return func(s *kvGroupStore) { s.revoker = rv }
}

// WithIdentityIndex makes the store claim every group id it creates in index
// (kind tenant.IdentityGroup) and release the claim when the group is
// deleted, so a router (NewRoutingService) can find the tenant that owns an
// id without searching every tenant's store. pkg/role does the same.
func WithIdentityIndex(index tenant.IdentityIndex) Option {
	return func(s *kvGroupStore) { s.identity = index }
}

// recordIdentity claims id for tenantID; it returns the claim token, or ""
// when no index is configured.
func (s *kvGroupStore) recordIdentity(ctx context.Context, id, tenantID string) (string, error) {
	if s.identity == nil {
		return "", nil
	}
	return s.identity.Record(ctx, tenant.IdentityGroup, id, tenantID)
}

// releaseClaim gives back the claim a failed create took. A stale claim was
// already replaced, so there is nothing left to release.
func (s *kvGroupStore) releaseClaim(ctx context.Context, id, token string) {
	if s.identity == nil || token == "" {
		return
	}
	_ = s.identity.Forget(ctx, tenant.IdentityGroup, id, token)
}

// forgetIdentity releases the claim on id held by tenantID once its group is
// deleted. A claim held by another tenant is a conflict, not a release.
func (s *kvGroupStore) forgetIdentity(ctx context.Context, id, tenantID string) error {
	if s.identity == nil {
		return nil
	}
	claim, held, err := s.identity.Lookup(ctx, tenant.IdentityGroup, id)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	if tenantid.IsSystem(tenantID) {
		tenantID = tenantid.System
	}
	if claim.Tenant != tenantID {
		return &tenant.IdentityClaimConflictError{Kind: tenant.IdentityGroup, ID: id}
	}
	if ferr := s.identity.Forget(ctx, tenant.IdentityGroup, id, claim.Token); ferr != nil {
		var stale *tenant.IdentityClaimStaleError
		if errors.As(ferr, &stale) {
			return nil
		}
		return ferr
	}
	return nil
}

// kvGroupStore implements GroupServicer over a kvstore.KVStore, with the key
// layout and name-claim pattern of pkg/role's store.
type kvGroupStore struct {
	store    kvstore.KVStore
	roles    RoleLookup
	revoker  MemberRevoker
	identity tenant.IdentityIndex
}

// NewStore returns a GroupServicer over store. The store must implement
// kvstore.AtomicKVStore so a name is claimed atomically.
func NewStore(store kvstore.KVStore, opts ...Option) (GroupServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	if _, ok := store.(kvstore.AtomicKVStore); !ok {
		return nil, ErrStoreNotAtomic
	}
	s := &kvGroupStore{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// CreateGroup persists a new group, claiming its name first.
func (s *kvGroupStore) CreateGroup(ctx context.Context, req *CreateGroupRequest) (created *Group, retErr error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	tenantID := tenantid.Normalize(req.TenantID)
	roleIDs, err := s.checkRoles(ctx, tenantID, req.RoleIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	g := &Group{
		ID:          uuid.New().String(),
		TenantID:    tenantID,
		Name:        req.Name,
		Description: req.Description,
		RoleIDs:     roleIDs,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	data, err := json.Marshal(g)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, ErrStoreNotAtomic
	}
	nameKey := nameIndexKey(tenantID, g.Name)
	idBytes := []byte(g.ID)
	stored, err := atomicStore.PutIfAbsent(ctx, nameKey, idBytes)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "claim name index", Err: err}
	}
	if !stored {
		return nil, &GroupNameTakenError{Name: g.Name, TenantID: tenantID}
	}
	// Claim the id in the identity index so a routed deployment finds the
	// owning tenant's store by id. The id is a fresh UUID: a conflict means
	// the index is not what this store wrote.
	var claimToken string
	// Every failure after the claim releases it, secondary indexes first and
	// the primary record last, as pkg/role's CreateRole does.
	var written []string
	defer func() {
		if retErr == nil {
			return
		}
		cctx := context.WithoutCancel(ctx)
		for _, k := range written {
			_ = s.store.Delete(cctx, k)
		}
		s.releaseClaim(cctx, g.ID, claimToken)
		_, _ = atomicStore.CompareAndDelete(cctx, nameKey, idBytes)
	}()
	claimToken, err = s.recordIdentity(ctx, g.ID, tenantID)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "claim identity", Err: err}
	}
	pk := primaryKey(g.ID)
	if err := s.store.Put(ctx, pk, data); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put primary", Err: err}
	}
	written = append([]string{pk}, written...)
	tk := tenantIndexKey(tenantID, g.ID)
	if err := s.store.Put(ctx, tk, idBytes); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put tenant index", Err: err}
	}
	written = append([]string{tk}, written...)
	return g, nil
}

// GetGroup returns the group with the given ID. A non-system caller reads
// only groups of its own tenant, as pkg/role's GetRole.
func (s *kvGroupStore) GetGroup(ctx context.Context, id string) (*Group, error) {
	g, _, err := s.readGroup(ctx, id)
	return g, err
}

// readGroup is GetGroup that also returns the record's stored bytes, the
// expected value of a CompareAndSwap that commits over exactly this version.
func (s *kvGroupStore) readGroup(ctx context.Context, id string) (*Group, []byte, error) {
	if id == "" {
		return nil, nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}
	data, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return nil, nil, &StoreError{Op: "get", Detail: "primary lookup", Err: err}
	}
	if data == nil {
		return nil, nil, &GroupNotFoundError{ID: id}
	}
	var g Group
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, nil, &StoreError{Op: "get", Detail: "unmarshal", Err: err}
	}
	if ctxTenant := tenant.FromContext(ctx); !tenantid.IsSystem(ctxTenant) && g.TenantID != ctxTenant {
		return nil, nil, &GroupNotFoundError{ID: id}
	}
	return &g, data, nil
}

// GetGroupByName returns the group of tenantID with the given name.
func (s *kvGroupStore) GetGroupByName(ctx context.Context, tenantID, name string) (*Group, error) {
	if name == "" {
		return nil, &ValidationError{Field: "name", Reason: "must not be empty"}
	}
	tenantID = tenantid.Normalize(tenantID)
	id, err := s.store.Get(ctx, nameIndexKey(tenantID, name))
	if err != nil {
		return nil, &StoreError{Op: "get by name", Detail: "name index lookup", Err: err}
	}
	if id == nil {
		return nil, &GroupNotFoundError{Name: name, TenantID: tenantID}
	}
	g, err := s.GetGroup(ctx, string(id))
	var nf *GroupNotFoundError
	if errors.As(err, &nf) {
		return nil, &GroupNotFoundError{Name: name, TenantID: tenantID}
	}
	return g, err
}

// UpdateGroup changes the name, description or role set of a group. A
// rename claims the new name before the record is rewritten and releases the
// old one after.
func (s *kvGroupStore) UpdateGroup(ctx context.Context, req *UpdateGroupRequest) (*Group, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, ErrStoreNotAtomic
	}
	prior, base, err := s.readGroup(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	g := *prior
	g.RoleIDs = slices.Clone(prior.RoleIDs)
	if req.RoleIDs != nil {
		roleIDs, err := s.checkRoles(ctx, g.TenantID, req.RoleIDs)
		if err != nil {
			return nil, err
		}
		g.RoleIDs = roleIDs
	}
	if req.Description != nil {
		g.Description = *req.Description
	}
	renamed := req.Name != "" && req.Name != prior.Name
	if renamed {
		if err := validateName(req.Name); err != nil {
			return nil, err
		}
		g.Name = req.Name
	}
	if req.Guard != nil {
		// The guard judges the write against the very record it commits
		// over: the CompareAndSwap below refuses the write if that record
		// changed after this read.
		if err := req.Guard(prior, &g); err != nil {
			return nil, err
		}
	}
	idBytes := []byte(g.ID)
	if renamed {
		stored, err := atomicStore.PutIfAbsent(ctx, nameIndexKey(g.TenantID, g.Name), idBytes)
		if err != nil {
			return nil, &StoreError{Op: "update", Detail: "claim name index", Err: err}
		}
		if !stored {
			return nil, &GroupNameTakenError{Name: g.Name, TenantID: g.TenantID}
		}
	}
	releaseNew := func() {
		if renamed {
			_, _ = atomicStore.CompareAndDelete(context.WithoutCancel(ctx), nameIndexKey(g.TenantID, g.Name), idBytes)
		}
	}
	g.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(&g)
	swapped := false
	if err == nil {
		swapped, err = atomicStore.CompareAndSwap(ctx, primaryKey(g.ID), base, data)
	}
	if err != nil {
		releaseNew()
		return nil, &StoreError{Op: "update", Detail: "put primary", Err: err}
	}
	if !swapped {
		releaseNew()
		return nil, &GroupConflictError{ID: g.ID}
	}
	if renamed {
		if _, err := atomicStore.CompareAndDelete(ctx, nameIndexKey(g.TenantID, prior.Name), idBytes); err != nil {
			return nil, &StoreError{Op: "update", Detail: "release old name", Err: err}
		}
	}
	return &g, nil
}

// DeleteGroup strips the group from every member, then removes its indexes
// and its record. Members are stripped first, so a member never names a
// group whose record is gone for longer than an interrupted delete, which a
// retry finishes.
func (s *kvGroupStore) DeleteGroup(ctx context.Context, id string) error {
	g, err := s.GetGroup(ctx, id)
	if err != nil {
		return err
	}
	if s.revoker == nil {
		return &StoreError{Op: "delete", Detail: "no member revoker configured"}
	}
	// Checked before members are revoked: a store that cannot release the
	// name claim must not leave a group whose members are already gone.
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return ErrStoreNotAtomic
	}
	if _, err := s.revoker.RevokeGroup(ctx, g.TenantID, g.ID); err != nil {
		return &StoreError{Op: "delete", Detail: "revoke members", Err: err}
	}
	if _, err := atomicStore.CompareAndDelete(ctx, nameIndexKey(g.TenantID, g.Name), []byte(g.ID)); err != nil {
		return &StoreError{Op: "delete", Detail: "name index", Err: err}
	}
	if err := s.store.Delete(ctx, tenantIndexKey(g.TenantID, g.ID)); err != nil {
		return &StoreError{Op: "delete", Detail: "tenant index", Err: err}
	}
	if err := s.store.Delete(ctx, primaryKey(g.ID)); err != nil {
		return &StoreError{Op: "delete", Detail: "primary", Err: err}
	}
	// Release the id's claim last, so a stale claim cannot keep routing the
	// id to a tenant that no longer holds it; a failure here is completed by
	// the router's reconcile on the next delete (routingGroupService).
	if err := s.forgetIdentity(ctx, g.ID, g.TenantID); err != nil {
		return &StoreError{Op: "delete", Detail: "identity index", Err: err}
	}
	return nil
}

// ListGroups returns a page of the tenant's groups ordered by ID.
func (s *kvGroupStore) ListGroups(ctx context.Context, req *ListGroupsRequest) (*ListGroupsResponse, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	tenantID := tenantid.Normalize(req.TenantID)
	page := max(req.Page, 1)
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	var ids []string
	err := s.store.Scan(ctx, keyPrefixTenant+tenantID+"/", func(_ string, value []byte) error {
		ids = append(ids, string(value))
		return nil
	})
	if err != nil {
		return nil, &StoreError{Op: "list", Detail: "scan tenant index", Err: err}
	}
	sort.Strings(ids)
	total := len(ids)
	start := min((page-1)*pageSize, total)
	end := min(start+pageSize, total)
	groups := make([]*Group, 0, end-start)
	for _, id := range ids[start:end] {
		g, err := s.GetGroup(ctx, id)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return &ListGroupsResponse{Groups: groups, Total: total, HasMore: end < total}, nil
}

// checkRoles refuses any role that does not resolve to a live role of
// tenantID, and returns the set de-duplicated and sorted.
func (s *kvGroupStore) checkRoles(ctx context.Context, tenantID string, roleIDs []string) ([]string, error) {
	out := []string{}
	for _, id := range roleIDs {
		if id == "" {
			return nil, &ValidationError{Field: "role_ids", Reason: "must not contain an empty id"}
		}
		if slices.Contains(out, id) {
			continue
		}
		if s.roles == nil {
			return nil, &RoleNotInTenantError{RoleID: id, TenantID: tenantID}
		}
		r, err := s.roles.GetRole(tenant.WithTenantID(ctx, tenantID), id)
		var nf *role.RoleNotFoundError
		if errors.As(err, &nf) {
			return nil, &RoleNotInTenantError{RoleID: id, TenantID: tenantID}
		}
		if err != nil {
			return nil, &StoreError{Op: "check roles", Detail: "role lookup", Err: err}
		}
		if r.TenantID != tenantID || r.Deleting {
			return nil, &RoleNotInTenantError{RoleID: id, TenantID: tenantID}
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// validateName applies the group name rules.
func validateName(name string) error {
	if name == "" {
		return &ValidationError{Field: "name", Reason: "must not be empty"}
	}
	if len(name) > maxNameLength {
		return &ValidationError{Field: "name", Reason: "must not exceed 256 bytes"}
	}
	return nil
}

func primaryKey(id string) string {
	return keyPrefixPrimary + id
}

func tenantIndexKey(tenantID, groupID string) string {
	return keyPrefixTenant + tenantID + "/" + groupID
}

func nameIndexKey(tenantID, name string) string {
	return keyPrefixName + tenantID + "/" + name
}
