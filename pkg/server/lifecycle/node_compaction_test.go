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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// buildNodeCompactionNode builds the services over a local MVCC store, with or
// without revision history.
func buildNodeCompactionNode(t *testing.T, noHistory bool) *lifecycle.Node {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		NoRevisionHistory: noHistory,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	return node
}

// The admin service reaches the shared store's compaction table, so the
// platform's node-keyspace policy is set and read back through it.
func TestBuildServices_AdminHoldsTheNodeCompactionPolicy(t *testing.T) {
	node := buildNodeCompactionNode(t, false)
	ctx := context.Background()
	if err := node.Admin.SetNodeCompaction(ctx, &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 9}); err != nil {
		t.Fatalf("SetNodeCompaction: %v", err)
	}
	got, err := node.Admin.NodeCompaction(ctx)
	if err != nil || got == nil || got.RetainRevisions != 9 {
		t.Fatalf("NodeCompaction = %+v, %v; want the policy set", got, err)
	}
	st, err := node.MVCC.(service.CompactionStateStore).CompactionState(ctx, compaction.NodeScope)
	if err != nil || st == nil || st.Policy == nil {
		t.Fatalf("the shared store holds no node-scope record: %+v, %v", st, err)
	}
}

// A shared store that keeps no revision history has no node keyspace to
// compact, and the verbs say so.
func TestBuildServices_NoRevisionHistoryHasNoNodeCompaction(t *testing.T) {
	node := buildNodeCompactionNode(t, true)
	var unavailable *service.NodeCompactionUnavailableError
	if _, err := node.Admin.NodeCompaction(context.Background()); !errors.As(err, &unavailable) {
		t.Fatalf("NodeCompaction = %v; want *service.NodeCompactionUnavailableError", err)
	}
}
