// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pool

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// VolumePoolSubsystem aggregates the resources needed to run barrier-mode
// tenants on dedicated per-tenant Pebble instances assigned via a volume pool.
// The subsystem owns lifecycle: the StateStore Pebble is closed when Close is
// invoked, and the per-tenant registry is returned to the caller so its
// CloseAll runs alongside the daemon or node shutdown handler.
type VolumePoolSubsystem struct {
	// Manager is the configured pool.Manager wrapped in pool.LocalOnly
	// to guarantee returned candidates implement common.PathAccessor.
	// Nil when no pools are configured.
	Manager objpool.Manager

	// StateStore persists the round-robin cursor across restarts.
	// Nil when no pools are configured.
	StateStore *PebbleStateStore

	// PerTenantRegistry tracks open per-tenant Pebble instances. Nil
	// when no pools are configured. CloseAll must be called from the
	// shutdown handler.
	PerTenantRegistry pebblestore.PerTenantRegistry

	// ReplicatedState is the cluster-wide round-robin cursor store. It is
	// non-nil only in clustered mode, where it backs the pool strategies in
	// place of the node-local PebbleStateStore so the cursor is a single
	// replicated value that survives leader failover. It is constructed
	// unbound; the caller binds it to the replicated system KV via Bind once
	// the data plane is available (see cmd/qrdbd/start.go). Nil in standalone
	// mode, where StateStore (node-local Pebble) backs the strategies.
	ReplicatedState *ReplicatedStateStore

	// DefaultPoolName is the name of the pool that
	// BarrierRegistry.RegisterTenant draws from when called with a nil
	// backend. Hardcoded to "default".
	DefaultPoolName string

	// stateStorage is the underlying Pebble that backs the StateStore.
	// Closed by Close.
	stateStorage *pebblestore.PebbleStorage
}

// Close releases the resources held by the volume-pool subsystem in reverse
// order of acquisition. The per-tenant registry is closed separately by the
// caller's shutdown handler so it can log progress alongside other tear-down
// steps.
func (v *VolumePoolSubsystem) Close() error {
	if v == nil || v.stateStorage == nil {
		return nil
	}
	return v.stateStorage.Close()
}

// BuildVolumePoolSubsystem builds the volume-pool Manager, StateStore, and
// PerTenantRegistry from the supplied pool configurations. It returns a nil
// subsystem (and nil error) when no pools are configured — callers run
// unchanged in that case and barrier-mode tenants must be registered with an
// explicit StorageBackend.
//
// dataDir is the node's data directory; the volume-pool state store opens a
// dedicated Pebble at <dataDir>/volumepool so the cursor lives independently
// of the system-tenant data plane.
//
// clustered selects where the round-robin cursor lives. When false (standalone
// / single-node), strategies use the node-local PebbleStateStore. When true
// (raft / clustered), strategies use a ReplicatedStateStore so the cursor — and
// therefore relative-slot assignment — is a single cluster-wide decision that
// survives leader failover. The replicated store is returned unbound in
// ReplicatedState; the caller binds it to the replicated system KV once the
// data plane is up. The node-local Pebble is still opened in clustered mode so
// StateStore stays a valid lifetime-pinned handle for diagnostics.
func BuildVolumePoolSubsystem(dataDir string, pools []qrdbconfig.VolumePoolConfig, logger *slog.Logger, clustered bool) (*VolumePoolSubsystem, error) {
	if len(pools) == 0 {
		return nil, nil
	}

	stateDir := filepath.Join(dataDir, "volumepool")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, &VolumePoolStorageError{Err: err}
	}

	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: stateDir})
	if err != nil {
		return nil, &VolumePoolStorageError{Err: err}
	}
	stateStore, err := NewPebbleStateStore(stateStorage)
	if err != nil {
		_ = stateStorage.Close()
		return nil, &VolumePoolStorageError{Err: err}
	}

	// Cursor backing store: node-local Pebble in standalone mode, replicated
	// system KV (late-bound) in clustered mode.
	var cursorStore objpool.StateStore = stateStore
	var replicatedState *ReplicatedStateStore
	if clustered {
		replicatedState = NewReplicatedStateStore()
		cursorStore = replicatedState
	}

	innerMgr := objpool.NewManager()
	for _, poolCfg := range pools {
		strategy, err := BuildPoolStrategy(poolCfg.Strategy, cursorStore)
		if err != nil {
			_ = stateStorage.Close()
			return nil, err
		}

		candidates, err := BuildLocalCandidates(poolCfg.Name, poolCfg.Paths)
		if err != nil {
			_ = stateStorage.Close()
			return nil, err
		}

		if err := innerMgr.Register(poolCfg.Name, candidates, strategy); err != nil {
			_ = stateStorage.Close()
			return nil, &VolumePoolRegisterError{Name: poolCfg.Name, Err: err}
		}

		if logger != nil {
			logger.Info("registered volume pool",
				slog.String("name", poolCfg.Name),
				slog.Int("backends", len(candidates)),
				slog.String("strategy", strategy.Name()))
		}
	}

	mgr := objpool.LocalOnly(innerMgr)
	perTenantReg := pebblestore.NewPerTenantRegistry(pebblestore.WithRegistryLogger(logger))

	return &VolumePoolSubsystem{
		Manager:           mgr,
		StateStore:        stateStore,
		ReplicatedState:   replicatedState,
		PerTenantRegistry: perTenantReg,
		DefaultPoolName:   "default",
		stateStorage:      stateStorage,
	}, nil
}

// BuildPoolStrategy constructs a pool.Strategy from the YAML strategy string.
// An empty string defaults to round_robin so configs that omit it get sensible
// behaviour. state is the cursor backing store: a *PebbleStateStore in
// standalone mode or a *ReplicatedStateStore in clustered mode (both satisfy
// objpool.StateStore).
func BuildPoolStrategy(name string, state objpool.StateStore) (objpool.Strategy, error) {
	if state == nil {
		return nil, &NilStateStoreError{}
	}
	switch name {
	case "", "round_robin":
		return &objpool.RoundRobinStrategy{State: state}, nil
	case "least_utilization":
		// Stub today — Pick returns ErrStrategyNotImplemented. Defining it
		// here lets operators reference the name in YAML without breaking
		// startup; they get a runtime error at first Pick instead of a
		// config-load failure.
		return &objpool.LeastUtilizationStrategy{}, nil
	default:
		return nil, &VolumePoolStrategyError{Strategy: name}
	}
}

// BuildLocalCandidates configures one local-filesystem Storage per supplied
// path. The path is created with 0700 permissions before objstore's Configure
// is called so the caller owns the volume root.
func BuildLocalCandidates(poolName string, paths []string) ([]common.Storage, error) {
	out := make([]common.Storage, 0, len(paths))
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, &VolumePoolPathError{Pool: poolName, Path: p, Err: err}
		}
		s := local.New()
		if err := s.Configure(map[string]string{"path": p}); err != nil {
			return nil, &VolumePoolPathError{Pool: poolName, Path: p, Err: err}
		}
		out = append(out, s)
	}
	return out, nil
}
