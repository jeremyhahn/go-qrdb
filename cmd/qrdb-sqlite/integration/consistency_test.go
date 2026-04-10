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

//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PRAGMA tests validate the PragmaStore in isolation; they do not require
// multi-protocol looping because PRAGMAs are local to the SQLite connection
// and independent of the transport layer. Each test creates a single env
// using any available protocol (REST as default).

// TestConsistency_PragmaSwitch verifies that SetConsistency accepts 'eventual'
// and that Consistency() reflects the updated value.
func TestConsistency_PragmaSwitch(t *testing.T) {
	env := newTestEnv(t, protoREST)

	require.NoError(t, env.pragma.SetConsistency("eventual"))
	assert.Equal(t, "eventual", env.pragma.Consistency())
}

// TestConsistency_WriteModePragma verifies that SetWriteMode accepts 'batched'
// and that WriteMode() reflects the updated value.
func TestConsistency_WriteModePragma(t *testing.T) {
	env := newTestEnv(t, protoREST)

	require.NoError(t, env.pragma.SetWriteMode("batched"))
	assert.Equal(t, "batched", env.pragma.WriteMode())
}

// TestConsistency_StatusPragma reads the exported Consistency and WriteMode
// accessors to verify the PragmaStore surfaces the full status with correct
// defaults.
func TestConsistency_StatusPragma(t *testing.T) {
	env := newTestEnv(t, protoREST)

	assert.Equal(t, "linearizable", env.pragma.Consistency(),
		"status should report linearizable consistency by default")
	assert.Equal(t, "sync", env.pragma.WriteMode(),
		"status should report sync write mode by default")
}

// TestConsistency_DefaultValues verifies that a freshly created test
// environment begins with the production-safe defaults: linearizable
// consistency and sync write mode.
func TestConsistency_DefaultValues(t *testing.T) {
	env := newTestEnv(t, protoREST)

	assert.Equal(t, "linearizable", env.pragma.Consistency(),
		"default consistency must be 'linearizable'")
	assert.Equal(t, "sync", env.pragma.WriteMode(),
		"default write mode must be 'sync'")
}

// TestConsistency_AllLevels cycles through every valid consistency level and
// confirms that SetConsistency accepts each value without error.
func TestConsistency_AllLevels(t *testing.T) {
	levels := []string{"linearizable", "strong", "eventual"}
	for _, level := range levels {
		level := level
		t.Run(level, func(t *testing.T) {
			env := newTestEnv(t, protoREST)
			require.NoError(t, env.pragma.SetConsistency(level),
				"SetConsistency(%q) must not fail", level)
			assert.Equal(t, level, env.pragma.Consistency())
		})
	}
}

// TestConsistency_AllWriteModes cycles through every valid write mode and
// confirms that SetWriteMode accepts each value without error.
func TestConsistency_AllWriteModes(t *testing.T) {
	modes := []string{"sync", "wal_durable", "batched"}
	for _, mode := range modes {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			env := newTestEnv(t, protoREST)
			require.NoError(t, env.pragma.SetWriteMode(mode),
				"SetWriteMode(%q) must not fail", mode)
			assert.Equal(t, mode, env.pragma.WriteMode())
		})
	}
}

// TestConsistency_InvalidLevel verifies that SetConsistency rejects unknown
// consistency level strings with a typed error without changing the current
// level.
func TestConsistency_InvalidLevel(t *testing.T) {
	env := newTestEnv(t, protoREST)

	err := env.pragma.SetConsistency("chaos")
	require.Error(t, err, "SetConsistency with invalid level must return an error")
	assert.Equal(t, "linearizable", env.pragma.Consistency(),
		"consistency must not change after a failed SetConsistency call")
}

// TestConsistency_InvalidWriteMode verifies that SetWriteMode rejects unknown
// write mode strings with a typed error without changing the current mode.
func TestConsistency_InvalidWriteMode(t *testing.T) {
	env := newTestEnv(t, protoREST)

	err := env.pragma.SetWriteMode("turbo")
	require.Error(t, err, "SetWriteMode with invalid mode must return an error")
	assert.Equal(t, "sync", env.pragma.WriteMode(),
		"write mode must not change after a failed SetWriteMode call")
}

// TestConsistency_E2E_SwitchAffectsVTableReads verifies that a consistency
// level change is visible to the virtual table layer during subsequent SQL
// reads against the real qrdbd server.
func TestConsistency_E2E_SwitchAffectsVTableReads(t *testing.T) {
	env := newTestEnv(t, protoREST)
	ctx := context.Background()

	key := uniqueKey(protoREST, "pragma-key")

	// Write a key via SDK so the data is in qrdbd independently of the vtab.
	require.NoError(t, env.client.KV().Put(ctx, key, []byte("pragma-val")))

	// Confirm the key is present under linearizable (default) consistency.
	assertRowCount(t, env.db, `SELECT * FROM kv WHERE key = ?`, 1, key)

	// Switch to eventual consistency.
	require.NoError(t, env.pragma.SetConsistency("eventual"))
	assert.Equal(t, "eventual", env.pragma.Consistency())

	// The same SELECT should still succeed; the qrdbd store serves both levels.
	assertRowCount(t, env.db, `SELECT * FROM kv WHERE key = ?`, 1, key)
}

// TestConsistency_MultiProtocol_DefaultValues verifies that a freshly created
// test environment begins with the production-safe defaults for every protocol.
func TestConsistency_MultiProtocol_DefaultValues(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			assert.Equal(t, "linearizable", env.pragma.Consistency(),
				"default consistency must be 'linearizable' for %s", proto)
			assert.Equal(t, "sync", env.pragma.WriteMode(),
				"default write mode must be 'sync' for %s", proto)
		})
	}
}
