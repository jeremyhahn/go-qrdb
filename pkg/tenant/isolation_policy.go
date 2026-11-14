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
)

// ---------------------------------------------------------------------------
// The isolation policy — a cluster floor and a per-tenant setting.
//
// See docs/multi-tenancy/architecture.md "The isolation policy" (owner
// rulings S1/S2, 2026-10-01/02). Both settings TIGHTEN ONLY: a cluster or a
// tenant that was promised strict isolation must never lose it for data it
// already holds.
//
// The policy governs ISOLATED tenants (barrier, volume, raft) only. A shared
// (prefix) tenant is never refused by the floor or by its own opt-in (owner
// ruling 2026-10-07, superseding S2). It decides operator verbs, backups and
// relay; it never decides where a record lives: every isolated tenant's
// records are in its home store (home_store.go) whatever its policy, so no
// change of either setting moves a record.
// ---------------------------------------------------------------------------

// clusterIsolationPolicyKey is the system-tenant record holding the
// cluster-wide isolation-policy floor.
const clusterIsolationPolicyKey = "__sys:cluster/isolation_policy"

// ClusterIsolationPolicy is the cluster-wide floor on tenant isolation
// enforcement.
type ClusterIsolationPolicy string

const (
	// ClusterIsolationPolicyUnset means the floor has never been written.
	// ClusterIsolationPolicy (the read) answers ClusterIsolationPolicyOperatorAccess
	// for it, preserving today's behavior for every deployment that has not
	// opted in. It is a legal predecessor for SetClusterIsolationPolicy, which
	// may move it to either strict or operator-access, exactly once; neither
	// of the other two values may move back to it.
	ClusterIsolationPolicyUnset ClusterIsolationPolicy = ""

	// ClusterIsolationPolicyStrict is the terminal, most restrictive floor:
	// no further transition is ever permitted.
	ClusterIsolationPolicyStrict ClusterIsolationPolicy = "strict"

	// ClusterIsolationPolicyOperatorAccess permits a tenant to opt itself
	// into strict (TenantIsolationPolicyStrict) while leaving every other
	// tenant at today's behavior.
	ClusterIsolationPolicyOperatorAccess ClusterIsolationPolicy = "operator-access"
)

// Valid reports whether p is a settable value. ClusterIsolationPolicyUnset is
// a valid READ result but is never a valid argument to Set — a floor is set
// to strict or operator-access, never back to unset.
func (p ClusterIsolationPolicy) Valid() bool {
	switch p {
	case ClusterIsolationPolicyStrict, ClusterIsolationPolicyOperatorAccess:
		return true
	default:
		return false
	}
}

// TenantIsolationPolicy is a tenant's own, self-selected isolation policy.
type TenantIsolationPolicy string

const (
	// TenantIsolationPolicyInherit is the default: the tenant's effective
	// policy is whatever the cluster floor says.
	TenantIsolationPolicyInherit TenantIsolationPolicy = ""

	// TenantIsolationPolicyStrict opts the tenant into strict isolation
	// regardless of the cluster floor. Tighten-only: it can never be moved
	// back to TenantIsolationPolicyInherit.
	TenantIsolationPolicyStrict TenantIsolationPolicy = "strict"
)

// Valid reports whether p is a recognized tenant isolation policy value.
func (p TenantIsolationPolicy) Valid() bool {
	switch p {
	case TenantIsolationPolicyInherit, TenantIsolationPolicyStrict:
		return true
	default:
		return false
	}
}

// EffectiveIsolationPolicy is strict whenever either the cluster floor or the
// tenant's own policy is strict (architecture.md: "The effective policy is
// strict when either setting is strict").
func EffectiveIsolationPolicy(floor ClusterIsolationPolicy, tenantPolicy TenantIsolationPolicy) ClusterIsolationPolicy {
	if floor == ClusterIsolationPolicyStrict || tenantPolicy == TenantIsolationPolicyStrict {
		return ClusterIsolationPolicyStrict
	}
	return ClusterIsolationPolicyOperatorAccess
}

