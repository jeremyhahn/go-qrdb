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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

var (
	errServedRefusal = errors.New("barrier sealed for database: app")
	errInnerWatch    = errors.New("inner watch failed")
)

// queuedWatcher delivers the responses queued on it; Close ends it.
type queuedWatcher struct {
	events chan watch.WatchResponse
	err    error
	once   sync.Once
	closed chan struct{}
}

func newQueuedWatcher() *queuedWatcher {
	return &queuedWatcher{events: make(chan watch.WatchResponse, 1), closed: make(chan struct{})}
}

func (w *queuedWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *queuedWatcher) WatchID() int64                     { return 7 }
func (w *queuedWatcher) Err() error                         { return w.err }
func (w *queuedWatcher) Close() error {
	w.once.Do(func() {
		close(w.closed)
		close(w.events)
	})
	return nil
}

// queuedWatchService opens queuedWatchers and entity channels it keeps for
// the test to drive, or fails every open with err.
type queuedWatchService struct {
	// onOpen, when set, runs as each watch opens, before it is returned: a
	// test lands a seal there, mid-open.
	onOpen    func()
	err       error
	watcher   *queuedWatcher
	entities  chan service.WatchEntityEvent
	entityCtx context.Context
}

var _ service.WatchServicer = (*queuedWatchService)(nil)

func (s *queuedWatchService) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.open()
}

func (s *queuedWatchService) WatchPrefix(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.open()
}

func (s *queuedWatchService) open() (watch.Watcher, error) {
	if s.onOpen != nil {
		s.onOpen()
	}
	if s.err != nil {
		return nil, s.err
	}
	s.watcher = newQueuedWatcher()
	return s.watcher, nil
}

func (s *queuedWatchService) WatchEntity(ctx context.Context, _ *service.WatchEntityRequest) (<-chan service.WatchEntityEvent, error) {
	return s.openEntities(ctx)
}

func (s *queuedWatchService) WatchEntityType(ctx context.Context, _ *service.WatchEntityTypeRequest) (<-chan service.WatchEntityEvent, error) {
	return s.openEntities(ctx)
}

func (s *queuedWatchService) openEntities(ctx context.Context) (<-chan service.WatchEntityEvent, error) {
	if s.onOpen != nil {
		s.onOpen()
	}
	if s.err != nil {
		return nil, s.err
	}
	s.entities = make(chan service.WatchEntityEvent, 1)
	s.entityCtx = ctx
	return s.entities, nil
}

// servedTestDatabase returns an open in-memory database.
func servedTestDatabase(t *testing.T) *database.Database {
	t.Helper()
	m := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	db, err := m.Create(database.DatabaseConfig{Name: "app", Backend: database.BackendMemory})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return db
}

func newTestServedWatchService(t *testing.T, inner *queuedWatchService) (*servedWatchService, *database.Database) {
	t.Helper()
	db := servedTestDatabase(t)
	return newServedWatchService(inner, db, func() error { return errServedRefusal }), db
}

// drained waits until the wrapper has taken the queued response off ch.
func drained[T any](t *testing.T, ch chan T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(ch) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the wrapper never took the queued event")
		}
		time.Sleep(time.Millisecond)
	}
}

// ended reports whether a watch's event channel closes within the deadline.
func ended[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("the watch is still open")
		}
	}
}

type openWatch func(*servedWatchService) (watch.Watcher, error)

var servedWatchOpeners = map[string]openWatch{
	"Watch": func(s *servedWatchService) (watch.Watcher, error) {
		return s.Watch(context.Background(), "k")
	},
	"WatchPrefix": func(s *servedWatchService) (watch.Watcher, error) {
		return s.WatchPrefix(context.Background(), "p/")
	},
}

// A key or prefix watch relays its events while the database serves, and
// ends -- closing the inner watch, with the database's refusal -- the moment
// the database stops serving.
func TestServedWatcher_RelaysUntilTheDatabaseStopsServing(t *testing.T) {
	for name, open := range servedWatchOpeners {
		t.Run(name, func(t *testing.T) {
			inner := &queuedWatchService{}
			s, db := newTestServedWatchService(t, inner)
			w, err := open(s)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if w.WatchID() != 7 {
				t.Fatalf("WatchID = %d, want the inner watcher's", w.WatchID())
			}
			inner.watcher.events <- watch.WatchResponse{Revision: 3}
			if resp := <-w.Events(); resp.Revision != 3 {
				t.Fatalf("relayed revision %d, want 3", resp.Revision)
			}
			db.SetStatus(database.StatusSealed)
			ended(t, w.Events())
			if !errors.Is(w.Err(), errServedRefusal) {
				t.Fatalf("Err = %v, want the database's refusal", w.Err())
			}
			select {
			case <-inner.watcher.closed:
			default:
				t.Fatal("the inner watch is still open")
			}
		})
	}
}

