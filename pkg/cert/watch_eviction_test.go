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

package cert

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// fakeWatchService: a minimal service.WatchServicer double. It hands back a
// fakeWatcher per prefix and lets a test deliver synthetic events on it --
// WatchForDeletions never cares HOW a delete or tombstone-put happened,
// only that the watcher reported one, so this is enough to exercise it
// without a real replicated store or Hub.
// ---------------------------------------------------------------------------

type fakeWatcher struct {
	events     chan watch.WatchResponse
	closed     chan struct{}
	once       sync.Once
	eventsOnce sync.Once
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{events: make(chan watch.WatchResponse), closed: make(chan struct{})}
}

func (w *fakeWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *fakeWatcher) WatchID() int64                     { return 1 }
func (w *fakeWatcher) Err() error                         { return nil }
func (w *fakeWatcher) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

// closeEvents closes the events channel directly, simulating the hub
// closing a watcher out from under its consumer -- distinct from Close,
// which a CONSUMER calls to stop watching and which this double never
// reacts to by closing events itself, matching a real watch.Watcher: the
// two directions are not the same event, so this uses its own sync.Once
// rather than Close's.
func (w *fakeWatcher) closeEvents() {
	w.eventsOnce.Do(func() { close(w.events) })
}

// deliver sends a single-event response, blocking until consumeCertCADeletes
// or consumeTombstonePuts actually receives it (an unbuffered channel), or
// the watcher was closed first.
func (w *fakeWatcher) deliver(t *testing.T, evType watch.EventType, key string) {
	t.Helper()
	select {
	case w.events <- watch.WatchResponse{Events: []*watch.Event{{Type: evType, Kv: &kv.KeyValue{Key: []byte(key)}}}}:
	case <-w.closed:
		t.Fatal("deliver: watcher was already closed")
	case <-time.After(5 * time.Second):
		t.Fatal("deliver: timed out, nothing consumed the event")
	}
}

type fakeWatchService struct {
	mu       sync.Mutex
	watchers map[string]*fakeWatcher

	// failNext, when > 0, makes WatchPrefix return an error instead of a
	// watcher, decrementing by one on every call -- so a test can force
	// reestablishWatch through a chosen number of failed retries before
	// it finally succeeds.
	failNext int
	// calls records the time of every WatchPrefix call (successful or
	// not), for a test asserting on the backoff between them.
	calls []time.Time
}

func newFakeWatchService() *fakeWatchService {
	return &fakeWatchService{watchers: make(map[string]*fakeWatcher)}
}

func (s *fakeWatchService) WatchPrefix(_ context.Context, prefix string, _ ...watch.WatchOption) (watch.Watcher, error) {
	s.mu.Lock()
	s.calls = append(s.calls, time.Now())
	if s.failNext > 0 {
		s.failNext--
		s.mu.Unlock()
		return nil, errors.New("fakeWatchService: forced WatchPrefix failure")
	}
	w := newFakeWatcher()
	s.watchers[prefix] = w
	s.mu.Unlock()
	return w, nil
}

func (s *fakeWatchService) callTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

func (s *fakeWatchService) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return nil, errors.New("fakeWatchService: Watch not implemented")
}
func (s *fakeWatchService) WatchEntity(context.Context, *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, errors.New("fakeWatchService: WatchEntity not implemented")
}
func (s *fakeWatchService) WatchEntityType(context.Context, *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return nil, errors.New("fakeWatchService: WatchEntityType not implemented")
}

// watcherFor blocks briefly for WatchForDeletions' goroutine to have called
// WatchPrefix for prefix, since that happens asynchronously relative to the
// caller only in the sense that WatchForDeletions itself calls WatchPrefix
// synchronously before returning -- so this should always find one
// immediately once WatchForDeletions has returned nil.
func (s *fakeWatchService) watcherFor(t *testing.T, prefix string) *fakeWatcher {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.watchers[prefix]
	if !ok {
		t.Fatalf("no watcher registered for prefix %q", prefix)
	}
	return w
}

var _ service.WatchServicer = (*fakeWatchService)(nil)

