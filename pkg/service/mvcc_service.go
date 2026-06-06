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
	"strconv"
	"strings"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// MVCCKVServicer extends KVServicer with MVCC-aware operations that expose
// revision metadata, historical reads, and range queries. Implementations
// operate on a versioned key-value store where every mutation creates a
// new revision.
type MVCCKVServicer interface {
	KVServicer

	// PutWithLease stores a key-value pair with an attached lease.
	// When the lease expires, the key is automatically deleted.
	// Returns the resulting key-value with revision metadata.
	PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*KeyValue, error)

	// PutWithLeaseAndMode stores a key-value pair with an attached lease
	// and the specified write durability. Returns the resulting key-value
	// with revision metadata.
	PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*KeyValue, error)

	// GetRevision retrieves a key-value pair with full MVCC revision
	// metadata at the latest revision. Returns KeyNotFoundError when
	// the key does not exist.
	GetRevision(ctx context.Context, key string) (*KeyValue, error)

	// GetRevisionWithConsistency retrieves a key-value pair with full
	// MVCC revision metadata at the specified consistency level.
	// Returns KeyNotFoundError when the key does not exist.
	GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*KeyValue, error)

	// GetAtRevision retrieves a key-value pair as it existed at the
	// specified historical revision. Returns RevisionCompactedError
	// when the revision has been compacted, and KeyNotFoundError
	// when the key did not exist at that revision.
	GetAtRevision(ctx context.Context, key string, revision int64) (*KeyValue, error)

	// GetAtRevisionWithConsistency retrieves a key-value pair at a
	// specific historical revision with the specified consistency level.
	// Returns RevisionCompactedError when the revision has been compacted,
	// and KeyNotFoundError when the key did not exist at that revision.
	GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*KeyValue, error)

	// Range returns key-value pairs in the half-open interval
	// [start, end). When end is nil, only the key matching start is
	// returned. The total count of matching keys (before any limit)
	// is returned alongside the results.
	Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, error)

	// RangeWithConsistency returns key-value pairs in the half-open
	// interval [start, end) with the specified consistency level.
	// When end is nil, only the key matching start is returned.
	RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...RangeOption) ([]*KeyValue, int64, error)

	// Compact discards all MVCC revisions older than the specified
	// revision, reclaiming storage. Compacted revisions can no longer
	// be read via GetAtRevision or watched via WithStartRevision.
	Compact(ctx context.Context, revision int64) error

	// CurrentRevision returns the latest committed MVCC revision.
	CurrentRevision(ctx context.Context) (int64, error)
}

// ---------------------------------------------------------------------------
// Error interfaces for cross-package error matching
// ---------------------------------------------------------------------------
//
// These interfaces allow the service layer to translate errors from the
// MVCC store without importing the mvcc package, preventing import cycles.
// The mvcc package's error types naturally satisfy these interfaces.

// keyNotFoundErr matches errors that carry a missing key name.
type keyNotFoundErr interface {
	error
	// KeyNotFound returns the key that was not found.
	KeyNotFound() string
}

// revisionCompactedErr matches errors about compacted revisions.
type revisionCompactedErr interface {
	error
	// RevisionCompacted returns the requested and compacted revisions.
	RevisionCompacted() (requested, compacted int64)
}

// revisionNotFoundErr matches errors about missing revisions.
type revisionNotFoundErr interface {
	error
	// RevisionNotFound returns the key and missing revision.
	RevisionNotFound() (key string, revision int64)
}

// ---------------------------------------------------------------------------
// MVCCKVService implementation
// ---------------------------------------------------------------------------

// MVCCKVService implements MVCCKVServicer backed by a QuicRaft host running
// an MVCCStateMachine. Write operations are proposed through Raft consensus
// using the extended wire protocol. Read operations use linearizable reads
// via SyncRead with typed MVCC query objects.
type MVCCKVService struct {
	host    Host
	shardID uint64
}

