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
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a settable clock for the estimate cache's TTL.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestEstimates returns an estimate cache driven by clock.
func newTestEstimates(clock *fakeClock) *rowEstimates {
	e := newRowEstimates()
	e.now = clock.Now
	return e
}

// constCounter counts n every time and records how often it was asked.
func constCounter(n uint64, calls *atomic.Int32) rowCounter {
	return func(context.Context) (uint64, error) {
		calls.Add(1)
		return n, nil
	}
}

var testEstimateKey = rowEstimateKey{store: storeID{tenant: "acme", db: "shop"}, tableID: 7, generation: 0}

func TestRowEstimates_FirstRequestCountsThenServesFromCache(t *testing.T) {
	clock := newFakeClock()
	e := newTestEstimates(clock)
	var calls atomic.Int32

	for range 3 {
		n, err := e.estimate(context.Background(), testEstimateKey, constCounter(12, &calls))
		require.NoError(t, err)
		assert.Equal(t, uint64(12), n)
	}
	clock.Advance(rowEstimateTTL - time.Nanosecond)
	_, err := e.estimate(context.Background(), testEstimateKey, constCounter(99, &calls))
	require.NoError(t, err)
	e.background.Wait()
	assert.Equal(t, int32(1), calls.Load(), "within the TTL the table is never counted again")
}

func TestRowEstimates_StaleEstimateIsServedWhileOneRecountRuns(t *testing.T) {
	clock := newFakeClock()
	e := newTestEstimates(clock)
	var calls atomic.Int32
	_, err := e.estimate(context.Background(), testEstimateKey, constCounter(3, &calls))
	require.NoError(t, err)

	clock.Advance(rowEstimateTTL)
	release := make(chan struct{})
	var recounts atomic.Int32
	slow := func(context.Context) (uint64, error) {
		recounts.Add(1)
		<-release
		return 8, nil
	}
	for range 5 {
		n, err := e.estimate(context.Background(), testEstimateKey, slow)
		require.NoError(t, err)
		assert.Equal(t, uint64(3), n, "the statement is served the previous estimate, not made to wait")
	}
	close(release)
	e.background.Wait()

	assert.Equal(t, int32(1), recounts.Load(), "concurrent requests share one background recount")
	n, err := e.estimate(context.Background(), testEstimateKey, slow)
	require.NoError(t, err)
	assert.Equal(t, uint64(8), n)
}

func TestRowEstimates_RecountIsDetachedFromTheStatement(t *testing.T) {
	clock := newFakeClock()
	e := newTestEstimates(clock)
	var calls atomic.Int32
	_, err := e.estimate(context.Background(), testEstimateKey, constCounter(1, &calls))
	require.NoError(t, err)

	clock.Advance(rowEstimateTTL)
	stmt, cancel := context.WithCancel(context.Background())
	var recountErr error
	_, err = e.estimate(stmt, testEstimateKey, func(rctx context.Context) (uint64, error) {
		cancel() // the statement ends while its recount is still running
		recountErr = rctx.Err()
		return 2, nil
	})
	require.NoError(t, err)
	e.background.Wait()
	assert.NoError(t, recountErr, "a statement ending must not cancel the recount it started")
}

func TestRowEstimates_FailedFirstCountIsReturnedAndNotCached(t *testing.T) {
	e := newTestEstimates(newFakeClock())
	boom := errors.New("store unavailable")

	_, err := e.estimate(context.Background(), testEstimateKey, func(context.Context) (uint64, error) { return 0, boom })
	require.ErrorIs(t, err, boom)

	var calls atomic.Int32
	n, err := e.estimate(context.Background(), testEstimateKey, constCounter(4, &calls))
	require.NoError(t, err)
	assert.Equal(t, uint64(4), n)
	assert.Equal(t, int32(1), calls.Load(), "a failed count leaves nothing cached")
}

