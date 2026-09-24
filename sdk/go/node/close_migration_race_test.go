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
)

// closeRaceStandaloneConfig returns a minimal none-strategy standalone config
// with every network protocol disabled, so NewStandaloneNode boots fast and
// needs no ports -- this suite only cares about migrateMu's ordering, not the
// data plane it guards.
func closeRaceStandaloneConfig(t *testing.T, dataDir string) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// TestNode_Close_WaitsForInFlightSystemMigration is the go-qrdb#226
// shutdown-discipline regression test for the SYSTEM-migration slice of the
// Close/EnableEncryption race documented on Node's "Accessor concurrency"
// comment: before Close acquired migrateMu, a system-scope migration still
// running past its transport handler's accept window (every handler races
// EnableEncryption against ~750ms and returns "migration_started" if it is
// still in flight) could have Close nil n.sealStore out from under
// nodeMigrationProvider.Reopen's canonicalRootKeyStrategy read, segfaulting on
// a nil *pebble.PebbleStorage.
//
// This test does not need to drive a real migration to pin that ordering: the
// property Close now provides is "blocks on migrateMu before doing anything
// else", which is exactly what acquiring the SAME mutex a migration holds
// proves, whoever the holder is. Holding migrateMu directly here -- rather
// than starting a real EnableEncryption call and racing it against a real
// Close on timing -- makes the test deterministic under -race instead of
// depending on the migration outstanding the 750ms accept window.
func TestNode_Close_WaitsForInFlightSystemMigration(t *testing.T) {
	ctx := context.Background()
	cfg := closeRaceStandaloneConfig(t, t.TempDir())

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	require.False(t, n.IsSealed(), "none-strategy node boots unsealed")

	// Simulate a system-scope migration already in flight: hold migrateMu the
	// way nodeSystemMigrator.EnableEncryption does for its entire recycle, and
	// release it only after a delay this test can observe Close waiting out.
	n.migrateMu.Lock()
	releasedAt := make(chan time.Time, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		releasedAt <- time.Now()
		n.migrateMu.Unlock()
	}()

	closeReturnedAt := make(chan time.Time, 1)
	closeErr := make(chan error, 1)
	go func() {
		err := n.Close()
		closeReturnedAt <- time.Now()
		closeErr <- err
	}()

	// Close must still be blocked well before the simulated migration
	// releases the lock -- if it is not, Close raced past migrateMu instead
	// of waiting for it, which is the exact defect this test exists to catch.
	select {
	case <-closeReturnedAt:
		t.Fatal("Close returned before the in-flight migration released migrateMu")
	case <-time.After(50 * time.Millisecond):
		// Expected: Close is still waiting.
	}

	rt := <-releasedAt
	require.NoError(t, <-closeErr)
	ct := <-closeReturnedAt
	require.False(t, ct.Before(rt), "Close returned (%v) before migrateMu was released (%v)", ct, rt)
}

// TestNode_Close_ReturnsPromptlyWithNoInFlightMigration is
// TestNode_Close_WaitsForInFlightSystemMigration's converse: Close must not
// pay migrateMu's cost when nothing holds it, so the new Lock in Close does
// not turn every ordinary shutdown into a slow one.
func TestNode_Close_ReturnsPromptlyWithNoInFlightMigration(t *testing.T) {
	ctx := context.Background()
	cfg := closeRaceStandaloneConfig(t, t.TempDir())

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- n.Close() }()

	select {
	case err := <-done:
		require.NoError(t, err)
		require.Less(t, time.Since(start), 2*time.Second, "Close took too long with no in-flight migration")
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s with no in-flight migration")
	}
}
