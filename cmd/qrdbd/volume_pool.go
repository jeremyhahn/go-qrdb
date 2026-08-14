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
	"log/slog"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// volumePoolSubsystem is a cmd/qrdbd-local alias for the shared
// pool.VolumePoolSubsystem. start.go holds a *volumePoolSubsystem so that
// daemon-local shutdown code (PerTenantRegistry.CloseAll, Close) reads
// exactly as before.
type volumePoolSubsystem = qrdbpool.VolumePoolSubsystem

// initVolumePoolSubsystem builds the volume-pool Manager, StateStore, and
// PerTenantRegistry from cfg.Storage.VolumePools by delegating to the shared
// pool.BuildVolumePoolSubsystem builder. Returns nil,nil when no pools are
// configured — the daemon runs unchanged in that case and barrier-mode tenants
// must be registered with an explicit StorageBackend.
// The daemon always runs on a Raft host (the system shard is replicated even
// for a single node), so the volume pool is built in clustered mode: the
// round-robin cursor lives in the replicated system KV (bound in start.go once
// the data plane is up) and tenant→slot assignment is a single cluster-wide
// decision. Embedded standalone nodes (sdk/go/node NewStandaloneNode) build the
// pool node-local instead.
func initVolumePoolSubsystem(dataDir string, pools []qrdbconfig.VolumePoolConfig, logger *slog.Logger) (*volumePoolSubsystem, error) {
	return qrdbpool.BuildVolumePoolSubsystem(dataDir, pools, logger, true)
}

// buildPoolStrategy is a package-level shim for tests in package main that
// test the strategy-selection logic. It delegates to the shared
// pool.buildPoolStrategy (exported via the pool package internals test shim).
// The real implementation lives in pkg/storage/pool.
//
// Note: the pool package does not export buildPoolStrategy because it is an
// internal helper. Tests in cmd/qrdbd that previously tested it directly
// continue to work through this thin wrapper.
func buildPoolStrategy(name string, state *qrdbpool.PebbleStateStore) (objpool.Strategy, error) {
	// Preserve the nil-state contract: a typed-nil *PebbleStateStore wrapped in
	// the objpool.StateStore interface is a non-nil interface, which would
	// bypass BuildPoolStrategy's nil guard. Pass an untyped nil instead.
	if state == nil {
		return qrdbpool.BuildPoolStrategy(name, nil)
	}
	return qrdbpool.BuildPoolStrategy(name, state)
}

// buildLocalCandidates is a package-level shim for tests in package main that
// test the candidate-creation path. Delegates to pool.BuildLocalCandidates.
func buildLocalCandidates(poolName string, paths []string) ([]common.Storage, error) {
	return qrdbpool.BuildLocalCandidates(poolName, paths)
}
