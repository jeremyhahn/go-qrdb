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

	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// completePendingTenantAdmin returns the barrier-ready hook that creates a
// tenant's first admin account from its pending onboarding record.
//
// The completion runs inside the init, unseal or status request that raised
// the hook, before that request answers, so the account exists when an init
// returns and the caller's next request finds it. A completion that fails
// fails a barrier initialization with its own error (tenant.BarrierReadyHook);
// an unseal or status read answers regardless and the next one retries it. A
// tenant whose identity records are still sealed is not a failure: the next
// unseal completes it. The completion runs as the caller -- the context keeps
// the caller's values -- but not on the caller's clock: the barrier operation
// may have spent most of its request's deadline waiting for a raft-isolated
// tenant's groups to elect a leader, so the completion has its own bounds.
//
// A Shamir barrier init asks for one attempt instead
// (tenant.AdminCompletionOnce), on the caller's context: it unseals only its
// own node, so the home group cannot elect a leader until holders have
// unsealed the others, and waiting would hold the init's holder certificates
// past the request that issued them. A home group this node does not lead,
// or identity records still sealed, then answer tenant.ErrTenantAdminPending:
// the record stays and the next unseal, quorum unseal or status read
// completes it.
//
// The completion writes only on the node leading the tenant's home group, so
// it runs once per tenant across the cluster. Elsewhere each attempt defers
// without writing, and the bounded retry keeps checking until the leader has
// removed the record or this node has become the leader, in which case it
// finishes what is still pending. Every write is guarded, so a completion
// that loses a race to another reports success.
func (n *Node) completePendingTenantAdmin(log *slog.Logger) tenant.BarrierReadyHook {
	return func(ctx context.Context, tenantID string, mode tenant.AdminCompletion) error {
		if (n.PendingAdmins == nil && n.PendingSOs == nil) || n.User == nil || n.Role == nil {
			return nil
		}
		deps := bootstrap.Dependencies{
			Users:         n.User,
			Roles:         n.Role,
			PendingAdmins: n.PendingAdmins,
			PendingSOs:    n.PendingSOs,
			Logger:        log,
			HomeLeader:    nodeHomeLeader{n: n},
		}
		if mode == tenant.AdminCompletionOnce {
			done, err := bootstrap.CompletePendingTenantAdminOnce(ctx, tenantID, deps, n.adminCompletionBounds)
			logAdminCompletion(log, tenantID, done, err)
			var deferred *bootstrap.AdminCompletionDeferredError
			if errors.Is(err, tenant.ErrTenantSealed) || errors.As(err, &deferred) {
				return tenant.ErrTenantAdminPending
			}
			return err
		}
		// The completion outlives the request that started it but not the
		// node: Stop cancels it.
		ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		defer context.AfterFunc(n.work.context(), cancel)()
		done, err := bootstrap.CompletePendingTenantAdminWithin(ctx, tenantID, deps, n.adminCompletionBounds)
		logAdminCompletion(log, tenantID, done, err)
		if errors.Is(err, tenant.ErrTenantSealed) {
			return nil
		}
		return err
	}
}

// AttachSOPINs gives tenant create the seam a tenant security officer's PIN
// is committed through (firstboot.SOPINSetter): the node's SO authenticator,
// built after the services. Until it is attached, and on a node with no
// tenant onboarding, a create that names an SO is refused.
func (n *Node) AttachSOPINs(pins firstboot.SOPINSetter) {
	if n.onboarding != nil && pins != nil {
		n.onboarding.AttachSOPINs(pins)
	}
}

// SOPINs is the SO PIN seam AttachSOPINs attached, or nil.
func (n *Node) SOPINs() firstboot.SOPINSetter {
	if n.onboarding == nil {
		return nil
	}
	return n.onboarding.SOPINs()
}

func logAdminCompletion(log *slog.Logger, tenantID string, done bool, err error) {
	var deferred *bootstrap.AdminCompletionDeferredError
	switch {
	case err == nil && done:
		log.Info("tenant admin account created from its onboarding record",
			slog.String("tenant", tenantID))
	case err == nil:
	case errors.Is(err, tenant.ErrTenantSealed):
		log.Debug("tenant admin completion deferred: identity records still sealed",
			slog.String("tenant", tenantID))
	case errors.As(err, &deferred):
		log.Debug("tenant admin completion left to the tenant home group's leader",
			slog.String("tenant", tenantID))
	case errors.Is(err, context.Canceled):
		log.Debug("tenant admin completion cancelled; it is retried on the next unseal or status read",
			slog.String("tenant", tenantID))
	default:
		log.Error("tenant admin completion failed; it is retried on the next unseal or status read",
			slog.String("tenant", tenantID), slog.Any("error", err))
	}
}

// databaseLeader reports whether this node leads a database's Raft group.
type databaseLeader interface {
	LeadsDatabase(tenantID, db string) bool
}

// homeLeaderEvents tells a handler each tenant whose home group this node has
// become ready to lead (server.RaftBackendFactory).
type homeLeaderEvents interface {
	OnHomeLeaderReady(fn func(tenantID string))
}

// homeLeadershipNotifier runs the barrier-ready hook for a tenant whose home
// group this node now leads (tenant.TenantService).
type homeLeadershipNotifier interface {
	HomeLeadershipGained(ctx context.Context, tenantID string)
}

var (
	_ homeLeaderEvents       = (*server.RaftBackendFactory)(nil)
	_ homeLeadershipNotifier = (*tenant.TenantService)(nil)
)

// databaseLeadership holds a databaseLeader for an atomic.Pointer.
type databaseLeadership struct{ databaseLeader }

// nodeHomeLeader is the bootstrap.HomeLeader of a node: the leader of the
// tenant's home group once AttachTenantHomes has given the node replicated
// homes, and every tenant's only writer until then.
type nodeHomeLeader struct{ n *Node }

func (l nodeHomeLeader) LeadsHome(tenantID string) bool {
	leadership := l.n.homeLeader.Load()
	return leadership == nil || leadership.LeadsDatabase(tenantID, database.TenantHomeName)
}

var _ bootstrap.HomeLeader = nodeHomeLeader{}
