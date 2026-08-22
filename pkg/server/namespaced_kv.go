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
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// namespacedKVService wraps a KVServicer and transparently prepends a
// namespace prefix to all keys. This provides per-tenant keyspace isolation
// within a shared Raft state machine without requiring a separate shard.
//
// The prefix format is "{namespace}/" (e.g., "acme/"). All key operations
// add the prefix on writes and strip it on reads.
//
// Enumeration additionally re-applies the internal-key guard on the stripped
// keys, for the same reason ValidateRestoreRequest runs on the unprefixed
// request: once the namespace is prepended, "acme/reports/__tbl:…" is an
// ordinary user key, so the inner service's own guard admits it and every
// reserved namespace — relational rows and index entries, the RBAC store, the
// time-series keyspace — would be disclosed to any caller holding KV access to
// the namespace. The guard belongs wherever the key is in the form the caller
// wrote it, which for a namespaced view is here.
type namespacedKVService struct {
	inner  service.KVServicer
	prefix string // namespace + "/"
}

// Compile-time interface check.
var _ service.KVServicer = (*namespacedKVService)(nil)

// newNamespacedKVService creates a KVServicer that prefixes all keys with
// "{namespace}/". namespace must be non-empty and must not contain "/".
func newNamespacedKVService(inner service.KVServicer, namespace string) *namespacedKVService {
	return &namespacedKVService{
		inner:  inner,
		prefix: namespace + "/",
	}
}

// Put stores a key-value pair under the namespace prefix.
func (s *namespacedKVService) Put(ctx context.Context, key string, value []byte) error {
	return s.inner.Put(ctx, s.prefix+key, value)
}

// PutIfAbsent stores value under the prefixed key only when that key is absent.
//
// The prefix is applied before the condition is evaluated, so the check is
// scoped to this tenant's namespace: two tenants may each hold the same logical
// key, and one tenant's write must never be declined because another tenant
// already used that name.
func (s *namespacedKVService) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return s.inner.PutIfAbsent(ctx, s.prefix+key, value)
}

// CompareAndDelete removes the namespaced key only when its stored value
// still equals expected.
func (s *namespacedKVService) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return s.inner.CompareAndDelete(ctx, s.prefix+key, expected)
}

// CompareAndSwap replaces the prefixed key's value only when it still equals
// expected.
func (s *namespacedKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return s.inner.CompareAndSwap(ctx, s.prefix+key, expected, next)
}

// Get retrieves a value by prefixed key.
func (s *namespacedKVService) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, s.prefix+key)
}

// Delete removes a key under the namespace prefix.
func (s *namespacedKVService) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, s.prefix+key)
}

// List returns sorted keys matching the given prefix within the namespace.
// The namespace prefix is stripped from the returned keys.
func (s *namespacedKVService) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.inner.List(ctx, s.prefix+prefix)
	if err != nil {
		return nil, err
	}
	return s.visibleKeys(keys, prefix), nil
}

// Scan returns all key-value pairs matching the given prefix within the
// namespace. The namespace prefix is stripped from the returned keys.
func (s *namespacedKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	result, err := s.inner.Scan(ctx, s.prefix+prefix)
	if err != nil {
		return nil, err
	}
	return s.visiblePairs(result, prefix), nil
}

// GetStale retrieves a value by prefixed key from the local state machine
// without Raft coordination.
func (s *namespacedKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return s.inner.GetStale(ctx, s.prefix+key)
}

// ListStale returns sorted keys matching the given prefix within the namespace
// from the local state machine without Raft coordination.
func (s *namespacedKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.inner.ListStale(ctx, s.prefix+prefix)
	if err != nil {
		return nil, err
	}
	return s.visibleKeys(keys, prefix), nil
}

// ScanStale returns all key-value pairs matching the given prefix within the
// namespace from the local state machine without Raft coordination.
func (s *namespacedKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	result, err := s.inner.ScanStale(ctx, s.prefix+prefix)
	if err != nil {
		return nil, err
	}
	return s.visiblePairs(result, prefix), nil
}

