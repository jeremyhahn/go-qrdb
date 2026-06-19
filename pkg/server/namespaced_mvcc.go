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
// Compact and CurrentRevision are intentionally not prefixed: MVCC revisions
// are a store-global monotonic counter, not a per-namespace value, so they are
// forwarded unchanged.
type namespacedMVCCService struct {
	*namespacedKVService
	inner  service.MVCCKVServicer
	prefix string
}

// Compile-time interface check.
var _ service.MVCCKVServicer = (*namespacedMVCCService)(nil)

// newNamespacedMVCCService creates an MVCCKVServicer that prefixes all keys
// with the given composite namespace prefix (e.g. "acme/reports/").
func newNamespacedMVCCService(inner service.MVCCKVServicer, prefix string) *namespacedMVCCService {
	return &namespacedMVCCService{
		namespacedKVService: &namespacedKVService{inner: inner, prefix: prefix},
		inner:               inner,
		prefix:              prefix,
	}
}

// PutWithLease stores a key-value pair with an attached lease under the
// namespace prefix.
func (s *namespacedMVCCService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*service.KeyValue, error) {
	kvp, err := s.inner.PutWithLease(ctx, s.prefix+key, value, leaseID)
	if err != nil {
		return nil, err
	}
	return stripKeyValue(kvp, s.prefix), nil
}

// PutWithLeaseAndMode stores a key-value pair with an attached lease and the
// specified write durability under the namespace prefix.
func (s *namespacedMVCCService) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*service.KeyValue, error) {
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
// keys have the namespace prefix stripped.
func (s *namespacedMVCCService) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	kvs, count, err := s.inner.Range(ctx, prefixStartBytes(start, s.prefix), prefixEndBytes(end, s.prefix), opts...)
	if err != nil {
		return nil, 0, err
	}
	return stripKeyValues(kvs, s.prefix), count, nil
}

// RangeWithConsistency returns key-value pairs in [start, end) within the
// namespace at the specified consistency level. Both bounds are confined to the
// namespace exactly as in Range.
func (s *namespacedMVCCService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	kvs, count, err := s.inner.RangeWithConsistency(ctx, prefixStartBytes(start, s.prefix), prefixEndBytes(end, s.prefix), opts, rangeOpts...)
	if err != nil {
		return nil, 0, err
	}
	return stripKeyValues(kvs, s.prefix), count, nil
}

// Compact forwards unchanged: MVCC revisions are a store-global counter, not a
// per-namespace value.
func (s *namespacedMVCCService) Compact(ctx context.Context, revision int64) error {
	return s.inner.Compact(ctx, revision)
}

// CurrentRevision forwards unchanged: the latest committed revision is global
// to the store.
func (s *namespacedMVCCService) CurrentRevision(ctx context.Context) (int64, error) {
	return s.inner.CurrentRevision(ctx)
}
