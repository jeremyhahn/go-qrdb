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
	return newNamespacedWatcher(w, s.prefix), nil
}

// WatchPrefix watches all keys matching the given prefix within the namespace,
// stripping the namespace prefix from delivered event keys.
func (s *namespacedWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	w, err := s.inner.WatchPrefix(ctx, s.prefix+prefix, opts...)
	if err != nil {
		return nil, err
	}
	return newNamespacedWatcher(w, s.prefix), nil
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
	return s.pumpEntityEvents(in, false), nil
}

// WatchEntityType watches all entities of a type, filtering delivered events to
// those whose entity ID falls under the namespace and stripping the prefix.
func (s *namespacedWatchService) WatchEntityType(ctx context.Context, req *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	in, err := s.inner.WatchEntityType(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.pumpEntityEvents(in, true), nil
}

// pumpEntityEvents forwards entity events, stripping the namespace prefix from
// each event ID. When filter is true, events whose ID does not fall under the
// namespace are dropped (used for type-level watches that span the store).
func (s *namespacedWatchService) pumpEntityEvents(in <-chan service.WatchEntityEvent, filter bool) <-chan service.WatchEntityEvent {
	out := make(chan service.WatchEntityEvent)
	go func() {
		defer close(out)
		for ev := range in {
			if filter && !strings.HasPrefix(ev.ID, s.prefix) {
				continue
			}
			ev.ID = strings.TrimPrefix(ev.ID, s.prefix)
			out <- ev
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
// watcher until the inner channel is closed (which happens on Close).
func newNamespacedWatcher(inner watch.Watcher, prefix string) *namespacedWatcher {
	w := &namespacedWatcher{
		inner:  inner,
		prefix: prefix,
		out:    make(chan watch.WatchResponse),
	}
	go w.pump()
	return w
}

// pump forwards responses from the inner watcher, stripping event keys, until
// the inner channel is closed.
func (w *namespacedWatcher) pump() {
	defer close(w.out)
	for resp := range w.inner.Events() {
		resp.Events = w.stripEvents(resp.Events)
		w.out <- resp
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
