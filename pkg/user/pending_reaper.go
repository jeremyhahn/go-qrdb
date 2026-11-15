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

package user

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// FactorPurger is the slice of the auth-factor service the reaper needs to
// remove a claimed account's enrolled factors (service.UserAuthFactorPurger
// satisfies it).
type FactorPurger interface {
	// Purge seals the user's factor set against new enrollments and removes
	// every factor of the user, active and revoked, with its credential
	// indexes. It is idempotent: a purge that fails part way is completed by
	// the next call. It returns how many factors it removed.
	Purge(ctx context.Context, userID uint64) (int, error)
}

// PendingReaperConfig configures a PendingReaper.
type PendingReaperConfig struct {
	// Users lists a tenant's accounts and must also implement
	// PendingExpirer, which is what the reaper deletes through.
	Users UserServicer

	// Factors, when set, removes each reaped account's enrolled factors.
	Factors FactorPurger

	// IsLeader reports whether this node may reap. A sweep on a node that is
	// not the leader does nothing, so exactly one node of a Raft group
	// deletes. Nil means always (a standalone node is its own leader).
	IsLeader func() bool

	// Now is the clock expiry is judged against. Nil means time.Now.
	Now func() time.Time

	// Logger receives per-account failures. Nil means slog.Default.
	Logger *slog.Logger

	// PageSize is the ListUsers page size. Zero or negative means 100.
	PageSize int
}

// PendingReaper deletes pending accounts whose TTL has run out
// (PendingExpired). It never deletes an account in any other state: the
// condition is re-checked by the store on the version it claims
// (PendingExpirer), so an activation that lands between the listing and the
// claim wins.
//
// Each account is reaped in three steps, claim, purge, delete:
//
//  1. ClaimExpiredPending durably marks the account deleting. From here it
//     cannot authenticate, be activated or enrol a factor.
//  2. FactorPurger.Purge seals the factor set and removes every factor.
//  3. DeleteExpiredPending removes the account.
//
// A failure after the claim leaves the account claimed in the store, not in
// this process: every later sweep, by this reaper or a fresh one on a new
// leader, lists it (ExpiryClaimed) and resumes at step 2.
type PendingReaper struct {
	users    UserServicer
	expirer  PendingExpirer
	factors  FactorPurger
	isLeader func() bool
	now      func() time.Time
	log      *slog.Logger
	pageSize int
}

// NewPendingReaper builds a reaper. It refuses a user service that cannot
// delete conditionally on the pending state.
func NewPendingReaper(cfg PendingReaperConfig) (*PendingReaper, error) {
	if cfg.Users == nil {
		return nil, &ValidationError{Field: "users", Reason: "must not be nil"}
	}
	expirer, ok := cfg.Users.(PendingExpirer)
	if !ok {
		return nil, ErrPendingExpiryUnsupported
	}
	r := &PendingReaper{
		users:    cfg.Users,
		expirer:  expirer,
		factors:  cfg.Factors,
		isLeader: cfg.IsLeader,
		now:      cfg.Now,
		log:      cfg.Logger,
		pageSize: cfg.PageSize,
	}
	if r.isLeader == nil {
		r.isLeader = func() bool { return true }
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.pageSize <= 0 {
		r.pageSize = 100
	}
	return r, nil
}

// Sweep deletes the tenant's expired pending accounts, and finishes those an
// earlier sweep claimed but did not delete, and returns how many it removed.
// Off the leader it returns 0 and touches nothing. An account that fails to
// delete does not stop the sweep; the failures are joined into the returned
// error and the next sweep resumes them.
func (r *PendingReaper) Sweep(ctx context.Context, tenantID string) (int, error) {
	if !r.isLeader() {
		return 0, nil
	}
	now := r.now().UTC()
	// Tenant-routed services (users, factors) resolve the tenant from ctx.
	ctx = tenant.WithTenantID(ctx, tenantID)

	// Collect first, delete after: deleting while paging would shift the
	// pages under the listing and skip accounts.
	var candidates []uint64
	for page := 1; ; page++ {
		resp, err := r.users.ListUsers(ctx, &ListUsersRequest{TenantID: tenantID, Page: page, PageSize: r.pageSize})
		if err != nil {
			return 0, err
		}
		for _, u := range resp.Users {
			if PendingReapable(u, now) {
				candidates = append(candidates, u.ID)
			}
		}
		if !resp.HasMore || len(resp.Users) == 0 {
			break
		}
	}
	// A claimed account whose delete stopped after the tenant index went is
	// no longer listed; its reap marker still names it.
	claimed, err := r.expirer.ListExpiryClaims(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	listed := make(map[uint64]bool, len(candidates))
	for _, id := range candidates {
		listed[id] = true
	}
	for _, id := range claimed {
		if !listed[id] {
			listed[id] = true
			candidates = append(candidates, id)
		}
	}

	var errs []error
	reaped := 0
	for _, id := range candidates {
		if err := ctx.Err(); err != nil {
			return reaped, errors.Join(append(errs, err)...)
		}
		ok, err := r.reap(ctx, id, now)
		if err != nil {
			r.log.WarnContext(ctx, "pending user reap failed", "tenant", tenantID, "user", id, "error", err)
			errs = append(errs, err)
		}
		if ok {
			reaped++
		}
	}
	return reaped, errors.Join(errs...)
}

// reap claims one account, purges its factors and deletes it. It reports
// whether the account was deleted; an account that stopped being an expired
// pending one before the claim, or is already gone, is skipped without
// error. A failure after the claim leaves the account claimed, for the next
// sweep to resume.
func (r *PendingReaper) reap(ctx context.Context, id uint64, now time.Time) (bool, error) {
	if _, err := r.expirer.ClaimExpiredPending(ctx, id, now); err != nil {
		var notPending *AccountNotPendingError
		var notFound *UserNotFoundError
		if errors.As(err, &notPending) || errors.As(err, &notFound) {
			return false, nil
		}
		return false, err
	}
	if r.factors != nil {
		if _, err := r.factors.Purge(ctx, id); err != nil {
			return false, err
		}
	}
	if _, err := r.expirer.DeleteExpiredPending(ctx, id, now); err != nil {
		var notFound *UserNotFoundError
		if errors.As(err, &notFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Run sweeps every tenant tenants returns on each tick until ctx ends. The
// caller owns the ticker (time.NewTicker(interval).C in production, a
// hand-driven channel in tests). A failed sweep is logged and retried on the
// next tick.
func (r *PendingReaper) Run(ctx context.Context, ticks <-chan time.Time, tenants func(context.Context) ([]string, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			if !r.isLeader() {
				continue
			}
			ids, err := tenants(ctx)
			if err != nil {
				r.log.WarnContext(ctx, "pending user reaper: list tenants failed", "error", err)
				continue
			}
			for _, t := range ids {
				if _, err := r.Sweep(ctx, t); err != nil {
					r.log.WarnContext(ctx, "pending user reaper: sweep failed", "tenant", t, "error", err)
				}
			}
		}
	}
}
