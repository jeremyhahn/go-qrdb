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

// The raft-free composition root: which storage each backend gets, and which
// ENGINE is composed over it.
//
// No build constraint: these name only NewLocalStorage, NewLocalEngine and the
// database/config types, all unconstrained.

package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestNewLocalStorage_EveryBackend walks every backend the composition root
// accepts, plus one it does not.
//
// The pebble arm additionally checks that the *pebble.DB handle comes back
// non-nil while the others come back nil. That handle is not decoration:
// NewLocalEngine passes it to WithMVCCPebbleDB and to timeseries.New, so a
// pebble node that returned a nil handle would compose an engine with no
// time-series storage and no direct DB access, and would do it silently.
func TestNewLocalStorage_EveryBackend(t *testing.T) {
	for _, tc := range []struct {
		backend    database.BackendType
		wantPebble bool
	}{
		{database.BackendMemory, false},
		{database.BackendFile, false},
		{database.BackendPebble, true},
		{database.BackendObjstore, false},
	} {
		t.Run(string(tc.backend), func(t *testing.T) {
			stg, pdb, err := lifecycle.NewLocalStorage(
				tc.backend, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
			if err != nil {
				t.Fatalf("NewLocalStorage(%s): %v", tc.backend, err)
			}
			if stg == nil {
				t.Fatalf("NewLocalStorage(%s) returned a nil storage and no error", tc.backend)
			}
			t.Cleanup(func() { _ = stg.Close() })

			if tc.wantPebble && pdb == nil {
				t.Errorf("%s must return a *pebble.DB handle: the MVCC engine and "+
					"timeseries.New are both built from it", tc.backend)
			}
			if !tc.wantPebble && pdb != nil {
				t.Errorf("%s returned a *pebble.DB handle; only the pebble backend has one", tc.backend)
			}
		})
	}
}

// TestNewLocalStorage_UnsupportedBackend: raft is a real BackendType and is
// deliberately NOT a local backend -- a raft-free composition root cannot
// build it. It must be refused with the typed error rather than falling
// through to a default.
func TestNewLocalStorage_UnsupportedBackend(t *testing.T) {
	for _, backend := range []database.BackendType{database.BackendRaft, "", "not-a-backend"} {
		t.Run(string(backend), func(t *testing.T) {
			stg, pdb, err := lifecycle.NewLocalStorage(
				backend, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
			if err == nil {
				t.Fatalf("NewLocalStorage(%q) returned no error", backend)
			}
			var unsupported *lifecycle.UnsupportedLocalBackendError
			if !asError(err, &unsupported) {
				t.Fatalf("error = %T (%v), want *UnsupportedLocalBackendError", err, err)
			}
			if unsupported.Backend != backend {
				t.Errorf("Backend = %q, want %q", unsupported.Backend, backend)
			}
			if stg != nil || pdb != nil {
				t.Error("a refused backend must return no storage and no handle")
			}
		})
	}
}

// TestNewLocalStorage_OpenFailureNamesTheBackend: a backend that cannot open
// its store reports *LocalStorageError naming WHICH backend failed, wrapping
// the cause, and hands back no storage -- an operator reading the boot error
// learns the backend without guessing from a bare filesystem message.
func TestNewLocalStorage_OpenFailureNamesTheBackend(t *testing.T) {
	// A data directory that is a regular file: neither the file backend nor
	// pebble can create its store directory under it.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tests := []struct {
		backend database.BackendType
		dataDir string
		objCfg  qrdbconfig.ObjstoreConfig
	}{
		{backend: database.BackendFile, dataDir: notADir},
		{backend: database.BackendPebble, dataDir: notADir},
		{backend: database.BackendObjstore, dataDir: t.TempDir(), objCfg: qrdbconfig.ObjstoreConfig{Provider: "no-such-provider"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.backend), func(t *testing.T) {
			stg, pdb, err := lifecycle.NewLocalStorage(tc.backend, tc.dataDir, nil, tc.objCfg)
			var target *lifecycle.LocalStorageError
			if !asError(err, &target) {
				t.Fatalf("error = %T (%v), want *LocalStorageError", err, err)
			}
			if target.Backend != tc.backend {
				t.Errorf("LocalStorageError.Backend = %q, want %q", target.Backend, tc.backend)
			}
			if target.Err == nil {
				t.Error("LocalStorageError carries no cause")
			}
			if stg != nil || pdb != nil {
				t.Error("a failed open must return no storage and no handle")
			}
		})
	}
}

// TestNewLocalStorage_PebbleRoutesThroughTheDataPlaneFS: when the caller
// supplies a data-plane filesystem (the seal barrier's encfs in production),
// every pebble file goes through it -- nothing is written to the real disk at
// <dataDir>/pebble, which is what keeps an encrypted node's store off the
// plaintext filesystem.
func TestNewLocalStorage_PebbleRoutesThroughTheDataPlaneFS(t *testing.T) {
	dataDir := t.TempDir()
	memFS := vfs.NewMem()

	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendPebble, dataDir, memFS, qrdbconfig.ObjstoreConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage(pebble, memFS): %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	if pdb == nil {
		t.Fatal("pebble backend returned no *pebble.DB handle")
	}

	if _, err := os.Stat(filepath.Join(dataDir, "pebble")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pebble wrote to the real disk despite a data-plane FS (stat err = %v)", err)
	}
	entries, err := memFS.List(filepath.Join(dataDir, "pebble"))
	if err != nil {
		t.Fatalf("data-plane FS has no pebble directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("data-plane FS holds no pebble files")
	}
}

// TestNewLocalStorage_ObjstoreDefaultsUnderDataDir pins the out-of-the-box
// objstore behaviour: with no provider and no path configured, the backend
// roots itself at <dataDir>/objstore rather than at the process working
// directory. Getting this wrong writes a node's data somewhere nobody backs
// up, and does it without an error.
func TestNewLocalStorage_ObjstoreDefaultsUnderDataDir(t *testing.T) {
	dataDir := t.TempDir()
	stg, _, err := lifecycle.NewLocalStorage(
		database.BackendObjstore, dataDir, nil, qrdbconfig.ObjstoreConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage(objstore): %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })

	if _, err := statDir(filepath.Join(dataDir, "objstore")); err != nil {
		t.Errorf("objstore did not root itself at <dataDir>/objstore: %v", err)
	}
}

// TestNewLocalStorage_ObjstoreHonoursAnExplicitPath is the other half: a
// configured path is used as given, and the default must not override it.
func TestNewLocalStorage_ObjstoreHonoursAnExplicitPath(t *testing.T) {
	dataDir := t.TempDir()
	explicit := filepath.Join(t.TempDir(), "elsewhere")

	stg, _, err := lifecycle.NewLocalStorage(database.BackendObjstore, dataDir, nil,
		qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": explicit}})
	if err != nil {
		t.Fatalf("NewLocalStorage(objstore): %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })

	if _, err := statDir(explicit); err != nil {
		t.Errorf("an explicit objstore path was not honoured: %v", err)
	}
	if _, err := statDir(filepath.Join(dataDir, "objstore")); err == nil {
		t.Error("the default path was created even though an explicit one was configured")
	}
}

// TestNewLocalStorage_DoesNotMutateTheCallerSettings: the settings map belongs
// to the caller's config, which is shared. Defaulting "path" into it in place
// would pin the FIRST node's data directory into every later composition built
// from the same config.
func TestNewLocalStorage_DoesNotMutateTheCallerSettings(t *testing.T) {
	settings := map[string]string{}
	cfg := qrdbconfig.ObjstoreConfig{Provider: "local", Settings: settings}

	stg, _, err := lifecycle.NewLocalStorage(database.BackendObjstore, t.TempDir(), nil, cfg)
	if err != nil {
		t.Fatalf("NewLocalStorage(objstore): %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })

	if got, ok := settings["path"]; ok {
		t.Errorf("the caller's settings map was mutated: path = %q", got)
	}
}

// TestNewLocalEngine_EngineBoundary pins the documented split: file and
// objstore compose the plain KV engine, while memory and pebble compose the
// MVCC-capable one because timeseries, vector, collections and the SQL catalog
// all depend on it.
//
// This is a behavioural boundary rather than an implementation detail, which
// is why it is asserted rather than left to the composition: a node whose
// backend quietly moved to the KV engine keeps serving KV traffic and starts
// failing every MVCC-dependent surface.
func TestNewLocalEngine_EngineBoundary(t *testing.T) {
	for _, tc := range []struct {
		backend  database.BackendType
		wantMVCC bool
	}{
		{database.BackendMemory, true},
		{database.BackendPebble, true},
		{database.BackendFile, false},
		{database.BackendObjstore, false},
	} {
		t.Run(string(tc.backend), func(t *testing.T) {
			stg, pdb, err := lifecycle.NewLocalStorage(
				tc.backend, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
			if err != nil {
				t.Fatalf("NewLocalStorage(%s): %v", tc.backend, err)
			}
			t.Cleanup(func() { _ = stg.Close() })

			eng, err := lifecycle.NewLocalEngine(tc.backend, stg, pdb, watch.NewHub(), lease.NewLessor())
			if err != nil {
				t.Fatalf("NewLocalEngine(%s): %v", tc.backend, err)
			}
			if eng == nil {
				t.Fatalf("NewLocalEngine(%s) returned a nil engine and no error", tc.backend)
			}

			// The CONCRETE type, not merely non-nil. Asserting non-nil here
			// would pass whichever engine were composed, which is the one
			// thing this test exists to tell apart.
			_, isMVCC := eng.(*engine.MVCCEngine)
			_, isKV := eng.(*engine.KVEngine)
			switch {
			case tc.wantMVCC && !isMVCC:
				t.Errorf("%s composed %T, want *engine.MVCCEngine: timeseries, vector, "+
					"collections and the SQL catalog all depend on MVCC", tc.backend, eng)
			case !tc.wantMVCC && !isKV:
				t.Errorf("%s composed %T, want *engine.KVEngine (the documented engine "+
					"boundary, pkg/engine/kv_engine.go)", tc.backend, eng)
			}
		})
	}
}

// TestLocalEngineKeepsLeases_MatchesTheComposedEngine pins the predicate to
// what the engine NewLocalEngine composes actually does with a lease grant:
// a composer that trusted it while it disagreed would either start the
// lease-backed volume-pool Reporter on an engine that refuses every grant
// (the file backend's boot logged exactly that refusal) or skip it on one
// that would have served it.
func TestLocalEngineKeepsLeases_MatchesTheComposedEngine(t *testing.T) {
	for _, tc := range []struct {
		backend database.BackendType
		want    bool
	}{
		{database.BackendMemory, true},
		{database.BackendPebble, true},
		{database.BackendFile, false},
		{database.BackendObjstore, false},
	} {
		t.Run(string(tc.backend), func(t *testing.T) {
			if got := lifecycle.LocalEngineKeepsLeases(tc.backend); got != tc.want {
				t.Fatalf("LocalEngineKeepsLeases(%s) = %v; want %v", tc.backend, got, tc.want)
			}
			stg, pdb, err := lifecycle.NewLocalStorage(tc.backend, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
			if err != nil {
				t.Fatalf("NewLocalStorage(%s): %v", tc.backend, err)
			}
			t.Cleanup(func() { _ = stg.Close() })
			eng, err := lifecycle.NewLocalEngine(tc.backend, stg, pdb, watch.NewHub(), lease.NewLessor())
			if err != nil {
				t.Fatalf("NewLocalEngine(%s): %v", tc.backend, err)
			}

			grant, err := protocol.EncodeLeaseGrant(60)
			if err != nil {
				t.Fatalf("EncodeLeaseGrant: %v", err)
			}
			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grant}}, results); err != nil {
				t.Fatalf("Update(lease grant): %v", err)
			}
			_, refused := protocol.DecodeAppError(results[0].Data)
			if granted := !refused && results[0].Value != 0; granted != tc.want {
				t.Fatalf("the %s engine granted a lease = %v (result %+v); LocalEngineKeepsLeases says %v",
					tc.backend, granted, results[0], tc.want)
			}
		})
	}
}

// TestNewLocalEngine_NilHubIsAccepted: the hub and the lessor are optional at this seam, and a
// nil one must not panic. The engine is the only publisher of change events,
// so a nil hub means "nothing is watching", not "misconfigured".
func TestNewLocalEngine_NilHubIsAccepted(t *testing.T) {
	stg, pdb, err := lifecycle.NewLocalStorage(
		database.BackendMemory, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })

	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, nil, nil)
	if err != nil {
		t.Fatalf("NewLocalEngine with a nil hub: %v", err)
	}
	if eng == nil {
		t.Fatal("NewLocalEngine returned a nil engine and no error")
	}
}

// TestNewLocalEngine_AttachesAuditChainUnconditionally is audit-replicated
// review finding N4 at the standalone/raft-free composition root: every MVCC
// engine this seam builds (memory and pebble backends) must carry a non-nil
// audit.Chain regardless of caller input, because the opcode handler has no
// notion of "standalone" and would otherwise refuse every
// protocol.OpAuditAppend this node receives. The file/objstore (KV) engine
// does not support the chain at all and is asserted nil here on purpose.
func TestNewLocalEngine_AttachesAuditChainUnconditionally(t *testing.T) {
	for _, tc := range []struct {
		backend   database.BackendType
		wantChain bool
	}{
		{database.BackendMemory, true},
		{database.BackendPebble, true},
		{database.BackendFile, false},
		{database.BackendObjstore, false},
	} {
		t.Run(string(tc.backend), func(t *testing.T) {
			stg, pdb, err := lifecycle.NewLocalStorage(
				tc.backend, t.TempDir(), nil, qrdbconfig.ObjstoreConfig{})
			if err != nil {
				t.Fatalf("NewLocalStorage(%s): %v", tc.backend, err)
			}
			t.Cleanup(func() { _ = stg.Close() })

			eng, err := lifecycle.NewLocalEngine(tc.backend, stg, pdb, nil, nil)
			if err != nil {
				t.Fatalf("NewLocalEngine(%s): %v", tc.backend, err)
			}

			mvcc, isMVCC := eng.(*engine.MVCCEngine)
			if !tc.wantChain {
				if isMVCC {
					t.Fatalf("%s composed *engine.MVCCEngine, want the KV engine", tc.backend)
				}
				return
			}
			if !isMVCC {
				t.Fatalf("%s composed %T, want *engine.MVCCEngine", tc.backend, eng)
			}
			if mvcc.AuditChain() == nil {
				t.Errorf("%s: MVCCEngine built with a nil audit.Chain attached", tc.backend)
			}
		})
	}
}

// asError is errors.As with the target already typed, kept here so the table
// cases above read as assertions rather than as plumbing.
func asError[T error](err error, target *T) bool {
	return errors.As(err, target)
}

// statDir reports whether path exists and is a directory.
func statDir(path string) (os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s exists but is not a directory", path)
	}
	return fi, nil
}
