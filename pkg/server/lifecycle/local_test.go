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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
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
