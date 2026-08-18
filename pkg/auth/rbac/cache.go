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

package rbac

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// cacheEntry holds a permission set with an expiration timestamp.
type cacheEntry struct {
	permissions map[permission.Permission]struct{}
	expiresAt   time.Time
}

// permissionCache provides a lock-free, TTL-based cache for resolved
// permission sets keyed by "tenantID:subject:<sorted role set>".
//
// The ROLE SET is part of the key on purpose: an identity's effective roles
// are re-resolved from the user store on every request (finalizeIdentity),
// so they can legitimately change between two requests -- most importantly
// from EMPTY to populated the moment a tenant's first user record becomes
// resolvable (sealed-window bootstrap, or a user created seconds earlier).
// Keying by (tenant, subject) alone let the empty-role resolution of a
// bootstrap request answer for the enriched identity for a full TTL,
// denying every request the subject was already entitled to make.
type permissionCache struct {
	entries sync.Map // map[string]*cacheEntry
	ttl     time.Duration
}

// newPermissionCache creates a new permission cache with the given TTL.
func newPermissionCache(ttl time.Duration) *permissionCache {
	return &permissionCache{ttl: ttl}
}

// cacheKey builds the composite key for cache lookups. roles is
// order-normalized so two identical sets in different orders share one
// entry.
func cacheKey(tenantID, subject string, roles []string) string {
	sorted := make([]string, len(roles))
	copy(sorted, roles)
	sort.Strings(sorted)
	return tenantID + ":" + subject + ":" + strings.Join(sorted, ",")
}

// cacheSubjectPrefix is the prefix every cache key for (tenantID, subject)
// shares, regardless of role set; invalidate deletes by it.
func cacheSubjectPrefix(tenantID, subject string) string {
	return tenantID + ":" + subject + ":"
}

// get returns the cached permission set for the given identity if the
// entry exists and has not expired. The second return value reports
// whether a valid entry was found.
func (c *permissionCache) get(tenantID, subject string, roles []string) (map[permission.Permission]struct{}, bool) {
	key := cacheKey(tenantID, subject, roles)
	val, ok := c.entries.Load(key)
	if !ok {
		return nil, false
	}
	entry := val.(*cacheEntry) //nolint:errcheck // type assertion on a value this container only ever holds
	if time.Now().After(entry.expiresAt) {
		c.entries.Delete(key)
		return nil, false
	}
	return entry.permissions, true
}

// set stores the given permissions in the cache with the configured TTL.
func (c *permissionCache) set(tenantID, subject string, roles []string, perms []permission.Permission) {
	permSet := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		permSet[p] = struct{}{}
	}
	c.entries.Store(cacheKey(tenantID, subject, roles), &cacheEntry{
		permissions: permSet,
		expiresAt:   time.Now().Add(c.ttl),
	})
}

// invalidate removes every cache entry for a specific identity, across all
// role sets it has been resolved under.
func (c *permissionCache) invalidate(tenantID, subject string) {
	prefix := cacheSubjectPrefix(tenantID, subject)
	c.entries.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			c.entries.Delete(key)
		}
		return true
	})
}

// invalidateAll clears all cached permission entries.
func (c *permissionCache) invalidateAll() {
	c.entries.Range(func(key, _ any) bool {
		c.entries.Delete(key)
		return true
	})
}

// roleIDEntry holds the identifiers that denote one system role, with an
// expiration timestamp.
type roleIDEntry struct {
	ids       []string
	expiresAt time.Time
}

// systemRoleIDCache is a lock-free, TTL-based cache for the identifiers that
// denote a named system role in a tenant, keyed by "tenantID/roleName".
//
// It is TTL-bounded rather than permanent even though the mapping is stable
// under normal operation. The store does NOT enforce system-role
// immutability: kvRoleStore.CreateRole persists System: false regardless of
// the request, so a seeded SO or Owner role can be renamed or deleted and
// recreated under a fresh identifier. A permanent cache would keep
// recognising the replaced identifier for the process lifetime, and the two
// controls that consult it — the SO deny-list and the strict-SoD Owner strip
// — would stop voiding a grant they exist to void.
//
// The TTL is the one the permission cache already applies to the same role
// data, so a replacement is picked up on exactly the schedule its permissions
// are; this adds no staleness class that authorization did not already have.
type systemRoleIDCache struct {
	entries sync.Map // map[string]*roleIDEntry
	ttl     time.Duration
}

// newSystemRoleIDCache creates a cache with the given TTL.
func newSystemRoleIDCache(ttl time.Duration) *systemRoleIDCache {
	return &systemRoleIDCache{ttl: ttl}
}

// systemRoleIDKey builds the composite key for a tenant's named system role.
func systemRoleIDKey(tenantID, roleName string) string {
	return tenantID + "/" + roleName
}

// get returns the cached identifiers if the entry exists and has not
// expired. The second return value reports whether a valid entry was found.
func (c *systemRoleIDCache) get(tenantID, roleName string) ([]string, bool) {
	key := systemRoleIDKey(tenantID, roleName)
	val, ok := c.entries.Load(key)
	if !ok {
		return nil, false
	}
	entry := val.(*roleIDEntry) //nolint:errcheck // type assertion on a value this container only ever holds
	if time.Now().After(entry.expiresAt) {
		c.entries.Delete(key)
		return nil, false
	}
	return entry.ids, true
}

// set stores the identifiers with the configured TTL.
func (c *systemRoleIDCache) set(tenantID, roleName string, ids []string) {
	c.entries.Store(systemRoleIDKey(tenantID, roleName), &roleIDEntry{
		ids:       ids,
		expiresAt: time.Now().Add(c.ttl),
	})
}

// invalidateAll clears every cached entry.
func (c *systemRoleIDCache) invalidateAll() {
	c.entries.Range(func(key, _ any) bool {
		c.entries.Delete(key)
		return true
	})
}