// PutWithMode stores a key-value pair under the namespace prefix with the
// specified write durability.
func (s *namespacedKVService) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	return s.inner.PutWithMode(ctx, s.prefix+key, value, mode)
}

// DeleteWithMode removes a key under the namespace prefix with the specified
// write durability.
func (s *namespacedKVService) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	return s.inner.DeleteWithMode(ctx, s.prefix+key, mode)
}

// GetWithConsistency retrieves a value by prefixed key with the specified
// consistency level.
func (s *namespacedKVService) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	return s.inner.GetWithConsistency(ctx, s.prefix+key, opts)
}

// ListWithConsistency returns sorted keys matching the given prefix within
// the namespace with the specified consistency level.
func (s *namespacedKVService) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	keys, err := s.inner.ListWithConsistency(ctx, s.prefix+prefix, opts)
	if err != nil {
		return nil, err
	}
	return s.visibleKeys(keys, prefix), nil
}

// ScanWithConsistency returns all key-value pairs matching the given prefix
// within the namespace with the specified consistency level.
func (s *namespacedKVService) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	result, err := s.inner.ScanWithConsistency(ctx, s.prefix+prefix, opts)
	if err != nil {
		return nil, err
	}
	return s.visiblePairs(result, prefix), nil
}

// ExistsWithConsistency checks whether a prefixed key exists with the
// specified consistency level.
func (s *namespacedKVService) ExistsWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (bool, error) {
	return s.inner.ExistsWithConsistency(ctx, s.prefix+key, opts)
}

// PutEntity stores a typed entity under the namespace prefix.
func (s *namespacedKVService) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	prefixed := *req
	prefixed.ID = s.prefix + req.ID
	return s.inner.PutEntity(ctx, &prefixed)
}

// GetEntity retrieves a typed entity under the namespace prefix.
func (s *namespacedKVService) GetEntity(ctx context.Context, req *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	prefixed := *req
	prefixed.ID = s.prefix + req.ID
	return s.inner.GetEntity(ctx, &prefixed)
}

// DeleteEntity removes a typed entity under the namespace prefix.
func (s *namespacedKVService) DeleteEntity(ctx context.Context, req *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	prefixed := *req
	prefixed.ID = s.prefix + req.ID
	return s.inner.DeleteEntity(ctx, &prefixed)
}

// ListEntities returns a paginated list of entity keys under the namespace.
// Entity IDs have the namespace prefix stripped.
func (s *namespacedKVService) ListEntities(ctx context.Context, req *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	prefixed := *req
	prefixed.Prefix = s.prefix + req.Prefix
	resp, err := s.inner.ListEntities(ctx, &prefixed)
	if err != nil {
		return nil, err
	}
	stripped := make([]string, len(resp.Keys))
	for i, k := range resp.Keys {
		stripped[i] = strings.TrimPrefix(k, s.prefix)
	}
	return &service.ListEntitiesResponse{
		Keys:          stripped,
		NextPageToken: resp.NextPageToken,
		TotalCount:    resp.TotalCount,
	}, nil
}

// stripPrefix removes the namespace prefix from each key in the slice.
func (s *namespacedKVService) stripPrefix(keys []string) []string {
	result := make([]string, len(keys))
	for i, k := range keys {
		result[i] = strings.TrimPrefix(k, s.prefix)
	}
	return result
}

// stripMapPrefix removes the namespace prefix from all keys in the map.
func (s *namespacedKVService) stripMapPrefix(m map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(m))
	for k, v := range m {
		result[strings.TrimPrefix(k, s.prefix)] = v
	}
	return result
}

// visibleKeys strips the namespace from keys and applies the internal-key
// guard against prefix as the caller wrote it (see the type's doc comment).
func (s *namespacedKVService) visibleKeys(keys []string, prefix string) []string {
	return service.FilterInternalKeys(s.stripPrefix(keys), prefix)
}

// visiblePairs strips the namespace from m's keys and applies the
// internal-key guard against prefix as the caller wrote it.
func (s *namespacedKVService) visiblePairs(m map[string][]byte, prefix string) map[string][]byte {
	return service.FilterInternalMap(s.stripMapPrefix(m), prefix)
}
