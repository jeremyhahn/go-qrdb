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

package lifecycle

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// undoneWatchBackoff is the first wait before re-establishing a closed
// undone-tenant watch; each failed attempt doubles it, up to
// undoneWatchBackoffCap.
var (
	undoneWatchBackoff    = 2 * time.Second
	undoneWatchBackoffCap = time.Minute
)

// undoneReconcileTimeout bounds one reconcile the watch runs. Each runs on its
// own context, as the boot reconcile's attempts do: on the node's background
// context one raft read that never answered would stall the watch for good.
var undoneReconcileTimeout = 30 * time.Second

// prefixWatcher opens a watch on a key prefix; the node's watch service is
// one.
type prefixWatcher interface {
	WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// undoneReconciler is the tenant service as the watch drives it: one undone
// id on demand, and a full pass after a gap in the watch.
type undoneReconciler interface {
	tenant.UndoReconciler
	ReconcileDeletedTenants(ctx context.Context) (*tenant.ReconcileReport, error)
}

// UndoneTenantWatchError is returned by WatchUndoneTenants when the watch on
// the tenant tombstones cannot be opened.
type UndoneTenantWatchError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UndoneTenantWatchError) Error() string {
	return "lifecycle: watch undone tenants: " + e.Err.Error()
}

// Unwrap returns the watch service's error.
func (e *UndoneTenantWatchError) Unwrap() error { return e.Err }

// startUndoneWatch makes this node clean up, and acknowledge, an undone
// tenant create as soon as the undone tombstone reaches it, instead of at its
// next deleted-tenant reconcile: a create of the id waits on every serving
// node's acknowledgement. Every tombstone write is handed to the tenant
// service's ReconcileUndone, which leaves any but an undone one alone. A
// write that arrives before the teardown completed (the metadata delete or
// the keyspace reclamation still to commit) acknowledges nothing; the
// teardown's last step rewrites the tombstone as TornDown, after both, and
// that write is the event this node acknowledges on. Once the watch is
// registered a full reconcile runs, which covers a write before it; a watch
// that closes while ctx is live is re-established, backing off, and followed
// by another, which covers a write the gap hid.
//
// Runs until ctx is canceled or n stops; StartNodeWatchers calls it once per
// node. The watch is n's own work (nodeWork), so Stop cancels it and waits for
// it to return before running any closer: every reconcile it runs reads
// through the tenant service, and so through the storage beneath it, and one
// still in flight when that storage closes holds an iterator open across the
// close, which Pebble refuses. Cancelling ctx alone does not end that
// reconcile in time, and a composer that rebuilds its data plane (an
// in-process encryption migration) keeps its long-lived ctx across the
// rebuild. A node without a watch service, or whose tenant service reconciles
// no undone id, starts nothing.
func startUndoneWatch(ctx context.Context, n *Node) error {
	if n == nil || n.Watch == nil || n.Tenant == nil {
		return nil
	}
	r, ok := n.Tenant.(undoneReconciler)
	if !ok {
		return nil
	}
	return watchUndoneTenants(ctx, &n.work, n.Watch, r, slog.Default())
}

// watchUndoneTenants opens the watch on the tenant tombstones and supervises
// it as a piece of work, until ctx ends or work stops. Once work has stopped
// it supervises nothing: the watch it opened is closed again.
func watchUndoneTenants(ctx context.Context, work *nodeWork, ws prefixWatcher, r undoneReconciler, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	stopWithNode := context.AfterFunc(work.context(), cancel)
	w, err := ws.WatchPrefix(ctx, tenant.TombstonePrefix, undoneWatchOptions()...)
	if err != nil {
		stopWithNode()
		cancel()
		return &UndoneTenantWatchError{Err: err}
	}
	if !work.start(func(context.Context) {
		defer cancel()
		defer stopWithNode()
		superviseUndoneWatch(ctx, ws, r, w, logger)
	}) {
		stopWithNode()
		cancel()
		_ = w.Close()
	}
	return nil
}

// undoneWatchOptions selects tombstone writes: a tombstone is deleted only
// when its id is reused, which leaves nothing to reconcile.
func undoneWatchOptions() []watch.WatchOption {
	return []watch.WatchOption{watch.WithFilters(watch.WatchFilterNoDelete)}
}

// superviseUndoneWatch consumes w until ctx ends, re-establishing it whenever
// it closes first. Each watch, the first included, is preceded by a full
// reconcile (reconcileUnwatched).
func superviseUndoneWatch(ctx context.Context, ws prefixWatcher, r undoneReconciler, w watch.Watcher, logger *slog.Logger) {
	for {
		reconcileUnwatched(ctx, r, logger)
		consumeUndoneTombstones(ctx, w, r, logger)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("lifecycle: undone-tenant watch closed; re-establishing")
		next, ok := reestablishUndoneWatch(ctx, ws, logger)
		if !ok {
			return
		}
		w = next
	}
}

// reconcileUnwatched runs one full deleted-tenant reconcile once a watch is
// registered. No event delivers a tombstone written before that -- before
// this node booted, or while a closed watch was being re-established -- and
// the boot reconcile, where a composition runs one, runs before the watch
// exists. Writes after registration queue on the watch meanwhile.
func reconcileUnwatched(ctx context.Context, r undoneReconciler, logger *slog.Logger) {
	rctx, cancel := context.WithTimeout(ctx, undoneReconcileTimeout)
	defer cancel()
	if _, err := r.ReconcileDeletedTenants(rctx); err != nil {
		logger.Warn("lifecycle: deleted-tenant reconcile before the undone-tenant watch's first event failed; run the reconcile-deleted-tenants verb",
			slog.Any("error", err))
	}
}

// reestablishUndoneWatch retries the watch, backing off, until it opens or
// ctx ends; ok is false only when ctx ended.
func reestablishUndoneWatch(ctx context.Context, ws prefixWatcher, logger *slog.Logger) (watch.Watcher, bool) {
	backoff := undoneWatchBackoff
	for {
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		w, err := ws.WatchPrefix(ctx, tenant.TombstonePrefix, undoneWatchOptions()...)
		if err == nil {
			return w, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		logger.Warn("lifecycle: re-establishing the undone-tenant watch failed; will retry",
			slog.Any("error", err), slog.Duration("backoff", backoff))
		if backoff *= 2; backoff > undoneWatchBackoffCap {
			backoff = undoneWatchBackoffCap
		}
	}
}

// consumeUndoneTombstones hands every tombstone write w delivers to
// r.ReconcileUndone, until ctx ends or w's channel closes. A reconcile that
// fails is logged and left to the next pass.
func consumeUndoneTombstones(ctx context.Context, w watch.Watcher, r undoneReconciler, logger *slog.Logger) {
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
				id := strings.TrimPrefix(key, tenant.TombstonePrefix)
				if id == "" || id == key {
					continue
				}
				rctx, cancel := context.WithTimeout(ctx, undoneReconcileTimeout)
				err := r.ReconcileUndone(rctx, id)
				cancel()
				if err != nil {
					logger.Warn("lifecycle: undone tenant cleanup left to the next reconcile",
						slog.String("tenant_id", id), slog.Any("error", err))
				}
			}
		}
	}
}