func TestRowEstimates_FailedRecountKeepsEstimateAndBacksOff(t *testing.T) {
	clock := newFakeClock()
	e := newTestEstimates(clock)
	var calls atomic.Int32
	_, err := e.estimate(context.Background(), testEstimateKey, constCounter(6, &calls))
	require.NoError(t, err)

	clock.Advance(rowEstimateTTL)
	var failures atomic.Int32
	failing := func(context.Context) (uint64, error) {
		failures.Add(1)
		return 0, errors.New("leader lost")
	}
	n, err := e.estimate(context.Background(), testEstimateKey, failing)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), n)
	e.background.Wait()

	n, err = e.estimate(context.Background(), testEstimateKey, failing)
	require.NoError(t, err)
	e.background.Wait()
	assert.Equal(t, uint64(6), n, "a failed recount keeps serving the previous estimate")
	assert.Equal(t, int32(1), failures.Load(), "a failed recount is not retried before another TTL")
}

func TestRowEstimates_GenerationsAreIndependent(t *testing.T) {
	e := newTestEstimates(newFakeClock())
	var calls atomic.Int32
	_, err := e.estimate(context.Background(), testEstimateKey, constCounter(50, &calls))
	require.NoError(t, err)

	next := testEstimateKey
	next.generation++
	n, err := e.estimate(context.Background(), next, constCounter(0, &calls))
	require.NoError(t, err)
	assert.Zero(t, n, "a TRUNCATE's new generation does not inherit the old count")
	assert.Equal(t, int32(2), calls.Load())
}

func TestRowEstimates_ForgetAndForgetStore(t *testing.T) {
	e := newTestEstimates(newFakeClock())
	other := rowEstimateKey{store: storeID{tenant: "acme", db: "billing"}, tableID: 7}
	e.store(testEstimateKey, 1)
	e.store(rowEstimateKey{store: testEstimateKey.store, tableID: 8}, 2)
	e.store(other, 3)
	require.Equal(t, int64(3), e.size.Load())

	e.forget(testEstimateKey)
	e.forget(testEstimateKey) // idempotent
	assert.Equal(t, int64(2), e.size.Load())

	e.forgetStore(storeID{tenant: "acme", db: "shop"})
	assert.Equal(t, int64(1), e.size.Load())
	_, ok := e.entries.Load(other)
	assert.True(t, ok, "another database's estimates survive")
}

func TestRowEstimates_StoreReplacesWithoutGrowing(t *testing.T) {
	e := newTestEstimates(newFakeClock())
	e.store(testEstimateKey, 1)
	e.store(testEstimateKey, 2)
	assert.Equal(t, int64(1), e.size.Load())
	v, ok := e.entries.Load(testEstimateKey)
	require.True(t, ok)
	assert.Equal(t, uint64(2), v.(*rowEstimate).count.Load())
}

func TestRowEstimates_BoundEvictsIdleEntriesFirst(t *testing.T) {
	clock := newFakeClock()
	e := newTestEstimates(clock)
	for i := range rowEstimateMaxEntries {
		e.store(rowEstimateKey{store: storeID{tenant: "idle"}, tableID: uint64(i)}, 1)
	}
	clock.Advance(rowEstimateIdle + time.Second)
	hot := rowEstimateKey{store: storeID{tenant: "hot"}, tableID: 1}
	e.store(hot, 5)
	e.background.Wait()

	assert.LessOrEqual(t, e.size.Load(), int64(rowEstimateMaxEntries))
	_, ok := e.entries.Load(hot)
	assert.True(t, ok, "the entry just stored is not the one evicted")
}

func TestRowEstimates_EvictionRunsOffTheCallersPath(t *testing.T) {
	e := newTestEstimates(newFakeClock())
	for i := range rowEstimateMaxEntries {
		e.store(rowEstimateKey{store: storeID{tenant: "busy"}, tableID: uint64(i)}, 1)
	}
	// An eviction already in flight: the next store past the bound must not
	// walk the cache itself, nor start a second walk.
	e.evicting.Store(true)
	e.store(rowEstimateKey{store: storeID{tenant: "busy"}, tableID: 1 << 40}, 1)
	assert.Equal(t, int64(rowEstimateMaxEntries+1), e.size.Load(), "the statement that grew the cache did not evict on its own path")

	e.evicting.Store(false)
	e.store(rowEstimateKey{store: storeID{tenant: "busy"}, tableID: 1 << 41}, 1)
	e.background.Wait()
	assert.LessOrEqual(t, e.size.Load(), int64(rowEstimateMaxEntries))
}

