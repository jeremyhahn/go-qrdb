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

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// quotaMVCCService wraps an MVCCKVServicer with storage-byte quota enforcement.
// The embedded quotaKVService supplies the accounted KVServicer surface
// (Put/Delete/PutEntity/...); the MVCC-only lease-bearing writes are accounted
// here, and the remaining MVCC read/maintenance operations pass straight
// through.
type quotaMVCCService struct {
	*quotaKVService
	mvcc service.MVCCKVServicer
}

// Compile-time interface checks.
var (
	_ service.MVCCKVServicer          = (*quotaMVCCService)(nil)
	_ service.CompactionPolicyHolder  = (*quotaMVCCService)(nil)
	_ service.RevisionHistoryReporter = (*quotaMVCCService)(nil)
)

// newQuotaMVCCService wraps inner with storage-byte quota enforcement for the
// given tenant and limit (MaxStorageBytes).
func newQuotaMVCCService(inner service.MVCCKVServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaMVCCService {
	return &quotaMVCCService{
		quotaKVService: newQuotaKVService(inner, acct, tenantID, limit),
		mvcc:           inner,
	}
}

// PutWithLease accounts len(value) bytes after admitting the write.
func (s *quotaMVCCService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*service.KeyValue, error) {
	n := len(value)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	kv, err := s.mvcc.PutWithLease(ctx, key, value, leaseID)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return kv, nil
}

// PutWithLeaseAndMode accounts len(value) bytes after admitting the write.
func (s *quotaMVCCService) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*service.KeyValue, error) {
	n := len(value)
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	kv, err := s.mvcc.PutWithLeaseAndMode(ctx, key, value, leaseID, mode)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return kv, nil
}

// ---------------------------------------------------------------------------
// MVCC read/maintenance passthroughs (no accounting).
// ---------------------------------------------------------------------------

func (s *quotaMVCCService) GetRevision(ctx context.Context, key string) (*service.KeyValue, error) {
	return s.mvcc.GetRevision(ctx, key)
}

func (s *quotaMVCCService) GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*service.KeyValue, error) {
	return s.mvcc.GetRevisionWithConsistency(ctx, key, opts)
}

func (s *quotaMVCCService) GetAtRevision(ctx context.Context, key string, revision int64) (*service.KeyValue, error) {
	return s.mvcc.GetAtRevision(ctx, key, revision)
}

func (s *quotaMVCCService) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*service.KeyValue, error) {
	return s.mvcc.GetAtRevisionWithConsistency(ctx, key, revision, opts)
}

func (s *quotaMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.mvcc.Range(ctx, start, end, opts...)
}

func (s *quotaMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	return s.mvcc.RangeWithConsistency(ctx, start, end, opts, rangeOpts...)
}

func (s *quotaMVCCService) Compact(ctx context.Context, revision int64) error {
	return s.mvcc.Compact(ctx, revision)
}

// KeepsRevisionHistory forwards to the wrapped view; a view that cannot say
// keeps history.
func (s *quotaMVCCService) KeepsRevisionHistory() bool {
	r, ok := s.mvcc.(service.RevisionHistoryReporter)
	return !ok || r.KeepsRevisionHistory()
}

// CompactionPolicy forwards to the wrapped view: a policy is no storage.
func (s *quotaMVCCService) CompactionPolicy(ctx context.Context) (*compaction.Policy, error) {
	h, ok := s.mvcc.(service.CompactionPolicyHolder)
	if !ok {
		return nil, nil
	}
	return h.CompactionPolicy(ctx)
}

// SetCompactionPolicy forwards to the wrapped view.
func (s *quotaMVCCService) SetCompactionPolicy(ctx context.Context, policy *compaction.Policy) error {
	h, ok := s.mvcc.(service.CompactionPolicyHolder)
	if !ok {
		return &service.CompactionPolicyUnavailableError{}
	}
	return h.SetCompactionPolicy(ctx, policy)
}

func (s *quotaMVCCService) CurrentRevision(ctx context.Context) (int64, error) {
	return s.mvcc.CurrentRevision(ctx)
}

// RestoreToRevision accounts the storage a selective restore adds before
// applying it. A restore is a bulk write like any other and must not be a
// route around the tenant's storage limit: recreating keys deleted since the
// target revision grows the keyspace, and reverting to a larger historical
// value grows it too.
//
// The plan is priced with a dry run through the inner service, which is the
// only way to learn the size of a restore before committing to it, and the
// reservation covers the plan's gross written bytes. Deletes in the plan free
// storage, but they are not netted off: crediting them up front would let a
// plan that deletes early and writes late admit a peak footprint above the
// limit. Over-reserving is the conservative direction — it can refuse a
// restore that would have just fit, never admit one that does not.
//
// A dry run is forwarded unaccounted: it writes nothing.
func (s *quotaMVCCService) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if req != nil && req.DryRun {
		return s.mvcc.RestoreToRevision(ctx, req)
	}

	plan, err := s.mvcc.RestoreToRevision(ctx, restoreDryRunOf(req))
	if err != nil {
		return nil, err
	}

	var written int64
	for _, c := range plan.Changes {
		written += c.ValueBytes
	}
	if written > 0 {
		if err := s.reserve(ctx, int(written)); err != nil {
			return nil, err
		}
	}

	result, err := s.mvcc.RestoreToRevision(ctx, req)
	if err != nil {
		if written > 0 {
			s.acct.Release(s.tenantID, written)
		}
		return nil, err
	}

	// Changes the compare-and-swap guard refused wrote nothing, so their share
	// of the reservation is given back. Without this a restore that conflicts
	// on most of its plan would leave the tenant charged for bytes no write
	// ever produced.
	var unapplied int64
	for _, c := range result.Conflicts {
		unapplied += c.ValueBytes
	}
	if unapplied > 0 {
		s.acct.Release(s.tenantID, unapplied)
	}

	return result, nil
}

// restoreDryRunOf returns a copy of req with DryRun set, used to price a
// restore before it is applied. A copy is taken rather than the caller's
// request mutated, so pricing never alters the request the caller passed in.
func restoreDryRunOf(req *service.RestoreRevisionRequest) *service.RestoreRevisionRequest {
	if req == nil {
		return nil
	}
	dry := *req
	dry.DryRun = true
	return &dry
}
