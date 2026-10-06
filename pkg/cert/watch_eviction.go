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
	"log/slog"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// watchReestablishBackoff is the STARTING delay WatchForDeletions'
// supervisors wait before asking the hub for a fresh watcher, after one of
// its two watched channels closes for a reason other than ctx being
// canceled (go-qrdb#591's review, non-blocking): the hub closing a
// watcher's channel is not necessarily terminal for the whole process (a
// mid-reconnect window, a leader transfer), and a consumer goroutine that
// simply exits on that leaves eviction permanently dark for the rest of
// this node's life, with nothing visible to say so. reestablishWatch
// doubles this on every failed WatchPrefix retry, capped at
// watchReestablishBackoffCap, so a hub that stays unavailable for a long
// time is retried at a bounded interval rather than hammered at a fixed
// 2-second rate for as long as it is down.
var watchReestablishBackoff = 2 * time.Second

// watchReestablishBackoffCap bounds how large reestablishWatch's
// exponential backoff may grow.
var watchReestablishBackoffCap = 1 * time.Minute

// WatchPrefixer is the one method WatchForDeletions needs from a watch
// service: a prefix watcher. Declared locally, narrower than
// service.WatchServicer, so this package depends only on pkg/watch rather
// than the whole pkg/service -- which itself depends on pkg/bootstrap,
// which depends back on pkg/cert for the onboarding seam
// (bootstrap.ProvisionTenant), an import cycle service.WatchServicer would
// otherwise close. Any service.WatchServicer satisfies this implicitly.
type WatchPrefixer interface {
	WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// WatchForDeletions starts two background watchers -- over __sys:cert_ca/
// for DELETE events and over the tenant tombstone prefix
// (tenant.TombstonePrefix) for PUT events -- that evict this provider's
// cached per-tenant state and notify every subscriber (OnInvalidate) the
// moment either fires, on EVERY node that observes the replicated write,
// not only the one that issued it:
//
//   - A __sys:cert_ca/{tenantID}/material or /certificate deletion evicts
//     perTenant[tenantID] (the built-in path): the in-memory
//     cakey.SoftwareProvider cached there holds its key and certificate
//     forever once established (Certificate/CertificateOnly's cold-cache
//     fast path never re-reads the store once cert is non-nil), so without
//     this a provider whose underlying record was deleted -- directly, or
//     as part of some future tenant-teardown step that reclaims it --
//     would keep serving the OLD key out of memory regardless of what the
//     store now holds.
//   - A tenant.TombstonePrefix + tenantID write -- pkg/tenant's
//     DeleteTenant, which every tenant deletion goes through regardless of
//     isolation mode -- evicts perTenant[tenantID] too and, unconditionally,
//     calls every OnInvalidate subscriber. Ordinary tenant teardown already
//     reaches the built-in path through the cert_ca DELETE watcher above as
//     well -- service.TenantKeyPrefixes's reclaim sweep includes
//     __sys:cert_ca/{tenantID}/, so deleting a tenant deletes its Key B
//     material and certificate records too -- but the tombstone write is
//     the ONLY eviction signal a factory-backed tenant has: resolveKeyProvider
//     never caches a factory's own Provider in perTenant, so there is
//     nothing there to evict, but the widened trust pool's OWN cache
//     (pkg/clienttrust.Pool, subscribed via OnInvalidate) still needs to
//     stop trusting a deleted tenant's CA immediately rather than waiting
//     out its own TTL.
//
// A no-op when WithWatchService was never called (watchSvc is nil) -- the
// documented "evict only by TTL" posture a deployment with no replicated
// watch hub has (a test constructing DefaultCertProvider directly, or an
// embedder that chose not to wire one). Runs until ctx is canceled; call
// once, after the node's watch hub is live.
func (p *DefaultCertProvider) WatchForDeletions(ctx context.Context) error {
	if p.watchSvc == nil {
		return nil
	}
	certCAWatcher, err := p.watchSvc.WatchPrefix(ctx, prefixCertCA, watch.WithFilters(watch.WatchFilterNoPut))
	if err != nil {
		return &StoreError{Op: "watch", Detail: prefixCertCA, Err: err}
	}
	tombstoneWatcher, err := p.watchSvc.WatchPrefix(ctx, tenant.TombstonePrefix, watch.WithFilters(watch.WatchFilterNoDelete))
	if err != nil {
		_ = certCAWatcher.Close()
		return &StoreError{Op: "watch", Detail: tenant.TombstonePrefix, Err: err}
	}
	go p.superviseWatch(ctx, "cert_ca deletions", prefixCertCA,
		[]watch.WatchOption{watch.WithFilters(watch.WatchFilterNoPut)}, certCAWatcher, p.consumeCertCADeletes)
	go p.superviseWatch(ctx, "tenant tombstones", tenant.TombstonePrefix,
		[]watch.WatchOption{watch.WithFilters(watch.WatchFilterNoDelete)}, tombstoneWatcher, p.consumeTombstonePuts)
	return nil
}

// superviseWatch runs consume over w until ctx is canceled or w's channel
// closes. A close while ctx is still live is NOT treated as a clean
// shutdown (go-qrdb#591's review, non-blocking): it is logged at WARN,
// naming label and prefix, and the watch is re-established against
// p.watchSvc, backing off between attempts -- bounded by ctx throughout,
// so a canceled context during a backoff wait stops retrying rather than
// looping forever on a hub that is gone for good (process shutdown). Once
// the watch comes back, invalidateAll() reconciles any delete the gap
// between the close and the new watcher may have missed -- a DELETE this
// node's watcher never saw because it landed exactly during the gap would
// otherwise leave a stale perTenant entry (or the widened trust pool's own
// cache) cached forever, never evicted by anything -- rather than trusting
// that nothing relevant happened while this node could not have known
// either way.
// Both of WatchForDeletions' watchers run through this so eviction never
// goes permanently dark for the rest of this node's life on one closed
// channel.
func (p *DefaultCertProvider) superviseWatch(ctx context.Context, label, prefix string, opts []watch.WatchOption, w watch.Watcher, consume func(context.Context, watch.Watcher)) {
	for {
		consume(ctx, w)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("qrdb/cert: watch closed; re-establishing",
			slog.String("watch", label), slog.String("prefix", prefix))

		next, err := p.reestablishWatch(ctx, label, prefix, opts)
		if err != nil {
			// Only reachable when ctx itself ended during the retry loop.
			return
		}
		p.invalidateAll()
		w = next
	}
}

// reestablishWatch retries p.watchSvc.WatchPrefix, backing off
// EXPONENTIALLY between attempts starting from watchReestablishBackoff and
// capped at watchReestablishBackoffCap, until it succeeds or ctx ends.
// Returns an error only when ctx itself is done -- a WatchPrefix failure
// is logged at WARN and retried, never given up on while ctx is still
// live, since the hub being briefly unavailable (not yet ready, a
// mid-reconnect window) is exactly the condition this exists to ride out.
func (p *DefaultCertProvider) reestablishWatch(ctx context.Context, label, prefix string, opts []watch.WatchOption) (watch.Watcher, error) {
	backoff := watchReestablishBackoff
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		w, err := p.watchSvc.WatchPrefix(ctx, prefix, opts...)
		if err == nil {
			return w, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		slog.Warn("qrdb/cert: re-establishing watch failed; will retry",
			slog.String("watch", label), slog.String("prefix", prefix), slog.Any("error", err), slog.Duration("backoff", backoff))
		if backoff *= 2; backoff > watchReestablishBackoffCap {
			backoff = watchReestablishBackoffCap
		}
	}
}

// OnInvalidate registers fn to be called, going forward, whenever a
// watched deletion evicts a tenant's cached Key B state (WatchForDeletions'
// two consumers). pkg/clienttrust.Pool is the first subscriber, from
// SetKeyBProvider, so its own widened-pool cache drops a deleted tenant's
// CA immediately instead of waiting out poolCacheTTL. Safe to call before
// WatchForDeletions has started, or when it is never called at all; fn is
// simply never invoked in that case.
func (p *DefaultCertProvider) OnInvalidate(fn func()) {
	if fn == nil {
		return
	}
	p.mu.Lock()
	p.invalidateHooks = append(p.invalidateHooks, fn)
	p.mu.Unlock()
}

// invalidateAll drops EVERY tenant's cached built-in cakey.SoftwareProvider
// and notifies every OnInvalidate subscriber, exactly as if every tenant
// had just been individually evicted. Called once a watch superviseWatch
// re-established came back up, so a DELETE that landed during the gap
// between the old watcher's channel closing and the new one taking over --
// invisible to this node either way, since no watcher was listening -- is
// still reconciled: the stale entry (or the widened trust pool's cache
// subscribed via OnInvalidate) is not trusted to still be correct merely
// because nothing was observed, since nothing being observed is exactly
// the gap's own failure mode, not evidence that nothing changed.
func (p *DefaultCertProvider) invalidateAll() {
	p.mu.Lock()
	p.perTenant = make(map[string]*perTenantEntry)
	hooks := p.invalidateHooks
	p.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// evictTenant drops tenantID's cached built-in cakey.SoftwareProvider, if
// any, so the next resolveKeyProvider call for tenantID constructs a fresh
// one from whatever the store currently holds (nothing, if its records
// were deleted) rather than continuing to serve the in-memory key and
// certificate the old instance cached. A no-op for a tenant this provider
// never cached (the common case for a factory-backed tenant, or for one
// whose Key B material was never resolved on this node).
func (p *DefaultCertProvider) evictTenant(tenantID string) {
	p.mu.Lock()
	delete(p.perTenant, tenantID)
	hooks := p.invalidateHooks
	p.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// consumeCertCADeletes evicts tenantID for every __sys:cert_ca/{tenantID}/
// material or certificate DELETE event w delivers, until ctx is canceled
// or w's channel closes.
func (p *DefaultCertProvider) consumeCertCADeletes(ctx context.Context, w watch.Watcher) {
	defer func() { _ = w.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-w.Events():
			if !ok {
				return
			}
			for _, ev := range resp.Events {
				if ev == nil || ev.Type != watch.EventDelete || ev.Kv == nil {
					continue
				}
				rel := strings.TrimPrefix(string(ev.Kv.Key), prefixCertCA)
				var tenantID string
				switch {
				case strings.HasSuffix(rel, materialRecordSuffix):
					tenantID = strings.TrimSuffix(rel, materialRecordSuffix)
				case strings.HasSuffix(rel, certRecordSuffix):
					tenantID = strings.TrimSuffix(rel, certRecordSuffix)
				default:
					continue
				}
				if tenantID == "" {
					continue
				}
				p.evictTenant(tenantID)
			}
		}
	}
}

// consumeTombstonePuts evicts tenantID, and notifies every OnInvalidate
// subscriber, for every tenant.TombstonePrefix + tenantID PUT event w
// delivers, until ctx is canceled or w's channel closes.
func (p *DefaultCertProvider) consumeTombstonePuts(ctx context.Context, w watch.Watcher) {
	defer func() { _ = w.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-w.Events():
			if !ok {
				return
			}
			for _, ev := range resp.Events {
				if ev == nil || ev.Type != watch.EventPut || ev.Kv == nil {
					continue
				}
				key := string(ev.Kv.Key)
				tenantID := strings.TrimPrefix(key, tenant.TombstonePrefix)
				if tenantID == "" || tenantID == key {
					continue
				}
				p.evictTenant(tenantID)
			}
		}
	}
}
