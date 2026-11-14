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
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// FactorPurger is the slice of the auth-factor service the reaper needs to
// remove an expired account's enrolled factors (service.UserAuthFactorServicer
// satisfies it).
type FactorPurger interface {
	// List returns every factor, active and revoked, of the user.
	List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error)

	// Delete removes a factor; force removes it even when it is the last.
	Delete(ctx context.Context, factorID string, force bool) error
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
// condition is re-checked by the store on the version it deletes
// (PendingExpirer), so an activation that lands between the listing and the
// delete wins.
type PendingReaper struct {
	users    UserServicer
	expirer  PendingExpirer
	factors  FactorPurger
	isLeader func() bool
	now      func() time.Time
	log      *slog.Logger
	pageSize int

	// orphans holds, per tenant, the factor ids of reaped accounts whose
	// removal failed; the next sweep of the tenant retries them.
	mu      sync.Mutex
	orphans map[string]map[string]uint64
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

// Sweep deletes the tenant's expired pending accounts and returns how many it
// removed. Off the leader it returns 0 and touches nothing. An account that
// fails to delete does not stop the sweep; the failures are joined into the
// returned error and the next sweep retries them.
func (r *PendingReaper) Sweep(ctx context.Context, tenantID string) (int, error) {
	if !r.isLeader() {
		return 0, nil
	}
	now := r.now().UTC()
	// Tenant-routed services (users, factors) resolve the tenant from ctx.
	ctx = tenant.WithTenantID(ctx, tenantID)

	// Factors a previous sweep failed to remove are retried first.
	var errs []error
	if err := r.retryOrphans(ctx, tenantID); err != nil {
		errs = append(errs, err)
	}

	// Collect first, delete after: deleting while paging would shift the
	// pages under the listing and skip accounts.
	var candidates []uint64
	for page := 1; ; page++ {
		resp, err := r.users.ListUsers(ctx, &ListUsersRequest{TenantID: tenantID, Page: page, PageSize: r.pageSize})
		if err != nil {
			return 0, errors.Join(append(errs, err)...)
		}
		for _, u := range resp.Users {
			if PendingExpired(u, now) {
				candidates = append(candidates, u.ID)
			}
		}
		if !resp.HasMore || len(resp.Users) == 0 {
			break
		}
	}

	reaped := 0
	for _, id := range candidates {
		if err := ctx.Err(); err != nil {
			return reaped, errors.Join(append(errs, err)...)
		}
		ok, err := r.reap(ctx, tenantID, id, now)
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

// reap deletes one account and its factors. It reports whether the account
// was deleted; an account that stopped being an expired pending one, or is
// already gone, is skipped without error.
func (r *PendingReaper) reap(ctx context.Context, tenantID string, id uint64, now time.Time) (bool, error) {
	// The factors are listed before the account goes, while the user id
	// still resolves, and removed after, so a refused delete leaves them.
	var factors []*auth.AuthFactor
	if r.factors != nil {
		var err error
		if factors, err = r.factors.List(ctx, id); err != nil {
			return false, err
		}
	}

	if _, err := r.expirer.DeleteExpiredPending(ctx, id, now); err != nil {
		var notPending *AccountNotPendingError
		var notFound *UserNotFoundError
		if errors.As(err, &notPending) || errors.As(err, &notFound) {
			return false, nil
		}
		return false, err
	}

	var errs []error
	for _, f := range factors {
		if err := r.deleteFactor(ctx, f.ID); err != nil {
			r.recordOrphan(tenantID, f.ID, id)
			errs = append(errs, err)
		}
	}
	return true, errors.Join(errs...)
}

// deleteFactor force-removes one factor; one already gone counts as removed,
// which is what makes the retry of an orphan idempotent.
func (r *PendingReaper) deleteFactor(ctx context.Context, factorID string) error {
	err := r.factors.Delete(ctx, factorID, true)
	var gone *auth.FactorNotFoundError
	if err == nil || errors.As(err, &gone) {
		return nil
	}
	return err
}

func (r *PendingReaper) recordOrphan(tenantID, factorID string, userID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.orphans == nil {
		r.orphans = make(map[string]map[string]uint64)
	}
	if r.orphans[tenantID] == nil {
		r.orphans[tenantID] = make(map[string]uint64)
	}
	r.orphans[tenantID][factorID] = userID
}

// Orphans returns how many factors of reaped accounts in the tenant still
// await removal.
func (r *PendingReaper) Orphans(tenantID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.orphans[tenantID])
}

// retryOrphans removes the factors a previous sweep of the tenant failed to
// remove, forgetting each one once it is gone.
func (r *PendingReaper) retryOrphans(ctx context.Context, tenantID string) error {
	r.mu.Lock()
	pending := make(map[string]uint64, len(r.orphans[tenantID]))
	for f, u := range r.orphans[tenantID] {
		pending[f] = u
	}
	r.mu.Unlock()

	var errs []error
	for factorID, userID := range pending {
		if err := r.deleteFactor(ctx, factorID); err != nil {
			r.log.WarnContext(ctx, "pending user reaper: orphaned factor removal failed",
				"tenant", tenantID, "user", userID, "factor", factorID, "error", err)
			errs = append(errs, err)
			continue
		}
		r.mu.Lock()
		delete(r.orphans[tenantID], factorID)
		if len(r.orphans[tenantID]) == 0 {
			delete(r.orphans, tenantID)
		}
		r.mu.Unlock()
	}
	return errors.Join(errs...)
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
