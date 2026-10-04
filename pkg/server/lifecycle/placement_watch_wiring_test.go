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

// TestBuildServices_PlacementTableHasAChangeFeed: a node builds the
// placement table's change feed, so the replica-forwarding leader cache
// reacts to a placement change as the node applies it.
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
	if node.PlacementStore == nil || node.PlacementChanges == nil {
		t.Fatal("no placement table, or no change feed for it")
	}
	if _, err := node.PlacementChanges.Watch(ctx); err != nil {
		t.Fatalf("PlacementChanges.Watch = %v; want the node's change feed", err)
	}
	if hub.WatchCount() != 1 {
		t.Fatalf("hub watchers = %d; want the placement watch on the node's hub", hub.WatchCount())
	}
}
