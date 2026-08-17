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

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestStandaloneNode_WatchDeliversEvents pins the watch-hub wiring through
// createNodeEngine: the engine is the only publisher of change events, and
// it must publish into the SAME hub the WatchService subscribes on. Before
// the fix, the standalone (LocalBackend) path built the engine without the
// hub, so watches were created successfully but never delivered anything —
// a silent no-op this test would have caught.
//
// Note: under the ratified run-modes plan, watch becomes a raft-mode-only
// feature and this test will move to a raft-single-node fixture in the
// mode-gating phase; the WIRING property it pins (engine publishes into
// the served hub) is mode-independent and must survive that move.
func TestStandaloneNode_WatchDeliversEvents(t *testing.T) {
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

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	events, err := n.Client().Watch().WatchPrefix(ctx, "watch-wire/", transport.WatchOptions{})
	require.NoError(t, err)

	require.NoError(t, n.KVService().Put(ctx, "watch-wire/k1", []byte("v1")))

	select {
	case ev, ok := <-events:
		require.True(t, ok, "watch channel closed before delivering the event")
		require.NotEmpty(t, ev.Events, "watch response carried no events")
	case <-time.After(10 * time.Second):
		t.Fatal("watch delivered nothing for a matching Put — the engine is not publishing into the served watch hub")
	}
}
