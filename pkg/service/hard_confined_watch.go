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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// hardConfinedWatch wraps a WatchServicer, refusing a Watch on the
// hard-confined key outright (there is only one key in that call, nothing
// to filter) and filtering hard-confined events out of a WatchPrefix
// stream so a broad prefix watch is not refused merely because the
// namespace happens to lie within it -- the same "filter the enumeration,
// refuse the point read" split hardConfinedKV and hardConfinedMVCC make.
//
// WatchEntity and WatchEntityType are promoted unchanged through
// embedding: an entity key is composed from EntityType and ID by the
// underlying store, not supplied as a literal string, so a caller cannot
// address "__sys:cluster_ca/..." through them.
type hardConfinedWatch struct {
	WatchServicer
}

// NewHardConfinedWatch wraps svc, or returns nil unchanged so a resolver
// that found no service configured for a scope keeps reporting that
// rather than a wrapped nil satisfying the interface.
func NewHardConfinedWatch(svc WatchServicer) WatchServicer {
	if svc == nil {
		return nil
	}
	return &hardConfinedWatch{WatchServicer: svc}
}

func (h *hardConfinedWatch) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	if isHardConfinedKey(key) {
		return nil, &HardConfinedKeyError{Key: key}
	}
	return h.WatchServicer.Watch(ctx, key, opts...)
}

// WatchPrefix wraps the underlying watcher so every delivered event for a
// hard-confined key is dropped before it ever reaches the caller, rather
// than refusing prefixes that happen to span the namespace (go-qrdb#555's
// own false-positive lesson: an outright refusal here would break a
// legitimate broad watch the same way it broke a broad Range).
func (h *hardConfinedWatch) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	inner, err := h.WatchServicer.WatchPrefix(ctx, prefix, opts...)
	if err != nil {
		return nil, err
	}
	if !hardConfinedPrefixOverlaps(prefix) {
		return inner, nil
	}
	return newHardConfinedFilteredWatcher(inner), nil
}

// hardConfinedFilteredWatcher wraps a watch.Watcher, dropping every
// WatchResponse event whose key lies in the hard-confined namespace
// before it reaches the caller.
type hardConfinedFilteredWatcher struct {
	inner    watch.Watcher
	events   chan watch.WatchResponse
	done     chan struct{}
	closeErr error
}

func newHardConfinedFilteredWatcher(inner watch.Watcher) *hardConfinedFilteredWatcher {
	w := &hardConfinedFilteredWatcher{
		inner:  inner,
		events: make(chan watch.WatchResponse),
		done:   make(chan struct{}),
	}
	go w.pump()
	return w
}

func (w *hardConfinedFilteredWatcher) pump() {
	defer close(w.events)
	for resp := range w.inner.Events() {
		filtered := make([]*watch.Event, 0, len(resp.Events))
		for _, ev := range resp.Events {
			if eventTouchesHardConfinedKey(ev) {
				continue
			}
			filtered = append(filtered, ev)
		}
		resp.Events = filtered
		select {
		case w.events <- resp:
		case <-w.done:
			return
		}
	}
}

// eventTouchesHardConfinedKey reports whether ev's current or previous
// key-value lies in the hard-confined namespace. A delete event carries
// its key only in PrevKv (Kv's key-value state AFTER the event, which for
// a delete is absent), so both are checked.
func eventTouchesHardConfinedKey(ev *watch.Event) bool {
	if ev == nil {
		return false
	}
	if ev.Kv != nil && isHardConfinedKey(string(ev.Kv.Key)) {
		return true
	}
	if ev.PrevKv != nil && isHardConfinedKey(string(ev.PrevKv.Key)) {
		return true
	}
	return false
}

func (w *hardConfinedFilteredWatcher) Events() <-chan watch.WatchResponse { return w.events }

func (w *hardConfinedFilteredWatcher) WatchID() int64 { return w.inner.WatchID() }

func (w *hardConfinedFilteredWatcher) Close() error {
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	return w.inner.Close()
}

func (w *hardConfinedFilteredWatcher) Err() error { return w.inner.Err() }
