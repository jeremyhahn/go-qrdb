// Copyright 2026 Jeremy Hahn
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

package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// RoutingUserAuthFactorServiceConfig holds the dependencies for the routed
// user auth-factor service.
type RoutingUserAuthFactorServiceConfig struct {
	// Resolver maps a tenant ID to the KVStore backing that tenant's IAM
	// data. Required.
	Resolver tenant.StoreResolver

	// UserStateUpdater is threaded into every per-tenant service; see
	// UserAuthFactorServiceConfig. May be nil.
	UserStateUpdater UserStateUpdater

	// AuditLogger is threaded into every per-tenant service; see
	// UserAuthFactorServiceConfig. May be nil.
	AuditLogger audit.AuditLogger

	// LegacyStore is the UNCONFINED root store holding records written
	// before factor storage was tenant-routed. When set, the service
	// migrates them into their owning tenants before it serves anything.
	// Nil skips the migration, which is correct only where no such records
	// can exist.
	LegacyStore kvstore.KVStore

	// Logger receives the migration's outcome, including the ERROR line
	// naming each refused record. Defaults to slog.Default().
	Logger *slog.Logger
}

// routingUserAuthFactorService is a UserAuthFactorServicer decorator that
// routes each call to the per-tenant userAuthFactorService backed by the
// KVStore the StoreResolver returns for the request's tenant.
//
// It is the same shape as pkg/user's routingUserService, over the same
// resolver, because auth factors are the same class of data those services
// hold: per-tenant IAM. For a logical-prefix (or system) tenant the resolver
// returns the shared system KVStore CONFINED to "{tenant}/", so a factor's
// records live at "{tenant}/__sys:auth/...". For a dedicated-isolation tenant
// it returns a store backed by that tenant's own TenantBarrier, so the factor
// record — which carries the credential lookup material — is encrypted with
// the tenant's DEK.
//
// SECURITY. This is the whole tenant dimension of factor resolution
// (go-qrdb#393). The lookup key is "__sys:auth/lookup/{type}/{credKeyHex}"
// and carries no tenant of its own, so two tenants presenting the same
// (type, credential) address the same record in whatever store they are
// pointed at. Pointing them at DIFFERENT stores is what separates them; there
// is no second check downstream to catch a call that skips this one. When a
// barrier tenant's barrier is sealed the resolver returns
// tenant.ErrTenantSealed and the routed call fails closed — there is no
// fallback to the shared store.
//
// ROUTING FOLLOWS THE ID THE CALL NAMES, which is the same rule pkg/user
// applies and it lands differently on the two kinds of id this interface
// carries:
//
//   - A USER id is a globally unique handle whose record lives in exactly one
//     tenant, so Add, List and HasActiveFactor resolve it through
//     tenant.IdentityIndex. Enrolment therefore lands in the tenant that owns
//     the user rather than wherever the enrolling context happened to point,
//     which is what keeps an enrolment written by an out-of-repo composer
//     findable by the login that follows it.
//   - A FACTOR id is issued inside a tenant at enrolment and only ever travels
//     back to the tenant that enrolled it. There is no index for it and none is
//     wanted: Get, Remove, Rotate, Relabel and MarkUsed route on the CONTEXT
//     tenant, so a caller presenting another tenant's factor id gets
//     auth.FactorNotFoundError from its own store, which is the answer that
//     preserves the boundary.
//
// LookupByCredentialKey names neither. It routes on the context — the tenant
// the login was begun under — and that is the whole isolation boundary this
// type exists for.
//
// Per-tenant services are cached in a sync.Map keyed by the resolved KVStore
// identity, so repeated calls for one tenant reuse one service instance —
// which also preserves the process-local mutex that makes Add, Rotate and
// Remove atomic against each other WITHIN a tenant.
type routingUserAuthFactorService struct {
	resolver tenant.StoreResolver
	identity tenant.IdentityIndex
	users    UserStateUpdater
	audit    audit.AuditLogger
	services sync.Map // map[kvstore.KVStore]*userAuthFactorService

	// legacy, logger, migrated and migrateMu carry the one-shot migration of
	// pre-routing records; see ensureMigrated.
	legacy    kvstore.KVStore
	logger    *slog.Logger
	migrated  atomic.Bool
	migrateMu sync.Mutex
}

// Compile-time assertions. The router must satisfy every interface the
// per-tenant service does: consumers reach ActiveFactorChecker and
// AttestationRecorder by type assertion on a UserAuthFactorServicer, and an
// assertion that silently stopped matching would turn a fail-closed security
// gate into a skipped one at the call site rather than here.
var (
	_ UserAuthFactorServicer = (*routingUserAuthFactorService)(nil)
	_ ActiveFactorChecker    = (*routingUserAuthFactorService)(nil)
	_ AttestationRecorder    = (*routingUserAuthFactorService)(nil)
	_ auth.FactorStore       = (*routingUserAuthFactorService)(nil)
)

