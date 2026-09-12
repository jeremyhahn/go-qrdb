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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// WatchServicer provides real-time change notifications for keys in the
// MVCC key-value store. Events are delivered in revision order, and
// watchers can resume from a historical revision to replay missed events.
type WatchServicer interface {
	// Watch creates a watcher that delivers events for the specified
	// key. The watcher remains active until Close is called or the
	// context is canceled.
	Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)

	// WatchPrefix creates a watcher that delivers events for all keys
	// matching the given prefix. The watcher remains active until
	// Close is called or the context is canceled.
	WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)

	// WatchEntity watches for changes to a specific entity identified
	// by its type and ID. Events are transformed from raw KV events
	// into entity-typed events and delivered on the returned channel.
	// The channel is closed when the context is canceled.
	//
	// A watch that ends for a reason — a watcher evicted for overflow, a
	// compaction that outran an in-progress replay — delivers one final
	// event with Canceled set and the reason carried verbatim before the
	// channel closes. A watch that ends cleanly delivers no such event, so
	// the two are told apart by what arrived, not by the closure.
	WatchEntity(ctx context.Context, req *WatchEntityRequest) (<-chan WatchEntityEvent, error)

	// WatchEntityType watches for changes to all entities of a given
	// type. Events are transformed from raw KV prefix-watch events
	// into entity-typed events and delivered on the returned channel.
	// The channel is closed when the context is canceled. It carries the
	// same terminal event as WatchEntity when the watch ends for a reason.
	WatchEntityType(ctx context.Context, req *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error)
}

// WatchHub is the interface required by the WatchService to create and
// manage watchers. This interface decouples the service layer from the
// watch package to prevent import cycles.
type WatchHub interface {
	// Watch creates a watcher for the specified key.
	Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)

	// WatchPrefix creates a watcher for all keys matching the prefix.
	WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// WatchService implements WatchServicer by delegating to a WatchHub.
type WatchService struct {
	hub WatchHub
}

// NewWatchService creates a new WatchServicer backed by the given hub.
func NewWatchService(hub WatchHub) WatchServicer {
	return &WatchService{hub: hub}
}

// Watch creates a watcher that delivers events for the specified key.
func (s *WatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return s.hub.Watch(ctx, key, opts...)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix.
func (s *WatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return s.hub.WatchPrefix(ctx, prefix, opts...)
}

// WatchEntity watches for changes to a specific entity. The entity's
// storage key is constructed as "{entityType}/{id}" and a raw watcher
// is created for that key. Raw KV events are transformed into
// WatchEntityEvent values on the returned channel.
func (s *WatchService) WatchEntity(ctx context.Context, req *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}
	if req.ID == "" {
		return nil, &EntityEmptyIDError{EntityType: req.EntityType}
	}

	key := entityKey(req.EntityType, req.ID)

	var opts []watch.WatchOption
	if req.StartRevision > 0 {
		opts = append(opts, watch.WithStartRevision(req.StartRevision))
	}

	w, err := s.hub.Watch(ctx, key, opts...)
	if err != nil {
		return nil, err
	}

	ch := make(chan WatchEntityEvent, 16)
	go s.relayEntityEvents(ctx, w, req.EntityType, ch)

	return ch, nil
}

// WatchEntityType watches for changes to all entities of a given type.
// The entity type prefix is constructed as "{entityType}/" and a prefix
// watcher is created. Raw KV events are transformed into WatchEntityEvent
// values on the returned channel.
func (s *WatchService) WatchEntityType(ctx context.Context, req *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}

	prefix := entityPrefix(req.EntityType)

	var opts []watch.WatchOption
	if req.StartRevision > 0 {
		opts = append(opts, watch.WithStartRevision(req.StartRevision))
	}

	w, err := s.hub.WatchPrefix(ctx, prefix, opts...)
	if err != nil {
		return nil, err
	}

	ch := make(chan WatchEntityEvent, 16)
	go s.relayEntityEvents(ctx, w, req.EntityType, ch)

	return ch, nil
}

// relayEntityEvents reads from a raw watcher and transforms events into
// entity-typed events, sending them on the output channel. The goroutine
// exits when the watcher's event channel is closed or the context is
// canceled, and it always closes the output channel and the watcher on its
// way out.
//
// It also carries the terminal half of the watch contract. The hub ends a
// watch it evicts by recording the reason on the watcher and closing the
// event channel WITHOUT emitting a terminal response, so turning that into
// something the consumer can read is this relay's job — exactly as it is the
// job of every transport handler on the key-value watch. Returning on channel
// close without consulting Err() made an overflow eviction, or a compaction
// racing an in-progress replay, arrive as an ordinary clean close.
func (s *WatchService) relayEntityEvents(ctx context.Context, w watch.Watcher, entityType EntityType, out chan<- WatchEntityEvent) {
	defer close(out)
	defer func() {
		_ = w.Close()
	}()

	prefix := entityPrefix(entityType)

	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-w.Events():
			if !ok {
				if wErr := w.Err(); wErr != nil {
					sendTerminalEntityEvent(ctx, entityType, wErr.Error(), out)
				}
				return
			}
			// A hub that fronts a remote stream reports a cancellation the way
			// every wire client does: in-band on the response. Forward it and
			// stop, as those clients do — a stream that has said why it ended
			// has nothing further to say.
			if resp.Canceled {
				sendTerminalEntityEvent(ctx, entityType, resp.CancelReason, out)
				return
			}
			for _, evt := range resp.Events {
				if evt == nil || evt.Kv == nil {
					continue
				}

				// Strip the entity type prefix to extract the entity ID.
				fullKey := string(evt.Kv.Key)
				id := strings.TrimPrefix(fullKey, prefix)

				var eventType WatchEventType
				if evt.Type == watch.EventDelete {
					eventType = WatchEventDelete
				}

				entityEvent := WatchEntityEvent{
					EntityType: entityType,
					ID:         id,
					Data:       evt.Kv.Value,
					Revision:   resp.Revision,
					Type:       eventType,
				}

				select {
				case <-ctx.Done():
					return
				case out <- entityEvent:
				}
			}
		}
	}
}

// sendTerminalEntityEvent delivers the single event that names why a watch
// ended. It is only ever called with a reason: a watch that ended cleanly
// sends nothing, which is what keeps the two apart for the consumer.
//
// The reason travels verbatim. transport.WatchResponse.Err() classifies a
// cancellation by that text, so preserving it is what lets a compacted entity
// replay be recognized in-process as the same condition it is over the wire;
// summarizing or recoding it would break the classification silently.
func sendTerminalEntityEvent(ctx context.Context, entityType EntityType, reason string, out chan<- WatchEntityEvent) {
	select {
	case out <- WatchEntityEvent{EntityType: entityType, Canceled: true, CancelReason: reason}:
	case <-ctx.Done():
	}
}
