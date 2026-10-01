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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// idleWatchService opens watches that deliver nothing until closed: a stream
// left open by a quiet key, which only the database's state can end.
type idleWatchService struct {
	service.WatchServicer
	mu      sync.Mutex
	opened  int
	nextID  int64
	closers []*idleWatcher
}

func (s *idleWatchService) open() *idleWatcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened++
	s.nextID++
	w := &idleWatcher{id: s.nextID, events: make(chan watch.WatchResponse)}
	s.closers = append(s.closers, w)
	return w
}

func (s *idleWatchService) Watch(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.open(), nil
}

func (s *idleWatchService) WatchPrefix(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.open(), nil
}

// idleWatcher delivers nothing; Close ends it.
type idleWatcher struct {
	id     int64
	events chan watch.WatchResponse
	once   sync.Once
}

func (w *idleWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *idleWatcher) WatchID() int64                     { return w.id }
func (w *idleWatcher) Err() error                         { return nil }
func (w *idleWatcher) Close() error {
	w.once.Do(func() { close(w.events) })
	return nil
}

// TestOpenWatch_EndsWhenTheDatabaseIsSealed pins that a watch opened before
// its database is sealed ends with the seal, on every network transport. The
// registry refused a sealed database when a watch opened, but one already
// open kept delivering the database's events -- a stream outlived the
// refusal every new request got.
func TestOpenWatch_EndsWhenTheDatabaseIsSealed(t *testing.T) {
	assertOpenWatchesEnd(t, func(db *database.Database) { db.SetStatus(database.StatusSealed) }, "barrier sealed")
}

// A watch opened before its database is dropped ends with the drop.
func TestOpenWatch_EndsWhenTheDatabaseIsDropped(t *testing.T) {
	assertOpenWatchesEnd(t, func(db *database.Database) {
		if err := db.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}, "database not found")
}

func assertOpenWatchesEnd(t *testing.T, stop func(*database.Database), reason string) {
	t.Helper()
	mgr, db := openLegacyVault(t)
	db.SetStatus(database.StatusOpen)

	watches := &idleWatchService{}
	svcs := newFilledServices("default")
	svcs.KV = &serializedKV{KVServicer: svcs.KV}
	svcs.Watch = watches
	dbReg := NewDatabaseServiceRegistry("default")
	dbReg.Register("default", svcs)

	socketPath := filepath.Join(t.TempDir(), "qrdb.sock")
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseServiceRegistry(dbReg),
		WithDatabaseManager(mgr),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	streams := map[string]<-chan transport.WatchResponse{}
	for name, tr := range dialScopedTransports(t, srv, "legacyvault") {
		if name == "mcp" {
			// MCP's watch is a bounded poll, not a server-side stream: every
			// poll is a new request, which the registry refuses once the
			// database stops serving.
			continue
		}
		wc, ok := tr.(transport.WatchClient)
		if !ok {
			t.Fatalf("%s transport has no watch surface", name)
		}
		ch, err := wc.Watch(ctx, "k", transport.WatchOptions{})
		if err != nil {
			t.Fatalf("%s Watch on an open database: %v", name, err)
		}
		streams[name] = ch
	}
	if len(streams) != 4 {
		t.Fatalf("%d transports opened a watch, want REST, gRPC, Unix and QUIC", len(streams))
	}
	// Every stream is open and idle before the database stops serving.
	waitFor(t, func() bool {
		watches.mu.Lock()
		defer watches.mu.Unlock()
		return watches.opened >= len(streams)
	})
	for name, ch := range streams {
		select {
		case resp, ok := <-ch:
			t.Fatalf("%s watch delivered %+v (open %v) before the database stopped serving", name, resp, ok)
		default:
		}
	}

	stop(db)

	for name, ch := range streams {
		var cancelReason string
		ended := false
		deadline := time.After(10 * time.Second)
		for !ended {
			select {
			case resp, ok := <-ch:
				if !ok {
					ended = true
					break
				}
				if resp.Canceled {
					cancelReason = resp.CancelReason
				}
			case <-deadline:
				t.Fatalf("%s watch still open 10s after the database stopped serving (%s)", name, reason)
			}
		}
		// The stream ends with the database's typed refusal, not a bare
		// close a client would read as a dropped connection.
		if !strings.Contains(cancelReason, reason) {
			t.Errorf("%s watch ended with %q, want the %s refusal", name, cancelReason, reason)
		}
	}
}

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
