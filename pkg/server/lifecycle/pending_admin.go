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
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// pendingAdminCompletionTimeout bounds one completion of a pending tenant
// admin: the tenant's system roles, the admin's account and its certificate
// binding, each a replicated write.
const pendingAdminCompletionTimeout = 15 * time.Second

// completePendingTenantAdmin returns the tenant service's barrier-ready hook:
// it creates the first admin of a tenant onboarded before its barrier existed
// (bootstrap.CompletePendingTenantAdmin). The node's services are read when
// the hook runs, not when it is built: the tenant service is constructed
// before the pending-admin store and the user and role services it needs are
// final.
//
// A tenant whose identity records are still unreadable (its barrier sealed
// again, or a Shamir quorum not yet met) is not an error: the hook runs again
// on the next unseal or status read.
//
// The completion runs as the caller -- the role and user it creates are the
// caller's to create -- but not on the caller's clock: the barrier operation
// that raised the hook has already succeeded, and on a raft-isolated tenant it
// may have spent most of its request's deadline waiting for the tenant's
// groups to elect a leader. The replicated writes get their own
// pendingAdminCompletionTimeout instead.
func (n *Node) completePendingTenantAdmin(log *slog.Logger) tenant.BarrierReadyHook {
	return func(ctx context.Context, tenantID string) {
		if n.PendingAdmins == nil || n.User == nil || n.Role == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingAdminCompletionTimeout)
		defer cancel()
		done, err := bootstrap.CompletePendingTenantAdmin(ctx, tenantID, bootstrap.Dependencies{
			Users:         n.User,
			Roles:         n.Role,
			PendingAdmins: n.PendingAdmins,
			Logger:        log,
		})
		switch {
		case err == nil && done:
			log.Info("tenant admin account created from its onboarding record",
				slog.String("tenant", tenantID))
		case err == nil:
		case errors.Is(err, tenant.ErrTenantSealed):
			log.Debug("tenant admin completion deferred: identity records still sealed",
				slog.String("tenant", tenantID))
		default:
			log.Error("tenant admin completion failed; it is retried on the next unseal or status read",
				slog.String("tenant", tenantID), slog.Any("error", err))
		}
	}
}
