// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// cachedTenants counts the entries c holds, checking that size agrees.
func cachedTenants(t *testing.T, c *tenantStatusCache) int64 {
	t.Helper()
	var n int64
	c.entries.Range(func(any, any) bool { n++; return true })
	if got := c.size.Load(); got != n {
		t.Fatalf("size = %d, but the cache holds %d entries", got, n)
	}
	return n
}

// lookup reads tenantID through c, failing on any error but not-found.
func lookup(t *testing.T, c *tenantStatusCache, tenantID string) {
	t.Helper()
	if _, err := c.GetTenant(context.Background(), tenantID); err != nil && !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("GetTenant(%s): %v", tenantID, err)
	}
}

// Expired entries, negative lookups included, are reclaimed by the first
// insert once the cache is half full and their TTL has passed, and not
// left until each tenant happens to be queried again.
func TestTenantStatusCache_InsertReclaimsExpired(t *testing.T) {
	for name, rec := range map[string]*countingTenants{
		"found":     {rec: &tenant.TenantInfo{Status: tenant.StatusActive}},
		"not-found": {err: tenant.ErrTenantNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			clock := &stepClock{now: time.Unix(1000, 0)}
			c := newTenantStatusCache(rec, time.Second, clock.Now, 8)
			for i := range 4 {
				lookup(t, c, "once-"+strconv.Itoa(i))
			}
			if n := cachedTenants(t, c); n != 4 {
				t.Fatalf("cached %d tenants, want 4", n)
			}

			clock.now = clock.now.Add(time.Second)
			lookup(t, c, "next")
			if n := cachedTenants(t, c); n != 1 {
				t.Fatalf("after the TTL passed cached %d tenants, want only the new one", n)
			}
			if _, ok := c.entries.Load("next"); !ok {
				t.Fatal("the inserting tenant was not cached")
			}
		})
	}
}

// One-off tenant IDs cannot grow the cache past its bound: once it is full
// of fresh entries a further tenant is answered from the store, uncached,
// with the store's status, and the next sweep frees the slots.
func TestTenantStatusCache_BoundedByMaxEntries(t *testing.T) {
	rec := &countingTenants{rec: &tenant.TenantInfo{Status: tenant.StatusSuspended}}
	clock := &stepClock{now: time.Unix(1000, 0)}
	c := newTenantStatusCache(rec, time.Second, clock.Now, 8)
	for i := range 100 {
		lookup(t, c, "once-"+strconv.Itoa(i))
		if n := cachedTenants(t, c); n > 8 {
			t.Fatalf("after %d tenants cached %d, want at most 8", i+1, n)
		}
	}
	if n := cachedTenants(t, c); n != 8 {
		t.Fatalf("cached %d tenants, want the bound 8", n)
	}

	// An uncached tenant is read from the store on every lookup and still
	// refused: the bound never turns a miss into an assumed answer.
	before := len(rec.calls)
	for range 2 {
		info, err := c.GetTenant(context.Background(), "once-99")
		if err != nil || info.Status != tenant.StatusSuspended {
			t.Fatalf("uncached tenant = %+v, %v; want suspended", info, err)
		}
	}
	if got := len(rec.calls) - before; got != 2 {
		t.Fatalf("uncached tenant read %d times, want 2", got)
	}

	clock.now = clock.now.Add(time.Second)
	lookup(t, c, "later")
	if n := cachedTenants(t, c); n != 1 {
		t.Fatalf("after the TTL passed cached %d tenants, want 1", n)
	}
}

// A cache at its bound sweeps at most once per TTL: inserts within the
// same TTL do not walk the map again, and the sweep after it reclaims what
// expired in between.
func TestTenantStatusCache_SweepsOncePerTTL(t *testing.T) {
	rec := &countingTenants{rec: &tenant.TenantInfo{Status: tenant.StatusActive}}
	clock := &stepClock{now: time.Unix(1000, 0)}
	c := newTenantStatusCache(rec, time.Second, clock.Now, 4)
	lookup(t, c, "a")
	lookup(t, c, "b")

	// Half full: this insert sweeps (nothing has expired) and arms the
	// next sweep a TTL later.
	clock.now = clock.now.Add(500 * time.Millisecond)
	lookup(t, c, "c")
	// a and b expire here, but the next sweep is not due until 1500ms.
	clock.now = clock.now.Add(600 * time.Millisecond)
	lookup(t, c, "d")
	if n := cachedTenants(t, c); n != 4 {
		t.Fatalf("cached %d tenants before the next sweep, want 4", n)
	}

	// The sweep due at 1500ms reclaims a, b and c, whose TTL ends there.
	clock.now = clock.now.Add(400 * time.Millisecond)
	lookup(t, c, "e")
	for _, gone := range []string{"a", "b", "c"} {
		if _, ok := c.entries.Load(gone); ok {
			t.Fatalf("expired tenant %s survived the sweep", gone)
		}
	}
	if n := cachedTenants(t, c); n != 2 {
		t.Fatalf("cached %d tenants after the sweep, want d and e", n)
	}
}

// Re-reading a tenant whose entry expired replaces it in its own slot.
func TestTenantStatusCache_RefreshKeepsSlot(t *testing.T) {
	rec := &countingTenants{rec: &tenant.TenantInfo{Status: tenant.StatusActive}}
	clock := &stepClock{now: time.Unix(1000, 0)}
	c := newTenantStatusCache(rec, time.Second, clock.Now, 2)
	lookup(t, c, "acme")
	for range 3 {
		clock.now = clock.now.Add(time.Second)
		lookup(t, c, "acme")
	}
	if n := cachedTenants(t, c); n != 1 {
		t.Fatalf("cached %d tenants, want acme once", n)
	}
	if got := len(rec.calls); got != 4 {
		t.Fatalf("store read %d times, want once per TTL (4)", got)
	}
}
