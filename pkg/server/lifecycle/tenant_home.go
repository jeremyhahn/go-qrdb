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
	"errors"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// defaultHomeReadyTimeout bounds how long EnsureHome waits for a new
	// home group to elect a leader and serve a read.
	defaultHomeReadyTimeout = 30 * time.Second
	// defaultHomeReadyInterval is the pause between EnsureHome's readiness
	// probes.
	defaultHomeReadyInterval = 100 * time.Millisecond
	// homeReadyProbeKey is the key EnsureHome reads to learn the home group
	// serves linearizable reads. It is never written.
	homeReadyProbeKey = "__home/ready"
)

// TenantHomeServices resolves the services of a tenant's dedicated Raft
// group, starting the local replica when this node holds one.
// *server.RaftBackendFactory implements it.
type TenantHomeServices interface {
	Services(ctx context.Context, tenantID, db string) (*server.DatabaseServices, error)

	// IsShardJoining reports whether this node's replica of shardID is still
	// in its join window: added after the group formed and not yet caught up.
	IsShardJoining(shardID uint64) bool

	// HomeGroup names the Raft propose target of tenantID's home group:
	// the gated host and the shard of its database.TenantHomeName database.
	HomeGroup(ctx context.Context, tenantID string) (service.Host, uint64, error)
}

// TenantHomeCatchingUpError is returned by HomeStore while this node's replica
// of a tenant's home group is catching up. A node is not in the home group's
// serving set until its replica has caught up: the request is refused here as
// a retryable unavailable and served by a node that is, never from a stale
// replica and never by proxying the tenant's records through this node.
type TenantHomeCatchingUpError struct {
	TenantID string
}

// Error implements error.
func (e *TenantHomeCatchingUpError) Error() string {
	return "lifecycle: tenant " + e.TenantID + " home replica on this node is catching up"
}

// TenantHomeProvisioner places a tenant's home group. *service.DatabaseService
// implements it.
type TenantHomeProvisioner interface {
	ProvisionTenantHome(ctx context.Context, tenantID string) (*placement.Placement, error)
}

// TenantHomeNotReadyError is returned by EnsureHome when a home group was
// placed and started but did not serve a read within the readiness bound.
type TenantHomeNotReadyError struct {
	TenantID string
	Err      error
}

