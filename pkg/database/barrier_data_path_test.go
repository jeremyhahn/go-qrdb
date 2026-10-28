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

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backend: raft database is served from its Raft group, never from the
// in-memory placeholder the Manager opens for it. A per-database barrier
// would wrap that placeholder, so creation must refuse it rather than accept
// a barrier that encrypts nothing and whose seal protects nothing.
func TestManager_Create_BarrierOnRaftBackend_RefusedOffDataPath(t *testing.T) {
	mgr := newCatalogManager(nil, t.TempDir(), WithBarrierManager(newTestBarrierManager(t)))
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "raftvault",
		Backend: BackendRaft,
		Raft:    &DatabaseRaftConfig{ClusterMode: RaftLocal},
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase},
	}
	_, err := mgr.CreateWithCredentials(context.Background(), cfg, testCreds())
	var off *BarrierOffDataPathError
	if !errors.As(err, &off) {
		t.Fatalf("CreateWithCredentials(raft + barrier) error = %v (%T), want *BarrierOffDataPathError", err, err)
	}
	if off.Name != "raftvault" || off.ServedBy != ServedByRaftGroup {
		t.Errorf("refusal = %+v, want Name raftvault, ServedBy %q", off, ServedByRaftGroup)
	}
	if _, getErr := mgr.Get("raftvault"); getErr == nil {
		t.Error("the refused database was left in the catalog")
	}
}

// An explicit DataDir on a non-raft backend is the one shape whose data path
// is the barrier-wrapped store, so the refusal must not reach it.
func TestManager_Create_BarrierOnOwnStore_Accepted(t *testing.T) {
	root := t.TempDir()
	mgr := newCatalogManager(nil, filepath.Join(root, "node"), WithBarrierManager(newTestBarrierManager(t)))
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "ownstore",
		Backend: BackendPebble,
		DataDir: filepath.Join(root, "ownstore-data"),
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase},
	}
	db, err := mgr.CreateWithCredentials(context.Background(), cfg, testCreds())
	if err != nil {
		t.Fatalf("CreateWithCredentials(own store + barrier) error = %v, want nil", err)
	}
	if !db.BarrierEnabled() {
		t.Error("BarrierEnabled() = false, want true")
	}
}

// Enabling a barrier on an existing raft database is refused by the same
// data-path rule, before the migration rule is consulted.
func TestManager_UpdateBarrier_RaftBackend_RefusedOffDataPath(t *testing.T) {
	mgr := newTestManager(t)
	defer func() { _ = mgr.Close() }()

	cfg := DatabaseConfig{
		Name:    "raftupd",
		Backend: BackendRaft,
		Raft:    &DatabaseRaftConfig{ClusterMode: RaftLocal},
	}
	if _, err := mgr.Create(cfg); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	err := mgr.UpdateBarrier("raftupd", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase})
	var off *BarrierOffDataPathError
	if !errors.As(err, &off) {
		t.Fatalf("UpdateBarrier(raft, enable) error = %v (%T), want *BarrierOffDataPathError", err, err)
	}
}

// A barrier-enabled raft record written before the refusal still opens, and
// opens sealed: the request resolver refuses every data-plane request to a
// sealed database, so keeping it in the catalog keeps it fail-closed.
func TestManager_Open_LegacyRaftBarrier_OpensSealed(t *testing.T) {
	dataDir := t.TempDir()
	mgr := newCatalogManager(nil, dataDir, WithBarrierManager(newTestBarrierManager(t)))
	defer func() { _ = mgr.Close() }()

	meta := DatabaseMeta{
		Name:    "legacyraft",
		Backend: BackendRaft,
		Raft:    &DatabaseRaftConfig{ClusterMode: RaftLocal},
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase},
	}
	if err := os.MkdirAll(mgr.dbDir("", "legacyraft"), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := testCatalogFor(mgr.dataDir).Create(context.Background(), meta); err != nil {
		t.Fatalf("catalog Create: %v", err)
	}
	db, err := mgr.Open(context.Background(), "", "legacyraft")
	if err != nil {
		t.Fatalf("Open(legacy raft + barrier) error = %v, want nil", err)
	}
	if db.Status() != StatusSealed {
		t.Errorf("Status() = %q, want %q", db.Status(), StatusSealed)
	}
}

func TestDatabaseConfig_BarrierOnDataPath(t *testing.T) {
	cases := []struct {
		name string
		cfg  DatabaseConfig
		want bool
	}{
		{"own store", DatabaseConfig{Backend: BackendPebble, DataDir: "/srv/db"}, true},
		{"own file store", DatabaseConfig{Backend: BackendFile, DataDir: "/srv/db"}, true},
		{"shared default", DatabaseConfig{Backend: BackendPebble}, false},
		{"whitespace data dir", DatabaseConfig{Backend: BackendPebble, DataDir: "  "}, false},
		{"raft", DatabaseConfig{Backend: BackendRaft}, false},
		{"raft with data dir", DatabaseConfig{Backend: BackendRaft, DataDir: "/srv/db"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.BarrierOnDataPath(); got != c.want {
				t.Errorf("BarrierOnDataPath() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBarrierOffDataPathError_Error(t *testing.T) {
	msg := (&BarrierOffDataPathError{Name: "vault", ServedBy: ServedByVolumeTenantStore}).Error()
	for _, want := range []string{`"vault"`, ServedByVolumeTenantStore, "not from a store the barrier wraps"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}
}
