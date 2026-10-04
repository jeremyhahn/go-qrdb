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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

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
//     isolation mode -- evicts perTenant[tenantID] too (belt-and-braces:
//     today's teardown does not itself touch __sys:cert_ca/, so this is
//     presently the only replicated signal for the built-in path as well)
//     and, unconditionally, calls every OnInvalidate subscriber. This is
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
	go p.consumeCertCADeletes(ctx, certCAWatcher)
	go p.consumeTombstonePuts(ctx, tombstoneWatcher)
	return nil
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
