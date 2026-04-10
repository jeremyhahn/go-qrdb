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

// Package mvccadapter bridges the statemachine.MVCCStore interface to
// mvcc.RaftStorage. It lives in a separate package to avoid the import
// cycle: statemachine -> mvcc -> service -> statemachine.
//
// The adapter translates between the wire protocol byte constants used
// by the FSM and the typed enumerations used by RaftStorage.
package mvccadapter

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// Adapter implements the statemachine.MVCCStore interface by delegating
// to an mvcc.RaftStorage instance. It bridges the state machine layer's
// revision-aware operations to the MVCC engine's Apply-path methods,
// translating between the wire protocol byte constants used by the FSM
// and the typed enumerations used by RaftStorage.
type Adapter struct {
	rs *mvcc.RaftStorage
}

// Compile-time interface compliance check.
var _ statemachine.MVCCStore = (*Adapter)(nil)

// NewAdapter creates a new adapter wrapping the given RaftStorage.
func NewAdapter(rs *mvcc.RaftStorage) *Adapter {
	return &Adapter{rs: rs}
}

// Put stores a key-value pair at the given revision with an optional lease
// attachment. The revision parameter is the Raft log index assigned by the
// FSM's Update path. Returns the resulting *service.KeyValue.
func (a *Adapter) Put(key, value []byte, leaseID int64, revision uint64) (interface{}, error) {
	//nolint:gosec // G115: uint64 to int64 conversion for revision - Raft log indexes are always positive and bounded
	kv, err := a.rs.PutAtRevision(context.Background(), key, value, leaseID, int64(revision))
	if err != nil {
		return nil, err
	}
	return kv, nil
}

// Delete removes a key at the given revision. Returns the previous
// *service.KeyValue state, or nil if the key did not exist.
func (a *Adapter) Delete(key []byte, revision uint64) (interface{}, error) {
	//nolint:gosec // G115: uint64 to int64 conversion for revision - Raft log indexes are always positive and bounded
	kv, err := a.rs.DeleteAtRevision(context.Background(), key, int64(revision))
	if err != nil {
		return nil, err
	}
	if kv == nil {
		return nil, nil
	}
	return kv, nil
}

// Range queries a range of keys. When countOnly is true, only the count
// of matching keys is returned as the result. When keysOnly is true,
// values are omitted from the returned key-value pairs.
func (a *Adapter) Range(start, end []byte, revision, limit int64, countOnly, keysOnly bool) (interface{}, int64, error) {
	results, err := a.rs.Range(context.Background(), start, end, revision, limit)
	if err != nil {
		return nil, 0, err
	}

	currentRev := a.rs.RaftCurrentRevision()

	if countOnly {
		return int64(len(results)), currentRev, nil
	}

	if keysOnly {
		for _, kv := range results {
			kv.Value = nil
		}
	}

	return results, currentRev, nil
}

// Compact removes all data entries at or below the specified revision.
func (a *Adapter) Compact(revision int64) error {
	return a.rs.Compact(revision)
}

// Transaction evaluates compare preconditions and executes the success or
// failure operations atomically. The compare targets and results use the
// service.CompareTarget / service.CompareResult byte encoding from the
// wire protocol. Operations use the kv.Op* byte encoding (1=put, 2=get,
// 3=delete).
func (a *Adapter) Transaction(compares []statemachine.MVCCCompare, successOps, failureOps []statemachine.MVCCOp) (bool, int64, interface{}, error) {
	txn, err := a.rs.BeginRaftTxn(context.Background())
	if err != nil {
		return false, 0, nil, &statemachine.StorageError{Op: "mvcc-txn-begin", Inner: err}
	}

	// Add all compare conditions.
	for _, c := range compares {
		rc, convertErr := convertCompare(c)
		if convertErr != nil {
			_ = txn.Rollback()
			return false, 0, nil, convertErr
		}
		if addErr := txn.AddCompare(rc); addErr != nil {
			_ = txn.Rollback()
			return false, 0, nil, &statemachine.StorageError{Op: "mvcc-txn-compare", Inner: addErr}
		}
	}

	// Convert and set success operations.
	raftSuccessOps, err := convertOps(successOps)
	if err != nil {
		_ = txn.Rollback()
		return false, 0, nil, err
	}
	if setErr := txn.SetSuccess(raftSuccessOps); setErr != nil {
		_ = txn.Rollback()
		return false, 0, nil, &statemachine.StorageError{Op: "mvcc-txn-set-success", Inner: setErr}
	}

	// Convert and set failure operations.
	raftFailureOps, err := convertOps(failureOps)
	if err != nil {
		_ = txn.Rollback()
		return false, 0, nil, err
	}
	if setErr := txn.SetFailure(raftFailureOps); setErr != nil {
		_ = txn.Rollback()
		return false, 0, nil, &statemachine.StorageError{Op: "mvcc-txn-set-failure", Inner: setErr}
	}

	// Commit and return results.
	result, err := txn.Commit()
	if err != nil {
		return false, 0, nil, &statemachine.StorageError{Op: "mvcc-txn-commit", Inner: err}
	}

	return result.Succeeded, result.Revision, result.Responses, nil
}

