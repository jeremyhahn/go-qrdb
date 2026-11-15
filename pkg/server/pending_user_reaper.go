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

package server

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// DefaultPendingUserSweepInterval is how often the pending-user reaper sweeps
// when no interval is configured.
const DefaultPendingUserSweepInterval = time.Minute

// WithPendingUserReaper configures the reaper that deletes pending users past
// their TTL. interval is the sweep period (zero means
// DefaultPendingUserSweepInterval, negative disables the reaper). isLeader
// gates each tenant's sweep so exactly one node deletes that tenant's pending
// users: the leader of the group that serves the tenant (see
// PendingSweepLeader). nil means this node leads every group (standalone).
func WithPendingUserReaper(interval time.Duration, isLeader func(ctx context.Context, tenantID string) bool) ServerOption {
	return func(o *serverOptions) {
		o.pendingSweepInterval = interval
		o.pendingSweepLeader = isLeader
	}
}

// PendingSweepLeader composes the per-tenant leader gate of the pending-user
// reaper. A tenant that lives in its home store (an isolated tenant) is swept
// only on the leader of its home group, whatever the system group's leader
// sees: the system leader may hold that tenant sealed while another node
// serves it. Every other tenant (system, shared) lives in the system KV group
// and is swept on that group's leader. A home lookup that fails sweeps
// nothing, so no node deletes from a store it cannot place.
//
// leadsSystem reports whether this node leads the system KV group (nil means
// it does). homes places tenants (nil means none is isolated). leadsHome
// reports whether this node leads tenantID's home group (nil means it never
// does).
func PendingSweepLeader(leadsSystem func() bool, homes tenant.HomeLocator, leadsHome func(tenantID string) bool) func(ctx context.Context, tenantID string) bool {
	return func(ctx context.Context, tenantID string) bool {
		if homes != nil {
			inHome, err := homes.InHome(ctx, tenantID)
			if err != nil {
				return false
			}
			if inHome {
				return leadsHome != nil && leadsHome(tenantID)
			}
		}
		return leadsSystem == nil || leadsSystem()
	}
}

// withPendingUserTicks replaces the reaper's ticker; tests drive sweeps
// through it instead of waiting on a clock.
func withPendingUserTicks(ticks <-chan time.Time, now func() time.Time) ServerOption {
	return func(o *serverOptions) {
		o.pendingSweepTicks = ticks
		o.pendingSweepNow = now
	}
}

// startPendingUserReaper starts the reaper over the served user service. It
// needs a user service that deletes conditionally on the pending state; any
// other is left unreaped (logged once).
func (s *Server) startPendingUserReaper() {
	if s.opts.userSvc == nil || s.opts.pendingSweepInterval < 0 {
		return
	}
	cfg := user.PendingReaperConfig{
		Users:    s.opts.userSvc,
		IsLeader: s.opts.pendingSweepLeader,
		Now:      s.opts.pendingSweepNow,
		Logger:   s.logger,
	}
	if s.opts.userAuthFactorSvc != nil {
		// Reaping without purging would leave a deleted account's factors
		// and credential indexes behind, so a factor service that cannot
		// purge disables the reaper instead.
		purger, ok := s.opts.userAuthFactorSvc.(user.FactorPurger)
		if !ok {
			s.logger.Warn("pending user reaper disabled", "error", "auth factor service cannot purge a user's factors")
			return
		}
		cfg.Factors = purger
	}
	reaper, err := user.NewPendingReaper(cfg)
	if err != nil {
		s.logger.Warn("pending user reaper disabled", "error", err)
		return
	}

	ticks := s.opts.pendingSweepTicks
	var ticker *time.Ticker
	if ticks == nil {
		interval := s.opts.pendingSweepInterval
		if interval == 0 {
			interval = DefaultPendingUserSweepInterval
		}
		ticker = time.NewTicker(interval)
		ticks = ticker.C
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.pendingReaperCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if ticker != nil {
			defer ticker.Stop()
		}
		reaper.Run(ctx, ticks, s.pendingSweepTenants)
	}()
}

// pendingSweepTenants lists the tenants a sweep covers: the system tenant and
// every registered tenant.
func (s *Server) pendingSweepTenants(ctx context.Context) ([]string, error) {
	ids := []string{tenantid.System}
	if s.opts.tenantSvc == nil {
		return ids, nil
	}
	infos, err := s.opts.tenantSvc.ListTenants(ctx)
	if err != nil {
		return ids, err
	}
	for _, info := range infos {
		if info.ID != "" && info.ID != tenantid.System {
			ids = append(ids, info.ID)
		}
	}
	return ids, nil
}

func (s *Server) stopPendingUserReaper() {
	if s.pendingReaperCancel != nil {
		s.pendingReaperCancel()
		s.pendingReaperCancel = nil
	}
}
