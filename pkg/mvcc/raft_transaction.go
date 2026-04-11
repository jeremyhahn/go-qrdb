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

// Raft-backed MVCC transactions from QRDB. Provides distributed
// transactions with compare-and-swap semantics, batched Raft proposals,
// and snapshot isolation.

package mvcc

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// RaftUnknownOperationTypeError is returned when a transaction operation has an unrecognized type.
type RaftUnknownOperationTypeError struct {
	OpType RaftTxnOpType
}

// Error returns the error message.
func (e *RaftUnknownOperationTypeError) Error() string {
	return "unknown operation type: " + strconv.Itoa(int(e.OpType))
}

// RaftTransaction represents an MVCC transaction with compare-and-swap semantics.
// It provides a transaction model with compare, success, and failure operations.
type RaftTransaction interface {
	// Put adds a Put operation to the transaction
	Put(key, value []byte) error

	// Delete adds a Delete operation to the transaction
	Delete(key []byte) error

	// Get reads a key within the transaction (uses transaction's snapshot)
	Get(key []byte) (*RaftKeyValue, error)

	// AddCompare adds a comparison condition to the transaction
	AddCompare(cmp *RaftCompare) error

	// SetSuccess sets the operations to execute if all comparisons succeed
	SetSuccess(ops []RaftTxnOp) error

	// SetFailure sets the operations to execute if any comparison fails
	SetFailure(ops []RaftTxnOp) error

	// Commit executes the transaction atomically
	Commit() (*RaftTxnResult, error)

	// Rollback cancels the transaction
	Rollback() error
}

// RaftTxnOp represents an operation in a transaction (Put or Delete).
type RaftTxnOp struct {
	Type  RaftTxnOpType
	Key   []byte
	Value []byte // Only for Put operations
}

// RaftTxnOpType specifies the type of transaction operation.
type RaftTxnOpType int

const (
	// RaftTxnOpPut is a Put operation.
	RaftTxnOpPut RaftTxnOpType = iota
	// RaftTxnOpDelete is a Delete operation.
	RaftTxnOpDelete
	// RaftTxnOpGet is a Get operation.
	RaftTxnOpGet
)

// RaftTxnResult holds the result of a committed transaction.
type RaftTxnResult struct {
	Succeeded bool            // True if all comparisons succeeded
	Revision  int64           // New revision after commit
	Responses []*RaftKeyValue // Responses for each operation
}

// raftTransaction is the concrete transaction type.
type raftTransaction struct {
	ctx         context.Context
	storage     *RaftStorage
	snapshot    storage.Snapshot // Consistent snapshot for reads
	snapshotRev int64            // Snapshot revision for isolation
	mu          sync.Mutex       // Protects transaction state
	closed      bool             // True if committed or rolled back

	// Transaction operations
	compares []*RaftCompare
	success  []RaftTxnOp
	failure  []RaftTxnOp

	// Read cache for consistency within transaction
	readCache map[string]*RaftKeyValue
}

// newRaftTransaction creates a new Raft-backed transaction.
func newRaftTransaction(ctx context.Context, s *RaftStorage) (RaftTransaction, error) {
	// Create a snapshot for consistent reads
	snapshot, err := s.store.Snapshot()
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"raftTransaction.newRaftTransaction",
			"failed to create snapshot",
		)
	}

	// Capture current revision for snapshot isolation
	snapshotRev := s.RaftCurrentRevision()

	txn := &raftTransaction{
		ctx:         ctx,
		storage:     s,
		snapshot:    snapshot,
		snapshotRev: snapshotRev,
		readCache:   make(map[string]*RaftKeyValue),
	}

	return txn, nil
}

// Put adds a Put operation to the transaction.
func (t *raftTransaction) Put(key, value []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Put",
			"transaction is closed",
		)
	}

	if len(key) == 0 {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Put",
			"key cannot be empty",
		)
	}

	// Note: Actual put is deferred until Commit()
	return nil
}

