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

package main

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

// observabilityConfig writes a qrdbd config whose observability section is
// body.
func observabilityConfig(t *testing.T, body string) string {
	t.Helper()
	return writeBindHostConfig(t, "  host: \"127.0.0.1\"\n  admin_port: 19443\nobservability:\n"+body)
}

// keepProcessObservability restores the default logger and the slow-sync
// threshold configureObservability sets for the whole process.
func keepProcessObservability(t *testing.T) {
	t.Helper()
	prevLog, prevThreshold := slog.Default(), syncwatch.SlowThreshold()
	t.Cleanup(func() {
		slog.SetDefault(prevLog)
		syncwatch.SetSlowThreshold(prevThreshold)
	})
}

// TestConfigureObservability_SlowSyncThresholdFromConfig: qrdbd start with
// observability.slow_sync_threshold: 50ms makes the Raft WAL's and every
// storage backend's observer warn on a 100ms sync and stay quiet on a 10ms
// one.
func TestConfigureObservability_SlowSyncThresholdFromConfig(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	keepProcessObservability(t)
	f := resolveWithArgs(t, "--config", observabilityConfig(t, "  slow_sync_threshold: 50ms"))

	require.NoError(t, configureObservability(f))
	require.Equal(t, 50*time.Millisecond, syncwatch.SlowThreshold())

	log := syncwatchtest.NewLog()
	slog.SetDefault(log.Logger())
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
		require.Equal(t, slog.LevelWarn, r.Level)
		require.Equal(t, 50*time.Millisecond, r.Attrs["threshold"])
		require.Equal(t, 100*time.Millisecond, r.Attrs["took"])
	}
}

// TestConfigureObservability_SlowSyncThresholdDefaultAndEnv: without the key
// the threshold is one second, and QRDB_OBSERVABILITY_SLOW_SYNC_THRESHOLD
// overrides the file.
func TestConfigureObservability_SlowSyncThresholdDefaultAndEnv(t *testing.T) {
	keepProcessObservability(t)
	syncwatch.SetSlowThreshold(5 * time.Second)

	require.NoError(t, configureObservability(resolveWithArgs(t)))
	require.Equal(t, qrdbconfig.DefaultSlowSyncThreshold, syncwatch.SlowThreshold())

	t.Setenv("QRDB_OBSERVABILITY_SLOW_SYNC_THRESHOLD", "75ms")
	require.NoError(t, configureObservability(resolveWithArgs(t, "--config", observabilityConfig(t, "  slow_sync_threshold: 50ms"))))
	require.Equal(t, 75*time.Millisecond, syncwatch.SlowThreshold())
}

// TestConfigureObservability_SlowSyncThresholdOutOfRangeRefused: an
// out-of-range threshold stops qrdbd start with the typed config error,
// whether it arrives in the file or reaches the boot step directly.
func TestConfigureObservability_SlowSyncThresholdOutOfRangeRefused(t *testing.T) {
	keepProcessObservability(t)
	for _, bad := range []string{"0s", "-1s", "1m1s"} {
		var ve *qrdbconfig.ValidationError
		_, err := resolveStartFlagsErr(t, observabilityConfig(t, "  slow_sync_threshold: "+bad))
		require.True(t, errors.As(err, &ve), "config %q: %v", bad, err)
		require.Equal(t, "observability.slow_sync_threshold", ve.Field)

		f := &startFlags{logFormat: "text", logLevel: "info", observability: qrdbconfig.ObservabilityConfig{SlowSyncThreshold: bad}}
		err = configureObservability(f)
		require.True(t, errors.As(err, &ve), "boot step %q: %v", bad, err)
		require.Equal(t, "observability.slow_sync_threshold", ve.Field)
	}
}

// TestConfigureObservability_InvalidLogConfigRefused: an invalid log format
// or level stops qrdbd start before the slow-sync threshold is applied.
func TestConfigureObservability_InvalidLogConfigRefused(t *testing.T) {
	keepProcessObservability(t)
	syncwatch.SetSlowThreshold(5 * time.Second)
	for name, f := range map[string]*startFlags{
		"format": {logFormat: "xml", logLevel: "info"},
		"level":  {logFormat: "text", logLevel: "loud"},
	} {
		f.observability = qrdbconfig.ObservabilityConfig{SlowSyncThreshold: "50ms"}
		require.Error(t, configureObservability(f), name)
		require.Equal(t, 5*time.Second, syncwatch.SlowThreshold(), name)
	}
}

// TestConfigureObservability_DebugRaftRaisesLogLevel: --debug-raft makes the
// process logger emit debug records whatever --log-level says.
func TestConfigureObservability_DebugRaftRaisesLogLevel(t *testing.T) {
	keepProcessObservability(t)
	ctx := context.Background()

	require.NoError(t, configureObservability(&startFlags{logFormat: "text", logLevel: "info"}))
	require.False(t, slog.Default().Enabled(ctx, slog.LevelDebug))

	require.NoError(t, configureObservability(&startFlags{logFormat: "text", logLevel: "info", debugRaft: true}))
	require.True(t, slog.Default().Enabled(ctx, slog.LevelDebug))
}
