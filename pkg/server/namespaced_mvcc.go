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

package server

import (
	"context"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// namespacedMVCCService wraps an MVCCKVServicer so that every key is
// transparently prefixed with the composite (tenant, db) namespace and
// stripped from returned key-values. The KV portion of the MVCC interface is
// satisfied by the embedded *namespacedKVService, which prefixes the inner
// MVCC service's KV methods. The MVCC-specific methods below add the same
// prefixing for revision-aware operations.
//
// CurrentRevision is intentionally not prefixed: MVCC revisions are a
// store-global monotonic counter, not a per-namespace value. Compact is
// confined to the namespace: it compacts the view's own prefix through the
// inner service's service.PrefixCompactor, and the store keeps a compaction
// floor per prefix.
//
// A write that attaches a lease attaches only a lease this namespace owns
// (leases): lease IDs are global, and attaching another tenant's lease would
// tie this namespace's key to a lease that tenant controls -- revoking or
// letting it expire would delete the key -- and reveal that the ID is live.
// A lease this namespace does not own is answered with the not-found a
// missing lease gets. Lease ID 0 is no lease.
type namespacedMVCCService struct {
	dbGuard
	*namespacedKVService
	inner  service.MVCCKVServicer
	prefix string
	leases leaseOwnership

	// compactor is inner's per-prefix compaction, nil when inner has none;
	// see Compact.
	compactor service.PrefixCompactor

	// states is inner's replicated compaction table, nil when inner has
	// none; history reports whether inner's state machine keeps MVCC
	// history at all. See CompactionPolicy.
	states  service.CompactionStateStore
	history service.RevisionHistoryReporter

	// binding identifies the database this view serves -- its owner, name
	// and incarnation -- for the compaction policy it writes.
	binding compaction.Binding
}

// leaseOwnership confines lease attachment to the namespace's own leases.
// It is satisfied by *namespacedLeaseService.
type leaseOwnership interface {
	// requireOwned returns nil for a lease this namespace owns and
	// *service.LeaseNotFoundError for any other.
	requireOwned(ctx context.Context, leaseID int64) error
}

// Compile-time interface checks.
var (
	_ service.MVCCKVServicer          = (*namespacedMVCCService)(nil)
	_ service.CompactionPolicyHolder  = (*namespacedMVCCService)(nil)
	_ service.RevisionHistoryReporter = (*namespacedMVCCService)(nil)
)

// newNamespacedMVCCService creates an MVCCKVServicer that prefixes all keys
// with the given composite namespace prefix (e.g. "acme/reports/"). leases is
// the namespace's lease view; with none, no lease can be attached.
func newNamespacedMVCCService(inner service.MVCCKVServicer, prefix string, leases leaseOwnership) *namespacedMVCCService {
	// The view compacts through the inner service's per-prefix compaction,
	// detected here once: an inner service without one leaves Compact
	// refusing rather than compacting anything wider than this namespace.
	compactor, _ := inner.(service.PrefixCompactor)
	states, _ := inner.(service.CompactionStateStore)
	history, _ := inner.(service.RevisionHistoryReporter)
	return &namespacedMVCCService{
		namespacedKVService: &namespacedKVService{inner: inner, prefix: prefix},
		inner:               inner,
		prefix:              prefix,
		leases:              leases,
		compactor:           compactor,
		states:              states,
		history:             history,
	}
}

// KeepsRevisionHistory implements service.RevisionHistoryReporter for the
// inner service: a view keeps history exactly when its store does.
func (s *namespacedMVCCService) KeepsRevisionHistory() bool {
	return s.history == nil || s.history.KeepsRevisionHistory()
}

// CompactionPolicy returns this database's scheduled-compaction policy from
// the replicated record in its own store: nil when it has none, when the
// record belongs to another incarnation of the name, or when the store keeps
// no MVCC history and so can have none.
func (s *namespacedMVCCService) CompactionPolicy(ctx context.Context) (*compaction.Policy, error) {
	if s.states == nil || !s.KeepsRevisionHistory() {
		return nil, nil
	}
	st, err := s.states.CompactionState(ctx, s.prefix)
	if err != nil || st == nil {
		return nil, err
	}
	if st.Tenant != s.binding.Tenant || st.Database != s.binding.Database || st.Incarnation != s.binding.Incarnation {
		return nil, nil
	}
	return st.Policy, nil
}

// SetCompactionPolicy writes this database's policy -- bound to its owner,
// name and incarnation -- into the replicated record in its own store, as a
// write of the database: it carries the view's drop-fence guard, so a policy
// can never be written for an incarnation that has been dropped. nil turns
// the policy off.
func (s *namespacedMVCCService) SetCompactionPolicy(ctx context.Context, policy *compaction.Policy) error {
	if s.states == nil || !s.KeepsRevisionHistory() {
		return &service.CompactionPolicyUnavailableError{Database: s.binding.Database}
	}
	binding := s.binding
	binding.Policy = policy
	return s.states.SetCompactionPolicy(s.scope(ctx), s.prefix, binding)
}

// requireOwnedLease returns nil for no lease (0) or a lease this namespace
// owns, and *service.LeaseNotFoundError for any other.
func (s *namespacedMVCCService) requireOwnedLease(ctx context.Context, leaseID int64) error {
	if leaseID == 0 {
		return nil
	}
	if s.leases == nil {
		return &service.LeaseNotFoundError{LeaseID: leaseID}
	}
	return s.leases.requireOwned(ctx, leaseID)
}

// PutWithLease stores a key-value pair with an attached lease under the
// namespace prefix. The lease must be this namespace's.
func (s *namespacedMVCCService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*service.KeyValue, error) {
	if err := s.requireOwnedLease(ctx, leaseID); err != nil {
		return nil, err
	}
	ctx = s.scope(ctx)
	kvp, err := s.inner.PutWithLease(ctx, s.prefix+key, value, leaseID)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// PutWithLeaseAndMode stores a key-value pair with an attached lease and the
// specified write durability under the namespace prefix. The lease must be
// this namespace's.
func (s *namespacedMVCCService) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*service.KeyValue, error) {
	if err := s.requireOwnedLease(ctx, leaseID); err != nil {
		return nil, err
	}
	ctx = s.scope(ctx)
	kvp, err := s.inner.PutWithLeaseAndMode(ctx, s.prefix+key, value, leaseID, mode)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// GetRevision retrieves a key-value pair with revision metadata under the
// namespace prefix.
func (s *namespacedMVCCService) GetRevision(ctx context.Context, key string) (*service.KeyValue, error) {
	kvp, err := s.inner.GetRevision(ctx, s.prefix+key)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// GetRevisionWithConsistency retrieves a key-value pair with revision metadata
// at the specified consistency level under the namespace prefix.
func (s *namespacedMVCCService) GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*service.KeyValue, error) {
	kvp, err := s.inner.GetRevisionWithConsistency(ctx, s.prefix+key, opts)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// GetAtRevision retrieves a key-value pair at a historical revision under the
// namespace prefix.
func (s *namespacedMVCCService) GetAtRevision(ctx context.Context, key string, revision int64) (*service.KeyValue, error) {
	kvp, err := s.inner.GetAtRevision(ctx, s.prefix+key, revision)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// GetAtRevisionWithConsistency retrieves a key-value pair at a historical
// revision and consistency level under the namespace prefix.
func (s *namespacedMVCCService) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*service.KeyValue, error) {
	kvp, err := s.inner.GetAtRevisionWithConsistency(ctx, s.prefix+key, revision, opts)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// Range returns key-value pairs in the half-open interval [start, end) within
// the namespace. The lower bound is confined to the namespace prefix and the
// upper bound to the namespace successor, so an absent start or end can never
// escape the namespace and expose another tenant's or database's keys. Returned
// keys have the namespace prefix stripped. The MVCC revision the read was
// performed at is a store-global value and is passed through unchanged.
func (s *namespacedMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	kvs, count, revision, err := s.inner.Range(ctx, prefixStartBytes(start, s.prefix), prefixEndBytes(end, s.prefix), opts...)
	if err != nil {
		return nil, 0, 0, err
	}
	return stripKeyValues(kvs, s.prefix), count, revision, nil
}

// RangeWithConsistency returns key-value pairs in [start, end) within the
// namespace at the specified consistency level. Both bounds are confined to the
// namespace exactly as in Range. The MVCC revision the read was performed at
// is a store-global value and is passed through unchanged.
func (s *namespacedMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	kvs, count, revision, err := s.inner.RangeWithConsistency(ctx, prefixStartBytes(start, s.prefix), prefixEndBytes(end, s.prefix), opts, rangeOpts...)
	if err != nil {
		return nil, 0, 0, err
	}
	return stripKeyValues(kvs, s.prefix), count, revision, nil
}

// Compact compacts THIS namespace's history and nothing else: the inner
// service compacts the keys under the view's own prefix, so another
// database's and another tenant's history -- which share the store's revision
// counter on a shared shard -- is untouched. The prefix comes from the view,
// which the service registry built for the (tenant, database) the request
// resolved to; the caller names no keyspace and cannot widen it. A
// dedicated-raft database's view has an empty prefix because its shard is its
// whole keyspace.
//
// An inner service that cannot compact by prefix is refused with
// *service.CompactScopeRequiredError rather than compacted whole.
func (s *namespacedMVCCService) Compact(ctx context.Context, revision int64) error {
	ctx = s.scope(ctx)
	if s.compactor == nil {
		return &service.CompactScopeRequiredError{}
	}
	return s.compactor.CompactPrefix(ctx, s.prefix, revision)
}

// CurrentRevision forwards unchanged: the latest committed revision is global
// to the store.
func (s *namespacedMVCCService) CurrentRevision(ctx context.Context) (int64, error) {
	return s.inner.CurrentRevision(ctx)
}

// RestoreToRevision confines a selective point-in-time restore to this
// namespace: it names the keys it writes, so it is prefixed and forwarded.
//
// The request is validated BEFORE the namespace prefix is attached, and this
// ordering is the whole point of the method. Two guards would otherwise be
// defeated by prefixing:
//
//   - The internal-key refusal. "acme/reports/" + "__sys:roles/x" is an
//     ordinary user key, which the inner service's guard admits. Validating
//     the caller's own key first is what keeps "__sys:" unrestorable from
//     inside a namespaced view.
//   - The empty-scope refusal. "acme/reports/" + "" is a non-empty prefix,
//     which the inner service accepts — and which spans this tenant and
//     database's ENTIRE keyspace. A caller who supplied no scope at all would
//     get a whole-database rollback, the exact outcome
//     *RestoreScopeEmptyError exists to prevent.
//
// The inner service validates again on the fully qualified keys, which is
// what protects the system tenant, whose namespace prefix is empty. Neither
// check subsumes the other.
//
// Every key the plan reaches is derived from this namespace's prefix: a
// prefix scope becomes prefix+Prefix and an explicit key list becomes
// prefix+key, so neither can name another tenant's or database's keyspace.
// Returned plan keys are stripped back to the caller's view.
func (s *namespacedMVCCService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	ctx = s.scope(ctx)
	if err := service.ValidateRestoreRequest(req); err != nil {
		return nil, err
	}

	scoped := &service.RestoreRevisionRequest{
		Revision: req.Revision,
		DryRun:   req.DryRun,
		Force:    req.Force,
	}
	if req.Prefix != "" {
		scoped.Prefix = s.prefix + req.Prefix
	}
	if len(req.Keys) > 0 {
		scoped.Keys = make([]string, 0, len(req.Keys))
		for _, k := range req.Keys {
			scoped.Keys = append(scoped.Keys, s.prefix+k)
		}
	}

	result, err := s.inner.RestoreToRevision(ctx, scoped)
	if err != nil {
		return nil, err
	}
	return stripRestoreResult(result, s.prefix), nil
}

// stripRestoreResult removes the namespace prefix from every key in a restore
// plan — both the applied changes and the conflicts — so the caller sees the
// keys it named. The result is rewritten in place; it is a value the inner
// service just constructed for this call and is not shared.
func stripRestoreResult(result *service.RestoreRevisionResult, prefix string) *service.RestoreRevisionResult {
	if result == nil {
		return nil
	}
	for i := range result.Changes {
		result.Changes[i].Key = strings.TrimPrefix(result.Changes[i].Key, prefix)
	}
	for i := range result.Conflicts {
		result.Conflicts[i].Key = strings.TrimPrefix(result.Conflicts[i].Key, prefix)
	}
	return result
}
