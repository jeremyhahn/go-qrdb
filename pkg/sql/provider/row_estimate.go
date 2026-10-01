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

package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Planner row-count estimates
//
// go-mysql-server asks a table for its row count several times while it
// plans every statement -- for index costing, for table-scan statistics,
// and once more through DataLength -- and information_schema.tables asks
// again for every table it lists. Counting the table each time made every
// statement, a primary-key point lookup included, cost time proportional to
// the table's size. The planner needs an order of magnitude, not an exact
// figure, so it is served from a cached estimate per table generation,
// counted with a local (stale) read and refreshed in the background once it
// is older than rowEstimateTTL. See docs/sql/engineering.md, "Planner
// statistics".
// ---------------------------------------------------------------------------

const (
	// rowEstimateTTL is how long an estimate is served before the next
	// request for it starts a background recount. The stale value keeps
	// being served while the recount runs, so no statement waits for one.
	rowEstimateTTL = 10 * time.Second

	// rowEstimateRefreshTimeout bounds one background recount.
	rowEstimateRefreshTimeout = 30 * time.Second

	// rowEstimateMaxEntries bounds how many table generations' estimates one
	// provider keeps. A TRUNCATE or DROP issued on another node leaves this
	// node's entry for the old generation behind; the bound, enforced by
	// evicting entries nobody asked for within rowEstimateIdle, is what
	// keeps those from accumulating.
	rowEstimateMaxEntries = 4096

	// rowEstimateIdle is how long an entry may go unrequested before it is
	// eligible for eviction once the cache is over rowEstimateMaxEntries.
	rowEstimateIdle = 6 * rowEstimateTTL
)

// rowEstimateKey names one generation of one relational table in one
// (tenant, database) store. The generation is part of the key because a
// TRUNCATE moves the table to an empty keyspace: the new generation's
// count starts from scratch rather than inheriting the old one's.
type rowEstimateKey struct {
	scope      string // "<tenant>/<database>"
	tableID    uint64
	generation uint64
}

// rowEstimate is one table generation's cached row count.
type rowEstimate struct {
	count      atomic.Uint64
	countedAt  atomic.Int64 // unix nanoseconds of the count being served
	usedAt     atomic.Int64 // unix nanoseconds of the last request
	refreshing atomic.Bool
}

// rowCounter counts a table generation's rows.
type rowCounter func(ctx context.Context) (uint64, error)

// rowEstimates is the planner's row-count estimate cache. Its zero value is
// not usable; see newRowEstimates. It is safe for concurrent use.
type rowEstimates struct {
	ttl time.Duration
	now func() time.Time

	entries sync.Map // rowEstimateKey -> *rowEstimate
	size    atomic.Int64

	// refreshes counts the background recounts in flight; tests wait on it.
	refreshes sync.WaitGroup
}

// newRowEstimates returns an empty estimate cache.
func newRowEstimates() *rowEstimates {
	return &rowEstimates{ttl: rowEstimateTTL, now: time.Now}
}

// estimate returns key's row-count estimate. The first request counts
// synchronously with count and caches the result; a later request is served
// from the cache, and one arriving after the estimate is older than the TTL
// starts a single background recount while the current value keeps being
// served. Only a failed first count is returned as an error: a failed
// recount leaves the previous estimate in place and is retried a TTL later.
func (e *rowEstimates) estimate(ctx context.Context, key rowEstimateKey, count rowCounter) (uint64, error) {
	now := e.now().UnixNano()
	if est, ok := e.load(key); ok {
		est.usedAt.Store(now)
		if now-est.countedAt.Load() >= int64(e.ttl) && est.refreshing.CompareAndSwap(false, true) {
			e.refresh(ctx, est, count)
		}
		return est.count.Load(), nil
	}

	n, err := count(ctx)
	if err != nil {
		return 0, err
	}
	e.store(key, n)
	return n, nil
}

// refresh recounts est in the background. The recount is detached from the
// requesting statement's cancellation -- the statement does not wait for
// it -- and bounded by rowEstimateRefreshTimeout instead.
func (e *rowEstimates) refresh(ctx context.Context, est *rowEstimate, count rowCounter) {
	e.refreshes.Add(1)
	go func() {
		defer e.refreshes.Done()
		defer est.refreshing.Store(false)
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowEstimateRefreshTimeout)
		defer cancel()
		if n, err := count(rctx); err == nil {
			est.count.Store(n)
		}
		// A failed recount keeps serving the previous estimate and is not
		// retried before another TTL has passed, so a store that cannot
		// answer is not asked again on every statement.
		est.countedAt.Store(e.now().UnixNano())
	}()
}

// store records n as key's freshly counted estimate.
func (e *rowEstimates) store(key rowEstimateKey, n uint64) {
	now := e.now().UnixNano()
	est := &rowEstimate{}
	est.count.Store(n)
	est.countedAt.Store(now)
	est.usedAt.Store(now)
	if _, loaded := e.entries.Swap(key, est); loaded {
		return
	}
	if e.size.Add(1) > rowEstimateMaxEntries {
		e.evict()
	}
}

// forget drops key's estimate.
func (e *rowEstimates) forget(key rowEstimateKey) {
	if _, loaded := e.entries.LoadAndDelete(key); loaded {
		e.size.Add(-1)
	}
}

// forgetScope drops every estimate of the (tenant, database) store scope:
// the database was dropped, and a database later created under the same
// name starts its table IDs over.
func (e *rowEstimates) forgetScope(scope string) {
	e.each(func(key rowEstimateKey, _ *rowEstimate) bool {
		if key.scope == scope {
			e.forget(key)
		}
		return true
	})
}

// evict brings the cache back under rowEstimateMaxEntries: entries nobody
// requested within rowEstimateIdle go first, then arbitrary ones. An
// evicted estimate is only a recount away.
func (e *rowEstimates) evict() {
	cutoff := e.now().Add(-rowEstimateIdle).UnixNano()
	e.each(func(key rowEstimateKey, est *rowEstimate) bool {
		if est.usedAt.Load() < cutoff {
			e.forget(key)
		}
		return true
	})
	e.each(func(key rowEstimateKey, _ *rowEstimate) bool {
		if e.size.Load() <= rowEstimateMaxEntries {
			return false
		}
		e.forget(key)
		return true
	})
}

// load returns key's cached estimate.
func (e *rowEstimates) load(key rowEstimateKey) (*rowEstimate, bool) {
	v, ok := e.entries.Load(key)
	if !ok {
		return nil, false
	}
	est, ok := v.(*rowEstimate)
	return est, ok
}

// each calls fn for every cached estimate until fn returns false.
func (e *rowEstimates) each(fn func(rowEstimateKey, *rowEstimate) bool) {
	e.entries.Range(func(k, v any) bool {
		key, kok := k.(rowEstimateKey)
		est, vok := v.(*rowEstimate)
		if !kok || !vok {
			return true
		}
		return fn(key, est)
	})
}

// estimateScope returns the store scope a (tenant, database) pair's
// estimates are kept under.
func estimateScope(tenantID, database string) string {
	return tenantID + "/" + database
}
