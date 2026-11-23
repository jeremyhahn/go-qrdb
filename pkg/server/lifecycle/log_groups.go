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
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// logGroups is the node's service.LogGroupSource: the barrier-sealed Raft
// home groups a barrier tenant's verifiable logs are applied in. The groups are the dedicated-raft services AttachTenantHomes
// attaches; until they are attached, the log fails closed with
// *service.BarrierStoreUnavailableError.
type logGroups struct {
	host     service.Host
	services atomic.Pointer[logGroupServices]

	// barriers is the tenant barrier registry of a standalone node, attached
	// by AttachLocalTenantHomes; see TenantLogStore.
	barriers atomic.Pointer[seal.BarrierRegistry]
	// gate admits each standalone log-store operation (AdmitTenantLog):
	// the home-store gate, attached with barriers.
	gate atomic.Pointer[logGate]
}

// logGate boxes the attached home-store admission gate.
type logGate struct {
	admission tenant.HomeAdmission
}

// logGroupServices boxes the attached dedicated-raft services.
type logGroupServices struct {
	services TenantHomeServices
}

var (
	_ service.LogGroupSource        = (*logGroups)(nil)
	_ service.LogBarrierStoreSource = (*logGroups)(nil)
	_ service.LogStoreAdmission     = (*logGroups)(nil)
)

// attach makes services the source of every barrier group.
func (g *logGroups) attach(services TenantHomeServices) {
	if g == nil || services == nil {
		return
	}
	g.services.Store(&logGroupServices{services: services})
}

// attachLocal makes registry the source of every barrier tenant's
// standalone log store (TenantLogStore): the tenant barrier's own store, at
// its data root. It is for a standalone node, which attaches no home groups.
// gate admits each log-store operation as it admits the home store's, so an
// encryption migration fences and drains both; nil admits every operation.
func (g *logGroups) attachLocal(registry *seal.BarrierRegistry, gate tenant.HomeAdmission) {
	if g == nil || registry == nil {
		return
	}
	if gate != nil {
		g.gate.Store(&logGate{admission: gate})
	}
	g.barriers.Store(registry)
}

// AdmitTenantLog implements service.LogStoreAdmission through the attached
// home-store gate; without one every operation is admitted.
func (g *logGroups) AdmitTenantLog(tenantID string) (func(), error) {
	box := g.gate.Load()
	if box == nil {
		return func() {}, nil
	}
	return box.admission.AdmitHome(tenantID)
}

// TenantLogStore implements service.LogBarrierStoreSource: the barrier the
// node's registry holds for tenantID now and the KV store over it, whose
// every value is encrypted under the tenant's key and every call refused
// with seal.ErrSealed while it is sealed. Home groups win: a node with home
// groups attached, or no registry, has no standalone store and refuses with
// *service.BarrierStoreUnavailableError.
func (g *logGroups) TenantLogStore(_ context.Context, tenantID string) (service.LogBarrier, kvstore.KVStore, error) {
	registry := g.barriers.Load()
	if registry == nil || g.services.Load() != nil {
		return nil, nil, &service.BarrierStoreUnavailableError{TenantID: tenantID}
	}
	tb, err := registry.GetTenant(tenantID)
	if err != nil {
		return nil, nil, err
	}
	kv, err := seal.NewTenantBarrierKVStore(tb)
	if err != nil {
		return nil, nil, err
	}
	return tb, kv, nil
}

// TenantHomeGroup implements service.LogGroupSource: tenantID's home group,
// whose log and snapshots are under the tenant's key. A sealed tenant barrier
// refuses with tenant.ErrTenantSealed, the error its other reads return.
func (g *logGroups) TenantHomeGroup(ctx context.Context, tenantID string) (service.Host, uint64, error) {
	box := g.services.Load()
	if box == nil {
		return nil, 0, &service.BarrierStoreUnavailableError{TenantID: tenantID}
	}
	svcs, err := box.services.Services(ctx, tenantID, database.TenantHomeName)
	if err != nil {
		if errors.Is(err, seal.ErrSealed) {
			return nil, 0, tenant.ErrTenantSealed
		}
		return nil, 0, err
	}
	if box.services.IsShardJoining(svcs.ShardID) {
		return nil, 0, &TenantHomeCatchingUpError{TenantID: tenantID}
	}
	return g.host, svcs.ShardID, nil
}
