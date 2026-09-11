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

import "context"

// ---------------------------------------------------------------------------
// What deleting a tenant has to reach
//
// A tenant owns four things this package cannot touch on its own, so each is
// a seam wired at composition time rather than a dependency taken here:
//
//   - its DATABASES, whose catalog lives in pkg/database -- which imports
//     this package, so the arrow cannot be reversed;
//   - its DATA ENCRYPTION KEY, in pkg/dek;
//   - its KEYSPACE in the shared store, which is a replicated state machine
//     several layers above this one;
//   - its BARRIER and the durable records behind it, which the barrier
//     registry owns and this service only holds a handle to.
//
// The three declared here are narrow on purpose: a teardown that accepted
// database.Manager, dek.TenantDEKManager and a KV servicer would drag a
// filesystem catalog, a key hierarchy and forty data-plane methods into a
// package whose job is tenant metadata. Every production type satisfies its
// seam structurally at the wiring site.
// ---------------------------------------------------------------------------

// DatabaseLister reports the databases a tenant still owns.
//
// It is the seam behind DeleteTenant's bottom-up refusal: a tenant with
// databases left is refused, so each database's own drop -- which reclaims
// the keyspace that database owns -- runs before the tenant's does.
// *database.Manager satisfies it.
type DatabaseLister interface {
	// TenantDatabases returns the names of the databases owned by tenantID,
	// sorted. An unknown tenant owns none, which is not an error.
	TenantDatabases(ctx context.Context, tenantID string) ([]string, error)
}

// DEKShredder destroys a tenant's data encryption key.
//
// Destroying the key is what makes a dedicated-isolation tenant's data
// unreadable at the moment of deletion, before any byte of it has been
// reclaimed -- which is why it runs first and why a failure aborts the
// deletion. dek.TenantDEKManager satisfies it.
type DEKShredder interface {
	// DeleteTenantDEK removes tenantID's DEK. A tenant that never had one is
	// the success case, not a not-found error: the deletion must be
	// re-issuable, and a prefix tenant may never have had a key at all.
	DeleteTenantDEK(ctx context.Context, tenantID string) error
}

// KeyspaceReclaimer deletes the data a tenant owns in the shared store.
//
// It is the tenant-level peer of database.KeyspaceReclaimer, and
// service.KeyspaceReclaimService implements both -- deliberately, because
// they reclaim the same shapes of key over different scopes and a second
// implementation of that walk is exactly what this repository forbids.
type KeyspaceReclaimer interface {
	// ReclaimTenantKeyspace deletes every key, secondary index and vector
	// namespace tenantID owns in the store serving it.
	//
	// Implementations MUST be idempotent: a keyspace that is already empty is
	// the success case, which is what lets a re-issued delete finish an
	// interrupted one.
	ReclaimTenantKeyspace(ctx context.Context, tenantID string) error
}

// teardownSeams holds DeleteTenant's optional dependencies.
//
// It is embedded in BOTH tenant services so a node behaves identically
// whether or not the seal subsystem is enabled. Keyspace isolation is not a
// property of encryption -- a node with seal off still has prefix tenants,
// and they are exactly the tenants whose data stays READABLE after a delete
// -- so a teardown that only worked on the encrypted service would miss the
// case go-qrdb#413 is about.
type teardownSeams struct {
	databases   DatabaseLister
	dekShredder DEKShredder
	reclaimer   KeyspaceReclaimer
}

// TeardownWiring is the post-construction wiring surface for DeleteTenant's
// seams, implemented by every TenantServicer this package ships.
//
// It exists for the ordering reason database.Manager.SetKeyspaceReclaimer
// exists: a tenant service is built where the metadata store and the barrier
// registry meet, which is before the data plane that serves the keyspace and
// before the database catalog that bounds the teardown. A composition
// resolves it from the TenantServicer it built:
//
//	if w, ok := tenantSvc.(tenant.TeardownWiring); ok {
//		w.SetKeyspaceReclaimer(reclaimSvc)
//	}
//
// Thread-safety: call the setters before serving delete requests.
type TeardownWiring interface {
	// SetDatabaseLister sets the catalog DeleteTenant consults before it
	// refuses a tenant that still owns databases.
	//
	// With no lister wired the refusal cannot be evaluated and DeleteTenant
	// proceeds. That is safe rather than fail-open because correctness does
	// not rest on the refusal: the tombstone retires the id and the
	// reclamation removes the bytes whether or not the databases were
	// dropped first. What the refusal buys is a BOUND on the reclamation --
	// after the drops, what remains under the tenant is its identity
	// records, which the quota plane already limits -- and an explicit
	// teardown order the operator can see.
	SetDatabaseLister(DatabaseLister)

	// SetDEKShredder sets the tenant DEK manager whose key DeleteTenant
	// destroys. With none wired the shred step is skipped, which is correct
	// for a composition that never issued a tenant DEK.
	SetDEKShredder(DEKShredder)

	// SetKeyspaceReclaimer sets the reclaimer DeleteTenant runs after the
	// tombstone commits. A nil reclaimer means DeleteTenant retires the id
	// and leaves the bytes, which is correct for a composition with no
	// shared store behind it and a leak for one with -- so a daemon asserts
	// on it rather than discovering it later.
	SetKeyspaceReclaimer(KeyspaceReclaimer)
}

// Compile-time checks: both tenant services carry the teardown seams.
var (
	_ TeardownWiring = (*TenantService)(nil)
	_ TeardownWiring = (*kvTenantService)(nil)
)

// SetDatabaseLister implements TeardownWiring.
func (t *teardownSeams) SetDatabaseLister(l DatabaseLister) { t.databases = l }

// SetDEKShredder implements TeardownWiring.
func (t *teardownSeams) SetDEKShredder(d DEKShredder) { t.dekShredder = d }

// SetKeyspaceReclaimer implements TeardownWiring.
func (t *teardownSeams) SetKeyspaceReclaimer(r KeyspaceReclaimer) { t.reclaimer = r }

// KeyspaceReclaimer returns the configured reclaimer, or nil when none is
// wired.
func (t *teardownSeams) KeyspaceReclaimer() KeyspaceReclaimer { return t.reclaimer }

// WithDatabaseLister injects the catalog DeleteTenant consults before it
// refuses a tenant that still owns databases. See
// TeardownWiring.SetDatabaseLister.
func WithDatabaseLister(l DatabaseLister) TenantServiceOption {
	return func(s *TenantService) { s.SetDatabaseLister(l) }
}

// WithDEKShredder injects the tenant DEK manager whose key DeleteTenant
// destroys. See TeardownWiring.SetDEKShredder.
func WithDEKShredder(d DEKShredder) TenantServiceOption {
	return func(s *TenantService) { s.SetDEKShredder(d) }
}

// WithKeyspaceReclaimer injects the reclaimer DeleteTenant runs after the
// tombstone commits. Prefer SetKeyspaceReclaimer in a daemon; see
// TeardownWiring.
func WithKeyspaceReclaimer(r KeyspaceReclaimer) TenantServiceOption {
	return func(s *TenantService) { s.SetKeyspaceReclaimer(r) }
}
