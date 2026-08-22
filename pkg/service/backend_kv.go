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

package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// Compile-time check that BackendKVService implements KVServicer.
var _ KVServicer = (*BackendKVService)(nil)

// BackendKVService implements KVServicer by delegating directly to a
// database.Backend. It is intended for embedded (non-Raft) use where
// all operations are local -- including a barrier-enabled dedicated-store
// database's own encrypting Backend() (see database.BarrierBackend).
// Consistency and write mode parameters are accepted for interface
// compatibility but are ignored because there is no Raft layer to
// coordinate with.
type BackendKVService struct {
	backend database.Backend

	// name identifies the database this service is scoped to, used only to
	// annotate a BarrierSealedError when the backend fails closed because its
	// barrier is sealed. Empty for callers that do not care (e.g. the
	// embedded SQL connector), which do not need error attribution.
	name string
}

// NewBackendKVService creates a new BackendKVService wrapping the given
// database.Backend instance.
func NewBackendKVService(backend database.Backend) *BackendKVService {
	return &BackendKVService{backend: backend}
}

// NewNamedBackendKVService creates a new BackendKVService wrapping the given
// database.Backend instance, identified by name for error attribution. Use
// this constructor when the backend may be barrier-wrapped (see
// database.NewBarrierBackend) so a sealed-barrier failure surfaces as a
// service.BarrierSealedError naming the database instead of the bare
// database.SealedError, letting transports map it to their standard
// sealed-barrier status instead of a generic internal error.
func NewNamedBackendKVService(name string, backend database.Backend) *BackendKVService {
	return &BackendKVService{backend: backend, name: name}
}

// translateSealed maps a database.SealedError (returned by a barrier-wrapped
// Backend -- see database.BarrierBackend -- when its barrier is sealed) into
// a BarrierSealedError, the typed error every transport already maps to its
// standard sealed-barrier status (HTTP 503 / gRPC Unavailable). Any other
// error, including nil, is returned unchanged.
func (s *BackendKVService) translateSealed(err error) error {
	var sealed *database.SealedError
	if errors.As(err, &sealed) {
		return &BarrierSealedError{TenantID: s.name}
	}
	return err
}

// Put stores a key-value pair via the underlying backend.
func (s *BackendKVService) Put(ctx context.Context, key string, value []byte) error {
	return s.translateSealed(s.backend.Put(ctx, []byte(key), value))
}

// PutIfAbsent stores value under key only when the key is absent.
//
// The guarantee belongs to the backend, so this asserts for the capability and
// refuses when it is missing rather than emulating it with a Get followed by a
// Put. That emulation is the precise failure this operation exists to prevent:
// two callers can both observe "absent" in the window between the two calls and
// both write, so a single-use credential is issued twice.
//
// A backend that cannot do this is not broken — a RemoteBackend, for instance,
// would need the operation added to its wire protocol first — so the refusal
// names the limitation instead of failing obscurely.
func (s *BackendKVService) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	atomicBackend, ok := s.backend.(database.AtomicBackend)
	if !ok {
		return false, database.ErrAtomicWriteUnsupported
	}
	stored, err := atomicBackend.PutIfAbsent(ctx, []byte(key), value)
	if err != nil {
		return false, s.translateSealed(err)
	}
	return stored, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, delegating to the backend's own guarded delete. A backend without
// one refuses with database.ErrAtomicWriteUnsupported rather than degrading
// to a read-then-delete, which is the window the operation exists to close.
func (s *BackendKVService) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	atomicBackend, ok := s.backend.(database.AtomicBackend)
	if !ok {
		return false, database.ErrAtomicWriteUnsupported
	}
	deleted, err := atomicBackend.CompareAndDelete(ctx, []byte(key), expected)
	if err != nil {
		return false, s.translateSealed(err)
	}
	return deleted, nil
}

// CompareAndSwap replaces key's value with next only when its stored value
// still equals expected. A backend that cannot honour it is refused rather
// than degraded to a read-then-write, which would deliver the exact race the
// capability exists to eliminate while reporting success.
func (s *BackendKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	atomicBackend, ok := s.backend.(database.AtomicBackend)
	if !ok {
		return false, database.ErrAtomicWriteUnsupported
	}
	swapped, err := atomicBackend.CompareAndSwap(ctx, []byte(key), expected, next)
	if err != nil {
		return false, s.translateSealed(err)
	}
	return swapped, nil
}

// Get retrieves a value by key. Returns KeyNotFoundError when the key
// does not exist.
func (s *BackendKVService) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := s.backend.Get(ctx, []byte(key))
	if err != nil {
		return nil, s.translateSealed(err)
	}
	if v == nil {
		return nil, &KeyNotFoundError{Key: key}
	}
	return v, nil
}

// Delete removes a key via the underlying backend.
func (s *BackendKVService) Delete(ctx context.Context, key string) error {
	return s.translateSealed(s.backend.Delete(ctx, []byte(key)))
}

// List returns sorted keys matching the given prefix.
func (s *BackendKVService) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.backend.List(ctx, prefix)
	if err != nil {
		return nil, s.translateSealed(err)
	}
	filtered := FilterInternalKeys(keys, prefix)
	sort.Strings(filtered)
	return filtered, nil
}