// ---------------------------------------------------------------------------
// Compare mapping: service.CompareTarget/CompareResult byte -> RaftCompare
// ---------------------------------------------------------------------------

// compareTargetMap maps service.CompareTarget byte values to
// mvcc.RaftCompareTarget enum values.
//
// service.CompareTarget:
//
//	0 = CompareVersion
//	1 = CompareCreateRevision
//	2 = CompareModRevision
//	3 = CompareValue
//
// mvcc.RaftCompareTarget:
//
//	0 = RaftCompareVersion
//	1 = RaftCompareCreate
//	2 = RaftCompareMod
//	3 = RaftCompareValue
var compareTargetMap = map[byte]mvcc.RaftCompareTarget{
	byte(service.CompareVersion):        mvcc.RaftCompareVersion,
	byte(service.CompareCreateRevision): mvcc.RaftCompareCreate,
	byte(service.CompareModRevision):    mvcc.RaftCompareMod,
	byte(service.CompareValue):          mvcc.RaftCompareValue,
}

// compareResultMap maps service.CompareResult byte values to
// mvcc.RaftCompareResult enum values.
//
// service.CompareResult:
//
//	0 = CompareEqual
//	1 = CompareGreater
//	2 = CompareLess
//	3 = CompareNotEqual
//
// mvcc.RaftCompareResult:
//
//	0 = RaftCompareEqual
//	1 = RaftCompareNotEqual
//	2 = RaftCompareGreater
//	3 = RaftCompareLess
var compareResultMap = map[byte]mvcc.RaftCompareResult{
	byte(service.CompareEqual):    mvcc.RaftCompareEqual,
	byte(service.CompareGreater):  mvcc.RaftCompareGreater,
	byte(service.CompareLess):     mvcc.RaftCompareLess,
	byte(service.CompareNotEqual): mvcc.RaftCompareNotEqual,
}

// convertCompare translates a statemachine.MVCCCompare to an
// mvcc.RaftCompare. Returns an error if the target or result byte
// does not map to a known RaftCompare constant.
func convertCompare(c statemachine.MVCCCompare) (*mvcc.RaftCompare, error) {
	target, ok := compareTargetMap[c.Target]
	if !ok {
		return nil, &statemachine.InvalidOpcodeError{Op: c.Target}
	}

	result, ok := compareResultMap[c.Result]
	if !ok {
		return nil, &statemachine.InvalidOpcodeError{Op: c.Result}
	}

	rc := &mvcc.RaftCompare{
		Key:    c.Key,
		Target: target,
		Result: result,
	}

	// Set the appropriate comparison operand based on target.
	switch target {
	case mvcc.RaftCompareVersion:
		rc.Revision = c.Version
	case mvcc.RaftCompareCreate:
		rc.Revision = c.CreateRevision
	case mvcc.RaftCompareMod:
		rc.Revision = c.ModRevision
	case mvcc.RaftCompareValue:
		rc.Value = c.Value
	}

	return rc, nil
}

// ---------------------------------------------------------------------------
// Operation mapping: wire protocol op bytes -> RaftTxnOp
// ---------------------------------------------------------------------------

// Wire protocol operation type constants from go-quicraft's pkg/sm/kv
// package. These are the byte values that arrive in MVCCOp.Type from
// the FSM's handleTransaction handler.
const (
	wireOpPut    byte = 1 // kv.OpPut
	wireOpGet    byte = 2 // kv.OpGet
	wireOpDelete byte = 3 // kv.OpDelete
)

// opTypeMap maps wire protocol op bytes to mvcc.RaftTxnOpType.
var opTypeMap = map[byte]mvcc.RaftTxnOpType{
	wireOpPut:    mvcc.RaftTxnOpPut,
	wireOpGet:    mvcc.RaftTxnOpGet,
	wireOpDelete: mvcc.RaftTxnOpDelete,
}

// convertOps translates a slice of statemachine.MVCCOp to mvcc.RaftTxnOp.
func convertOps(ops []statemachine.MVCCOp) ([]mvcc.RaftTxnOp, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	result := make([]mvcc.RaftTxnOp, len(ops))
	for i, op := range ops {
		opType, ok := opTypeMap[op.Type]
		if !ok {
			return nil, &statemachine.InvalidOpcodeError{Op: op.Type}
		}
		result[i] = mvcc.RaftTxnOp{
			Type:  opType,
			Key:   op.Key,
			Value: op.Value,
		}
	}
	return result, nil
}
