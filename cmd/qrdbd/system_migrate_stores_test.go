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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// TestSystemMigrationStores_ClassifiesByStateMachine pins which directories a
// system encryption migration re-encrypts, and as what kind of store: the
// pebble and mvcc state machines' own Pebble store in the host snapshot
// directory, or the storage-generic state machines' per-replica stores under
// sm/ in their --sm-storage backend, never a snapshot directory that is not a
// store. The vector store is always a Pebble store.
func TestSystemMigrationStores_ClassifiesByStateMachine(t *testing.T) {
	dataDir := t.TempDir()
	hostStore := filepath.Join(dataDir, "nodehost", "snapshots", fmt.Sprintf("shard-%d", shard.ShardKV))
	replica1 := filepath.Join(dataDir, "sm", "shard-1-replica-1")
	replica2 := filepath.Join(dataDir, "sm", "shard-2-replica-1")
	vector := filepath.Join(dataDir, "vector")
	for _, d := range []string{hostStore, replica1, replica2, vector} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}

	for name, tc := range map[string]struct {
		smType, smStorage string
		want              service.MigrationStores
	}{
		"pebble":                     {"pebble", "", service.MigrationStores{Pebble: []string{hostStore, vector}}},
		"mvcc":                       {"mvcc", "", service.MigrationStores{Pebble: []string{hostStore, vector}}},
		"disk over default storage":  {"disk", "", service.MigrationStores{Pebble: []string{replica1, replica2, vector}}},
		"disk over pebble storage":   {"disk", "pebble", service.MigrationStores{Pebble: []string{replica1, replica2, vector}}},
		"fast over file storage":     {"fast", "file", service.MigrationStores{Pebble: []string{vector}, File: []string{replica1, replica2}}},
		"concurrent over file store": {"concurrent", "file", service.MigrationStores{Pebble: []string{vector}, File: []string{replica1, replica2}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, systemMigrationStores(dataDir, tc.smType, tc.smStorage))
		})
	}
}

// TestSystemMigrationStores_NamesOnlyExistingDirectories pins that an absent
// store is not named: a fresh data directory has nothing to re-encrypt, and a
// regular file where a store would be is not a store.
func TestSystemMigrationStores_NamesOnlyExistingDirectories(t *testing.T) {
	dataDir := t.TempDir()
	require.Equal(t, service.MigrationStores{}, systemMigrationStores(dataDir, "pebble", ""))
	require.Equal(t, service.MigrationStores{}, systemMigrationStores(dataDir, "disk", "file"))

	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sm"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sm", "shard-1-replica-1"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "vector"), nil, 0o600))
	require.Equal(t, service.MigrationStores{}, systemMigrationStores(dataDir, "disk", "file"))
}
