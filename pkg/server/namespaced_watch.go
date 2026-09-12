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

package server

import (
	"context"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// namespacedWatchService wraps a WatchServicer so that watched keys and
// prefixes are transparently prefixed with the composite (tenant, db)
// namespace and the prefix is stripped from delivered event keys. Entity
// watches have their entity ID prefixed, and type-level entity watches are
// filtered to the namespace so a database only observes its own entities.
type namespacedWatchService struct {
	inner  service.WatchServicer
	prefix string
}

// Compile-time interface check.
var _ service.WatchServicer = (*namespacedWatchService)(nil)

// newNamespacedWatchService creates a WatchServicer scoped to the given
// composite namespace prefix.
func newNamespacedWatchService(inner service.WatchServicer, prefix string) *namespacedWatchService {
	return &namespacedWatchService{inner: inner, prefix: prefix}
}

// Watch watches a single key within the namespace, stripping the prefix from
// delivered event keys.
func (s *namespacedWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	w, err := s.inner.Watch(ctx, s.prefix+key, opts...)
	if err != nil {
		return nil, err
	}
	return newNamespacedWatcher(ctx, w, s.prefix), nil
}

// WatchPrefix watches all keys matching the given prefix within the namespace,
// stripping the namespace prefix from delivered event keys.
func (s *namespacedWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	w, err := s.inner.WatchPrefix(ctx, s.prefix+prefix, opts...)
	if err != nil {
		return nil, err
	}
	return newNamespacedWatcher(ctx, w, s.prefix), nil
}

// WatchEntity watches a specific entity, prefixing its ID so the watched key is
// scoped to the namespace and stripping the prefix from delivered event IDs.
func (s *namespacedWatchService) WatchEntity(ctx context.Context, req *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	if req == nil {
		return s.inner.WatchEntity(ctx, req)
	}
	cp := *req
	cp.ID = s.prefix + req.ID
	in, err := s.inner.WatchEntity(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return s.pumpEntityEvents(ctx, in, false), nil
}

// WatchEntityType watches all entities of a type, filtering delivered events to
// those whose entity ID falls under the namespace and stripping the prefix.
func (s *namespacedWatchService) WatchEntityType(ctx context.Context, req *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	in, err := s.inner.WatchEntityType(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.pumpEntityEvents(ctx, in, true), nil
}

// pumpEntityEvents forwards entity events, stripping the namespace prefix from
// each event ID. When filter is true, events whose ID does not fall under the
// namespace are dropped (used for type-level watches that span the store).
//
// The terminal event is exempt from both. It reports why the STREAM ended and
// names no entity, so it has no ID to strip and none to match the namespace
// against — a filter applied to it would discard the one event the consumer
// cannot reconstruct, handing back the silent close the service layer just
// stopped producing. It is also the last thing the stream says, so the pump
// stops there rather than waiting for a close that can add nothing.
//
// EVERY channel operation selects on ctx. A consumer ends a watch by cancelling
// the context it opened it with, so a cancel racing a delivery is the ordinary
// way this stream finishes, and a bare send on the unbuffered handover would
// have no reader and park for the life of the process — one goroutine per
// cancelled watch, and the deferred close(out) never running, on a wrapper
// every namespaced deployment puts on every watch (go-qrdb#499). Buffering does
// not fix that; it moves the block to the send after the buffer fills. The
// service layer's own relay already selects (service.WatchService.
// relayEntityEvents), which is why the cancel released it and stopped one hop
// short of here.
func (s *namespacedWatchService) pumpEntityEvents(ctx context.Context, in <-chan service.WatchEntityEvent, filter bool) <-chan service.WatchEntityEvent {
	out := make(chan service.WatchEntityEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				if ev.Canceled {
					select {
					case out <- ev:
					case <-ctx.Done():
					}
					return
				}
				if filter && !strings.HasPrefix(ev.ID, s.prefix) {
					continue
				}
				ev.ID = strings.TrimPrefix(ev.ID, s.prefix)
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// namespacedWatcher wraps a watch.Watcher and strips the namespace prefix from
// the keys of every delivered event.
type namespacedWatcher struct {
	inner  watch.Watcher
	prefix string
	out    chan watch.WatchResponse
}

// Compile-time interface check.
var _ watch.Watcher = (*namespacedWatcher)(nil)

// newNamespacedWatcher creates a watcher that strips the namespace prefix from
// event keys. It launches a goroutine that forwards events from the inner
// watcher until the inner channel is closed (which happens on Close) or ctx is
// done. ctx is the context the watch was opened with — the one whose
// cancellation is how a consumer ends the watch.
func newNamespacedWatcher(ctx context.Context, inner watch.Watcher, prefix string) *namespacedWatcher {
	w := &namespacedWatcher{
		inner:  inner,
		prefix: prefix,
		out:    make(chan watch.WatchResponse),
	}
	go w.pump(ctx)
	return w
}

// pump forwards responses from the inner watcher, stripping event keys, until
// the inner channel is closed or ctx is done.
//
// EVERY channel operation selects on ctx, for the reason spelled out on
// pumpEntityEvents: cancelling the watch is how a consumer leaves, so an
// unguarded send on the unbuffered handover parks forever the first time a
// cancel races a delivery (go-qrdb#499). The inner watcher's own event channel
// closing is NOT enough to release it — the parked send never gets back to the
// receive that would observe the close.
//
// The pump does not close the inner watcher on its way out, and that is
// deliberate: namespacedWatcher IS a watch.Watcher, so its consumer owns it and
// releases it through Close, which forwards. Closing here would make the
// wrapper a second owner of a watcher it was handed. Nothing is stranded by
// that — the inner watcher was opened with this same ctx, so the hub's own
// cleanup deregisters it when the ctx is cancelled whether or not anyone calls
// Close.
func (w *namespacedWatcher) pump(ctx context.Context) {
	defer close(w.out)
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-w.inner.Events():
			if !ok {
				return
			}
			resp.Events = w.stripEvents(resp.Events)
			select {
			case w.out <- resp:
			case <-ctx.Done():
				return
			}
		}
	}
}

// stripEvents returns a copy of events with the namespace prefix stripped from
// each event's current and previous key-values.
func (w *namespacedWatcher) stripEvents(events []*watch.Event) []*watch.Event {
	if events == nil {
		return nil
	}
	out := make([]*watch.Event, len(events))
	for i, ev := range events {
		if ev == nil {
			continue
		}
		cp := *ev
		cp.Kv = stripKeyValue(ev.Kv, w.prefix)
		cp.PrevKv = stripKeyValue(ev.PrevKv, w.prefix)
		out[i] = &cp
	}
	return out
}

// Events returns the channel on which prefix-stripped watch responses are
// delivered.
func (w *namespacedWatcher) Events() <-chan watch.WatchResponse { return w.out }

// WatchID returns the unique identifier of the underlying watcher.
func (w *namespacedWatcher) WatchID() int64 { return w.inner.WatchID() }

// Close cancels the underlying watcher; the forwarding goroutine then drains
// and closes the output channel.
func (w *namespacedWatcher) Close() error { return w.inner.Close() }

// Err returns the first error encountered by the underlying watcher.
func (w *namespacedWatcher) Err() error { return w.inner.Err() }
