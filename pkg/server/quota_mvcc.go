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

// Compile-time interface check.
var _ service.MVCCKVServicer = (*quotaMVCCService)(nil)

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

func (s *quotaMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	return s.mvcc.Range(ctx, start, end, opts...)
}

func (s *quotaMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	return s.mvcc.RangeWithConsistency(ctx, start, end, opts, rangeOpts...)
}

func (s *quotaMVCCService) Compact(ctx context.Context, revision int64) error {
	return s.mvcc.Compact(ctx, revision)
}

func (s *quotaMVCCService) CurrentRevision(ctx context.Context) (int64, error) {
	return s.mvcc.CurrentRevision(ctx)
}
