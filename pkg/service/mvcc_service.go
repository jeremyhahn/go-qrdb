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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
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

	// GetRevision retrieves a key-value pair with full MVCC revision
	// metadata at the latest revision. Returns KeyNotFoundError when
	// the key does not exist.
	GetRevision(ctx context.Context, key string) (*KeyValue, error)

	// GetAtRevision retrieves a key-value pair as it existed at the
	// specified historical revision. Returns RevisionCompactedError
	// when the revision has been compacted, and KeyNotFoundError
	// when the key did not exist at that revision.
	GetAtRevision(ctx context.Context, key string, revision int64) (*KeyValue, error)

	// Range returns key-value pairs in the half-open interval
	// [start, end). When end is nil, only the key matching start is
	// returned. The total count of matching keys (before any limit)
	// is returned alongside the results.
	Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, error)

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
	_, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return s.translateProposeError(err)
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
// read. Uses a range scan over metadata keys.
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
		Start: start,
		End:   end,
		Opts:  []RangeOption{WithKeysOnly()},
	})
	if err != nil {
		return nil, s.translateReadError(err)
	}

	rr, ok := result.(*MVCCRangeResult)
	if !ok {
		return nil, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	keys := make([]string, len(rr.KeyValues))
	for i, kv := range rr.KeyValues {
		keys[i] = string(kv.Key)
	}
	return keys, nil
}

// PutWithLease stores a key-value pair with an attached lease through Raft
// consensus. Returns the resulting KeyValue with full revision metadata.
func (s *MVCCKVService) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*KeyValue, error) {
	cmd := protocol.EncodeMVCCPut([]byte(key), value, leaseID)
	result, err := s.host.SyncPropose(ctx, s.shardID, cmd)
	if err != nil {
		return nil, s.translateProposeError(err)
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

// Range returns key-value pairs in the half-open interval [start, end)
// using a linearizable read.
func (s *MVCCKVService) Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCRangeQuery{
		Start: start,
		End:   end,
		Opts:  opts,
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
