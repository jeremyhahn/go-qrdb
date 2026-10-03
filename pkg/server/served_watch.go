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
	return watch.NewGuarded(w, s.db.Unserved(), s.refusal), nil
}

// WatchPrefix opens a watch on prefix that ends when the database stops
// serving.
func (s *servedWatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	w, err := s.inner.WatchPrefix(ctx, prefix, opts...)
	if err != nil {
		return nil, err
	}
	return watch.NewGuarded(w, s.db.Unserved(), s.refusal), nil
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
