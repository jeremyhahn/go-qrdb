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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// watchLimitedStandaloneNode starts a standalone node whose watch section is
// w, with no network listener and no authorization (not an authorization
// test; see testhelpers_test.go's sealedStandaloneConfig comment).
func watchLimitedStandaloneNode(t *testing.T, ctx context.Context, w qrdbconfig.WatchConfig) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)
	cfg.Watch.BufferSize = w.BufferSize
	cfg.Watch.MaxWatchers = w.MaxWatchers

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func TestStandaloneNode_MaxWatchersRefusesTheWatchPastTheCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n := watchLimitedStandaloneNode(t, ctx, qrdbconfig.WatchConfig{MaxWatchers: 2})

	for _, prefix := range []string{"a/", "b/"} {
		_, err := n.Client().Watch().WatchPrefix(ctx, prefix, transport.WatchOptions{})
		require.NoError(t, err, "watch %q is under max_watchers 2", prefix)
	}
	_, err := n.Client().Watch().WatchPrefix(ctx, "c/", transport.WatchOptions{})
	var le *transport.WatcherLimitError
	require.True(t, errors.As(err, &le), "third watch: err = %v (%T), want *transport.WatcherLimitError", err, err)
	require.Contains(t, le.Reason, (&watch.WatcherLimitError{Max: 2}).Error())
}

func TestStandaloneNode_ZeroMaxWatchersIsUnlimited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n := watchLimitedStandaloneNode(t, ctx, qrdbconfig.WatchConfig{BufferSize: 8})

	for i := 0; i < 16; i++ {
		_, err := n.Client().Watch().WatchPrefix(ctx, "u/", transport.WatchOptions{})
		require.NoError(t, err, "watch %d: max_watchers 0 must not cap", i)
	}
}