// clusterIsolationTransitionAllowed reports whether the cluster floor may
// move from `from` to `to`: unset may become either value; operator-access
// may only tighten to strict; strict never changes.
func clusterIsolationTransitionAllowed(from, to ClusterIsolationPolicy) bool {
	switch from {
	case ClusterIsolationPolicyUnset:
		return to == ClusterIsolationPolicyStrict || to == ClusterIsolationPolicyOperatorAccess
	case ClusterIsolationPolicyOperatorAccess:
		return to == ClusterIsolationPolicyStrict
	default: // ClusterIsolationPolicyStrict: terminal.
		return false
	}
}

// clusterPolicyGetter reads the stored cluster isolation floor.
type clusterPolicyGetter interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// clusterPolicyBackend is the minimal storage capability the cluster
// isolation-policy record needs: a plain read, a guarded create (for the
// first-ever write) and a compare-and-swap (the one legal edit,
// operator-access -> strict). Both seal.StorageBackend
// (TenantService's store) and kvstore.AtomicKVStore (kvTenantService's,
// when its backing store carries the capability) satisfy it. Only a write
// requires it; every read goes through clusterPolicyGetter.
type clusterPolicyBackend interface {
	clusterPolicyGetter
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	CompareAndSwap(ctx context.Context, key string, expected, value []byte) (bool, error)
}

// getClusterIsolationPolicy reads the cluster floor through backend,
// defaulting an absent record to ClusterIsolationPolicyOperatorAccess: the
// permissive, pre-existing behavior for any deployment that has not written
// the record. See getRawClusterIsolationPolicy for the undefaulted read
// SetClusterIsolationPolicy needs to decide a transition.
func getClusterIsolationPolicy(ctx context.Context, backend clusterPolicyGetter) (ClusterIsolationPolicy, error) {
	current, err := getRawClusterIsolationPolicy(ctx, backend)
	if err != nil {
		return "", err
	}
	if current == ClusterIsolationPolicyUnset {
		return ClusterIsolationPolicyOperatorAccess, nil
	}
	return current, nil
}

// getRawClusterIsolationPolicy reads the stored record without defaulting an
// absent one, so the caller can tell "never written" (a legal predecessor of
// either value) from "written operator-access" (which may only tighten).
func getRawClusterIsolationPolicy(ctx context.Context, backend clusterPolicyGetter) (ClusterIsolationPolicy, error) {
	data, err := backend.Get(ctx, clusterIsolationPolicyKey)
	if err != nil {
		if isAbsent(err) {
			return ClusterIsolationPolicyUnset, nil
		}
		return "", &StoreError{Op: "cluster-isolation-policy-get", Err: err}
	}
	if data == nil {
		return ClusterIsolationPolicyUnset, nil
	}
	return ClusterIsolationPolicy(data), nil
}

// setClusterIsolationPolicy writes next to the cluster floor, refusing any
// transition clusterIsolationTransitionAllowed does not permit. A race lost
// against a concurrent setter is reported as *IsolationPolicyChangeError,
// identically to a disallowed transition: the store is unchanged either way,
// and the caller re-reads to see what won.
func setClusterIsolationPolicy(ctx context.Context, backend clusterPolicyBackend, next ClusterIsolationPolicy) error {
	if !next.Valid() {
		return &InvalidIsolationPolicyError{Value: string(next)}
	}
	current, err := getRawClusterIsolationPolicy(ctx, backend)
	if err != nil {
		return err
	}
	if current == next {
		return nil
	}
	if !clusterIsolationTransitionAllowed(current, next) {
		return &IsolationPolicyChangeError{From: string(current), To: string(next)}
	}
	if current == ClusterIsolationPolicyUnset {
		stored, err := backend.PutIfAbsent(ctx, clusterIsolationPolicyKey, []byte(next))
		if err != nil {
			return &StoreError{Op: "cluster-isolation-policy-put", Err: err}
		}
		if !stored {
			return &IsolationPolicyChangeError{From: string(current), To: string(next)}
		}
		return nil
	}
	// current == ClusterIsolationPolicyOperatorAccess, next ==
	// ClusterIsolationPolicyStrict: the only transition left standing. One
	// compare-and-swap, so the floor is never unset in between and a lost
	// race is reported, never mistaken for success.
	swapped, err := backend.CompareAndSwap(ctx, clusterIsolationPolicyKey, []byte(current), []byte(next))
	if err != nil {
		return &StoreError{Op: "cluster-isolation-policy-swap", Err: err}
	}
	if !swapped {
		return &IsolationPolicyChangeError{From: string(current), To: string(next)}
	}
	return nil
}

