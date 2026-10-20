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

package tenant

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// StoreResolver maps a tenant ID to the kvstore.KVStore that backs that
// tenant's IAM data (users, roles).
//
// The resolution depends on the tenant's isolation type:
//
//   - prefix / system / unknown / empty tenants resolve to the SHARED system
//     KVStore, CONFINED to the tenant's own keyspace at "{tenant}/". The
//     confinement is a
//     property of the returned store, not of the caller: a dao.GenericDAO
//     keys entities "{entityType}/{id}" and has no tenant concept, so
//     returning the bare shared store gave every prefix tenant one
//     undifferentiated keyspace (#41).
//
//     NO prefix is exempt, the reserved "__sys:" keyspace included. An
//     exemption would be a second tenancy mechanism beside the namespace,
//     keyed on a string, and it let a crafted tenant id, a crafted DAO entity
//     type and pkg/permission's tenantless scope keys each reach across
//     tenants. The IAM stores additionally embed "...tenant/{tenantID}..."
//     segments of their own; that is now redundant with the namespace and is
//     a follow-on cleanup, not a second enforcement point.
//
//   - isolated tenants (IsolationBarrier, IsolationVolume, IsolationRaft)
//     resolve to their home store (home_store.go), whatever their isolation
//     policy: the tenant's own Raft group in raft mode, the tenant barrier's
//     own backend at its data root in standalone mode. None of their records
//     is in the system shard. The policy decides operator verbs, backups and
//     relay; it never decides where a record lives. All three modes register
//     their per-tenant barrier in the SAME BarrierRegistry, so the home store
//     is gated by the same seal that protects the tenant's data.
//
// SECURITY: resolution fails closed for every isolated tenant. When such a
// tenant's barrier is sealed or absent, or its home store cannot be reached,
// Resolve returns an error matching ErrTenantSealed and NEVER falls back to
// the shared system store.
type StoreResolver interface {
	// Resolve returns the KVStore that backs the given tenant's IAM data.
	// It returns ErrTenantSealed when the tenant is dedicated-isolation
	// (barrier/volume/raft) and its barrier is sealed.
	Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error)
}

// barrierRoutedIsolation is the set of isolated types: those whose records
// live in the tenant's home store rather than the shared system store. Every
// mode in the set registers a TenantBarrier in the SAME BarrierRegistry the
// resolver consults, so resolveHome gates all three identically and fails
// closed when the barrier is sealed:
//
//   - IsolationBarrier: keyspace-mode dedicated barrier.
//   - IsolationVolume:  dedicated DataRoot with the IAM barrier at
//     {DataRoot}/barrier.
//   - IsolationRaft:    dedicated DataRoot + per-database raft shards, IAM
//     barrier at {DataRoot}/barrier.
//
// IsolationPrefix (and the legacy empty isolation type, which
// EffectiveIsolationType coerces to IsolationPrefix) is deliberately absent:
// prefix tenants keep their IAM in the shared store.
var barrierRoutedIsolation = map[IsolationType]struct{}{
	IsolationBarrier: {},
	IsolationVolume:  {},
	IsolationRaft:    {},
}

// IAMBehindTenantBarrier reports whether a tenant of this isolation type keeps
// its identity records (users, roles, bindings) behind its own barrier -- and
// so cannot have any written before that barrier is initialized and unsealed.
// It reads the same table the resolver routes by, so the two cannot disagree.
func IAMBehindTenantBarrier(isolation IsolationType) bool {
	info := TenantInfo{IsolationType: isolation}
	_, routed := barrierRoutedIsolation[info.EffectiveIsolationType()]
	return routed
}

// isolationLookup is the minimal slice of TenantServicer the resolver
// needs: the persisted isolation type for a tenant. TenantService
// satisfies it directly.
type isolationLookup interface {
	GetTenant(ctx context.Context, tenantID string) (*TenantInfo, error)
}

// barrierLookup is the minimal slice of *seal.BarrierRegistry the resolver
// needs. *seal.BarrierRegistry satisfies it directly.
type barrierLookup interface {
	GetTenant(tenantID string) (*seal.TenantBarrier, error)
}

// Compile-time interface check.
var _ StoreResolver = (*defaultStoreResolver)(nil)

// defaultStoreResolver is the production StoreResolver. It holds the shared
// system KVStore, the barrier registry, a tenant-metadata lookup for
// resolving isolation types, and the home-store source isolated tenants
// resolve to. The barrier's seal state is read LIVE on every resolution, so
// the sealed/unsealed decision is never cached.
type defaultStoreResolver struct {
	shared   kvstore.KVStore
	registry barrierLookup
	tenants  isolationLookup

	// tombstones answers "was this id deleted?" so an UNKNOWN tenant can be
	// told apart from a RETIRED one. Without it, Resolve's deliberate
	// fall-through for unknown tenants hands a deleted tenant's id its old
	// identity keyspace back -- reclaimed and empty after go-qrdb#413, but
	// resolvable, which is one layer of defence short of the refusal
	// CreateTenant now makes. It is nil only when the caller supplied a
	// TenantServicer from outside this package.
	tombstones tombstoneLookup

	// namespaced caches the shared-store confinement wrapper per "{tenant}/"
	// prefix.
	namespaced sync.Map // map[string]kvstore.KVStore

	// home is the isolated tenants' home-store source, attached once the data
	// plane that hosts the home stores exists (SetHomeStoreSource). Until
	// then an isolated tenant fails closed.
	home atomic.Pointer[homeSourceRef]
}

