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
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// maxRevokeAttempts bounds the compare-and-swap retries RevokeRole makes on
// one group before it reports *GroupConflictError; DeleteRole's retry then
// finishes the strip.
const maxRevokeAttempts = 16

// NewRoleRevoker returns the role.HolderRevoker that strips a deleted role
// from every group of its tenant in kv, the store the role store over the
// same kv composes with user.NewRoleRevoker through role.HolderRevokers.
func NewRoleRevoker(kv kvstore.KVStore) (role.HolderRevoker, error) {
	svc, err := NewStore(kv)
	if err != nil {
		return nil, err
	}
	return svc.(*kvGroupStore), nil //nolint:errcheck // NewStore returns *kvGroupStore
}

// NewRoutingRoleRevoker is NewRoleRevoker for a tenant-routed deployment:
// each call strips the role from the groups in the tenant's own store.
func NewRoutingRoleRevoker(resolver tenant.StoreResolver) (role.HolderRevoker, error) {
	svc, err := NewRoutingService(resolver)
	if err != nil {
		return nil, err
	}
	return svc.(*routingGroupService), nil //nolint:errcheck // NewRoutingService returns *routingGroupService
}

// RevokeRole implements role.HolderRevoker: it removes roleID from the
// RoleIDs of every group of tenantID and reports how many groups it changed.
// Each group is rewritten by CompareAndSwap, retried on a lost race, so a
// concurrent group update is never overwritten; a group that no longer
// carries the role is left alone, which makes a retry idempotent.
func (s *kvGroupStore) RevokeRole(ctx context.Context, tenantID, roleID string) (int, error) {
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return 0, ErrStoreNotAtomic
	}
	tenantID = tenantid.Normalize(tenantID)
	var ids []string
	err := s.store.Scan(ctx, tenantIndexKey(tenantID, ""), func(_ string, value []byte) error {
		ids = append(ids, string(value))
		return nil
	})
	if err != nil {
		return 0, &StoreError{Op: "revoke_role", Detail: "scan tenant index", Err: err}
	}
	changed := 0
	for _, id := range ids {
		done, err := s.revokeFromGroup(ctx, atomicStore, tenantID, id, roleID)
		if err != nil {
			return changed, err
		}
		if done {
			changed++
		}
	}
	return changed, nil
}

// revokeFromGroup strips roleID from one group, reporting whether it wrote.
func (s *kvGroupStore) revokeFromGroup(ctx context.Context, atomicStore kvstore.AtomicKVStore, tenantID, id, roleID string) (bool, error) {
	key := primaryKey(id)
	for range maxRevokeAttempts {
		base, err := s.store.Get(ctx, key)
		if err != nil {
			return false, &StoreError{Op: "revoke_role", Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return false, nil
		}
		var g Group
		if err := json.Unmarshal(base, &g); err != nil {
			return false, &StoreError{Op: "revoke_role", Detail: "unmarshal", Err: err}
		}
		if g.TenantID != tenantID || !slices.Contains(g.RoleIDs, roleID) {
			return false, nil
		}
		g.RoleIDs = slices.DeleteFunc(slices.Clone(g.RoleIDs), func(r string) bool { return r == roleID })
		g.UpdatedAt = time.Now().UTC()
		data, err := json.Marshal(&g)
		if err != nil {
			return false, &StoreError{Op: "revoke_role", Detail: "marshal", Err: err}
		}
		swapped, err := atomicStore.CompareAndSwap(ctx, key, base, data)
		if err != nil {
			return false, &StoreError{Op: "revoke_role", Detail: "put primary", Err: err}
		}
		if swapped {
			return true, nil
		}
	}
	return false, &GroupConflictError{ID: id}
}

// RevokeRole implements role.HolderRevoker over the tenant's own store.
func (r *routingGroupService) RevokeRole(ctx context.Context, tenantID, roleID string) (int, error) {
	svc, err := r.storeFor(ctx, tenantid.Normalize(tenantID))
	if err != nil {
		return 0, &StoreError{Op: "revoke_role", Detail: "resolve tenant store", Err: err}
	}
	h, ok := svc.(role.HolderRevoker)
	if !ok {
		return 0, &StoreError{Op: "revoke_role", Detail: "tenant group store cannot revoke roles"}
	}
	return h.RevokeRole(ctx, tenantID, roleID)
}
