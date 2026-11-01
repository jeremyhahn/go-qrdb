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

package node

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestLocalEngine_PublishesIntoServedWatchHub pins the watch-hub wiring
// through the canonical local composition: the engine is the only
// publisher of change events, and it must publish into the SAME hub the
// WatchService subscribes on. Before the fix, the LocalBackend path built
// the engine without the hub, so watches were created successfully but
// never delivered anything — a silent no-op this test would have caught.
//
// Under the ratified run-modes split the watch SERVICE is raft-mode-only,
// so this pins the wiring property at the lifecycle layer (engine → hub →
// WatchService) rather than through a standalone node's public API, which
// now refuses watch (see TestStandaloneNode_RaftOnlyFamiliesRefused).
func TestLocalEngine_PublishesIntoServedWatchHub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hub := watch.NewHub()
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.StorageConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stg.Close() })

	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, hub, nil)
	require.NoError(t, err)

	local := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = local.Close() })
	kvSvc := service.NewKVService(local, shard.ShardKV)
	watchSvc := service.NewWatchService(hub)

	watcher, err := watchSvc.WatchPrefix(ctx, "watch-wire/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = watcher.Close() })

	require.NoError(t, kvSvc.Put(ctx, "watch-wire/k1", []byte("v1")))

	select {
	case ev, ok := <-watcher.Events():
		require.True(t, ok, "watch channel closed before delivering the event")
		require.NotEmpty(t, ev.Events, "watch response carried no events")
	case <-time.After(10 * time.Second):
		t.Fatal("watch delivered nothing for a matching Put — the engine is not publishing into the served watch hub")
	}
}

// TestStandaloneNode_WatchServedAndReported: a standalone node serves watch
// through its public client and reports watch, MVCC and lease in its
// capabilities (go-qrdb#582); they used to be refused and reported false.
func TestStandaloneNode_WatchServedAndReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	// Not an authorization test; see testhelpers_test.go's
	// sealedStandaloneConfig comment.
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	// Standard operations serve.
	require.NoError(t, n.KVService().Put(ctx, "std/k1", []byte("v1")))

	// The watch is served and delivers.
	ch, err := n.Client().Watch().WatchPrefix(ctx, "watch-wire/", transport.WatchOptions{})
	require.NoError(t, err, "watch is served on a standalone node")
	require.NoError(t, n.KVService().Put(ctx, "watch-wire/k2", []byte("v2")))
	select {
	case resp, ok := <-ch:
		require.True(t, ok, "watch stream closed before delivering the put")
		require.NotEmpty(t, resp.Events, "watch response carried no events")
	case <-ctx.Done():
		t.Fatal("the watch never delivered the put")
	}

	// Capabilities report the standalone truth.
	caps, err := n.Client().Admin().Capabilities(ctx)
	require.NoError(t, err)
	require.Equal(t, "standalone", caps.Mode)
	require.True(t, caps.Watch, "capabilities must report watch=true in standalone")
	require.True(t, caps.MVCC, "capabilities must report mvcc=true in standalone")
	require.True(t, caps.Lease, "capabilities must report lease=true in standalone")
	require.False(t, caps.Cluster, "cluster operations stay raft-only")
	require.True(t, caps.KV, "kv stays true in every mode")
}
