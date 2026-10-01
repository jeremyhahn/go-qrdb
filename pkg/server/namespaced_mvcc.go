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

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// namespacedMVCCService wraps an MVCCKVServicer so that every key is
// transparently prefixed with the composite (tenant, db) namespace and
// stripped from returned key-values. The KV portion of the MVCC interface is
// satisfied by the embedded *namespacedKVService, which prefixes the inner
// MVCC service's KV methods. The MVCC-specific methods below add the same
// prefixing for revision-aware operations.
//
// Compact and CurrentRevision are intentionally not prefixed: MVCC revisions
// are a store-global monotonic counter, not a per-namespace value, so they are
// forwarded unchanged.
//
// A write that attaches a lease attaches only a lease this namespace owns
// (leases): lease IDs are global, and attaching another tenant's lease would
// tie this namespace's key to a lease that tenant controls -- revoking or
// letting it expire would delete the key -- and reveal that the ID is live.
// A lease this namespace does not own is answered with the not-found a
// missing lease gets. Lease ID 0 is no lease.
type namespacedMVCCService struct {
	*namespacedKVService
	inner  service.MVCCKVServicer
	prefix string
	leases leaseOwnership
}

// leaseOwnership confines lease attachment to the namespace's own leases.
// It is satisfied by *namespacedLeaseService.
type leaseOwnership interface {
	// requireOwned returns nil for a lease this namespace owns and
	// *service.LeaseNotFoundError for any other.
	requireOwned(ctx context.Context, leaseID int64) error
}

// Compile-time interface check.
var _ service.MVCCKVServicer = (*namespacedMVCCService)(nil)

// newNamespacedMVCCService creates an MVCCKVServicer that prefixes all keys
// with the given composite namespace prefix (e.g. "acme/reports/"). leases is
// the namespace's lease view; with none, no lease can be attached.
func newNamespacedMVCCService(inner service.MVCCKVServicer, prefix string, leases leaseOwnership) *namespacedMVCCService {
	return &namespacedMVCCService{
		namespacedKVService: &namespacedKVService{inner: inner, prefix: prefix},
		inner:               inner,
		prefix:              prefix,
		leases:              leases,
	}
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

// Compact is gated to the system tenant. MVCC revisions are a store-global
// monotonic counter shared by every prefix-isolated tenant on this store, so a
// Compact to revision N discards ALL historical revisions below N for EVERY
// tenant's keys — destroying other tenants' time-travel history even though it
// is issued through a single tenant's namespaced view. Because compaction
// cannot be confined to the namespace prefix, a non-system caller is rejected
// here (the data-plane defense) in addition to the ScopeCross authz gate on
// the mvcc:compact permission. The system tenant — the legitimate store-global
// admin — is forwarded unchanged.
func (s *namespacedMVCCService) Compact(ctx context.Context, revision int64) error {
	if callerTenant := tenant.FromContext(ctx); !tenant.IsSystemTenant(callerTenant) {
		return &service.CompactSystemOnlyError{TenantID: callerTenant}
	}
	return s.inner.Compact(ctx, revision)
}

// CurrentRevision forwards unchanged: the latest committed revision is global
// to the store.
func (s *namespacedMVCCService) CurrentRevision(ctx context.Context) (int64, error) {
	return s.inner.CurrentRevision(ctx)
}

// RestoreToRevision confines a selective point-in-time restore to this
// namespace. Unlike Compact, a restore CAN be confined — it names the keys it
// writes — so it is prefixed and forwarded rather than reserved to the system
// tenant.
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
