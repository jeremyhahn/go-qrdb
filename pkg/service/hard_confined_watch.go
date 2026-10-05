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
// WatchEntity and WatchEntityType are NOT promoted through embedding:
// WatchService's own implementations call the hub's Watch/WatchPrefix
// directly and relay through a channel of WatchEntityEvent, not through
// this interface's Watch/WatchPrefix, so embedding would bypass the
// refusal and filter above entirely. WatchEntity's key is composed exactly
// like Watch's -- refused outright, matching Watch.
//
// WatchEntityType's prefix is "{entityType}/", and an EARLIER revision of
// this file assumed that meant it could only ever match one of
// hardConfinedPrefixes exactly or not overlap at all -- "an entity type is
// never a strict substring of another" -- and refused outright on that
// exact-match test alone. That assumption is false: an EntityType whose
// own string is a SUB-PATH of a confined prefix (e.g. one beginning
// "__sys:cert_ca/something") composes a prefix that lies INSIDE the
// confined namespace without equaling it, which neither isHardConfinedKey
// (wrong suffix test) nor an exact-equality check against
// hardConfinedPrefixes ever catches, yet would reach the real
// WatchEntityType unfiltered (go-qrdb#591's review, non-blocking -- found
// latent, closed before it was reachable in practice). WatchEntityType now
// uses hardConfinedPrefixOverlaps, the same overlap test WatchPrefix uses,
// and filters rather than refuses when it overlaps without being an exact
// root match, for the identical go-qrdb#555 reason WatchPrefix does: an
// outright refusal over a broad sweep that merely touches the confined
// namespace would break a legitimate broad watch the same way it broke a
// broad Range. The bare confined root itself (isHardConfinedRoot) is still
// refused outright, matching Watch and WatchEntity's own overt-probe
// treatment.
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
	inner  watch.Watcher
	events chan watch.WatchResponse
	done   chan struct{}
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

func (h *hardConfinedWatch) WatchEntity(ctx context.Context, req *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	if req != nil {
		if key := entityKey(req.EntityType, req.ID); isHardConfinedKey(key) {
			return nil, &HardConfinedKeyError{Key: key}
		}
	}
	return h.WatchServicer.WatchEntity(ctx, req)
}

func (h *hardConfinedWatch) WatchEntityType(ctx context.Context, req *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	if req == nil {
		return h.WatchServicer.WatchEntityType(ctx, req)
	}
	prefix := entityPrefix(req.EntityType)
	if isHardConfinedRoot(prefix) {
		return nil, &HardConfinedKeyError{Key: prefix}
	}
	inner, err := h.WatchServicer.WatchEntityType(ctx, req)
	if err != nil {
		return nil, err
	}
	if !hardConfinedPrefixOverlaps(prefix) {
		return inner, nil
	}
	return newHardConfinedFilteredEntityEvents(ctx, inner), nil
}

// hardConfinedFilteredEntityEvents is hardConfinedFilteredWatcher's analog
// for WatchEntityType's own channel of WatchEntityEvent: every event whose
// composed storage key (entityKey) lies in the hard-confined namespace is
// dropped before it reaches the caller.
//
// The send to out is selected against ctx.Done() (go-qrdb#591's review,
// non-blocking 4): out is unbuffered and this is the only goroutine that
// ever sends to it, so a caller that stops reading -- abandons the
// returned channel without draining it, rather than ranging it to
// completion -- would otherwise leave this goroutine parked on `out <- ev`
// forever, pinning inner's own upstream watch open for as long as the
// process runs. Returning on ctx.Done() instead lets the caller's own
// cancellation (the ONLY way whoever handed us ctx expects this watch to
// end) reclaim this goroutine even when nothing is listening on out
// anymore.
func newHardConfinedFilteredEntityEvents(ctx context.Context, inner <-chan WatchEntityEvent) <-chan WatchEntityEvent {
	out := make(chan WatchEntityEvent)
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-inner:
				if !ok {
					return
				}
				if isHardConfinedKey(entityKey(ev.EntityType, ev.ID)) {
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