// pollUntil retries check (returning true on success) up to 2 seconds,
// so these tests wait for the watcher goroutine to process an already
// -delivered event rather than racing it.
func pollUntil(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("pollUntil: condition never became true")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestDefaultCertProvider_WatchForDeletions_NilWatchServiceIsNoOp proves the
// documented no-op when WithWatchService was never called.
func TestDefaultCertProvider_WatchForDeletions_NilWatchServiceIsNoOp(t *testing.T) {
	p, _, _ := newTestProvider()
	if err := p.WatchForDeletions(context.Background()); err != nil {
		t.Fatalf("WatchForDeletions with no watch service = %v, want nil", err)
	}
}

// TestDefaultCertProvider_WatchForDeletions_EvictsOnMaterialDelete proves a
// __sys:cert_ca/{tenantID}/material DELETE event evicts perTenant[tenantID]
// and, the next time that tenant issues a certificate, forces a FRESH CA to
// be established rather than continuing to serve the one the evicted
// cakey.SoftwareProvider cached in memory.
func TestDefaultCertProvider_WatchForDeletions_EvictsOnMaterialDelete(t *testing.T) {
	ws := newFakeWatchService()
	p, kvStore, _ := newTestProvider(WithWatchService(ws))
	ctx := context.Background()

	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	certCAWatcher := ws.watcherFor(t, prefixCertCA)

	if _, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("GenerateClientCert (first): %v", err)
	}
	_, originalCACert, err := p.getCAMaterial(ctx, "acme")
	if err != nil {
		t.Fatalf("getCAMaterial (before eviction): %v", err)
	}

	p.mu.RLock()
	_, cached := p.perTenant["acme"]
	p.mu.RUnlock()
	if !cached {
		t.Fatal("expected acme's SoftwareProvider to be cached in perTenant after first issuance")
	}

	// Simulate what a direct deletion of acme's Key B records would
	// deliver: remove them from the store (so a later re-establishment
	// actually has nothing to load) and deliver the watch events
	// WatchForDeletions reacts to.
	if err := kvStore.Delete(ctx, prefixCertCA+"acme/material"); err != nil {
		t.Fatalf("Delete(material): %v", err)
	}
	if err := kvStore.Delete(ctx, prefixCertCA+"acme/certificate"); err != nil {
		t.Fatalf("Delete(certificate): %v", err)
	}
	certCAWatcher.deliver(t, watch.EventDelete, prefixCertCA+"acme/material")

	pollUntil(t, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		_, stillCached := p.perTenant["acme"]
		return !stillCached
	})

	if _, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("GenerateClientCert (second): %v", err)
	}
	_, freshCACert, err := p.getCAMaterial(ctx, "acme")
	if err != nil {
		t.Fatalf("getCAMaterial (after eviction): %v", err)
	}

	if string(originalCACert.Raw) == string(freshCACert.Raw) {
		t.Fatal("acme's CA certificate is byte-identical after eviction: a fresh CA was not established")
	}
}

// TestDefaultCertProvider_WatchForDeletions_TombstoneEvictsAndInvalidates
// proves a tenant tombstone PUT evicts perTenant[tenantID] (the built-in
// path) AND calls every OnInvalidate subscriber -- the signal a
// factory-backed tenant, which has nothing in perTenant to evict, still
// needs so the dynamic trust pool stops trusting its CA immediately.
func TestDefaultCertProvider_WatchForDeletions_TombstoneEvictsAndInvalidates(t *testing.T) {
	ws := newFakeWatchService()
	p, _, _ := newTestProvider(WithWatchService(ws))
	ctx := context.Background()

	var invalidated int
	var mu sync.Mutex
	p.OnInvalidate(func() {
		mu.Lock()
		invalidated++
		mu.Unlock()
	})

	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	tombstoneWatcher := ws.watcherFor(t, tenant.TombstonePrefix)

	if _, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "bob", TenantID: "globex"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	p.mu.RLock()
	_, cached := p.perTenant["globex"]
	p.mu.RUnlock()
	if !cached {
		t.Fatal("expected globex's SoftwareProvider to be cached in perTenant")
	}

	tombstoneWatcher.deliver(t, watch.EventPut, tenant.TombstonePrefix+"globex")

	pollUntil(t, func() bool {
		p.mu.RLock()
		_, stillCached := p.perTenant["globex"]
		p.mu.RUnlock()
		mu.Lock()
		got := invalidated
		mu.Unlock()
		return !stillCached && got == 1
	})
}

// TestDefaultCertProvider_WatchForDeletions_TombstoneInvalidatesFactoryTenant
// proves the tombstone path still notifies OnInvalidate for a
// factory-backed tenant, which resolveKeyProvider never caches in
// perTenant at all -- the ONLY eviction signal such a tenant has.
func TestDefaultCertProvider_WatchForDeletions_TombstoneInvalidatesFactoryTenant(t *testing.T) {
	ws := newFakeWatchService()
	hsm := newHSMDouble(t)
	p, _, _ := newTestProvider(WithWatchService(ws), WithKeyProviderFactory(singleTenantHSMFactory("acme", hsm)))
	ctx := context.Background()

	var invalidated atomicBool
	p.OnInvalidate(func() { invalidated.set(true) })

	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	tombstoneWatcher := ws.watcherFor(t, tenant.TombstonePrefix)

	tombstoneWatcher.deliver(t, watch.EventPut, tenant.TombstonePrefix+"acme")

	pollUntil(t, func() bool { return invalidated.get() })
}

