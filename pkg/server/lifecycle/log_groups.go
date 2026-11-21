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
}

// logGroupServices boxes the attached dedicated-raft services.
type logGroupServices struct {
	services TenantHomeServices
}

var _ service.LogGroupSource = (*logGroups)(nil)

// attach makes services the source of every barrier group.
func (g *logGroups) attach(services TenantHomeServices) {
	if g == nil || services == nil {
		return
	}
	g.services.Store(&logGroupServices{services: services})
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
