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

package lifecycle_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestBuildServices_PlacementTableHasAChangeFeed: the placement table a
// node builds watches its changes, so the tenant shard reconciler and the
// replica-forwarding leader cache react to a placement change as the node
// applies it rather than finding it on a poll.
func TestBuildServices_PlacementTableHasAChangeFeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{Host: host, WatchHub: hub, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()
	if node.PlacementStore == nil {
		t.Fatal("no placement table")
	}
	if _, err := node.PlacementStore.Watch(ctx); err != nil {
		t.Fatalf("PlacementStore.Watch = %v; want the node's change feed", err)
	}
	if hub.WatchCount() != 1 {
		t.Fatalf("hub watchers = %d; want the placement watch on the node's hub", hub.WatchCount())
	}
}