func TestRowEstimates_BoundHoldsWhenNothingIsIdle(t *testing.T) {
	e := newTestEstimates(newFakeClock())
	for i := range rowEstimateMaxEntries + 10 {
		e.store(rowEstimateKey{store: storeID{tenant: "busy"}, tableID: uint64(i)}, 1)
	}
	e.background.Wait()
	assert.LessOrEqual(t, e.size.Load(), int64(rowEstimateMaxEntries))
}

// ---------------------------------------------------------------------------
// Through the engine
// ---------------------------------------------------------------------------

// estimateConn is a connection over a provider whose estimate cache runs on
// clock.
func estimateConn(t *testing.T) (*txConn, *Provider, *fakeClock) {
	t.Helper()
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	clock := newFakeClock()
	prov.estimates.now = clock.Now
	return newTxConn(t, prov, "shop"), prov, clock
}

// tableRows reads information_schema.tables' TABLE_ROWS and DATA_LENGTH for
// shop.name.
func tableRows(t *testing.T, c *txConn, name string) (rows, dataLength uint64) {
	t.Helper()
	_, got := c.mustExec(t, "SELECT TABLE_ROWS, DATA_LENGTH FROM information_schema.tables WHERE TABLE_SCHEMA = 'shop' AND TABLE_NAME = '"+name+"'")
	require.Len(t, got, 1)
	return toUint64(t, got[0][0]), toUint64(t, got[0][1])
}

func toUint64(t *testing.T, v any) uint64 {
	t.Helper()
	switch n := v.(type) {
	case uint64:
		return n
	case int64:
		return uint64(n)
	case int32:
		return uint64(n)
	}
	t.Fatalf("unexpected %T %v", v, v)
	return 0
}

// insertRows inserts ids [from, to) into shop.t.
func insertRows(t *testing.T, c *txConn, from, to int) {
	t.Helper()
	for id := from; id < to; id++ {
		c.mustExec(t, "INSERT INTO t (id, v) VALUES ("+strconv.Itoa(id)+", 'x')")
	}
}

func TestRowEstimate_FollowsInsertsAndDeletesAtTheTTL(t *testing.T) {
	c, prov, clock := estimateConn(t)
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	insertRows(t, c, 1, 4)

	rows, length := tableRows(t, c, "t")
	assert.Equal(t, uint64(3), rows)
	assert.NotZero(t, length, "DATA_LENGTH is derived from the same estimate")

	insertRows(t, c, 4, 6)
	rows, _ = tableRows(t, c, "t")
	assert.Equal(t, uint64(3), rows, "within the TTL the estimate is not recounted")

	clock.Advance(rowEstimateTTL)
	tableRows(t, c, "t") // starts the recount
	prov.estimates.background.Wait()
	rows, _ = tableRows(t, c, "t")
	assert.Equal(t, uint64(5), rows)

	c.mustExec(t, "DELETE FROM t WHERE id < 5")
	clock.Advance(rowEstimateTTL)
	tableRows(t, c, "t")
	prov.estimates.background.Wait()
	rows, _ = tableRows(t, c, "t")
	assert.Equal(t, uint64(1), rows)
}

func TestRowEstimate_TruncateStartsTheNewGenerationAtZero(t *testing.T) {
	c, prov, _ := estimateConn(t)
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	insertRows(t, c, 1, 4)
	rows, _ := tableRows(t, c, "t")
	require.Equal(t, uint64(3), rows)
	require.Equal(t, int64(1), prov.estimates.size.Load())

	c.mustExec(t, "TRUNCATE TABLE t")

	rows, _ = tableRows(t, c, "t")
	assert.Zero(t, rows, "no TTL wait: the truncated table is known to be empty")
	assert.Equal(t, int64(1), prov.estimates.size.Load(), "the old generation's estimate is dropped, not kept beside the new one")
	insertRows(t, c, 10, 12)
	_, got := c.mustExec(t, "SELECT COUNT(*) FROM t")
	assert.Equal(t, int64(2), got[0][0])
}