// NewStoreResolver constructs the default StoreResolver. shared is the
// system KVStore used for prefix/system tenants; registry resolves a
// barrier tenant's TenantBarrier; tenants resolves a tenant's isolation
// type. All three must be non-nil.
func NewStoreResolver(shared kvstore.KVStore, registry *seal.BarrierRegistry, tenants TenantServicer) (StoreResolver, error) {
	if shared == nil {
		return nil, ErrNilStore
	}
	if tenants == nil {
		return nil, ErrNilTenantServicer
	}
	r := &defaultStoreResolver{
		shared:  shared,
		tenants: tenants,
	}
	// Both tenant services this package ships satisfy tombstoneLookup (the
	// assertions are in tombstone.go). A foreign TenantServicer does not and
	// cannot -- the method is unexported -- so such a composition gets
	// metadata-only resolution and relies on CreateTenant's refusal alone.
	if tl, ok := tenants.(tombstoneLookup); ok {
		r.tombstones = tl
	}
	// The registry is OPTIONAL, because keyspace isolation must not depend on
	// encryption being enabled.
	//
	// It used to be required, and the consequence was that a deployment with
	// the seal subsystem disabled got no resolver at all — its IAM stayed on
	// the raw shared store and every prefix tenant shared one keyspace, which
	// is the exact exposure #41 exists to close. A node that has not turned on
	// encryption still has tenants, and they are still entitled to be separate.
	//
	// Without a registry, dedicated-isolation tenants cannot be served at all:
	// there is no barrier to route them through. They fail CLOSED with
	// ErrTenantSealed rather than falling back to the shared store, which is
	// the same answer they get when a barrier exists but is sealed — the
	// caller cannot reach that tenant's IAM, and the reason is not the
	// caller's business.
	//
	// Assigning through the concrete-nil check keeps r.registry a genuinely
	// nil interface: storing a typed nil *seal.BarrierRegistry would make
	// registry != nil true and panic on first use.
	if registry != nil {
		r.registry = registry
	}
	return r, nil
}

// Resolve implements StoreResolver. See the StoreResolver doc comment for
// the fail-closed contract.
func (r *defaultStoreResolver) Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	// The system tenant and an empty/unset tenant always use the shared
	// store. The system tenant is structurally a prefix tenant so the IAM
	// stores work during bootstrap while tenant barriers are still sealed.
	if tenantid.IsSystem(tenantID) {
		return r.sharedFor(ctx, tenantID)
	}

	info, err := r.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		// An unknown tenant (no metadata) is treated as a logical-prefix
		// tenant against the shared store: this preserves the pre-isolation
		// behavior and keeps lookups by global ID working for callers that
		// have not (yet) registered tenant metadata. Only a tenant that is
		// explicitly barrier-isolated is routed through a barrier.
		if errors.Is(err, ErrTenantNotFound) {
			// "No metadata" has two meanings, and only one of them may fall
			// through to the shared store. A tenant that was never
			// registered is served as a logical-prefix tenant, which is what
			// keeps lookups by global id working for callers that have not
			// registered metadata. A tenant that was DELETED must not be:
			// its id is retired, and resolving it would hand a caller the
			// keyspace the deletion just reclaimed.
			if dErr := r.refuseIfDeleted(ctx, tenantID); dErr != nil {
				return nil, dErr
			}
			return r.sharedFor(ctx, tenantID)
		}
		return nil, err
	}

	// Only the isolated modes resolve to a home store. Prefix isolation — and
	// the legacy empty IsolationType, which EffectiveIsolationType coerces to
	// IsolationPrefix — resolves to the shared store.
	if _, routed := barrierRoutedIsolation[info.EffectiveIsolationType()]; !routed {
		return r.sharedFor(ctx, tenantID)
	}

	// An isolated tenant's records live in its own home store, never in the
	// system shard, whatever its policy (architecture.md "Strict isolation").
	// There is no fallback from one to the other: an isolated tenant whose
	// home cannot be reached fails closed.
	return r.resolveHome(ctx, tenantID)
}

// refuseIfDeleted returns a *TenantDeletedError when tenantID has been
// deleted, the store's error when the question could not be answered, and nil
// otherwise.
//
// An unanswerable question FAILS the resolution rather than falling through:
// the fall-through is what a deleted id must not get.
func (r *defaultStoreResolver) refuseIfDeleted(ctx context.Context, tenantID string) error {
	if r.tombstones == nil {
		return nil
	}
	at, tombstoned, err := r.tombstones.deletedAt(ctx, tenantID)
	if err != nil {
		return err
	}
	if !tombstoned {
		return nil
	}
	return &TenantDeletedError{TenantID: tenantID, DeletedAt: at}
}

