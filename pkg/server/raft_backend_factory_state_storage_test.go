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

package server

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// pebbleManifests counts the pebble MANIFEST files under root.
func pebbleManifests(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "MANIFEST-") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

// TestRaftBackendFactory_GroupStartHonorsRecordedStateStorage pins that a
// dedicated group's state machine runs over the state storage its database
// records, on the first start and on a restart, and that a database that
// records pebble keeps its on-disk state machine.
func TestRaftBackendFactory_GroupStartHonorsRecordedStateStorage(t *testing.T) {
	for _, tc := range []struct {
		storage database.BackendType
		onDisk  bool
	}{
		{storage: database.BackendPebble, onDisk: true},
		{storage: database.BackendMemory, onDisk: false},
	} {
		t.Run(string(tc.storage), func(t *testing.T) {
			host, _ := newSharedTestHost(t)
			reg := newVolumePooledRegistry(t)
			src := newFakeRaftInfoSource()
			f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger())
			t.Cleanup(func() { _ = f.CloseAll() })

			registerVolumeTenant(t, reg, "rp")
			dataRoot := filepath.Join(t.TempDir(), "rp")
			src.add("rp", "app", 1106, dataRoot)
			src.stateStorages = map[raftShardKey]database.BackendType{{tenantID: "rp", database: "app"}: tc.storage}
			dbDir := filepath.Join(dataRoot, "databases", "app")

			for _, phase := range []string{"start", "restart"} {
				if phase == "restart" {
					if err := f.Close("rp", "app"); err != nil {
						t.Fatalf("Close: %v", err)
					}
				}
				if _, err := f.Services(context.Background(), "rp", "app"); err != nil {
					t.Fatalf("%s: Services: %v", phase, err)
				}
				if got := pebbleManifests(t, dbDir) > 0; got != tc.onDisk {
					t.Fatalf("%s: on-disk state machine under %s = %v, want %v", phase, dbDir, got, tc.onDisk)
				}
			}
		})
	}
}
