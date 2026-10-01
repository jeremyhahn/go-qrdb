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
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// servedWatchService ends every watch it opens on a catalogued database when
// that database stops serving. A watch is a stream: the registry refuses a
// sealed or dropped database when the watch opens, but a watch already open
// would otherwise keep delivering the database's events after a seal or a
// drop. It is the one place every transport's watch -- gRPC, Unix, QUIC,
// REST, MCP and the etcd listener -- resolves through.
type servedWatchService struct {
	inner service.WatchServicer
	db    *database.Database
	// refusal is the error a watch ended by the database ends with.
	refusal func() error
}

var _ service.WatchServicer = (*servedWatchService)(nil)

// newServedWatchService wraps inner for db, the catalogued database it serves.
func newServedWatchService(inner service.WatchServicer, db *database.Database, refusal func() error) *servedWatchService {
	return &servedWatchService{inner: inner, db: db, refusal: refusal}
}

// Watch opens a watch on key that ends when the database stops serving.
func (s *servedWatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	w, err := s.inner.Watch(ctx, key, opts...)
	if err != nil {
		return nil, err
	}
	return newServedWatcher(w, s.db.Unserved(), s.refusal), nil
}

// WatchPrefix opens a watch on prefix that ends when the database stops
// serving.
func (s *servedWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	w, err := s.inner.WatchPrefix(ctx, prefix, opts...)
	if err != nil {
		return nil, err
	}
	return newServedWatcher(w, s.db.Unserved(), s.refusal), nil
}

// WatchEntity opens an entity watch whose channel closes when the database
// stops serving.
func (s *servedWatchService) WatchEntity(ctx context.Context, req *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	ctx, cancel := context.WithCancel(ctx)
	ch, err := s.inner.WatchEntity(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	return servedEntityEvents(ch, s.db.Unserved(), cancel), nil
}

// WatchEntityType opens an entity-type watch whose channel closes when the
// database stops serving.
func (s *servedWatchService) WatchEntityType(ctx context.Context, req *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	ctx, cancel := context.WithCancel(ctx)
	ch, err := s.inner.WatchEntityType(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	return servedEntityEvents(ch, s.db.Unserved(), cancel), nil
}

// servedEntityEvents forwards in until it closes or unserved fires; then it
// cancels the inner watch and closes the returned channel.
func servedEntityEvents(in <-chan service.WatchEntityEvent, unserved <-chan struct{}, cancel context.CancelFunc) <-chan service.WatchEntityEvent {
	out := make(chan service.WatchEntityEvent)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-unserved:
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-unserved:
					return
				}
			}
		}
	}()
	return out
}

// servedWatcher forwards an inner watcher's events until the inner watcher
// ends or the database stops serving; in the second case it closes the inner
// watcher and reports the database's refusal from Err.
type servedWatcher struct {
	inner   watch.Watcher
	events  chan watch.WatchResponse
	stop    chan struct{}
	once    sync.Once
	errMu   sync.Mutex
	err     error
	refusal func() error
}

var _ watch.Watcher = (*servedWatcher)(nil)

func newServedWatcher(inner watch.Watcher, unserved <-chan struct{}, refusal func() error) *servedWatcher {
	w := &servedWatcher{
		inner:   inner,
		events:  make(chan watch.WatchResponse),
		stop:    make(chan struct{}),
		refusal: refusal,
	}
	go w.forward(unserved)
	return w
}

// forward relays events until the inner watcher ends, the watcher is closed,
// or the database stops serving.
func (w *servedWatcher) forward(unserved <-chan struct{}) {
	defer close(w.events)
	for {
		select {
		case <-w.stop:
			return
		case <-unserved:
			w.end()
			return
		case resp, ok := <-w.inner.Events():
			if !ok {
				return
			}
			select {
			case w.events <- resp:
			case <-w.stop:
				return
			case <-unserved:
				w.end()
				return
			}
		}
	}
}

// end records the database's refusal and closes the inner watcher.
func (w *servedWatcher) end() {
	w.errMu.Lock()
	w.err = w.refusal()
	w.errMu.Unlock()
	_ = w.inner.Close()
}

// Events returns the forwarded events; the channel closes when the watch
// ends.
func (w *servedWatcher) Events() <-chan watch.WatchResponse { return w.events }

// WatchID returns the inner watcher's ID.
func (w *servedWatcher) WatchID() int64 { return w.inner.WatchID() }

// Close ends the watch.
func (w *servedWatcher) Close() error {
	w.once.Do(func() { close(w.stop) })
	return w.inner.Close()
}

// Err returns the database's refusal when the database ended the watch, and
// the inner watcher's error otherwise.
func (w *servedWatcher) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	if w.err != nil {
		return w.err
	}
	return w.inner.Err()
}