// A seal while an event waits for its reader still ends the watch: the
// wrapper does not hold a sealed database's event for a reader.
func TestServedWatcher_EndsWithAnUndeliveredEvent(t *testing.T) {
	inner := &queuedWatchService{}
	s, db := newTestServedWatchService(t, inner)
	w, err := s.Watch(context.Background(), "k")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	inner.watcher.events <- watch.WatchResponse{Revision: 1}
	drained(t, inner.watcher.events)
	db.SetStatus(database.StatusSealed)
	select {
	case <-inner.watcher.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the inner watch is still open after the seal")
	}
	if !errors.Is(w.Err(), errServedRefusal) {
		t.Fatalf("Err = %v, want the database's refusal", w.Err())
	}
}

// A watch the inner watcher ends, or the caller closes, ends without the
// database's refusal: Err is the inner watcher's.
func TestServedWatcher_EndsWithoutTheDatabase(t *testing.T) {
	t.Run("inner ended", func(t *testing.T) {
		inner := &queuedWatchService{}
		s, _ := newTestServedWatchService(t, inner)
		w, err := s.Watch(context.Background(), "k")
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		inner.watcher.err = errInnerWatch
		_ = inner.watcher.Close()
		ended(t, w.Events())
		if !errors.Is(w.Err(), errInnerWatch) {
			t.Fatalf("Err = %v, want the inner watcher's", w.Err())
		}
	})
	t.Run("closed while idle", func(t *testing.T) {
		inner := &queuedWatchService{}
		s, _ := newTestServedWatchService(t, inner)
		w, err := s.Watch(context.Background(), "k")
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		_ = w.Close() // idempotent
		ended(t, w.Events())
		if w.Err() != nil {
			t.Fatalf("Err = %v after Close, want nil", w.Err())
		}
	})
	t.Run("closed with an undelivered event", func(t *testing.T) {
		inner := &queuedWatchService{}
		s, _ := newTestServedWatchService(t, inner)
		w, err := s.Watch(context.Background(), "k")
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		inner.watcher.events <- watch.WatchResponse{Revision: 1}
		drained(t, inner.watcher.events)
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		ended(t, w.Events())
		if w.Err() != nil {
			t.Fatalf("Err = %v after Close, want nil", w.Err())
		}
	})
}

// An open the inner service refuses is refused unchanged, on every kind of
// watch.
func TestServedWatchService_OpenErrors(t *testing.T) {
	s, _ := newTestServedWatchService(t, &queuedWatchService{err: errInnerWatch})
	for name, open := range servedWatchOpeners {
		if _, err := open(s); !errors.Is(err, errInnerWatch) {
			t.Errorf("%s: %v, want the inner refusal", name, err)
		}
	}
	if _, err := s.WatchEntity(context.Background(), &service.WatchEntityRequest{}); !errors.Is(err, errInnerWatch) {
		t.Errorf("WatchEntity: %v, want the inner refusal", err)
	}
	if _, err := s.WatchEntityType(context.Background(), &service.WatchEntityTypeRequest{}); !errors.Is(err, errInnerWatch) {
		t.Errorf("WatchEntityType: %v, want the inner refusal", err)
	}
}

type openEntities func(*servedWatchService) (<-chan service.WatchEntityEvent, error)

var servedEntityOpeners = map[string]openEntities{
	"WatchEntity": func(s *servedWatchService) (<-chan service.WatchEntityEvent, error) {
		return s.WatchEntity(context.Background(), &service.WatchEntityRequest{})
	},
	"WatchEntityType": func(s *servedWatchService) (<-chan service.WatchEntityEvent, error) {
		return s.WatchEntityType(context.Background(), &service.WatchEntityTypeRequest{})
	},
}

// An entity watch relays its events while the database serves; a seal --
// whether the watch is idle or an event waits for its reader -- closes the
// channel and cancels the inner watch.
func TestServedEntityEvents_EndWhenTheDatabaseStopsServing(t *testing.T) {
	for name, open := range servedEntityOpeners {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pending=%v", name, pending), func(t *testing.T) {
				inner := &queuedWatchService{}
				s, db := newTestServedWatchService(t, inner)
				out, err := open(s)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				inner.entities <- service.WatchEntityEvent{ID: "e1"}
				if ev := <-out; ev.ID != "e1" {
					t.Fatalf("relayed %q, want e1", ev.ID)
				}
				if pending {
					inner.entities <- service.WatchEntityEvent{ID: "e2"}
					drained(t, inner.entities)
				}
				db.SetStatus(database.StatusSealed)
				select {
				case <-inner.entityCtx.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("the inner entity watch was not cancelled")
				}
				ended(t, out)
			})
		}
	}
}

// An entity watch whose inner channel closes ends with it.
func TestServedEntityEvents_EndWithTheInnerWatch(t *testing.T) {
	inner := &queuedWatchService{}
	s, _ := newTestServedWatchService(t, inner)
	out, err := s.WatchEntity(context.Background(), &service.WatchEntityRequest{})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}
	close(inner.entities)
	ended(t, out)
	if inner.entityCtx.Err() == nil {
		t.Fatal("the inner entity watch's context is still live")
	}
}
