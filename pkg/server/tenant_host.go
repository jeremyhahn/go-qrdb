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
	"log/slog"
	"path/filepath"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// tenantHostShardID is the single local Raft shard hosted by every per-tenant
// IsolationVolume Host. The tenant Host is a single-node, transport-less Raft
// cluster; all of the tenant's databases share this one shard and are isolated
// from one another by the composite (tenant, db) namespace prefix applied by
// the resolver, exactly as databases are isolated on the shared system shard.
const tenantHostShardID = shard.ShardKV

// tenantHost owns a single-node, transport-less per-tenant Raft Host backing an
// IsolationVolume tenant. The Host's WAL and state-machine data live entirely
// under the tenant's DataRoot ({DataRoot}/databases), and every Pebble file it
// writes is encrypted at rest under the tenant's own DEK via the tenant
// barrier. It exposes a full data-plane service set (KV/MVCC/lease/watch/
// timeseries/...) bound to the Host's single shard.
type tenantHost struct {
	tenantID string
	dataRoot string
	host     *quicraft.Host
	hub      *watch.Hub
	lessor   *lease.Lessor
	services *DatabaseServices
	cancel   context.CancelFunc
	logger   *slog.Logger
}

// newTenantHost builds and starts a per-tenant Raft Host rooted at the tenant's
// DataRoot, encrypting all on-disk state under the tenant barrier's DEK, and
// returns the host wrapper with its bound data-plane services.
//
// The tenant barrier (tb) MUST be unsealed before this is called: StartShard
// opens the encrypted Pebble immediately and a sealed barrier would fail every
// file open with seal.ErrSealed. The caller (TenantHostRegistry) drives the
// barrier through Initialize (create) or Unseal (boot re-attach) first.
func newTenantHost(tenantID, dataRoot string, tb *seal.TenantBarrier, logger *slog.Logger) (*tenantHost, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Per-tenant Raft host state lives under {DataRoot}/databases so that the
	// "barrier" sibling directory (the IAM Pebble) stays disjoint.
	hostRoot := filepath.Join(dataRoot, "databases")
	hostCfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(hostRoot, "wal"),
		NodeHostDir:   filepath.Join(hostRoot, "nodehost"),
		RaftAddress:   "127.0.0.1:63901", // unused: WithoutTransport disables network I/O
		ListenAddress: "127.0.0.1:63901",
		DeploymentID:  1,
	}

	// Encrypt the Raft WAL/LogDB under the tenant barrier's epoch DEK so the
	// dedicated volume is fully encrypted at rest: the state-machine Pebble is
	// wrapped by the encfs Encryptor below, and the replication log is wrapped
	// here. The underlying EpochBarrier satisfies quicraft.Barrier and must be
	// unsealed (the caller guarantees this).
	host, err := quicraft.NewHost(hostCfg,
		quicraft.WithoutTransport(),
		quicraft.WithBarrier(tb.Barrier().EpochBarrier()),
	)
	if err != nil {
		return nil, &TenantHostError{TenantID: tenantID, Op: "new-host", Err: err}
	}

	// Fresh per-tenant subsystems so leases and watches are isolated from the
	// system shard and from other tenants. The barrier-backed Encryptor wraps
	// every Pebble file the state machine creates with AES-256-GCM keyed by the
	// tenant DEK.
	lessor := lease.NewLessor()
	hub := watch.NewHub()
	createFn := newDataPlaneCreateFunc(tb, lessor, hub)

	shardCfg := quicraftconfig.Config{ShardID: tenantHostShardID, ReplicaID: 1}
	members := map[uint64]string{1: hostCfg.RaftAddress}
	if err := host.StartShard(members, false, createFn, shardCfg); err != nil {
		_ = host.Close()
		return nil, &TenantHostError{TenantID: tenantID, Op: "start-shard", Err: err}
	}

	// Wait for the single-node shard to elect itself leader before binding
	// services; until then proposals fail with "not leader". StartShard only
	// registers the replica — leadership is established asynchronously.
	if err := waitForTenantShardLeader(host); err != nil {
		_ = host.StopShard(tenantHostShardID)
		_ = host.Close()
		return nil, &TenantHostError{TenantID: tenantID, Op: "await-leader", Err: err}
	}

	adapter := raftbackend.New(host)
	ctx, cancel := context.WithCancel(context.Background())

	th := &tenantHost{
		tenantID: tenantID,
		dataRoot: dataRoot,
		host:     host,
		hub:      hub,
		lessor:   lessor,
		cancel:   cancel,
		logger:   logger,
		services: buildDataPlaneServices(adapter, tenantHostShardID, hub),
	}

	// Leader-side lease-expiry sweeper. The tenant host is a single-node
	// cluster, so it is always the leader; the sweeper revokes elapsed leases
	// through consensus, deleting attached keys and notifying watchers.
	if th.services.Lease != nil {
		go th.lessor.RunExpiry(ctx, lease.DefaultExpiryInterval,
			func() bool { return true },
			th.services.Lease.Revoke,
			logger,
		)
	}

	logger.Info("per-tenant volume host started",
		slog.String("tenant", tenantID),
		slog.String("data_root", dataRoot),
		slog.Uint64("shard_id", tenantHostShardID))

	return th, nil
}

