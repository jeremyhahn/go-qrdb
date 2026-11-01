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

package embedded

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// openTwoWatches opens two watches through a transport built with opts over
// a hub capped at one watcher, and returns the second watch's error.
func openTwoWatches(t *testing.T, opts ...Option) error {
	t.Helper()
	hub := watch.NewHub(watch.WithWatcherLimit(watch.NewWatcherLimit(1)))
	t.Cleanup(hub.Close)
	tr := newExtTransport(t, append([]Option{WithWatchService(service.NewWatchService(hub))}, opts...)...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := tr.Watch(ctx, "k", transport.WatchOptions{}); err != nil {
		t.Fatalf("first watch: %v", err)
	}
	_, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	return err
}

func TestWithCountedWatch_RefusesTheWatchPastTheCap(t *testing.T) {
	err := openTwoWatches(t, WithCountedWatch())
	var le *transport.WatcherLimitError
	if !errors.As(err, &le) || !strings.Contains(le.Reason, (&watch.WatcherLimitError{Max: 1}).Error()) {
		t.Fatalf("second counted watch: err = %v (%T), want *transport.WatcherLimitError at max_watchers 1", err, err)
	}
}

func TestWithCountedWatch_UnsetLeavesTheWatchUncounted(t *testing.T) {
	if err := openTwoWatches(t); err != nil {
		t.Fatalf("an uncounted transport's watch must not take a slot: %v", err)
	}
}

// hubWatchRegistry serves every (tenant, database) one watch service over a
// shared hub, so a database-scoped watch reaches the hub's watcher limit.
type hubWatchRegistry struct {
	service.Registry
	watch service.WatchServicer
}

func (r *hubWatchRegistry) WatchService(string, string) (service.WatchServicer, error) {
	return r.watch, nil
}

// openTwoScopedWatches opens two watches through WithDatabase on a transport
// whose registry serves a hub capped at one watcher, and returns the second
// watch's error.
func openTwoScopedWatches(t *testing.T, opts ...Option) error {
	t.Helper()
	hub := watch.NewHub(watch.WithWatcherLimit(watch.NewWatcherLimit(1)))
	t.Cleanup(hub.Close)
	reg := &hubWatchRegistry{watch: service.NewWatchService(hub)}
	base := newExtTransport(t, append([]Option{
		WithWatchService(&mockWatchService{}), WithServiceRegistry(reg),
	}, opts...)...)
	scoped, err := base.WithDatabase("ledger")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	tr, ok := scoped.(*Transport)
	if !ok {
		t.Fatalf("WithDatabase returned %T, want *Transport", scoped)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := tr.Watch(ctx, "k", transport.WatchOptions{}); err != nil {
		t.Fatalf("first scoped watch: %v", err)
	}
	_, err = tr.Watch(ctx, "k", transport.WatchOptions{})
	return err
}

func TestWithCountedWatch_ScopedTransportRefusesTheWatchPastTheCap(t *testing.T) {
	err := openTwoScopedWatches(t, WithCountedWatch())
	var le *transport.WatcherLimitError
	if !errors.As(err, &le) || !strings.Contains(le.Reason, (&watch.WatcherLimitError{Max: 1}).Error()) {
		t.Fatalf("second counted scoped watch: err = %v (%T), want *transport.WatcherLimitError at max_watchers 1", err, err)
	}
}

func TestWithCountedWatch_UnsetLeavesTheScopedWatchUncounted(t *testing.T) {
	if err := openTwoScopedWatches(t); err != nil {
		t.Fatalf("an uncounted scoped transport's watch must not take a slot: %v", err)
	}
}
