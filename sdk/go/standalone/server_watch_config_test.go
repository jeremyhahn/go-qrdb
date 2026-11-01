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

package standalone

import (
	"context"
	"errors"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newWatchConfigServer starts a memory-backed server with watch config wc,
// shut down at test end.
func newWatchConfigServer(t *testing.T, wc qrdbconfig.WatchConfig) *Server {
	t.Helper()
	srv, err := NewServer(context.Background(), &ServerConfig{Backend: database.BackendMemory, Watch: wc})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}

// openClientWatches opens n prefix watches through srv's client, cancelled at
// test end, and returns the first refusal (nil when all n open).
func openClientWatches(t *testing.T, srv *Server, n int) error {
	t.Helper()
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if _, err := srv.Client().Watch().WatchPrefix(ctx, "w/", transport.WatchOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func TestNewServer_MaxWatchersRefusesTheClientWatchPastTheCap(t *testing.T) {
	srv := newWatchConfigServer(t, qrdbconfig.WatchConfig{MaxWatchers: 2})
	if err := openClientWatches(t, srv, 2); err != nil {
		t.Fatalf("watches under the cap: %v", err)
	}
	var le *transport.WatcherLimitError
	if err := openClientWatches(t, srv, 1); !errors.As(err, &le) {
		t.Fatalf("third watch at max_watchers 2: err = %v (%T), want *transport.WatcherLimitError", err, err)
	}
}

func TestNewServer_ZeroWatchConfigLeavesWatchesUncapped(t *testing.T) {
	srv := newWatchConfigServer(t, qrdbconfig.WatchConfig{})
	if err := openClientWatches(t, srv, 16); err != nil {
		t.Fatalf("max_watchers 0 must not cap: %v", err)
	}
}

func TestNewServer_ClosedWatchFreesItsSlot(t *testing.T) {
	srv := newWatchConfigServer(t, qrdbconfig.WatchConfig{MaxWatchers: 1})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := srv.Client().Watch().WatchPrefix(ctx, "w/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("first watch: %v", err)
	}
	cancel()
	for range ch {
	}
	if err := openClientWatches(t, srv, 1); err != nil {
		t.Fatalf("watch after the first closed: %v", err)
	}
}