// Error implements error.
func (e *TenantHomeNotReadyError) Error() string {
	msg := "lifecycle: tenant " + e.TenantID + " home group not ready"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the last readiness probe's error.
func (e *TenantHomeNotReadyError) Unwrap() error { return e.Err }

// TenantHomes is the raft-mode tenant.HomeStoreSource over isolated tenants'
// home groups: a dedicated Raft group per tenant, named
// database.TenantHomeName, replicated on every node, with its log and
// snapshots under the tenant's key. Every node resolves the home store from
// its own replica once that replica has caught up, which is what lets any
// node authenticate an isolated tenant's callers.
type TenantHomes struct {
	services    TenantHomeServices
	provisioner TenantHomeProvisioner

	readyTimeout  time.Duration
	readyInterval time.Duration

	// stores caches the adapter built over each tenant's current services
	// bundle, keyed by tenant ID; see HomeStore.
	stores sync.Map // map[string]*homeStoreEntry
}

// homeStoreEntry is one cached home store and the services bundle it was
// built over.
type homeStoreEntry struct {
	services *server.DatabaseServices
	store    kvstore.KVStore
}

// Compile-time interface check.
var _ tenant.HomeStoreSource = (*TenantHomes)(nil)

// NewTenantHomes returns the home-store source over services and provisioner.
func NewTenantHomes(services TenantHomeServices, provisioner TenantHomeProvisioner) *TenantHomes {
	return &TenantHomes{
		services:      services,
		provisioner:   provisioner,
		readyTimeout:  defaultHomeReadyTimeout,
		readyInterval: defaultHomeReadyInterval,
	}
}

// AttachTenantHomes attaches isolated tenants' home groups to n's store
// resolver, over the dedicated-raft services: the raft-mode home source. See
// attachHomeSource for the served resolvers. It reports false, attaching
// nothing, when n has no routing resolver or its database service cannot
// place a home group; isolated tenants then fail closed rather than resolving
// to the system shard.
func AttachTenantHomes(n *Node, services TenantHomeServices, served ...tenant.StoreResolver) bool {
	if services == nil {
		return false
	}
	provisioner, ok := n.Database.(TenantHomeProvisioner)
	if !ok {
		return false
	}
	if !attachHomeSource(n, NewTenantHomes(services, provisioner), served) {
		return false
	}
	// The home group's leader is the one node that completes a tenant's
	// pending first admin.
	if leader, ok := services.(databaseLeader); ok {
		n.homeLeader.Store(&databaseLeadership{leader})
	}
	// A node that becomes that leader completes an admin left pending by an
	// init or unseal another node served. The event arrives on the Raft
	// host's event dispatcher, which must not wait out a completion's retry
	// budget, so the completion runs as the node's own work and stops with
	// the node.
	if events, ok := services.(homeLeaderEvents); ok {
		if notifier, ok := n.Tenant.(homeLeadershipNotifier); ok {
			events.OnHomeLeaderReady(func(tenantID string) {
				n.work.start(func(ctx context.Context) {
					notifier.HomeLeadershipGained(ctx, tenantID)
				})
			})
		}
	}
	return true
}

// AttachLocalTenantHomes attaches the standalone home source to n's store
// resolver: each isolated tenant's home store is its own barrier's backend
// at its data root (tenant.LocalHomes). It is for a standalone node only; a
// clustered node attaches its home groups with AttachTenantHomes, since a
// node-local store would hold the records on one node alone. It reports
// false, attaching nothing, when n has no routing resolver or registry is
// nil.
//
// gate admits every home-store operation (tenant.WithHomeAdmission): it is
// the node's tenant-host registry, whose encryption migration fences and
// drains the home store it re-encrypts. A nil gate admits every operation,
// for a node that runs no migration.
func AttachLocalTenantHomes(n *Node, registry *seal.BarrierRegistry, gate tenant.HomeAdmission, served ...tenant.StoreResolver) bool {
	var opts []tenant.LocalHomesOption
	if gate != nil {
		opts = append(opts, tenant.WithHomeAdmission(gate))
	}
	homes, err := tenant.NewLocalHomes(registry, opts...)
	if err != nil {
		return false
	}
	return attachHomeSource(n, homes, served)
}

// attachHomeSource attaches source to n's store resolver and to every served
// resolver -- one a composer built beside n's to back the services it serves
// (the daemon's authentication path). Every resolver that can route an
// isolated tenant must reach the same home store: one left without it fails
// the tenant closed, so its callers could not sign in on a node that had just
// written their records. It reports false, attaching nothing, when n's
// resolver cannot route to a home store.
func attachHomeSource(n *Node, source tenant.HomeStoreSource, served []tenant.StoreResolver) bool {
	router, ok := n.StoreResolver.(tenant.HomeRouter)
	if !ok {
		return false
	}
	router.SetHomeStoreSource(source)
	for _, r := range served {
		if other, ok := r.(tenant.HomeRouter); ok {
			other.SetHomeStoreSource(source)
		}
	}
	return true
}

// HomeStore implements tenant.HomeStoreSource. It fails, and the resolver
// fails closed, where the tenant's barrier is sealed, this node holds no
// replica of the home group, or its replica is still catching up
// (TenantHomeCatchingUpError).
//
// The services bundle is cached by the factory and replaced when the shard is
// retired (a barrier recycle, a restore), so the adapter is rebuilt whenever
// the bundle it was built over is no longer the current one.
func (h *TenantHomes) HomeStore(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	svcs, err := h.services.Services(ctx, tenantID, database.TenantHomeName)
	if err != nil {
		return nil, err
	}
	if err := h.caughtUp(tenantID, svcs.ShardID); err != nil {
		return nil, err
	}
	if cached, ok := h.stores.Load(tenantID); ok {
		if entry, ok := cached.(*homeStoreEntry); ok && entry.services == svcs {
			return entry.store, nil
		}
	}
	store, err := NewGuardedKVStoreAdapter(svcs.KV, svcs.Index, svcs.Transaction)
	if err != nil {
		return nil, err
	}
	h.stores.Store(tenantID, &homeStoreEntry{services: svcs, store: store})
	return store, nil
}

// HomeGroup returns the Raft propose target of tenantID's home group, the
// group its audit trail and verifiable logs apply in. It refuses as
// HomeStore does: a sealed tenant through the services lookup, and a home
// replica still catching up with *TenantHomeCatchingUpError.
func (h *TenantHomes) HomeGroup(ctx context.Context, tenantID string) (service.Host, uint64, error) {
	host, shardID, err := h.services.HomeGroup(ctx, tenantID)
	if err != nil {
		return nil, 0, err
	}
	if err := h.caughtUp(tenantID, shardID); err != nil {
		return nil, 0, err
	}
	return host, shardID, nil
}

// caughtUp refuses a home whose local replica of shardID is still in its
// join window: reads there could miss writes the group already committed.
func (h *TenantHomes) caughtUp(tenantID string, shardID uint64) error {
	if h.services.IsShardJoining(shardID) {
		return &TenantHomeCatchingUpError{TenantID: tenantID}
	}
	return nil
}

// EnsureHome implements tenant.HomeStoreSource. It places the home group
// and, where the tenant's barrier is unsealed on this node, starts the local
// replica and waits until the group serves a read. A sealed barrier is not a
// failure: the replicas start when the tenant is unsealed.
func (h *TenantHomes) EnsureHome(ctx context.Context, tenantID string) error {
	if _, err := h.provisioner.ProvisionTenantHome(ctx, tenantID); err != nil {
		return err
	}
	store, err := h.HomeStore(ctx, tenantID)
	if errors.Is(err, seal.ErrSealed) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, h.readyTimeout)
	defer cancel()
	ticker := time.NewTicker(h.readyInterval)
	defer ticker.Stop()
	for {
		_, err := store.Get(ctx, homeReadyProbeKey)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return &TenantHomeNotReadyError{TenantID: tenantID, Err: err}
		case <-ticker.C:
		}
	}
}
