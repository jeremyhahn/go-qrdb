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

package statemachine

import (
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// MVCC write handlers (called after Raft consensus via Update)
// ---------------------------------------------------------------------------

// handleMVCCPut decodes an MVCC Put command and delegates to the configured
// MVCCStore. Returns the Raft revision as the result value.
func (r *RegularStateMachine) handleMVCCPut(payload []byte, revision uint64) (sm.Result, error) {
	if r.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc put: " + err.Error()}
	}

	if _, putErr := r.mvccStore.Put(key, value, leaseID, revision); putErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-put", Inner: putErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleMVCCDelete decodes an MVCC Delete command and delegates to the
// configured MVCCStore. Returns the Raft revision as the result value.
func (r *RegularStateMachine) handleMVCCDelete(payload []byte, revision uint64) (sm.Result, error) {
	if r.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	key, err := protocol.DecodeMVCCDelete(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc delete: " + err.Error()}
	}

	if _, deleteErr := r.mvccStore.Delete(key, revision); deleteErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-delete", Inner: deleteErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleMVCCRange decodes an MVCC Range command and delegates to the
// configured MVCCStore. Returns the Raft revision as the result value.
func (r *RegularStateMachine) handleMVCCRange(payload []byte, revision uint64) (sm.Result, error) {
	if r.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	p, err := protocol.DecodeMVCCRange(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc range: " + err.Error()}
	}

	if _, _, rangeErr := r.mvccStore.Range(p.Start, p.End, p.Revision, p.Limit, p.CountOnly, p.KeysOnly); rangeErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-range", Inner: rangeErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleMVCCCompact decodes an MVCC Compact command and delegates to the
// configured MVCCStore. Returns the Raft revision as the result value.
func (r *RegularStateMachine) handleMVCCCompact(payload []byte, revision uint64) (sm.Result, error) {
	if r.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	compactRevision, err := protocol.DecodeMVCCCompact(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc compact: " + err.Error()}
	}

	if compactErr := r.mvccStore.Compact(compactRevision); compactErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-compact", Inner: compactErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleTransaction decodes a Transaction command, converts the decoded
// wire types to statemachine-level types, and delegates to the configured
// MVCCStore. Returns the transaction revision as the result value and
// encodes the succeeded flag into Result.Data.
func (r *RegularStateMachine) handleTransaction(payload []byte, _ uint64) (sm.Result, error) {
	if r.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "transaction: " + err.Error()}
	}

	compares := make([]MVCCCompare, len(txn.Compares))
	for i, c := range txn.Compares {
		compares[i] = MVCCCompare{
			Key:            c.Key,
			Target:         c.Target,
			Result:         c.Result,
			Value:          c.Value,
			Version:        c.Version,
			CreateRevision: c.CreateRevision,
			ModRevision:    c.ModRevision,
		}
	}

	successOps := make([]MVCCOp, len(txn.SuccessOps))
	for i, op := range txn.SuccessOps {
		successOps[i] = MVCCOp{
			Type:     op.Type,
			Key:      op.Key,
			Value:    op.Value,
			RangeEnd: op.RangeEnd,
			LeaseID:  op.LeaseID,
		}
	}

	failureOps := make([]MVCCOp, len(txn.FailureOps))
	for i, op := range txn.FailureOps {
		failureOps[i] = MVCCOp{
			Type:     op.Type,
			Key:      op.Key,
			Value:    op.Value,
			RangeEnd: op.RangeEnd,
			LeaseID:  op.LeaseID,
		}
	}

	succeeded, txnRevision, _, txnErr := r.mvccStore.Transaction(compares, successOps, failureOps)
	if txnErr != nil {
		return sm.Result{}, &StorageError{Op: "transaction", Inner: txnErr}
	}

	var data []byte
	if succeeded {
		data = []byte{1}
	} else {
		data = []byte{0}
	}

	return sm.Result{Value: uint64(txnRevision), Data: data}, nil
}

// ---------------------------------------------------------------------------
// MVCC read handlers (called from Lookup without Raft consensus)
// ---------------------------------------------------------------------------

// lookupMVCC handles MVCC-related read queries dispatched from Lookup. It
// returns:
//   - (result, true, nil) when the query was a recognized MVCC type
//   - (nil, true, err) when recognized but an error occurred
//   - (nil, false, nil) when the query is not an MVCC type
func (r *RegularStateMachine) lookupMVCC(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {

	case MVCCGetQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := r.mvccStore.Range(q.Key, nil, 0, 1, false, false)
		return result, true, err

	case *MVCCGetQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := r.mvccStore.Range(q.Key, nil, 0, 1, false, false)
		return result, true, err

	case MVCCGetAtRevisionQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := r.mvccStore.Range(q.Key, nil, q.Revision, 1, false, false)
		return result, true, err

	case *MVCCGetAtRevisionQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := r.mvccStore.Range(q.Key, nil, q.Revision, 1, false, false)
		return result, true, err

	case MVCCRangeQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := r.mvccStore.Range(q.Start, q.End, q.Revision, q.Limit, q.CountOnly, q.KeysOnly)
		return result, true, err

	case *MVCCRangeQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, _, err := r.mvccStore.Range(q.Start, q.End, q.Revision, q.Limit, q.CountOnly, q.KeysOnly)
		return result, true, err

	case MVCCRevisionQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		_, rev, err := r.mvccStore.Range(nil, nil, 0, 0, true, false)
		return rev, true, err

	case *MVCCRevisionQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		_, rev, err := r.mvccStore.Range(nil, nil, 0, 0, true, false)
		return rev, true, err

	case MVCCScanChangesQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, err := r.mvccStore.ScanChanges(q.StartRevision, q.EndRevision, q.Prefix, q.Limit)
		return result, true, err

	case *MVCCScanChangesQuery:
		if r.mvccStore == nil {
			return nil, true, &ServiceNotConfiguredError{Service: "mvcc"}
		}
		result, err := r.mvccStore.ScanChanges(q.StartRevision, q.EndRevision, q.Prefix, q.Limit)
		return result, true, err

	default:
		return nil, false, nil
	}
}