// Scan returns all key-value pairs matching the given prefix.
func (s *BackendKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(keys))
	for _, k := range keys {
		v, err := s.backend.Get(ctx, []byte(k))
		if err != nil {
			return nil, s.translateSealed(err)
		}
		if v != nil {
			result[k] = v
		}
	}
	return FilterInternalMap(result, prefix), nil
}

// GetStale delegates to Get. There is no Raft layer so all reads are local.
func (s *BackendKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return s.Get(ctx, key)
}

// ListStale delegates to List. There is no Raft layer so all reads are local.
func (s *BackendKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return s.List(ctx, prefix)
}

// ScanStale delegates to Scan. There is no Raft layer so all reads are local.
func (s *BackendKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return s.Scan(ctx, prefix)
}

// PutWithMode delegates to Put. The write mode is ignored because there
// is no Raft layer to coordinate with.
func (s *BackendKVService) PutWithMode(ctx context.Context, key string, value []byte, _ writemode.Mode) error {
	return s.Put(ctx, key, value)
}

// DeleteWithMode delegates to Delete. The write mode is ignored.
func (s *BackendKVService) DeleteWithMode(ctx context.Context, key string, _ writemode.Mode) error {
	return s.Delete(ctx, key)
}

// GetWithConsistency delegates to Get. Consistency is ignored.
func (s *BackendKVService) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return s.Get(ctx, key)
}

// ListWithConsistency delegates to List. Consistency is ignored.
func (s *BackendKVService) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return s.List(ctx, prefix)
}

// ScanWithConsistency delegates to Scan. Consistency is ignored.
func (s *BackendKVService) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return s.Scan(ctx, prefix)
}

// ExistsWithConsistency checks whether a key exists. Consistency is ignored.
func (s *BackendKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	v, err := s.backend.Get(ctx, []byte(key))
	if err != nil {
		return false, s.translateSealed(err)
	}
	return v != nil, nil
}

// PutEntity stores a typed entity. The entity key is constructed as
// "{entityType}/{id}" and stored via Put.
func (s *BackendKVService) PutEntity(ctx context.Context, req *PutEntityRequest) (*PutEntityResponse, error) {
	if err := validateEntityWrite(req.EntityType, req.ID, req.Data); err != nil {
		return nil, err
	}

	key := entityKey(req.EntityType, req.ID)
	if err := s.Put(ctx, key, req.Data); err != nil {
		return nil, err
	}

	now := time.Now().UnixNano()
	return &PutEntityResponse{
		Version:   1,
		Revision:  1,
		Timestamp: now,
	}, nil
}

// GetEntity retrieves a typed entity. Returns KeyNotFoundError when the
// entity does not exist.
func (s *BackendKVService) GetEntity(ctx context.Context, req *GetEntityRequest) (*GetEntityResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}
	if req.ID == "" {
		return nil, &EntityEmptyIDError{EntityType: req.EntityType}
	}

	key := entityKey(req.EntityType, req.ID)
	data, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixNano()
	return &GetEntityResponse{
		Data:      data,
		Version:   1,
		Revision:  1,
		Timestamp: now,
	}, nil
}

// DeleteEntity removes a typed entity.
func (s *BackendKVService) DeleteEntity(ctx context.Context, req *DeleteEntityRequest) (*DeleteEntityResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}
	if req.ID == "" {
		return nil, &EntityEmptyIDError{EntityType: req.EntityType}
	}

	key := entityKey(req.EntityType, req.ID)
	if err := s.Delete(ctx, key); err != nil {
		return nil, err
	}

	return &DeleteEntityResponse{
		Deleted:  true,
		Revision: 1,
	}, nil
}

// ListEntities returns a paginated list of entity keys for a given type.
func (s *BackendKVService) ListEntities(ctx context.Context, req *ListEntitiesRequest) (*ListEntitiesResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}

	prefix := entityPrefix(req.EntityType)
	if req.Prefix != "" {
		prefix += req.Prefix
	}

	allKeys, err := s.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	// Strip the entity type prefix from returned keys.
	typePrefix := entityPrefix(req.EntityType)
	stripped := make([]string, 0, len(allKeys))
	for _, k := range allKeys {
		stripped = append(stripped, strings.TrimPrefix(k, typePrefix))
	}

	totalCount := int64(len(stripped))

	// Apply page token (offset-based).
	startIdx := 0
	if req.PageToken != "" {
		offset, parseErr := strconv.Atoi(req.PageToken)
		if parseErr != nil {
			return nil, &InvalidPageTokenError{Token: req.PageToken}
		}
		startIdx = offset
	}

	if startIdx > len(stripped) {
		startIdx = len(stripped)
	}

	remaining := stripped[startIdx:]

	// Apply page size.
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = len(remaining)
	}

	var page []string
	var nextPageToken string
	if len(remaining) > pageSize {
		page = remaining[:pageSize]
		nextPageToken = strconv.Itoa(startIdx + pageSize)
	} else {
		page = remaining
	}

	return &ListEntitiesResponse{
		Keys:          page,
		NextPageToken: nextPageToken,
		TotalCount:    totalCount,
	}, nil
}