// IdentityNamespace returns the keyspace prefix a tenant's IDENTITY records
// live under: users, roles, permission scopes and invitations.
//
// It is exported because more than one place must compose it and they MUST
// agree. Firstboot seeds the system tenant's admin user and roles before the
// StoreResolver exists, and the RBAC authorizer reads them back through the
// resolver afterwards; if those two compose the prefix differently the admin
// has no resolvable role bindings and every authorization check denies. That
// is precisely what happened when the writer used the raw store and the
// reader used a confined one, and it is the third time a divergence between a
// write path and a read path has come from re-deriving a key prefix by hand
// rather than calling one function.
//
// Note this is deliberately NOT ComposeNamespace: that is the DATA plane's
// "{tenant}/{database}/". Identity is tenant-wide, because a user must
// authenticate whichever database a request names.
//
// The empty id and tenantid.System are the same tenant and yield the same
// prefix; IsSystemTenant is the authority on that, so callers cannot
// disagree about it either.
func IdentityNamespace(tenantID string) string {
	if tenantid.IsSystem(tenantID) {
		tenantID = tenantid.System
	}
	return tenantID + "/"
}

// sharedFor returns the shared store CONFINED to the tenant's keyspace.
//
// The namespace is the TENANT alone — "{tenant}/" — deliberately NOT
// ComposeNamespace's "{tenant}/{db}/". This store serves a tenant's IDENTITY
// (users, roles, permission scopes, invitations), and identity belongs to a
// tenant, not to one of its databases: the same user must authenticate
// whichever database a request names.
//
// Including the database was measured to break login outright. Firstboot
// writes the admin user with no database bound to its context, so it landed
// under "{tenant}/system/", while any request carrying a database resolved
// "{tenant}/{db}/" and found nothing. Unit tests using a single database
// could not see it; the integration stack, which authenticates for real,
// failed every CLI suite.
//
// Database scoping of the DATA plane is unaffected and lives where it always
// has — the transport registries and the SQL provider apply ComposeNamespace
// when they key into the shared store.
//
// Returning r.shared itself gave every prefix tenant the same handle over one
// undifferentiated keyspace: a dao.GenericDAO keys entities "{entityType}/{id}"
// and has no tenant concept, so one tenant's DAO read another's rows and one
// tenant's index answered another's query. The confinement is a property of
// the store now, not of every caller remembering to prefix.
//
// Note this is deliberately NOT tenant.ComposeNamespace: that formula is the
// DATA plane's "{tenant}/{db}/", applied by the transports and the SQL
// provider. This plane is identity, which is tenant-wide.
//
// Wrappers are cached per tenant, matching the namespace.
func (r *defaultStoreResolver) sharedFor(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	// Validate HERE, not only at tenant creation. Resolve accepts an id it has
	// never seen — an unknown tenant deliberately falls through to the shared
	// store — so an id containing '/' would build a prefix nested INSIDE
	// another tenant's namespace ("acme/evil/" sits under "acme/"), and a scan
	// by the outer tenant would list the inner one's keys. Creation already
	// rejects the charset; this closes the path that never goes through
	// creation.
	if err := validateTenantIDCharset(tenantID); err != nil {
		return nil, err
	}
	// IdentityNamespace canonicalizes "" to tenantid.System, so the two
	// representations of the system tenant cannot end up with two namespaces.
	prefix := IdentityNamespace(tenantID)
	if cached, ok := r.namespaced.Load(prefix); ok {
		return cached.(kvstore.KVStore), nil //nolint:errcheck // type assertion on a value this container only ever holds
	}
	wrapper, err := kvstore.NewNamespacedKVStore(r.shared, prefix)
	if err != nil {
		return nil, err
	}
	actual, _ := r.namespaced.LoadOrStore(prefix, wrapper)
	return actual.(kvstore.KVStore), nil //nolint:errcheck // type assertion on a value this container only ever holds
}

// requireUnsealed refuses an isolated tenant whose barrier is absent or sealed
// on this node, with ErrTenantSealed, before its home store is consulted.
func (r *defaultStoreResolver) requireUnsealed(tenantID string) error {
	if r.registry == nil {
		// No barrier registry on this node: an isolated tenant has no barrier
		// to gate its home store, so its records are unreachable here. Fail
		// closed with the same error a sealed barrier produces rather than
		// serving it from the shared store, which would hand a tenant that
		// asked for cryptographic isolation an unencrypted keyspace.
		return ErrTenantSealed
	}
	tb, err := r.registry.GetTenant(tenantID)
	if err != nil {
		// An isolated tenant with NO registered barrier on this node is the
		// same fail-closed state as a sealed one: its records cannot be
		// resolved here until the barrier arrives (self-managed tenants get
		// theirs via key delivery when a placement lands on this node). The
		// distinction matters to callers — ErrTenantSealed flows through the
		// auth middleware as a retryable unavailable (503) plus the sealed
		// marker that unlocks the IdentityGated barrier bootstrap endpoints,
		// whereas an opaque registry error would surface as a hard 403.
		return ErrTenantSealed
	}
	// Read the seal state LIVE — never cache the sealed/unsealed decision.
	if tb.IsSealed() {
		return ErrTenantSealed
	}
	return nil
}
