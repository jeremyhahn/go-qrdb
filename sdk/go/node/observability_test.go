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
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

// keepSlowSyncThreshold restores the process's slow-sync threshold when t
// ends.
func keepSlowSyncThreshold(t *testing.T) {
	t.Helper()
	prev := syncwatch.SlowThreshold()
	t.Cleanup(func() { syncwatch.SetSlowThreshold(prev) })
}

// TestNewStandaloneNode_AppliesSlowSyncThreshold: a node built with
// observability.slow_sync_threshold: 50ms makes the Raft WAL's and every
// storage backend's observer warn on a 100ms sync and stay quiet on a 10ms
// one.
func TestNewStandaloneNode_AppliesSlowSyncThreshold(t *testing.T) {
	keepSlowSyncThreshold(t)
	cfg := newMemoryNodeCfg(t)
	cfg.Observability.SlowSyncThreshold = "50ms"

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.Equal(t, 50*time.Millisecond, syncwatch.SlowThreshold())

	log := syncwatchtest.NewLog()
	prev := slog.Default()
	slog.SetDefault(log.Logger())
	t.Cleanup(func() { slog.SetDefault(prev) })
	for _, observe := range []func(time.Duration){
		database.NewRaftMetrics().ObserveFsyncLatency,
		syncwatch.ForStorage(syncwatch.BackendPebble).Observe,
		syncwatch.ForStorage(syncwatch.BackendFile).Observe,
	} {
		observe(10 * time.Millisecond)
		observe(100 * time.Millisecond)
	}
	recs := log.Records()
	require.Len(t, recs, 3, "one warning per 100ms sync, none for a 10ms one")
	for _, r := range recs {
		require.Equal(t, 50*time.Millisecond, r.Attrs["threshold"])
		require.Equal(t, 100*time.Millisecond, r.Attrs["took"])
	}
}

// TestNodeConstructors_RefuseOutOfRangeSlowSyncThreshold: both constructors
// refuse an out-of-range threshold with the typed config error before they
// open anything, and leave the process's threshold alone.
func TestNodeConstructors_RefuseOutOfRangeSlowSyncThreshold(t *testing.T) {
	keepSlowSyncThreshold(t)
	syncwatch.SetSlowThreshold(5 * time.Second)
	for _, bad := range []string{"0s", "-1s", "1m1s"} {
		cfg := newMemoryNodeCfg(t)
		cfg.Observability.SlowSyncThreshold = bad
		for name, build := range map[string]func() (*Node, error){
			"standalone": func() (*Node, error) { return NewStandaloneNode(context.Background(), cfg, nil) },
			"clustered": func() (*Node, error) {
				return NewClusteredNode(context.Background(), cfg, map[uint64]string{1: "127.0.0.1:1"}, nil)
			},
		} {
			n, err := build()
			var ve *qrdbconfig.ValidationError
			require.True(t, errors.As(err, &ve), "%s %q: %v", name, bad, err)
			require.Equal(t, "observability.slow_sync_threshold", ve.Field)
			require.Nil(t, n)
			require.Equal(t, 5*time.Second, syncwatch.SlowThreshold())
		}
	}
}