// NewRoutingUserAuthFactorService constructs the tenant-routed M14 user
// auth-factor service. Resolver must be non-nil.
//
// When cfg.LegacyStore is set, the service migrates pre-routing records into
// their owning tenants before it serves anything — see ensureMigrated for why
// that happens at first use rather than here.
func NewRoutingUserAuthFactorService(cfg RoutingUserAuthFactorServiceConfig) (UserAuthFactorServicer, error) {
	if cfg.Resolver == nil {
		return nil, &auth.InvalidFactorError{Reason: "store resolver is required"}
	}
	index, err := tenant.NewResolvedIdentityIndex(cfg.Resolver)
	if err != nil {
		return nil, &auth.InvalidFactorError{Reason: "identity index: " + err.Error()}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &routingUserAuthFactorService{
		resolver: cfg.Resolver,
		identity: index,
		users:    cfg.UserStateUpdater,
		audit:    cfg.AuditLogger,
		legacy:   cfg.LegacyStore,
		logger:   logger,
	}, nil
}

// ensureMigrated runs the legacy-record migration exactly once, before this
// service answers anything.
//
// It is LAZY, and that is a property of where the store lives rather than a
// preference. The records are read through Raft, and a composition root builds
// its services while the shard is still electing: a linearizable read at
// construction time fails with "not leader, leader unknown" on a node that is
// otherwise perfectly healthy. Running at first use puts the read at a moment
// the store can serve it.
//
// Doing it here, rather than in a second call a composition root makes, is what
// makes the ORDER unrepresentable. Migrating after the surface is already
// serving would strand every already-enrolled credential for as long as the gap
// lasted, and the resulting failure looks like a bad credential rather than a
// migration that has not run yet. No operation on this service can outrun it.
//
// Three outcomes, and only one of them is done:
//
//   - A STORE failure does not consume the attempt and IS returned. The flag
//     stays clear, so a caller that hit an unavailable shard is told so and the
//     next call tries again.
//   - A TERMINAL refusal — unindexed owner, absent owner, unreadable record —
//     consumes the attempt and is NOT returned. Every placeable record was
//     placed, the rest are named at ERROR, and re-running would only re-refuse
//     them.
//   - A TRANSIENT refusal — the owning tenant's store is not reachable from
//     here yet — does NOT consume the attempt and is NOT returned either. The
//     service serves normally, because every other tenant's records were
//     placed and none of this is their business, and the next call re-runs over
//     whatever is left in the legacy keyspace. That keyspace shrinks to the
//     refused records alone, so the repeat is a prefix scan over a handful of
//     keys rather than the original migration. It is not free: the repeat runs
//     under migrateMu, so factor calls serialise behind it for as long as a
//     tenant stays unreachable. That is the price of the resumability promised
//     above, and it is bounded by the residue rather than by the original
//     record count.
//
// The last case is the one that matters most and it is not hypothetical: a
// self-managed barrier tenant is registered SEALED at creation and a node that
// does not host a tenant's barrier has none until key delivery arrives. Marking
// the migration done through either would strand those records for the life of
// the process, and the docs above promise a resumable migration.
func (r *routingUserAuthFactorService) ensureMigrated(ctx context.Context) error {
	if r.legacy == nil || r.migrated.Load() {
		return nil
	}
	r.migrateMu.Lock()
	defer r.migrateMu.Unlock()
	if r.migrated.Load() {
		return nil
	}
	report, err := MigrateFactorsToTenants(ctx, r.legacy, r.resolver, r.identity, r.logger)
	if err != nil {
		var refused *FactorMigrationRefusedError
		if !errors.As(err, &refused) {
			return err
		}
		if report != nil && (report.Migrated > 0 || report.Pruned > 0) {
			r.logReport(report)
		}
		if refused.Transient() {
			r.logger.Warn("auth-factor migration could not reach every owning tenant; the records it could not place stay where they are and it will run again",
				slog.Int("refused", len(refused.Refusals)))
			return nil
		}
		r.logger.Error("auth-factor migration left records behind; they resolve for nobody until an operator places or removes them",
			slog.Int("refused", len(refused.Refusals)))
		r.migrated.Store(true)
		return nil
	}
	if report != nil && (report.Migrated > 0 || report.Pruned > 0) {
		r.logReport(report)
	}
	r.migrated.Store(true)
	return nil
}

// logReport records what one run placed.
func (r *routingUserAuthFactorService) logReport(report *FactorMigrationReport) {
	r.logger.Info("auth-factor records migrated into their owning tenants",
		slog.Int("migrated", report.Migrated), slog.Int("pruned", report.Pruned))
}

// serviceForTenant returns the per-tenant service for an explicitly named
// tenant, bypassing the context.
func (r *routingUserAuthFactorService) serviceForTenant(ctx context.Context, tenantID string) (*userAuthFactorService, error) {
	if err := r.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	kv, err := r.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return r.serviceForKV(kv)
}

// serviceForOwnerRead returns the per-tenant service holding the factors of
// userID, for a READ.
//
// tenant.OwningTenant is the shared rule and it is deliberately forgiving: an
// unindexed id, or one the caller may not reach, routes to the CALLER's own
// tenant, where the read finds nothing. That is the same answer pkg/user's
// by-id reads give and it discloses nothing about where the id actually lives.
func (r *routingUserAuthFactorService) serviceForOwnerRead(ctx context.Context, userID uint64, op string) (*userAuthFactorService, error) {
	// The index lookup below reads through the resolver too, so the
	// migration must be settled before it, not only before the store call it
	// selects.
	if err := r.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	if userID == 0 {
		// Zero is not a user id. Route to the caller's own tenant so the
		// store answers with the InvalidFactorError its callers expect,
		// rather than an index error describing the lookup.
		return r.serviceFor(ctx)
	}
	target, err := tenant.OwningTenant(ctx, r.identity, tenant.IdentityUser, tenant.IdentityID(userID), op)
	if err != nil {
		return nil, err
	}
	return r.serviceForTenant(ctx, target)
}

// serviceForOwnerWrite returns the per-tenant service that must hold a factor
// being enrolled for userID.
//
// It differs from the read path in ONE way, and the difference is the point: a
// caller that may not reach the owning tenant is REFUSED rather than routed to
// its own. A write routed to the caller's tenant would bind a credential of
// the caller's choosing to another tenant's user id, and the only thing
// standing between that record and an authenticated session is the user lookup
// downstream refusing the same cross-tenant read. That is a boundary held by
// two independent components agreeing; this holds it here, where the write is.
func (r *routingUserAuthFactorService) serviceForOwnerWrite(ctx context.Context, userID uint64, op string) (*userAuthFactorService, error) {
	if err := r.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	if userID == 0 {
		return r.serviceFor(ctx)
	}
	claim, ok, err := r.identity.Lookup(ctx, tenant.IdentityUser, tenant.IdentityID(userID))
	if err != nil {
		return nil, err
	}
	if !ok {
		// An unindexed user id places nothing: a single-tenant composition
		// records no claims, and there the caller's tenant IS the record's
		// tenant.
		return r.serviceFor(ctx)
	}
	target, err := tenant.EffectiveTenant(ctx, claim.Tenant, op)
	if err != nil {
		return nil, err
	}
	return r.serviceForTenant(ctx, target)
}

// serviceFor resolves the caller's tenant store and returns the per-tenant
// service bound to it.
func (r *routingUserAuthFactorService) serviceFor(ctx context.Context) (*userAuthFactorService, error) {
	if err := r.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	kv, err := r.resolver.Resolve(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return r.serviceForKV(kv)
}

// serviceForKV builds (or returns a cached) per-tenant service for a resolved
// store, caching by store identity exactly as routingUserService does.
//
// A store whose dynamic type is NOT comparable skips the cache: kvstore.KVStore
// is an interface and nothing requires its implementations to be comparable,
// so using such a value as a sync.Map key panics before any factor operation
// runs. The cache is an optimization — but note that skipping it also drops
// the shared mutex, so an uncacheable store serialises mutations only within
// one call. Every store this repository resolves is comparable; the branch
// exists so a caller's representation cannot crash the process.
func (r *routingUserAuthFactorService) serviceForKV(kv kvstore.KVStore) (*userAuthFactorService, error) {
	cfg := UserAuthFactorServiceConfig{KV: kv, UserStateUpdater: r.users, AuditLogger: r.audit}
	if !kvstore.UsableAsMapKey(kv) {
		return newUserAuthFactorService(cfg)
	}
	if cached, ok := r.services.Load(kv); ok {
		return cached.(*userAuthFactorService), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	svc, err := newUserAuthFactorService(cfg)
	if err != nil {
		return nil, err
	}
	actual, _ := r.services.LoadOrStore(kv, svc)
	return actual.(*userAuthFactorService), nil //nolint:errcheck // type assertion on a value this container only ever holds
}

// Add routes to the tenant that OWNS the user, refusing a caller that may not
// reach it. See serviceForOwnerWrite.
func (r *routingUserAuthFactorService) Add(ctx context.Context, userID uint64, factor *auth.AuthFactor) error {
	svc, err := r.serviceForOwnerWrite(ctx, userID, "enroll auth factor")
	if err != nil {
		return err
	}
	return svc.Add(ctx, userID, factor)
}

// AddAttested implements AttestationRecorder, routed like Add.
func (r *routingUserAuthFactorService) AddAttested(ctx context.Context, userID uint64, factor *auth.AuthFactor, attestation auth.VerifiedAttestation) error {
	svc, err := r.serviceForOwnerWrite(ctx, userID, "enroll attested auth factor")
	if err != nil {
		return err
	}
	return svc.AddAttested(ctx, userID, factor, attestation)
}

// AddConditional routes on the tenant that owns the user, exactly as Add
// does: the factor set the condition is decided on lives beside the factors
// it counts.
func (r *routingUserAuthFactorService) AddConditional(ctx context.Context, userID uint64, factor *auth.AuthFactor, cond auth.FactorAddCondition) (uint64, error) {
	svc, err := r.serviceForOwnerWrite(ctx, userID, "conditionally enroll auth factor")
	if err != nil {
		return 0, err
	}
	return svc.AddConditional(ctx, userID, factor, cond)
}

// FactorSetVersion routes on the tenant that owns the user, exactly as List
// does.
func (r *routingUserAuthFactorService) FactorSetVersion(ctx context.Context, userID uint64) (uint64, error) {
	svc, err := r.serviceForOwnerRead(ctx, userID, "read auth factor set version")
	if err != nil {
		return 0, err
	}
	return svc.FactorSetVersion(ctx, userID)
}

// List routes to the tenant that owns the user.
func (r *routingUserAuthFactorService) List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	svc, err := r.serviceForOwnerRead(ctx, userID, "list auth factors")
	if err != nil {
		return nil, err
	}
	return svc.List(ctx, userID)
}

// Get routes to the caller's tenant. A factor id belonging to another tenant
// is not found here, which is the boundary rather than a shortcoming.
func (r *routingUserAuthFactorService) Get(ctx context.Context, factorID string) (*auth.AuthFactor, error) {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return nil, err
	}
	return svc.Get(ctx, factorID)
}

// LookupByCredentialKey routes to the tenant the LOGIN was begun under. This
// is the resolution go-qrdb#393 exists to scope: the credential alone says
// nothing about which tenant is presenting it.
func (r *routingUserAuthFactorService) LookupByCredentialKey(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return nil, err
	}
	return svc.LookupByCredentialKey(ctx, t, key)
}