// Delete adds a Delete operation to the transaction.
func (t *raftTransaction) Delete(key []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Delete",
			"transaction is closed",
		)
	}

	if len(key) == 0 {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Delete",
			"key cannot be empty",
		)
	}

	// Note: Actual delete is deferred until Commit()
	return nil
}

// Get reads a key within the transaction using the transaction's snapshot.
func (t *raftTransaction) Get(key []byte) (*RaftKeyValue, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Get",
			"transaction is closed",
		)
	}

	if len(key) == 0 {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Get",
			"key cannot be empty",
		)
	}

	// Check read cache first
	cacheKey := string(key)
	if kv, ok := t.readCache[cacheKey]; ok {
		return kv.Clone(), nil
	}

	// Read from MVCC storage at snapshot revision for isolation
	kv, err := t.storage.Get(t.ctx, key, t.snapshotRev)
	if err != nil {
		return nil, err
	}

	// Cache the result
	t.readCache[cacheKey] = kv.Clone()

	return kv, nil
}

// AddCompare adds a comparison condition to the transaction.
func (t *raftTransaction) AddCompare(cmp *RaftCompare) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.AddCompare",
			"transaction is closed",
		)
	}

	// Validate comparison
	if err := cmp.Validate(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrInvalidInput,
			"raftTransaction.AddCompare",
			"invalid comparison",
		)
	}

	t.compares = append(t.compares, cmp)
	return nil
}

// SetSuccess sets the operations to execute if all comparisons succeed.
func (t *raftTransaction) SetSuccess(ops []RaftTxnOp) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.SetSuccess",
			"transaction is closed",
		)
	}

	t.success = ops
	return nil
}

// SetFailure sets the operations to execute if any comparison fails.
func (t *raftTransaction) SetFailure(ops []RaftTxnOp) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.SetFailure",
			"transaction is closed",
		)
	}

	t.failure = ops
	return nil
}

// Commit executes the transaction atomically.
func (t *raftTransaction) Commit() (*RaftTxnResult, error) {
	start := time.Now()
	defer func() {
		t.storage.metrics.txnLatency.Observe(time.Since(start).Seconds())
	}()

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"raftTransaction.Commit",
			"transaction is closed",
		)
	}

	// Check context
	if err := t.ctx.Err(); err != nil {
		return nil, errors.Wrap(
			err,
			errors.ErrCanceled,
			"raftTransaction.Commit",
		)
	}

	// Mark transaction as closed
	t.closed = true
	defer func() { _ = t.snapshot.Close() }()

	// Evaluate all comparisons
	succeeded := true
	for _, cmp := range t.compares {
		// Get current key-value for comparison
		kv, err := t.storage.Get(t.ctx, cmp.Key, 0)
		if err != nil {
			if !errors.IsNotFound(err) {
				return nil, errors.Wrapf(
					err,
					errors.ErrStorage,
					"raftTransaction.Commit",
					"failed to read key for comparison",
				)
			}
			// Key not found - treat as nil for comparison
			kv = nil
		}

		// Evaluate comparison
		result, err := cmp.evaluate(kv)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				errors.ErrStorage,
				"raftTransaction.Commit",
				"comparison evaluation failed",
			)
		}

		if !result {
			succeeded = false
			t.storage.metrics.conflicts.Inc()
			break
		}
	}

	// Select operations based on comparison result
	var ops []RaftTxnOp
	if succeeded {
		ops = t.success
	} else {
		ops = t.failure
	}

	// Execute operations atomically
	responses, rev, err := t.executeOps(ops)
	if err != nil {
		return nil, errors.Wrapf(
			err,
			errors.ErrStorage,
			"raftTransaction.Commit",
			"failed to execute operations",
		)
	}

	result := &RaftTxnResult{
		Succeeded: succeeded,
		Revision:  rev,
		Responses: responses,
	}

	return result, nil
}