// ClusterIsolationPolicyReader resolves the cluster-wide isolation floor.
// Both TenantService and the no-seal kvTenantService implement it; a
// composer that needs only a read (e.g. pkg/service's vector namespace
// create guard) can depend on this instead of the full TenantServicer.
type ClusterIsolationPolicyReader interface {
	ClusterIsolationPolicy(ctx context.Context) (ClusterIsolationPolicy, error)
}

// checkIsolationPolicyChangeFor enforces UpdateTenant's isolation-policy
// precondition: tighten-only (TenantIsolationPolicyInherit ->
// TenantIsolationPolicyStrict is the only legal transition). No record moves
// when a policy changes -- every isolated tenant's records are in its home
// store whatever its policy -- so there is no other precondition. A patch
// naming no isolation-policy change, or the tenant's current one, is a no-op.
func checkIsolationPolicyChangeFor(info *TenantInfo, patch TenantPatch) error {
	if patch.IsolationPolicy == "" || patch.IsolationPolicy == info.IsolationPolicy {
		return nil
	}
	if !patch.IsolationPolicy.Valid() {
		return &InvalidIsolationPolicyError{Value: string(patch.IsolationPolicy)}
	}
	if info.IsolationPolicy == TenantIsolationPolicyStrict {
		return &IsolationPolicyChangeError{From: string(info.IsolationPolicy), To: string(patch.IsolationPolicy)}
	}
	return nil
}

// ClusterIsolationPolicy returns the cluster-wide isolation floor.
func (s *TenantService) ClusterIsolationPolicy(ctx context.Context) (ClusterIsolationPolicy, error) {
	return getClusterIsolationPolicy(ctx, s.store)
}

// SetClusterIsolationPolicy tightens the cluster-wide isolation floor. See
// clusterIsolationTransitionAllowed for the legal transitions.
func (s *TenantService) SetClusterIsolationPolicy(ctx context.Context, next ClusterIsolationPolicy) error {
	backend, ok := s.store.(clusterPolicyBackend)
	if !ok {
		return ErrTenantStoreNotAtomic
	}
	return setClusterIsolationPolicy(ctx, backend, next)
}

// ClusterIsolationPolicy returns the cluster-wide isolation floor. The read
// needs only Get, never the write capability: a store without CompareAndSwap
// (e.g. one on the earlier PutIfAbsent/CompareAndDelete contract) may still
// hold a strict floor written before an upgrade, and reading it as
// operator-access would silently loosen enforcement. There is no "no Get"
// fallback because the store field is a kvstore.KVStore, whose Get is part of
// the type; only SetClusterIsolationPolicy requires clusterPolicyBackend.
func (s *kvTenantService) ClusterIsolationPolicy(ctx context.Context) (ClusterIsolationPolicy, error) {
	return getClusterIsolationPolicy(ctx, s.store)
}

// SetClusterIsolationPolicy tightens the cluster-wide isolation floor,
// refusing with ErrTenantStoreNotAtomic when the backing store cannot commit
// the change atomically.
func (s *kvTenantService) SetClusterIsolationPolicy(ctx context.Context, next ClusterIsolationPolicy) error {
	backend, ok := s.store.(clusterPolicyBackend)
	if !ok {
		return ErrTenantStoreNotAtomic
	}
	return setClusterIsolationPolicy(ctx, backend, next)
}