// atomicBool is a tiny mutex-guarded bool, avoiding a data race between the
// watcher goroutine's write and the polling test goroutine's read.
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set(v bool) {
	b.mu.Lock()
	b.v = v
	b.mu.Unlock()
}
func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// TestDefaultCertProvider_WatchForDeletions_IgnoresUnrelatedKeys proves a
// delete or put under the watched prefixes that is NOT a material,
// certificate, or tombstone record is ignored rather than evicting
// whatever TrimSuffix/TrimPrefix happens to produce from it.
func TestDefaultCertProvider_WatchForDeletions_IgnoresUnrelatedKeys(t *testing.T) {
	ws := newFakeWatchService()
	p, _, _ := newTestProvider(WithWatchService(ws))
	ctx := context.Background()

	if _, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "carol", TenantID: "acme"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	certCAWatcher := ws.watcherFor(t, prefixCertCA)

	// A sibling record type under the same prefix, not material or
	// certificate.
	certCAWatcher.deliver(t, watch.EventDelete, prefixCertCA+"acme/some-other-record")

	// Give the (wrongly-matching) case a moment it would need if it were
	// going to fire, then confirm acme is still cached.
	time.Sleep(50 * time.Millisecond)
	p.mu.RLock()
	_, stillCached := p.perTenant["acme"]
	p.mu.RUnlock()
	if !stillCached {
		t.Error("an unrelated record under the cert_ca prefix evicted acme's cache entry")
	}
}