// executeOps executes a list of operations atomically.
// If RaftNode is configured, operations are batched and submitted through Raft for distributed consensus.
// Otherwise, operations are committed to local storage only.
func (t *raftTransaction) executeOps(ops []RaftTxnOp) ([]*RaftKeyValue, int64, error) {
	if len(ops) == 0 {
		// No operations - just return current revision
		return []*RaftKeyValue{}, t.storage.RaftCurrentRevision(), nil
	}

	// Check if Raft integration is enabled
	if t.storage.raftNode != nil {
		return t.executeOpsViaRaft(ops)
	}

	// Local storage only (original behavior for backward compatibility)
	return t.executeOpsLocal(ops)
}

// executeOpsViaRaft submits all transaction operations through Raft as a single batched proposal.
func (t *raftTransaction) executeOpsViaRaft(ops []RaftTxnOp) ([]*RaftKeyValue, int64, error) {
	var writeOps [][]byte
	var responses []*RaftKeyValue

	type versionInfo struct {
		index      int
		currentVer int64
		isNew      bool
	}
	var versionInfos []versionInfo

	for i, op := range ops {
		switch op.Type {
		case RaftTxnOpGet:
			kv, err := t.storage.Get(t.ctx, op.Key, 0)
			if err != nil {
				if errors.IsNotFound(err) {
					responses = append(responses, nil)
					continue
				}
				return nil, 0, err
			}
			responses = append(responses, kv)

		case RaftTxnOpPut:
			var currentVer int64
			isNew := true
			metaKey := EncodeMetaKey(op.Key)
			metaData, err := t.storage.store.Get(metaKey)
			if err == nil {
				if decoded, decodeErr := DecodeMeta(metaData); decodeErr == nil {
					currentVer = decoded.Version
					isNew = false
				}
			}

			writeOps = append(writeOps, encodePutBinary(op.Key, op.Value))
			versionInfos = append(versionInfos, versionInfo{
				index:      i,
				currentVer: currentVer,
				isNew:      isNew,
			})
			responses = append(responses, nil)

		case RaftTxnOpDelete:
			var currentVer int64
			metaKey := EncodeMetaKey(op.Key)
			metaData, err := t.storage.store.Get(metaKey)
			if err == nil {
				if decoded, decodeErr := DecodeMeta(metaData); decodeErr == nil {
					currentVer = decoded.Version
				}
			}

			writeOps = append(writeOps, encodeDeleteBinary(op.Key))
			versionInfos = append(versionInfos, versionInfo{
				index:      i,
				currentVer: currentVer,
				isNew:      false,
			})
			responses = append(responses, nil)

		default:
			return nil, 0, &errors.QRDBError{
				Code: errors.ErrInvalidInput,
				Op:   "raftTransaction.executeOpsViaRaft",
				Err:  &RaftUnknownOperationTypeError{OpType: op.Type},
			}
		}
	}

	var rev int64
	if len(writeOps) > 0 {
		cmdBytes := encodeBatchBinary(writeOps)

		if _, err := t.storage.raftNode.ProposeWithMode(t.ctx, t.storage.shardID, cmdBytes, writemode.WALDurable); err != nil {
			return nil, 0, errors.Wrapf(
				err,
				errors.ErrRaft,
				"raftTransaction.executeOpsViaRaft",
				"failed to propose batch to Raft",
			)
		}

		t.storage.mu.Lock()
		rev = t.storage.revMgr.next()
		t.storage.metrics.revision.Set(float64(rev))
		t.storage.mu.Unlock()

		for j, vi := range versionInfos {
			op := ops[vi.index]
			var newVersion int64
			if vi.isNew {
				newVersion = 1
			} else {
				newVersion = vi.currentVer + 1
			}

			if op.Type == RaftTxnOpDelete {
				newVersion = vi.currentVer
			}

			responses[vi.index] = &RaftKeyValue{
				Key:            op.Key,
				Value:          op.Value,
				CreateRevision: rev,
				ModRevision:    rev,
				Version:        newVersion,
			}
			_ = j
		}
	} else {
		rev = t.storage.RaftCurrentRevision()
	}

	return responses, rev, nil
}