func TestRowEstimate_DropForgetsTheTable(t *testing.T) {
	c, prov, _ := estimateConn(t)
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	insertRows(t, c, 1, 3)
	tableRows(t, c, "t")
	require.Equal(t, int64(1), prov.estimates.size.Load())

	c.mustExec(t, "DROP TABLE t")
	assert.Zero(t, prov.estimates.size.Load())

	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	rows, _ := tableRows(t, c, "t")
	assert.Zero(t, rows, "a table re-created under the same name starts from its own count")
}

// TestCountStar_StaysExactWhileTheEstimateIsStale is the guard on the
// estimate being reported inexact: if go-mysql-server were told it was
// exact, it would answer COUNT(*) with it, and every count below would read
// the 3 the estimate still holds.
func TestCountStar_StaysExactWhileTheEstimateIsStale(t *testing.T) {
	c, _, _ := estimateConn(t)
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	insertRows(t, c, 1, 4)
	rows, _ := tableRows(t, c, "t")
	require.Equal(t, uint64(3), rows)

	count := func() int64 {
		_, got := c.mustExec(t, "SELECT COUNT(*) FROM t")
		require.Len(t, got, 1)
		return got[0][0].(int64)
	}
	assert.Equal(t, int64(3), count())
	insertRows(t, c, 4, 9)
	assert.Equal(t, int64(8), count())
	c.mustExec(t, "DELETE FROM t WHERE id > 6")
	assert.Equal(t, int64(6), count())

	c.mustExec(t, "START TRANSACTION")
	c.mustExec(t, "INSERT INTO t (id, v) VALUES (100, 'tx')")
	c.mustExec(t, "DELETE FROM t WHERE id = 1")
	c.mustExec(t, "DELETE FROM t WHERE id = 2")
	assert.Equal(t, int64(5), count(), "the transaction counts its own uncommitted rows")
	c.mustExec(t, "ROLLBACK")
	assert.Equal(t, int64(6), count())

	rows, _ = tableRows(t, c, "t")
	assert.Equal(t, uint64(3), rows, "the estimate itself was never recounted")
}

func TestRowCount_PlanningAPointLookupDoesNotCountTheTable(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	c := newTxConn(t, prov, "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(16))")
	insertRows(t, c, 1, 6)

	var counts atomic.Int32
	h.store.onCountOnly = func() { counts.Add(1) }
	for i := 1; i < 6; i++ {
		_, got := c.mustExec(t, "SELECT v FROM t WHERE id = "+strconv.Itoa(i))
		require.Len(t, got, 1)
		c.mustExec(t, "UPDATE t SET v = 'y' WHERE id = "+strconv.Itoa(i))
	}
	c.mustExec(t, "DELETE FROM t WHERE id = 1")
	assert.Equal(t, int32(1), counts.Load(), "the table is counted once, not on every statement")
}

func TestLookupFailure_KeepsTheClientClassification(t *testing.T) {
	stale := sqle.ErrLockDeadlock.New("t")
	assert.Same(t, stale, lookupFailure("e", stale), "a retryable serialization failure is not hidden")

	wire := mysql.NewSQLError(1205, "HY000", "%s", "Lock wait timeout exceeded")
	assert.Same(t, wire, lookupFailure("e", wire), "a lock-wait timeout keeps its MySQL number")

	nowait := mysql.NewSQLError(3572, "HY000", "%s", "NOWAIT is set")
	assert.Same(t, nowait, lookupFailure("e", nowait), "a NOWAIT refusal keeps its MySQL number")

	plain := errors.New("disk")
	got := lookupFailure("e", plain)
	var lookupErr *IndexLookupError
	require.ErrorAs(t, got, &lookupErr)
	assert.Equal(t, "e", lookupErr.IndexName)
	assert.ErrorIs(t, got, plain)

	// A classified error buried under an unclassified one is classified by
	// nobody: the client tests the outermost error only, so it is wrapped
	// like any other.
	buried := &wrappedError{err: sqle.ErrLockDeadlock.New("t")}
	require.ErrorAs(t, lookupFailure("e", buried), &lookupErr)
}

// wrappedError is an unclassified error wrapping another.
type wrappedError struct{ err error }

func (e *wrappedError) Error() string { return "wrapped: " + e.err.Error() }
func (e *wrappedError) Unwrap() error { return e.err }