// tenantShardLeaderTimeout bounds how long newTenantHost waits for the
// single-node tenant shard to elect a leader.
const tenantShardLeaderTimeout = 15 * time.Second

// waitForTenantShardLeader polls the host until the tenant shard reports a
// valid leader (the single local replica), or the timeout elapses.
func waitForTenantShardLeader(host *quicraft.Host) error {
	return waitForShardLeader(host, tenantHostShardID)
}

// waitForShardLeader polls the host until the given shard reports a valid
// leader (the single local replica), or the timeout elapses. It is shared by
// the per-tenant volume Host and the dedicated per-database raft shards.
func waitForShardLeader(host *quicraft.Host, shardID uint64) error {
	deadline := time.Now().Add(tenantShardLeaderTimeout)
	for {
		if _, ok, err := host.GetLeaderID(shardID); err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrTenantShardNoLeader
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// newDataPlaneCreateFunc builds the MVCC state-machine CreateFunc for a
// dedicated per-tenant shard. Every Pebble file the state machine creates is
// wrapped with AES-256-GCM keyed by the tenant DEK via the barrier-backed
// at-rest Encryptor, and the shard gets a fresh Lessor and watch Hub so leases
// and watches are isolated from the system shard and from other tenants. It is
// shared by the IsolationVolume per-tenant Host (tenant_host.go) and the
// IsolationRaft dedicated shards on the shared host (raft_backend_factory.go).
func newDataPlaneCreateFunc(tb *seal.TenantBarrier, lessor *lease.Lessor, hub *watch.Hub) sm.CreateFunc {
	enc := atrest.New(tb.Barrier())
	return store.NewMVCCCreateFuncWith(
		[]store.Option{store.WithEncryptor(enc)},
		store.WithLessor(lessor),
		store.WithWatchHub(hub),
		store.WithTimeSeriesEnabled(),
	)
}

// buildDataPlaneServices binds the full data-plane service set to a single
// shard on the given host and watch hub. It mirrors the core binding block of
// lifecycle.BuildServices for the system shard and is shared by the per-tenant
// volume Host and the dedicated per-database raft shards. Vector storage is
// intentionally omitted: it requires a separate per-tenant Pebble vector store
// rather than the Raft state machine, which is out of scope here.
func buildDataPlaneServices(host service.Host, shardID uint64, hub *watch.Hub) *DatabaseServices {
	kv := service.NewKVService(host, shardID)
	mvcc := service.NewMVCCKVService(host, shardID)
	idx := service.NewIndexService(host, shardID)
	txn := service.NewTransactionService(host, shardID)
	return &DatabaseServices{
		Name:        "",
		ShardID:     shardID,
		KV:          kv,
		MVCC:        mvcc,
		Batch:       service.NewBatchService(host, shardID),
		Transaction: txn,
		Lease:       service.NewLeaseService(host, shardID),
		TimeSeries:  service.NewTimeSeriesService(host, shardID),
		Index:       idx,
		Watch:       service.NewWatchService(hub),
		// SequenceServicer backs Collection's IDStrategyIncrement document
		// IDs with a Raft-replicated compare-and-swap counter instead of the
		// process-local counter that would otherwise restart at 0 on every
		// process restart and diverge across replicas -- see
		// service.CollectionService.WithSequenceService.
		Collection: service.NewCollectionServiceWithIndex(kv, idx).
			WithSequenceService(service.NewSequenceService(mvcc, txn)),
	}
}

// Close stops the tenant Host's lease sweeper, stops its shard, and closes the
// Host, releasing the Pebble lock on {DataRoot}/databases so a subsequent boot
// (or test re-open) can re-attach to the same on-disk location.
func (t *tenantHost) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	var firstErr error
	if err := t.host.StopShard(tenantHostShardID); err != nil {
		firstErr = &TenantHostError{TenantID: t.tenantID, Op: "stop-shard", Err: err}
	}
	if err := t.host.Close(); err != nil && firstErr == nil {
		firstErr = &TenantHostError{TenantID: t.tenantID, Op: "close-host", Err: err}
	}
	if firstErr == nil && t.logger != nil {
		t.logger.Info("per-tenant volume host closed", slog.String("tenant", t.tenantID))
	}
	return firstErr
}
