// Copyright 2026 Jeremy Hahn
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

package middleware

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TenantStatusTTL is how long a node's auth chain answers a tenant's status
// from its last read of the tenant record. It bounds freshness: a status
// change committed through Raft is enforced by every node's chain -- on every
// transport and every open session -- within TenantStatusTTL of its commit,
// and the record is read at most once per tenant per TenantStatusTTL on each
// node rather than once per operation.
const TenantStatusTTL = time.Second

// TenantStatusCacheMaxEntries bounds how many tenants a node's status cache
// holds. Once it holds half as many, an insert first reclaims every expired
// entry (at most once per TTL); once it is full of fresh entries, a further
// tenant's status is answered from the store and not cached, so a stream of
// one-off tenant IDs cannot grow it without limit.
const TenantStatusCacheMaxEntries = 4096

// tenantStatusEntry is one cached answer: the tenant's status, or that it
// has no record.
type tenantStatusEntry struct {
	status   tenant.Status
	notFound bool
	expires  time.Time
}

// tenantStatusCache is a lock-free, TTL-bounded TenantStatusReader over the
// tenant record store. It keeps only the Status: the gate reads nothing else.
// A failed read is never cached, so an unavailable store is retried on the
// next operation and keeps refusing it until the store answers.
//
// The cache holds at most maxEntries tenants. size counts them: a new key
// reserves its slot before it is stored, and a sweep releases the slot of
// each entry it deletes. Expired entries are reclaimed on insert, never by a
// goroutine of the cache's own.
type tenantStatusCache struct {
	reader     TenantStatusReader
	ttl        time.Duration
	now        func() time.Time
	maxEntries int64
	entries    sync.Map // map[string]*tenantStatusEntry
	size       atomic.Int64
	// nextSweep is the UnixNano before which no insert sweeps again.
	nextSweep atomic.Int64
}

// NewTenantStatusCache answers reader's tenant statuses from a per-tenant
// cache bounded by ttl (see TenantStatusTTL) and TenantStatusCacheMaxEntries.
// A non-positive ttl caches nothing and returns reader itself.
func NewTenantStatusCache(reader TenantStatusReader, ttl time.Duration) TenantStatusReader {
	if ttl <= 0 {
		return reader
	}
	return newTenantStatusCache(reader, ttl, time.Now, TenantStatusCacheMaxEntries)
}

func newTenantStatusCache(reader TenantStatusReader, ttl time.Duration, now func() time.Time, maxEntries int64) *tenantStatusCache {
	return &tenantStatusCache{reader: reader, ttl: ttl, now: now, maxEntries: maxEntries}
}

// GetTenant answers a TenantInfo carrying only tenantID's Status, from the
// cache while the entry is fresh and from the record store otherwise.
func (c *tenantStatusCache) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	info, _, err := c.leaseTenant(ctx, tenantID)
	return info, err
}

// tenantStatusLeaser is a TenantStatusReader that also reports how much
// longer each answer it gives is served, so a holder of the answer -- a
// stream's admission of a delivery its consumer has not taken yet -- trusts
// it no longer than the reader itself would.
type tenantStatusLeaser interface {
	leaseTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, time.Duration, error)
}

var _ tenantStatusLeaser = (*tenantStatusCache)(nil)

// leaseTenant is GetTenant with the time left on the entry the answer came
// from: an answer served from an entry about to expire holds only until it
// does. A failed read is not cached and holds for nothing.
func (c *tenantStatusCache) leaseTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, time.Duration, error) {
	now := c.now()
	if v, ok := c.entries.Load(tenantID); ok {
		// A value of any other type is a miss: the status is re-read
		// from the store, never assumed active.
		if e, ok := v.(*tenantStatusEntry); ok && e != nil && now.Before(e.expires) {
			info, err := e.answer(tenantID)
			return info, e.expires.Sub(now), err
		}
	}
	info, err := c.reader.GetTenant(ctx, tenantID)
	expires := now.Add(c.ttl)
	switch {
	case errors.Is(err, tenant.ErrTenantNotFound):
		c.store(tenantID, &tenantStatusEntry{notFound: true, expires: expires}, now)
		info = nil
	case err != nil:
		return nil, 0, err
	default:
		e := &tenantStatusEntry{status: info.Status, expires: expires}
		c.store(tenantID, e, now)
		info, err = e.answer(tenantID)
	}
	// The entry's clock started before the read, so what the read took
	// comes off its life.
	return info, max(expires.Sub(c.now()), 0), err
}

// store caches e for tenantID. Replacing a tenant's entry takes no slot; a
// new tenant takes one only if the cache, after reclaiming what has expired,
// has one free, and is otherwise answered uncached.
func (c *tenantStatusCache) store(tenantID string, e *tenantStatusEntry, now time.Time) {
	if old, ok := c.entries.Load(tenantID); ok && c.entries.CompareAndSwap(tenantID, old, e) {
		return
	}
	if !c.reserve(now) {
		return
	}
	if _, loaded := c.entries.Swap(tenantID, e); loaded {
		// A concurrent insert stored and counted this tenant first.
		c.size.Add(-1)
	}
}

// reserve takes a slot for a new entry, sweeping first once the cache is at
// least half full, and reports false when every slot holds a fresh entry.
func (c *tenantStatusCache) reserve(now time.Time) bool {
	if c.size.Load() >= c.maxEntries/2 {
		c.sweep(now)
	}
	for {
		n := c.size.Load()
		if n >= c.maxEntries {
			return false
		}
		if c.size.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// sweep deletes every entry expired at now, at most once per ttl: an entry
// fresh at one sweep has expired by the next one it can run.
func (c *tenantStatusCache) sweep(now time.Time) {
	next := c.nextSweep.Load()
	if now.UnixNano() < next || !c.nextSweep.CompareAndSwap(next, now.Add(c.ttl).UnixNano()) {
		return
	}
	c.entries.Range(func(k, v any) bool {
		if e, ok := v.(*tenantStatusEntry); !ok || e == nil || !now.Before(e.expires) {
			if c.entries.CompareAndDelete(k, v) {
				c.size.Add(-1)
			}
		}
		return true
	})
}

func (e *tenantStatusEntry) answer(tenantID string) (*tenant.TenantInfo, error) {
	if e.notFound {
		return nil, tenant.ErrTenantNotFound
	}
	return &tenant.TenantInfo{ID: tenantID, Status: e.status}, nil
}