// TestDefaultCertProvider_WatchForDeletions_ReestablishesClosedWatch is
// go-qrdb#591's review (non-blocking) red/green proof: the hub closing a
// watcher's events channel out from under its consumer -- simulated here
// by closeEvents, distinct from a consumer-initiated Close -- must not
// leave eviction permanently dark for the rest of this provider's life.
// The supervisor re-establishes a fresh watch against the SAME
// WatchServicer, and an event delivered on THAT NEW watcher is still
// acted on.
func TestDefaultCertProvider_WatchForDeletions_ReestablishesClosedWatch(t *testing.T) {
	old := watchReestablishBackoff
	watchReestablishBackoff = time.Millisecond
	t.Cleanup(func() { watchReestablishBackoff = old })

	ws := newFakeWatchService()
	p, _, _ := newTestProvider(WithWatchService(ws))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	if _, err := p.GenerateClientCert(context.Background(), &GenerateRequest{Username: "dave", TenantID: "acme"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	firstWatcher := ws.watcherFor(t, prefixCertCA)
	firstWatcher.closeEvents()

	// The supervisor re-establishes by calling WatchPrefix again, which
	// registers a NEW *fakeWatcher for the same prefix; poll for it to
	// stop being the one that just closed.
	var secondWatcher *fakeWatcher
	pollUntil(t, func() bool {
		secondWatcher = ws.watcherFor(t, prefixCertCA)
		return secondWatcher != firstWatcher
	})

	secondWatcher.deliver(t, watch.EventDelete, prefixCertCA+"acme/material")
	pollUntil(t, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		_, stillCached := p.perTenant["acme"]
		return !stillCached
	})
}

// TestDefaultCertProvider_WatchForDeletions_ReestablishBackoffGrowsAndCaps
// is the red/green proof for go-qrdb#591's final delta review (non-blocking
// 1): reestablishWatch must not retry a failing WatchPrefix at the SAME
// fixed interval forever -- each failed attempt must wait LONGER than the
// last, up to watchReestablishBackoffCap.
func TestDefaultCertProvider_WatchForDeletions_ReestablishBackoffGrowsAndCaps(t *testing.T) {
	oldBackoff, oldCap := watchReestablishBackoff, watchReestablishBackoffCap
	watchReestablishBackoff = 20 * time.Millisecond
	watchReestablishBackoffCap = 60 * time.Millisecond
	t.Cleanup(func() { watchReestablishBackoff, watchReestablishBackoffCap = oldBackoff, oldCap })

	ws := newFakeWatchService()
	// WatchForDeletions' own two initial calls (one per watcher) succeed;
	// account for both before forcing the reestablish attempts to fail.
	p, _, _ := newTestProvider(WithWatchService(ws))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	firstWatcher := ws.watcherFor(t, prefixCertCA)

	ws.mu.Lock()
	ws.failNext = 3 // three failed retries, then the fourth call succeeds
	ws.mu.Unlock()
	firstWatcher.closeEvents()

	pollUntil(t, func() bool {
		return len(ws.callTimes()) >= 6 // 2 initial + 3 failed + 1 successful retry
	})
	calls := ws.callTimes()
	// calls[0]/[1] are WatchForDeletions' own two initial WatchPrefix
	// calls (cert_ca, then tombstone), both well before the close; the
	// cert_ca retries start at calls[2].
	retryGaps := make([]time.Duration, 0, 4)
	for i := 2; i < len(calls)-1; i++ {
		retryGaps = append(retryGaps, calls[i+1].Sub(calls[i]))
	}
	if len(retryGaps) < 3 {
		t.Fatalf("expected at least 3 retry gaps, got %d (%v)", len(retryGaps), retryGaps)
	}
	// A capped backoff plateaus (two consecutive gaps both near the cap),
	// and real goroutine scheduling jitter can make a later plateaued gap
	// measure a few milliseconds BELOW an earlier one despite both being
	// the same nominal backoff -- so shrinkage is only a real violation
	// past a small tolerance, not any decrease at all.
	const jitterTolerance = 15 * time.Millisecond
	for i := 1; i < len(retryGaps); i++ {
		if retryGaps[i]+jitterTolerance < retryGaps[i-1] {
			t.Errorf("retry gap %d (%v) is well below retry gap %d (%v): backoff shrank",
				i, retryGaps[i], i-1, retryGaps[i-1])
		}
	}
	// Growth actually happened at all (not merely "never shrank" from a
	// flat sequence): the first retry gap must be meaningfully shorter
	// than the last, which only reaching the cap explains.
	if first, last := retryGaps[0], retryGaps[len(retryGaps)-1]; first+jitterTolerance >= last {
		t.Errorf("no growth observed across retries: first gap %v, last gap %v", first, last)
	}
	for i, g := range retryGaps {
		if g > watchReestablishBackoffCap+jitterTolerance {
			t.Errorf("retry gap %d = %v, exceeds the cap %v by more than scheduling slack", i, g, watchReestablishBackoffCap)
		}
	}
}

// TestDefaultCertProvider_WatchForDeletions_ReestablishmentInvalidatesEverything
// is the red/green proof for go-qrdb#591's final delta review (non-blocking
// 1's second half): a DELETE that lands exactly during the gap between the
// old watcher's channel closing and the new one taking over is invisible
// to this node's watcher either way, so superviseWatch must not assume
// nothing relevant happened during that gap -- it invalidates every
// cached tenant and fires every OnInvalidate subscriber once the watch is
// re-established, with no delete event needing to be delivered at all.
func TestDefaultCertProvider_WatchForDeletions_ReestablishmentInvalidatesEverything(t *testing.T) {
	old := watchReestablishBackoff
	watchReestablishBackoff = time.Millisecond
	t.Cleanup(func() { watchReestablishBackoff = old })

	ws := newFakeWatchService()
	p, _, _ := newTestProvider(WithWatchService(ws))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var invalidated int32
	p.OnInvalidate(func() { atomic.AddInt32(&invalidated, 1) })

	if err := p.WatchForDeletions(ctx); err != nil {
		t.Fatalf("WatchForDeletions: %v", err)
	}
	if _, err := p.GenerateClientCert(context.Background(), &GenerateRequest{Username: "erin", TenantID: "initech"}); err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	p.mu.RLock()
	_, cachedBefore := p.perTenant["initech"]
	p.mu.RUnlock()
	if !cachedBefore {
		t.Fatal("expected initech's SoftwareProvider to be cached in perTenant before the gap")
	}

	firstWatcher := ws.watcherFor(t, prefixCertCA)
	firstWatcher.closeEvents()

	// No delete is ever delivered on the new watcher: reconciliation must
	// come from reestablishment itself, not from an event this test never
	// sends.
	pollUntil(t, func() bool {
		p.mu.RLock()
		defer p.mu.RUnlock()
		_, stillCached := p.perTenant["initech"]
		return !stillCached
	})
	if got := atomic.LoadInt32(&invalidated); got == 0 {
		t.Error("expected OnInvalidate to fire on watch reestablishment, even with no delete event delivered")
	}
}
