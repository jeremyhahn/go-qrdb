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
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// openHubWatches opens n counted exact-key watches on h (the way a transport
// opens a client's), closed at test end, and returns the error of the first
// refusal (nil when all n open).
func openHubWatches(t *testing.T, h *watch.Hub, n int) error {
	t.Helper()
	for i := 0; i < n; i++ {
		w, err := h.Watch(watch.CountedContext(context.Background()), "k")
		if err != nil {
			return err
		}
		t.Cleanup(func() { _ = w.Close() })
	}
	return nil
}

// requireWatcherLimit fails unless err is a *watch.WatcherLimitError for want.
func requireWatcherLimit(t *testing.T, err error, want uint64) {
	t.Helper()
	var le *watch.WatcherLimitError
	if !errors.As(err, &le) || le.Max != want {
		t.Fatalf("err = %v (%T), want *watch.WatcherLimitError{Max: %d}", err, err, want)
	}
}

func TestWatchHubOptions_OneLimitAcrossEveryHub(t *testing.T) {
	opts := WatchHubOptions(qrdbconfig.WatchConfig{MaxWatchers: 2})
	node, tenant := watch.NewHub(opts...), watch.NewHub(opts...)
	defer node.Close()
	defer tenant.Close()

	if err := openHubWatches(t, node, 1); err != nil {
		t.Fatal(err)
	}
	if err := openHubWatches(t, tenant, 1); err != nil {
		t.Fatal(err)
	}
	requireWatcherLimit(t, openHubWatches(t, node, 1), 2)
	requireWatcherLimit(t, openHubWatches(t, tenant, 1), 2)
}

func TestWatchHubOptions_ZeroIsUnlimitedAtTheDefaultBuffer(t *testing.T) {
	h := watch.NewHub(WatchHubOptions(qrdbconfig.WatchConfig{})...)
	defer h.Close()
	if err := openHubWatches(t, h, 16); err != nil {
		t.Fatalf("max_watchers 0 must not cap: %v", err)
	}
}

func TestWatchHubOptions_BufferSizeBoundsTheConsumerLag(t *testing.T) {
	h := watch.NewHub(WatchHubOptions(qrdbconfig.WatchConfig{BufferSize: 1})...)
	defer h.Close()
	w, err := h.Watch(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for rev := int64(1); rev <= 2; rev++ {
		h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), ModRevision: rev}, nil)
	}
	for range w.Events() {
	}
	if !errors.Is(w.Err(), watch.ErrWatcherOverflow) {
		t.Fatalf("Err() = %v, want ErrWatcherOverflow at buffer_size 1", w.Err())
	}
}

func TestWithWatchHubOptions_ReachesTheFactory(t *testing.T) {
	opts := WatchHubOptions(qrdbconfig.WatchConfig{MaxWatchers: 1})
	f := &RaftBackendFactory{}
	WithWatchHubOptions(opts...)(f)
	if len(f.watchHubOpts) != len(opts) {
		t.Fatalf("watchHubOpts = %d options, want %d", len(f.watchHubOpts), len(opts))
	}
	h := watch.NewHub(f.watchHubOpts...)
	defer h.Close()
	requireWatcherLimit(t, openHubWatches(t, h, 2), 1)
}

func TestWithWatchHubOptions_NoneLeavesDefaults(t *testing.T) {
	f := &RaftBackendFactory{}
	WithWatchHubOptions()(f)
	if len(f.watchHubOpts) != 0 {
		t.Fatalf("watchHubOpts = %d options, want none", len(f.watchHubOpts))
	}
}

func TestWithTenantHostWatchHubOptions_ReachesTheRegistry(t *testing.T) {
	opts := WatchHubOptions(qrdbconfig.WatchConfig{MaxWatchers: 1})
	r := &TenantHostRegistry{}
	WithTenantHostWatchHubOptions(opts...)(r)
	h := watch.NewHub(r.watchHubOpts...)
	defer h.Close()
	requireWatcherLimit(t, openHubWatches(t, h, 2), 1)
}

func TestWithTenantHostWatchHubOptions_NoneLeavesDefaults(t *testing.T) {
	r := &TenantHostRegistry{}
	WithTenantHostWatchHubOptions()(r)
	if len(r.watchHubOpts) != 0 {
		t.Fatalf("watchHubOpts = %d options, want none", len(r.watchHubOpts))
	}
}