// NewMVCCKVService creates a new MVCCKVServicer that operates on the specified
// shard. The shard must be running an MVCCStateMachine for MVCC operations
// to succeed.
func NewMVCCKVService(host Host, shardID uint64) MVCCKVServicer {
	return &MVCCKVService{
		host:    host,
		shardID: shardID,
	}
}

// Put stores a key-value pair through Raft consensus with MVCC revision
// tracking. The value is stored with a zero lease ID.
func (s *MVCCKVService) Put(ctx context.Context, key string, value []byte) error {
	cmd := protocol.EncodeMVCCPut([]byte(key), value, 0)
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	// Surface a state-machine-encoded application error (e.g. a unique-index
	// violation that rejected the write without stalling consensus).
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// Get retrieves the latest value for a key with a linearizable read.
// Returns KeyNotFoundError when the key does not exist.
func (s *MVCCKVService) Get(ctx context.Context, key string) ([]byte, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCGetQuery{Key: []byte(key)})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv.Value, nil
}

// Delete removes a key through Raft consensus with MVCC tombstone tracking.
func (s *MVCCKVService) Delete(ctx context.Context, key string) error {
	cmd := protocol.EncodeMVCCDelete([]byte(key))
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// List returns sorted keys matching the given prefix with a linearizable
// read. Uses a range scan over metadata keys. Internal system keys are
// excluded unless the prefix itself targets the system key namespace.
func (s *MVCCKVService) List(ctx context.Context, prefix string) ([]string, error) {
	// For MVCC, use a range query with KeysOnly to get all keys.
	var start, end []byte
	if prefix == "" {
		start = []byte{0x00}
		end = []byte{0xFF}
	} else {
		start = []byte(prefix)
		end = prefixEnd(start)
	}

	result, err := s.host.SyncRead(ctx, s.shardID, MVCCRangeQuery{
		Start:    start,
		End:      end,
		KeysOnly: true,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	skipFilter := isSystemPrefix(prefix)
	keys := make([]string, 0, len(rr.KeyValues))
	for _, kv := range rr.KeyValues {
		k := string(kv.Key)
		if skipFilter || !strings.HasPrefix(k, systemKeyPrefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// Scan returns all key-value pairs matching the given prefix. Uses a
// PrefixQuery for compatibility with the basic FastStateMachine. Internal
// system keys (prefixed with "__sys:") are excluded from the results
// unless the prefix itself targets the system key namespace.
func (s *MVCCKVService) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	query := &statemachine.PrefixQuery{
		Prefix: []byte(prefix),
		Limit:  0,
	}
	result, err := s.host.SyncRead(ctx, s.shardID, query)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}
	data, ok := result.(map[string][]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return filterSystemMap(data, prefix), nil
}

// GetStale retrieves a value by key from the local state machine without
// Raft coordination. Returns potentially stale data but with sub-millisecond
// latency. Returns KeyNotFoundError when the key does not exist.
func (s *MVCCKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	result, err := s.host.StaleRead(ctx, s.shardID, MVCCGetQuery{Key: []byte(key)})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv.Value, nil
}

// ListStale returns sorted keys matching the given prefix from the local
// state machine without Raft coordination. Returns potentially stale data
// but with sub-millisecond latency. Internal system keys are excluded
// unless the prefix itself targets the system key namespace.
func (s *MVCCKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	var start, end []byte
	if prefix == "" {
		start = []byte{0x00}
		end = []byte{0xFF}
	} else {
		start = []byte(prefix)
		end = prefixEnd(start)
	}

	result, err := s.host.StaleRead(ctx, s.shardID, MVCCRangeQuery{
		Start:    start,
		End:      end,
		KeysOnly: true,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	skipFilter := isSystemPrefix(prefix)
	keys := make([]string, 0, len(rr.KeyValues))
	for _, kv := range rr.KeyValues {
		k := string(kv.Key)
		if skipFilter || !strings.HasPrefix(k, systemKeyPrefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// ScanStale returns all key-value pairs matching the given prefix from the
// local state machine without Raft coordination. Returns potentially stale
// data but with sub-millisecond latency. Internal system keys are excluded
// unless the prefix itself targets the system key namespace.
func (s *MVCCKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	query := &statemachine.PrefixQuery{
		Prefix: []byte(prefix),
		Limit:  0,
	}
	result, err := s.host.StaleRead(ctx, s.shardID, query)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}
	data, ok := result.(map[string][]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return filterSystemMap(data, prefix), nil
}

// ---------------------------------------------------------------------------
// Write mode operations
// ---------------------------------------------------------------------------

// PutWithMode stores a key-value pair with the specified write durability
// through Raft consensus with MVCC revision tracking. The value is stored
// with a zero lease ID.
func (s *MVCCKVService) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	cmd := protocol.EncodeMVCCPut([]byte(key), value, 0)
	result, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
	if err != nil {
		return s.translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return resultErr
	}
	return nil
}

// DeleteWithMode removes a key with the specified write durability through
// Raft consensus with MVCC tombstone tracking.
func (s *MVCCKVService) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	cmd := protocol.EncodeMVCCDelete([]byte(key))
	_, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Consistency-level read operations
// ---------------------------------------------------------------------------

// GetWithConsistency retrieves the latest value for a key with the specified
// consistency level. Returns KeyNotFoundError when the key does not exist.
func (s *MVCCKVService) GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, MVCCGetQuery{Key: []byte(key)}, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv.Value, nil
}

// ListWithConsistency returns sorted keys matching the given prefix with
// the specified consistency level. Internal system keys are excluded unless
// the prefix itself targets the system key namespace.
func (s *MVCCKVService) ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error) {
	var start, end []byte
	if prefix == "" {
		start = []byte{0x00}
		end = []byte{0xFF}
	} else {
		start = []byte(prefix)
		end = prefixEnd(start)
	}

	result, err := s.host.ReadWithConsistency(ctx, s.shardID, MVCCRangeQuery{
		Start:    start,
		End:      end,
		KeysOnly: true,
	}, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	skipFilter := isSystemPrefix(prefix)
	keys := make([]string, 0, len(rr.KeyValues))
	for _, kv := range rr.KeyValues {
		k := string(kv.Key)
		if skipFilter || !strings.HasPrefix(k, systemKeyPrefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// ScanWithConsistency returns all key-value pairs matching the given prefix
// with the specified consistency level. Internal system keys are excluded
// unless the prefix itself targets the system key namespace.
func (s *MVCCKVService) ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error) {
	query := &statemachine.PrefixQuery{
		Prefix: []byte(prefix),
		Limit:  0,
	}
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, query, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}
	if result == nil {
		return nil, nil
	}
	data, ok := result.(map[string][]byte)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}
	return filterSystemMap(data, prefix), nil
}

// ExistsWithConsistency checks whether a key exists with the specified
// consistency level. Returns true when the key is present and false when
// it is not found. Other errors are propagated to the caller.
func (s *MVCCKVService) ExistsWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (bool, error) {
	_, err := s.host.ReadWithConsistency(ctx, s.shardID, MVCCGetQuery{Key: []byte(key)}, opts)
	if err != nil {
		var knf keyNotFoundErr
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, s.translateReadError(err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// MVCC-specific operations
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease through Raft
// consensus. Returns the resulting KeyValue with full revision metadata.
func (s *MVCCKVService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*KeyValue, error) {
	cmd := protocol.EncodeMVCCPut([]byte(key), value, leaseID)
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return nil, s.translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, resultErr
	}

	// The state machine returns the assigned revision in Result.Value.
	// Perform a follow-up read to get the full KeyValue with metadata.
	rev := int64(result.Value)

	readResult, err := s.host.SyncRead(ctx, s.shardID, MVCCGetAtRevisionQuery{
		Key:      []byte(key),
		Revision: rev,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := readResult.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv, nil
}

// PutWithLeaseAndMode stores a key-value pair with an attached lease and
// the specified write durability through Raft consensus. Returns the
// resulting KeyValue with full revision metadata.
func (s *MVCCKVService) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*KeyValue, error) {
	cmd := protocol.EncodeMVCCPut([]byte(key), value, leaseID)
	result, err := s.host.ProposeWithMode(ctx, s.shardID, cmd, mode)
	if err != nil {
		return nil, s.translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return nil, resultErr
	}

	// The state machine returns the assigned revision in Result.Value.
	// Perform a follow-up read to get the full KeyValue with metadata.
	rev := int64(result.Value)

	readResult, err := s.host.SyncRead(ctx, s.shardID, MVCCGetAtRevisionQuery{
		Key:      []byte(key),
		Revision: rev,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := readResult.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv, nil
}

// GetRevision retrieves a key-value pair with full MVCC revision metadata
// at the latest revision using a linearizable read.
func (s *MVCCKVService) GetRevision(ctx context.Context, key string) (*KeyValue, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCGetQuery{Key: []byte(key)})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv, nil
}

// GetRevisionWithConsistency retrieves a key-value pair with full MVCC
// revision metadata at the specified consistency level. Returns
// KeyNotFoundError when the key does not exist.
func (s *MVCCKVService) GetRevisionWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) (*KeyValue, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, MVCCGetQuery{Key: []byte(key)}, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv, nil
}

// GetAtRevision retrieves a key-value pair at a specific historical revision
// using a linearizable read.
func (s *MVCCKVService) GetAtRevision(ctx context.Context, key string, revision int64) (*KeyValue, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCGetAtRevisionQuery{
		Key:      []byte(key),
		Revision: revision,
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv, nil
}

// GetAtRevisionWithConsistency retrieves a key-value pair at a specific
// historical revision with the specified consistency level. Returns
// RevisionCompactedError when the revision has been compacted, and
// KeyNotFoundError when the key did not exist at that revision.
func (s *MVCCKVService) GetAtRevisionWithConsistency(ctx context.Context, key string, revision int64, opts consistency.ReadOptions) (*KeyValue, error) {
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, MVCCGetAtRevisionQuery{
		Key:      []byte(key),
		Revision: revision,
	}, opts)
	if err != nil {
		return nil, s.translateReadError(err)
	}

	kv, ok := result.(*KeyValue)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return kv, nil
}

// Range returns key-value pairs in the half-open interval [start, end)
// using a linearizable read.
func (s *MVCCKVService) Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, error) {
	// Apply functional options to build flat query fields.
	var ro RangeOptions
	for _, o := range opts {
		o(&ro)
	}
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCRangeQuery{
		Start:     start,
		End:       end,
		Revision:  ro.Revision,
		Limit:     ro.Limit,
		CountOnly: ro.CountOnly,
		KeysOnly:  ro.KeysOnly,
	})
	if err != nil {
		return nil, 0, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return nil, 0, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return rr.KeyValues, rr.Revision, nil
}

// RangeWithConsistency returns key-value pairs in the half-open interval
// [start, end) with the specified consistency level.
func (s *MVCCKVService) RangeWithConsistency(ctx context.Context, start, end []byte, opts consistency.ReadOptions, rangeOpts ...RangeOption) ([]*KeyValue, int64, error) {
	// Apply functional options to build flat query fields.
	var ro RangeOptions
	for _, o := range rangeOpts {
		o(&ro)
	}
	result, err := s.host.ReadWithConsistency(ctx, s.shardID, MVCCRangeQuery{
		Start:     start,
		End:       end,
		Revision:  ro.Revision,
		Limit:     ro.Limit,
		CountOnly: ro.CountOnly,
		KeysOnly:  ro.KeysOnly,
	}, opts)
	if err != nil {
		return nil, 0, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return nil, 0, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return rr.KeyValues, rr.Revision, nil
}

// Compact discards all MVCC revisions older than the specified revision
// through Raft consensus.
func (s *MVCCKVService) Compact(ctx context.Context, revision int64) error {
	cmd := protocol.EncodeMVCCCompact(revision)
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
	}
	return nil
}

// CurrentRevision returns the latest committed MVCC revision using a
// linearizable read.
func (s *MVCCKVService) CurrentRevision(ctx context.Context) (int64, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCRevisionQuery{})
	if err != nil {
		return 0, s.translateReadError(err)
	}

	rev, ok := result.(int64)
	if !ok {
		return 0, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	return rev, nil
}

// PutEntity stores a typed entity through MVCC-aware Raft consensus.
func (s *MVCCKVService) PutEntity(ctx context.Context, req *PutEntityRequest) (*PutEntityResponse, error) {
	if err := validateEntityWrite(req.EntityType, req.ID, req.Data); err != nil {
		return nil, err
	}

	key := entityKey(req.EntityType, req.ID)
	if err := s.Put(ctx, key, req.Data); err != nil {
		return nil, err
	}

	rev, _ := s.CurrentRevision(ctx)
	return &PutEntityResponse{
		Version:   1,
		Revision:  rev,
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// GetEntity retrieves a typed entity with a linearizable read.
func (s *MVCCKVService) GetEntity(ctx context.Context, req *GetEntityRequest) (*GetEntityResponse, error) {
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

	rev, _ := s.CurrentRevision(ctx)
	return &GetEntityResponse{
		Data:      data,
		Version:   1,
		Revision:  rev,
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// DeleteEntity removes a typed entity through MVCC-aware Raft consensus.
func (s *MVCCKVService) DeleteEntity(ctx context.Context, req *DeleteEntityRequest) (*DeleteEntityResponse, error) {
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

	rev, _ := s.CurrentRevision(ctx)
	return &DeleteEntityResponse{
		Deleted:  true,
		Revision: rev,
	}, nil
}

// ListEntities returns a paginated list of entity keys for a given type.
func (s *MVCCKVService) ListEntities(ctx context.Context, req *ListEntitiesRequest) (*ListEntitiesResponse, error) {
	if req.EntityType == "" {
		return nil, &EntityEmptyTypeError{}
	}

	prefix := entityPrefix(req.EntityType)
	if req.Prefix != "" {
		prefix = prefix + req.Prefix
	}

	allKeys, err := s.List(ctx, prefix)
	if err != nil {
		return nil, err
	}

	typePrefix := entityPrefix(req.EntityType)
	stripped := make([]string, 0, len(allKeys))
	for _, k := range allKeys {
		stripped = append(stripped, strings.TrimPrefix(k, typePrefix))
	}

	totalCount := int64(len(stripped))

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

	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = len(remaining)
	}

	var page []string
	var nextPageToken string
	if pageSize >= len(remaining) {
		page = remaining
	} else {
		page = remaining[:pageSize]
		nextPageToken = strconv.Itoa(startIdx + pageSize)
	}

	return &ListEntitiesResponse{
		Keys:          page,
		NextPageToken: nextPageToken,
		TotalCount:    totalCount,
	}, nil
}

// translateProposeError converts QuicRaft proposal errors to service-level
// typed errors.
func (s *MVCCKVService) translateProposeError(err error) error {
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}
	return &ProposeError{Err: err}
}

// translateReadError converts QuicRaft read errors and MVCC store errors
// to service-level typed errors. Uses error interfaces to avoid importing
// the mvcc package directly, preventing import cycles.
func (s *MVCCKVService) translateReadError(err error) error {
	// MVCC key not found.
	var knf keyNotFoundErr
	if errors.As(err, &knf) {
		return &KeyNotFoundError{Key: knf.KeyNotFound()}
	}

	// MVCC revision compacted.
	var rce revisionCompactedErr
	if errors.As(err, &rce) {
		requested, compacted := rce.RevisionCompacted()
		return &RevisionCompactedError{
			Requested: requested,
			Compacted: compacted,
		}
	}

	// MVCC revision not found.
	var rnf revisionNotFoundErr
	if errors.As(err, &rnf) {
		_, revision := rnf.RevisionNotFound()
		return &RevisionNotFoundError{Revision: revision}
	}

	// Not leader.
	var nle *quicraft.NotLeaderError
	if errors.As(err, &nle) {
		return &NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}

	return &ReadError{Err: err}
}

// prefixEnd computes the key immediately after the given prefix. Returns nil
// if the prefix is all 0xFF bytes.
func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}
