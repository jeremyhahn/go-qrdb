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
// A tenant owns three things this package cannot touch on its own, so each is
// a seam wired at composition time rather than a dependency taken here:
//
//   - its DATABASES, whose catalog lives in pkg/database -- which imports
//     this package, so the arrow cannot be reversed;
//   - its KEYSPACE in the shared store, which is a replicated state machine
//     several layers above this one;
//   - its BARRIER and the durable records behind it, which the barrier
//     registry owns and this service only holds a handle to.
//
// Its data encryption keyring (pkg/dek) is not a fourth: the keyring is a
// record in the tenant's own store, so it becomes unreachable at the fence
// (the store resolver refuses a retired id) and is destroyed with the
// tenant's barrier and reclaimed keyspace.
//
// The seams declared here are narrow on purpose: a teardown that accepted
// database.Manager and a KV servicer would drag a filesystem catalog and
// forty data-plane methods into a package whose job is tenant metadata.
// Every production type satisfies its seam structurally at the wiring site.
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

// HomeRetirer removes an isolated tenant's home group from the replicated
// placement table.
//
// The home group is a placement with no catalog entry, so the bottom-up
// refusal cannot see it and no DROP DATABASE removes it. DeleteTenant retires
// it after the fence and before the barrier is destroyed: once the placement
// is gone no node starts a replica of it again, and every node's shard
// reconciler closes and removes the replica it holds. *service.DatabaseService
// satisfies it.
type HomeRetirer interface {
	// RetireTenantHome deletes tenantID's home-group placement. A tenant with
	// no home group is the success case.
	RetireTenantHome(ctx context.Context, tenantID string) error
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
	databases DatabaseLister
	reclaimer KeyspaceReclaimer
	homes     HomeRetirer
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

	// SetKeyspaceReclaimer sets the reclaimer DeleteTenant runs after the
	// fence commits. A nil reclaimer means DeleteTenant retires the id
	// and leaves the bytes, which is correct for a composition with no
	// shared store behind it and a leak for one with -- so a daemon asserts
	// on it rather than discovering it later.
	SetKeyspaceReclaimer(KeyspaceReclaimer)

	// SetHomeRetirer sets the seam DeleteTenant retires an isolated tenant's
	// home group through. With none wired a deleted tenant's home placement
	// is left until the keyspace reclamation removes its placement range,
	// which is after the barrier the home group runs under is destroyed.
	SetHomeRetirer(HomeRetirer)
}

// Compile-time checks: both tenant services carry the teardown seams.
var (
	_ TeardownWiring = (*TenantService)(nil)
	_ TeardownWiring = (*kvTenantService)(nil)
)

// SetDatabaseLister implements TeardownWiring.
func (t *teardownSeams) SetDatabaseLister(l DatabaseLister) { t.databases = l }

// SetKeyspaceReclaimer implements TeardownWiring.
func (t *teardownSeams) SetKeyspaceReclaimer(r KeyspaceReclaimer) { t.reclaimer = r }

// SetHomeRetirer implements TeardownWiring.
func (t *teardownSeams) SetHomeRetirer(h HomeRetirer) { t.homes = h }

// HomeRetirer returns the configured home retirer, or nil when none is wired.
func (t *teardownSeams) HomeRetirer() HomeRetirer { return t.homes }

// KeyspaceReclaimer returns the configured reclaimer, or nil when none is
// wired.
func (t *teardownSeams) KeyspaceReclaimer() KeyspaceReclaimer { return t.reclaimer }

// WithDatabaseLister injects the catalog DeleteTenant consults before it
// refuses a tenant that still owns databases. See
// TeardownWiring.SetDatabaseLister.
func WithDatabaseLister(l DatabaseLister) TenantServiceOption {
	return func(s *TenantService) { s.SetDatabaseLister(l) }
}

// WithKeyspaceReclaimer injects the reclaimer DeleteTenant runs after the
// fence commits. Prefer SetKeyspaceReclaimer in a daemon; see
// TeardownWiring.
func WithKeyspaceReclaimer(r KeyspaceReclaimer) TenantServiceOption {
	return func(s *TenantService) { s.SetKeyspaceReclaimer(r) }
}
