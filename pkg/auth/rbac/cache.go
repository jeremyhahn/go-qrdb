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
// permission sets keyed by "tenantID:subject".
type permissionCache struct {
	entries sync.Map // map[string]*cacheEntry
	ttl     time.Duration
}

// newPermissionCache creates a new permission cache with the given TTL.
func newPermissionCache(ttl time.Duration) *permissionCache {
	return &permissionCache{ttl: ttl}
}

// cacheKey builds the composite key for cache lookups.
func cacheKey(tenantID, subject string) string {
	return tenantID + ":" + subject
}

// get returns the cached permission set for the given identity if the
// entry exists and has not expired. The second return value reports
// whether a valid entry was found.
func (c *permissionCache) get(tenantID, subject string) (map[permission.Permission]struct{}, bool) {
	key := cacheKey(tenantID, subject)
	val, ok := c.entries.Load(key)
	if !ok {
		return nil, false
	}
	entry := val.(*cacheEntry)
	if time.Now().After(entry.expiresAt) {
		c.entries.Delete(key)
		return nil, false
	}
	return entry.permissions, true
}

// set stores the given permissions in the cache with the configured TTL.
func (c *permissionCache) set(tenantID, subject string, perms []permission.Permission) {
	permSet := make(map[permission.Permission]struct{}, len(perms))
	for _, p := range perms {
		permSet[p] = struct{}{}
	}
	c.entries.Store(cacheKey(tenantID, subject), &cacheEntry{
		permissions: permSet,
		expiresAt:   time.Now().Add(c.ttl),
	})
}

// invalidate removes the cache entry for a specific identity.
func (c *permissionCache) invalidate(tenantID, subject string) {
	c.entries.Delete(cacheKey(tenantID, subject))
}

// invalidateAll clears all cached permission entries.
func (c *permissionCache) invalidateAll() {
	c.entries.Range(func(key, _ any) bool {
		c.entries.Delete(key)
		return true
	})
}
