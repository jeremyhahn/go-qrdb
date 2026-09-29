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

package service

import (
	"context"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// WarnBarriersOffDataPath logs database.BarrierOffDataPathWarning once for
// every barrier-enabled database whose owning tenant serves it from the
// tenant's own store -- a volume- or raft-isolated tenant -- rather than from
// the per-database store the barrier wraps.
//
// database.Manager.Open warns for the records it can classify alone (a
// backend: raft database), but it cannot see tenant isolation, and
// CreateForTenantWithCredentials refuses such a barrier only for new
// databases. A record written before that refusal opens sealed and fails
// closed; once unsealed it is unprotected. The composition root calls this
// once the tenant service exists, so the operator is told at boot.
//
// A database with no owning tenant, the system tenant's, or one whose tenant
// cannot be read is skipped: its isolation is unknown here, and a lookup
// failure is the tenant service's to report.
func WarnBarriersOffDataPath(ctx context.Context, dbs []database.DatabaseInfo, tenants TenantInfoLookup, logger *slog.Logger) {
	if tenants == nil || logger == nil {
		return
	}
	for _, db := range dbs {
		if db.Barrier == nil || !db.Barrier.Enabled || db.TenantID == "" || tenantid.IsSystem(db.TenantID) {
			continue
		}
		info, err := tenants.GetTenant(ctx, db.TenantID)
		if err != nil || info == nil {
			continue
		}
		servedBy, offPath := isolationBarrierDataPath[info.EffectiveIsolationType()]
		if !offPath {
			continue
		}
		logger.Warn(database.BarrierOffDataPathWarning,
			slog.String("name", db.Name),
			slog.String("tenant", db.TenantID),
			slog.String("served_by", servedBy))
	}
}