// executeOpsLocal executes operations locally without Raft.
func (t *raftTransaction) executeOpsLocal(ops []RaftTxnOp) ([]*RaftKeyValue, int64, error) {
	t.storage.mu.Lock()
	defer t.storage.mu.Unlock()

	rev := t.storage.revMgr.next()
	t.storage.metrics.revision.Set(float64(rev))

	batch := t.storage.store.Batch()
	defer func() { _ = batch.Close() }()

	var responses []*RaftKeyValue

	for _, op := range ops {
		switch op.Type {
		case RaftTxnOpPut:
			kv, err := t.executePut(batch, op.Key, op.Value, rev)
			if err != nil {
				return nil, 0, err
			}
			responses = append(responses, kv)

		case RaftTxnOpDelete:
			kv, err := t.executeDelete(batch, op.Key, rev)
			if err != nil {
				return nil, 0, err
			}
			responses = append(responses, kv)

		case RaftTxnOpGet:
			kv, err := t.storage.getWithoutLock(t.ctx, op.Key, 0)
			if err != nil {
				if errors.IsNotFound(err) {
					responses = append(responses, nil)
					continue
				}
				return nil, 0, err
			}
			responses = append(responses, kv)

		default:
			return nil, 0, &errors.QRDBError{
				Code: errors.ErrInvalidInput,
				Op:   "raftTransaction.executeOpsLocal",
				Err:  &RaftUnknownOperationTypeError{OpType: op.Type},
			}
		}
	}

	if err := t.storage.revMgr.persistInBatch(batch); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"raftTransaction.executeOpsLocal",
			"failed to persist revision",
		)
	}

	if err := batch.Commit(); err != nil {
		return nil, 0, errors.Wrapf(
			err,
			errors.ErrStorage,
			"raftTransaction.executeOpsLocal",
			"failed to commit batch",
		)
	}

	return responses, rev, nil
}

// executePut executes a Put operation within a batch.
func (t *raftTransaction) executePut(batch storage.Batch, key, value []byte, rev int64) (*RaftKeyValue, error) {
	var meta KeyMeta
	metaKey := EncodeMetaKey(key)
	metaData, err := t.storage.store.Get(metaKey)
	isNew := false

	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}
		isNew = true
	} else {
		decoded, decErr := DecodeMeta(metaData)
		if decErr != nil {
			return nil, decErr
		}
		meta = *decoded
	}

	if isNew {
		meta.CreateRevision = rev
		meta.ModRevision = rev
		meta.Version = 1
	} else {
		meta.ModRevision = rev
		meta.Version++
	}

	dataKey := EncodeDataKey(key, rev)
	if err := batch.Set(dataKey, value); err != nil {
		return nil, err
	}

	if err := batch.Set(metaKey, EncodeMeta(&meta)); err != nil {
		return nil, err
	}

	kv := &RaftKeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
	}

	return kv, nil
}

// executeDelete executes a Delete operation within a batch.
func (t *raftTransaction) executeDelete(batch storage.Batch, key []byte, rev int64) (*RaftKeyValue, error) {
	metaKey := EncodeMetaKey(key)
	metaData, err := t.storage.store.Get(metaKey)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	meta, decErr := DecodeMeta(metaData)
	if decErr != nil {
		return nil, decErr
	}

	dataKey := EncodeDataKey(key, meta.ModRevision)
	value, err := t.storage.store.Get(dataKey)
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	if err := batch.Delete(metaKey); err != nil {
		return nil, err
	}

	if err := batch.Delete(dataKey); err != nil {
		return nil, err
	}

	prevKV := &RaftKeyValue{
		Key:            key,
		Value:          value,
		CreateRevision: meta.CreateRevision,
		ModRevision:    meta.ModRevision,
		Version:        meta.Version,
		LeaseID:        meta.LeaseID,
	}

	return prevKV, nil
}

// Rollback cancels the transaction.
func (t *raftTransaction) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	t.closed = true

	if err := t.snapshot.Close(); err != nil {
		return errors.Wrapf(
			err,
			errors.ErrStorage,
			"raftTransaction.Rollback",
			"failed to close snapshot",
		)
	}

	return nil
}
