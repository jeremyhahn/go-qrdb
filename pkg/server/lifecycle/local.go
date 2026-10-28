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
	"context"
	"log/slog"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// local.go is the canonical raft-free storage/engine composition shared by
// every LocalBackend composition root: the qrdbd daemon's mode:standalone
// branch, sdk/go/node.NewStandaloneNode, and the SDK's embedded transport.
// Keeping it here (the same package that composes the service graph over
// the resulting engine) prevents the three roots from drifting.

// NewLocalStorage creates the node-local storage backend for a raft-free
// composition and returns the storage interface plus an optional *pebble.DB
// handle (non-nil only for the pebble backend). enc is the at-rest encryption
// every on-disk backend writes through: the pebble backend installs its file
// system and the file backend does its record I/O through it, so both are
// encrypted under the same barrier by the same mechanism (the seal barrier's
// encfs). A nil or disabled enc leaves them plaintext. The memory backend
// holds nothing on disk and the objstore backend is not a local file system,
// so both ignore enc.
func NewLocalStorage(backendType database.BackendType, dataDir string, enc *atrest.Encryptor, objCfg qrdbconfig.ObjstoreConfig) (storage.Storage, *pebble.DB, error) {
	switch backendType {
	case database.BackendMemory:
		return memory.NewStorage(), nil, nil

	case database.BackendFile:
		stg, err := file.New(filepath.Join(dataDir, "file"), file.WithEncryptor(enc))
		if err != nil {
			return nil, nil, &LocalStorageError{Backend: backendType, Err: err}
		}
		return stg, nil, nil

	case database.BackendObjstore:
		provider, settings := resolveObjstoreSettings(objCfg, dataDir)
		stg, err := objstore.New(provider, settings, objstore.WithCredentialFiles(objstore.CredentialFiles{
			KeyFile:    objCfg.Credentials.KeyFile,
			SecretFile: objCfg.Credentials.SecretFile,
		}))
		if err != nil {
			return nil, nil, &LocalStorageError{Backend: backendType, Err: err}
		}
		return stg, nil, nil

	case database.BackendPebble:
		pebbleDir := filepath.Join(dataDir, "pebble")
		cfg := pebbleStorage.DefaultConfig(pebbleDir)
		if enc.Enabled() {
			cfg.FS = pebbleStorage.WrapVFS(nil, enc.Wrap)
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

// NewLocalEngine creates the store engine for a raft-free composition. Every
// backend -- memory, file, pebble and objstore alike -- composes the
// MVCC-capable engine: secondary indexes and time series are storage-backed
// (pkg/index.StoragePebbleIndex, pkg/timeseries.Storage), so they work
// identically over any storage.Storage and their writes land in the same
// apply batch as the rest of the entry's effect. The plain KV engine
// (pkg/engine/kv_engine.go) is no longer composed here.
//
// hub is the node's watch hub and MUST be the same instance handed to
// Infra.WatchHub: the engine is the only publisher of change events, so an
// engine built without the hub leaves the WatchService subscribed to a hub
// nothing ever writes to — watches then "work" but never deliver.
//
// lr is the node's lessor and MUST be the same instance the composer runs the
// expiry sweeper over (lease.Lessor.RunExpiry, see StartLocalLeaseExpiry):
// the engine rebuilds the durable leases into it on open, and an engine whose
// lessor nothing sweeps grants leases whose keys are never deleted.
//
// LocalEngineKeepsLeases reports whether the engine NewLocalEngine composes
// for backendType keeps leases. Every backend now composes the MVCC-capable
// engine, which always does; the predicate stays so the composers that gate
// lease-backed features on it -- the volume-pool health Reporter among them --
// do not need their own copy of this reasoning.
func LocalEngineKeepsLeases(_ database.BackendType) bool {
	return true
}

// The MVCC engine also always gets a fresh log registry, vlog.Chain, the
// replicated audit chain's instance among its logs (engine.WithMVCCLogs):
// unlike hub and lr, nothing outside the engine
// needs to share this instance, so one is constructed here rather than
// threaded in from the caller. It is attached unconditionally for the same
// reason the raft composition in cmd/qrdbd/start.go attaches one
// unconditionally to every mvcc/pebble state machine: a standalone node is a
// single replica, so there is no sibling to diverge from, but the opcode
// handler itself has no notion of "standalone" and would otherwise refuse
// every protocol.OpAuditAppend this composition root receives with
// *statemachine.ServiceNotConfiguredError.
//
// pdb is accepted for callers that still need the raw pebble handle for
// backend-specific concerns outside the engine (compaction scheduling,
// metrics); the engine itself needs only stg, which every backend provides.
func NewLocalEngine(_ database.BackendType, stg storage.Storage, pdb *pebble.DB, hub *watch.Hub, lr *lease.Lessor) (backend.SMAdapter, error) {
	mvccOpts := []engine.MVCCOption{engine.WithMVCCLogs(vlog.NewChain())}
	if lr != nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCLessor(lr))
	}
	if hub != nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCWatchHub(hub))
	}

	// Time series rides the same storage.Storage the engine itself is built
	// over, on every backend -- not only pebble -- so its writes land in the
	// same apply batch as everything else the entry does.
	if tsStorage, err := timeseries.New(stg); err == nil {
		mvccOpts = append(mvccOpts, engine.WithMVCCTimeSeries(tsStorage))
	}

	eng, err := engine.NewMVCCEngine(stg, mvccOpts...)
	if err != nil {
		return nil, &LocalEngineError{Err: err}
	}
	return eng, nil
}

// StartLocalLeaseExpiry starts the lease-expiry sweeper of a raft-free
// composition. The sweeper also stops when ctx is cancelled.
//
// A standalone node is the only replica of its data, so it is always the one
// that sweeps: the leader gate RunExpiry consults in raft mode is constant
// here. The revoke still goes through leaseSvc -- the same apply path a raft
// leader's revoke takes -- so an expired lease deletes its keys, removes its
// durable records and notifies watchers exactly as in raft mode. Without the
// sweeper a lease's Done channel closes on time but nothing deletes the keys
// bound to it, which leaves a dead holder's lock held for good.
//
// The returned stop function cancels the sweeper and WAITS for it to exit.
// A composer calls it before closing the backend the sweeper revokes
// through: cancelling alone would leave a revoke in flight against a backend
// that is closing. It is safe to call more than once, and the sweeper also
// stops when ctx is cancelled.
//
// It starts nothing, and returns a no-op stop, when lr or leaseSvc is nil (a
// composition without leases).
func StartLocalLeaseExpiry(ctx context.Context, lr *lease.Lessor, leaseSvc service.LeaseServicer, logger *slog.Logger) (stop func()) {
	if lr == nil || leaseSvc == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, lease.DefaultExpiryInterval, alwaysSweeps, leaseSvc.Revoke, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}

// alwaysSweeps is the leader gate of a node that is the only replica.
func alwaysSweeps() bool { return true }
