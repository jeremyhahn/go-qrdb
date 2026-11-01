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

package watch

import (
	"context"
	"sync/atomic"
)

// HubOption configures a Hub at construction.
type HubOption func(*Hub)

// WithEventBufferSize sets the event buffer of every watcher the hub opens,
// in responses (config.Watch.BufferSize, "watch.buffer_size" in YAML). A
// watcher whose consumer falls this far behind is evicted with
// ErrWatcherOverflow. n <= 0 keeps the default of 4096.
func WithEventBufferSize(n int) HubOption {
	return func(h *Hub) {
		if n > 0 {
			h.bufferSize = n
		}
	}
}

// WithWatcherLimit draws every watcher the hub opens from l, which the
// node's other hubs share, so the cap bounds the node rather than each
// database. A nil l leaves the hub unlimited.
func WithWatcherLimit(l *WatcherLimit) HubOption {
	return func(h *Hub) { h.limit = l }
}

// countedKey is the context key CountedContext sets.
type countedKey struct{}

// CountedContext returns ctx marked so a watch opened with it, or with a
// context derived from it, holds a slot of the hub's WatcherLimit. The
// transports mark every watch their callers open (service.NewCountedWatch);
// the node's own watchers -- tenant tombstones, placement, admin records --
// open unmarked, so watch.max_watchers bounds what callers hold and can never
// starve the node of the watches it runs on.
func CountedContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, countedKey{}, true)
}

// isCounted reports whether ctx carries CountedContext's mark.
func isCounted(ctx context.Context) bool {
	counted, _ := ctx.Value(countedKey{}).(bool)
	return counted
}

// WatcherLimit caps the counted watchers (CountedContext) open at once across
// every Hub that shares it (config.Watch.MaxWatchers, "watch.max_watchers" in
// YAML). A watcher holds its slot from registration until the hub
// deregisters it. A nil *WatcherLimit is unlimited. It is safe for concurrent
// use.
type WatcherLimit struct {
	limit  uint64
	active atomic.Uint64
}

// NewWatcherLimit returns a limit of n open watchers, or nil, which is
// unlimited, when n <= 0.
func NewWatcherLimit(n int) *WatcherLimit {
	if n <= 0 {
		return nil
	}
	return &WatcherLimit{limit: uint64(n)}
}

// Active returns the number of watchers holding a slot of l.
func (l *WatcherLimit) Active() uint64 {
	if l == nil {
		return 0
	}
	return l.active.Load()
}

// acquire takes a slot, or returns a *WatcherLimitError when every slot is
// held.
func (l *WatcherLimit) acquire() error {
	if l == nil {
		return nil
	}
	for {
		cur := l.active.Load()
		if cur >= l.limit {
			return &WatcherLimitError{Max: l.limit}
		}
		if l.active.CompareAndSwap(cur, cur+1) {
			return nil
		}
	}
}

// acquireFor takes a slot for a watch opened with ctx and reports whether it
// did: only a counted watch on a limited hub holds one.
func (l *WatcherLimit) acquireFor(ctx context.Context) (bool, error) {
	if l == nil || !isCounted(ctx) {
		return false, nil
	}
	if err := l.acquire(); err != nil {
		return false, err
	}
	return true, nil
}

// release returns a slot taken by acquire.
func (l *WatcherLimit) release() {
	if l == nil {
		return
	}
	l.active.Add(^uint64(0))
}