// Remove routes to the caller's tenant.
func (r *routingUserAuthFactorService) Remove(ctx context.Context, factorID string, force bool) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.Remove(ctx, factorID, force)
}

// Delete routes to the caller's tenant, exactly as Remove does.
func (r *routingUserAuthFactorService) Delete(ctx context.Context, factorID string, force bool) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.Delete(ctx, factorID, force)
}

// Rotate routes to the caller's tenant. Both the old and the new factor live
// in that one store, so the rotation cannot straddle a tenant boundary.
func (r *routingUserAuthFactorService) Rotate(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.Rotate(ctx, oldID, newFactor)
}

// RotateAttested implements AttestationRecorder, routed to the caller's tenant.
func (r *routingUserAuthFactorService) RotateAttested(ctx context.Context, oldID string, newFactor *auth.AuthFactor, attestation auth.VerifiedAttestation) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.RotateAttested(ctx, oldID, newFactor, attestation)
}

// Relabel routes to the caller's tenant.
func (r *routingUserAuthFactorService) Relabel(ctx context.Context, factorID string, label string) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.Relabel(ctx, factorID, label)
}

// MarkUsed routes to the caller's tenant.
//
// The canonical caller is auth.LoginResolver, which performs this update on a
// DETACHED context so a returning login response cannot cancel it. That
// context is derived with context.WithoutCancel, which keeps the tenant
// binding: derived with context.Background() it would carry none, resolve the
// system tenant, and silently stop recording LastUsedAt for every other
// tenant.
func (r *routingUserAuthFactorService) MarkUsed(ctx context.Context, factorID string, ts time.Time) error {
	svc, err := r.serviceFor(ctx)
	if err != nil {
		return err
	}
	return svc.MarkUsed(ctx, factorID, ts)
}

// HasActiveFactor implements ActiveFactorChecker, routed to the tenant that
// owns the user. It stays fail-closed through the router: a resolver error — a sealed
// barrier above all — is returned, never folded into "no active factor",
// which a security gate would read as a reason to proceed.
func (r *routingUserAuthFactorService) HasActiveFactor(ctx context.Context, userID uint64) (bool, error) {
	svc, err := r.serviceForOwnerRead(ctx, userID, "check active auth factor")
	if err != nil {
		return false, err
	}
	return svc.HasActiveFactor(ctx, userID)
}
