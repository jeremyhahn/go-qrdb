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

package lifecycle

import (
	"path/filepath"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// local.go is the canonical raft-free storage/engine composition shared by
// every LocalBackend composition root: the qrdbd daemon's mode:standalone
// branch, sdk/go/node.NewStandaloneNode, and the SDK's embedded transport.
// Keeping it here (the same package that composes the service graph over
// the resulting engine) prevents the three roots from drifting.

// NewLocalStorage creates the node-local storage backend for a raft-free
// composition and returns the storage interface plus an optional *pebble.DB
// handle (non-nil only for the pebble backend). When dataPlaneFS is non-nil
// it is wired into the pebble backend's vfs.FS so all on-disk I/O routes
// through it (the seal barrier's encfs). The memory, file, and objstore
// backends ignore dataPlaneFS — encfs wraps Pebble's vfs specifically.
func NewLocalStorage(backendType database.BackendType, dataDir string, dataPlaneFS vfs.FS, objCfg qrdbconfig.ObjstoreConfig) (storage.Storage, *pebble.DB, error) {
	switch backendType {
	case database.BackendMemory:
		return memory.NewStorage(), nil, nil

	case database.BackendFile:
		stg, err := file.New(filepath.Join(dataDir, "file"))
		if err != nil {
			return nil, nil, &LocalStorageError{Backend: backendType, Err: err}
		}
		return stg, nil, nil

	case database.BackendObjstore:
		provider, settings := resolveObjstoreSettings(objCfg, dataDir)
		stg, err := objstore.New(provider, settings)
		if err != nil {
			return nil, nil, &LocalStorageError{Backend: backendType, Err: err}
		}
		return stg, nil, nil

	case database.BackendPebble:
		pebbleDir := filepath.Join(dataDir, "pebble")
		cfg := pebbleStorage.DefaultConfig(pebbleDir)
		if dataPlaneFS != nil {
			cfg.FS = dataPlaneFS
		}
		stg, err := pebbleStorage.New(cfg)
		if err != nil {
			return nil, nil, &LocalStorageError{Backend: backendType, Err: err}
		}
		pdb := stg.DB()
		return stg, pdb, nil

	default:
		return nil, nil, &UnsupportedLocalBackendError{Backend: backendType}
	}
}

// resolveObjstoreSettings derives the go-objstore provider and settings for
// an objstore-backed node. An empty provider defaults to "local"; for the
// local provider an empty "path" is rooted at <dataDir>/objstore so the
// backend works out of the box. The caller's settings map is never mutated.
func resolveObjstoreSettings(objCfg qrdbconfig.ObjstoreConfig, dataDir string) (string, map[string]string) {
	provider := objCfg.Provider
	if provider == "" {
		provider = "local"
	}
	settings := make(map[string]string, len(objCfg.Settings)+1)
	for k, v := range objCfg.Settings {
		settings[k] = v
	}
	if provider == "local" && settings["path"] == "" {
		settings["path"] = filepath.Join(dataDir, "objstore")
	}
	return provider, settings
}

// NewLocalEngine creates the store engine for a raft-free composition.
// Pebble and memory backends compose the MVCC-capable engine — an
// implementation detail that keeps timeseries/vector/collections and the
// SQL catalog working — while file and objstore compose the plain KV
// engine (the documented engine boundary, pkg/engine/kv_engine.go).
//
// hub is the node's watch hub and MUST be the same instance handed to
// Infra.WatchHub: the engine is the only publisher of change events, so an
// engine built without the hub leaves the WatchService subscribed to a hub
// nothing ever writes to — watches then "work" but never deliver.
func NewLocalEngine(backendType database.BackendType, stg storage.Storage, pdb *pebble.DB, hub *watch.Hub) (backend.SMAdapter, error) {
	if backendType == database.BackendFile || backendType == database.BackendObjstore {
		kvOpts := []engine.KVOption{}
		if hub != nil {
			kvOpts = append(kvOpts, engine.WithKVWatchHub(hub))
		}
		return engine.NewKVEngine(stg, kvOpts...), nil
	}

	mvccOpts := []engine.MVCCOption{
		engine.WithMVCCLessor(lease.NewLessor()),
	}
	if hub != nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCWatchHub(hub))
	}
	if pdb != nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCPebbleDB(pdb))
		if tsStorage, err := timeseries.New(pdb); err == nil {
			mvccOpts = append(mvccOpts, engine.WithMVCCTimeSeries(tsStorage))
		}
	}

	eng, err := engine.NewMVCCEngine(stg, mvccOpts...)
	if err != nil {
		return nil, &LocalEngineError{Err: err}
	}
	return eng, nil
}
