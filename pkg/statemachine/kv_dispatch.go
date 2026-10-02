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
// MVCC write handlers
// ---------------------------------------------------------------------------

// handleMVCCPut decodes an MVCC Put command and delegates to the
// configured MVCCStore.
func (k *KVStateMachine) handleMVCCPut(payload []byte, revision uint64) (sm.Result, error) {
	if k.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	key, value, leaseID, err := protocol.DecodeMVCCPut(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc put: " + err.Error()}
	}

	if _, putErr := k.mvccStore.Put(key, value, leaseID, revision); putErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-put", Inner: putErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleMVCCDelete decodes an MVCC Delete command and delegates to the
// configured MVCCStore.
func (k *KVStateMachine) handleMVCCDelete(payload []byte, revision uint64) (sm.Result, error) {
	if k.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	key, err := protocol.DecodeMVCCDelete(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc delete: " + err.Error()}
	}

	if _, deleteErr := k.mvccStore.Delete(key, revision); deleteErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-delete", Inner: deleteErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleMVCCRange decodes an MVCC Range command and delegates to the
// configured MVCCStore.
func (k *KVStateMachine) handleMVCCRange(payload []byte, revision uint64) (sm.Result, error) {
	if k.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	p, err := protocol.DecodeMVCCRange(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc range: " + err.Error()}
	}

	if _, _, rangeErr := k.mvccStore.Range(p.Start, p.End, p.Revision, p.Limit, p.CountOnly, p.KeysOnly); rangeErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-range", Inner: rangeErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleMVCCCompact decodes an OpMVCCCompactPrefix command and delegates to
// the configured MVCCStore.
func (k *KVStateMachine) handleMVCCCompact(payload []byte, revision uint64) (sm.Result, error) {
	if k.mvccStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	prefix, compactRevision, err := protocol.DecodeMVCCCompactPrefix(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "mvcc compact: " + err.Error()}
	}

	if compactErr := k.mvccStore.Compact(prefix, compactRevision); compactErr != nil {
		return sm.Result{}, &StorageError{Op: "mvcc-compact", Inner: compactErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleTransaction applies a Transaction command.
//
// With an MVCCStore injected, the store evaluates it. Without one — every
// state machine the registry builds — it runs on the NATIVE plane against the
// backend's own keyspace, with the same compare semantics as the default
// pebble state machine; see native_txn.go.
func (k *KVStateMachine) handleTransaction(payload []byte, revision uint64) (sm.Result, error) {
	if k.mvccStore == nil {
		out, err := applyNativeTransaction(k.backend, payload)
		if err != nil {
			return sm.Result{}, err
		}
		notifyMutations(k.watchManager, out.applied, revision)
		return transactionResult(out, revision), nil
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

	succeeded, txnRevision, _, txnErr := k.mvccStore.Transaction(compares, successOps, failureOps)
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
// Index write handlers
// ---------------------------------------------------------------------------

// handleIndexCreate decodes an IndexCreate command and delegates to the
// configured IndexManager.
func (k *KVStateMachine) handleIndexCreate(payload []byte, revision uint64) (sm.Result, error) {
	if k.indexManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "index"}
	}

	name, fieldName, opts, err := protocol.DecodeIndexCreateOpts(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "index create: " + err.Error()}
	}

	if createErr := k.indexManager.CreateIndex(name, fieldName, opts); createErr != nil {
		return indexManagerFailure("index-create", createErr)
	}

	return sm.Result{Value: revision}, nil
}

// handleIndexDrop decodes an IndexDrop command and delegates to the
// configured IndexManager.
func (k *KVStateMachine) handleIndexDrop(payload []byte, revision uint64) (sm.Result, error) {
	if k.indexManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "index"}
	}

	name, dropOpts, err := protocol.DecodeIndexDropOpts(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "index drop: " + err.Error()}
	}

	if dropErr := k.indexManager.DropIndex(name, dropOpts); dropErr != nil {
		return indexManagerFailure("index-drop", dropErr)
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Lease write handlers
// ---------------------------------------------------------------------------

// handleLeaseGrant decodes a Lease Grant command and delegates to the
// configured LeaseManager.
func (k *KVStateMachine) handleLeaseGrant(payload []byte, _ uint64) (sm.Result, error) {
	if k.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	ttl, err := protocol.DecodeLeaseGrant(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease grant: " + err.Error()}
	}

	leaseID, _, grantErr := k.leaseManager.Grant(ttl)
	if grantErr != nil {
		return sm.Result{}, grantErr
	}

	return sm.Result{Value: uint64(leaseID)}, nil
}

// handleLeaseRevoke decodes a Lease Revoke command and delegates to
// the configured LeaseManager.
func (k *KVStateMachine) handleLeaseRevoke(payload []byte, revision uint64) (sm.Result, error) {
	if k.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	leaseID, err := protocol.DecodeLeaseRevoke(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease revoke: " + err.Error()}
	}

	if revokeErr := k.leaseManager.Revoke(leaseID); revokeErr != nil {
		return sm.Result{}, revokeErr
	}

	return sm.Result{Value: revision}, nil
}

// handleLeaseKeepAlive decodes a Lease KeepAlive command and delegates
// to the configured LeaseManager.
func (k *KVStateMachine) handleLeaseKeepAlive(payload []byte, revision uint64) (sm.Result, error) {
	if k.leaseManager == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "lease"}
	}

	leaseID, err := protocol.DecodeLeaseKeepAlive(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "lease keep-alive: " + err.Error()}
	}

	if _, keepAliveErr := k.leaseManager.KeepAlive(leaseID); keepAliveErr != nil {
		return sm.Result{}, keepAliveErr
	}

	return sm.Result{Value: revision}, nil
}

// ---------------------------------------------------------------------------
// Time-series write handlers
// ---------------------------------------------------------------------------

// handleTSWrite decodes a Time-Series Write command and delegates to
// the configured TimeSeriesStore.
func (k *KVStateMachine) handleTSWrite(payload []byte, revision uint64) (sm.Result, error) {
	if k.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write: " + err.Error()}
	}

	if writeErr := k.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); writeErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-write", Inner: writeErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSWriteBatch decodes a Time-Series WriteBatch command and
// delegates to the configured TimeSeriesStore.
func (k *KVStateMachine) handleTSWriteBatch(payload []byte, revision uint64) (sm.Result, error) {
	if k.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	params, err := protocol.DecodeTSWriteBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write batch: " + err.Error()}
	}

	points := make([]TSDataPoint, len(params))
	for i, p := range params {
		points[i] = TSDataPoint{
			Metric:    p.Metric,
			Timestamp: p.Timestamp,
			Value:     p.Value,
			Tags:      p.Tags,
		}
	}

	if writeErr := k.tsStore.WriteDataPoints(points); writeErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-batch", Inner: writeErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSWritePoint decodes a Time-Series WritePoint command and
// delegates to the configured TimeSeriesStore.
func (k *KVStateMachine) handleTSWritePoint(payload []byte, revision uint64) (sm.Result, error) {
	if k.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSWrite(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts write point: " + err.Error()}
	}

	if writeErr := k.tsStore.WriteDataPoint(p.Metric, p.Timestamp, p.Value, p.Tags); writeErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-write-point", Inner: writeErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSDeleteSeries decodes a Time-Series DeleteSeries command and
// delegates to the configured TimeSeriesStore.
func (k *KVStateMachine) handleTSDeleteSeries(payload []byte, revision uint64) (sm.Result, error) {
	if k.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSDeleteSeries(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts delete series: " + err.Error()}
	}

	if delErr := k.tsStore.DeleteSeries(p.Metric, p.StartNs, p.EndNs, p.Tags); delErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-delete-series", Inner: delErr}
	}

	return sm.Result{Value: revision}, nil
}

// handleTSSetRetention decodes a Time-Series SetRetention command and
// delegates to the configured TimeSeriesStore.
func (k *KVStateMachine) handleTSSetRetention(payload []byte, revision uint64) (sm.Result, error) {
	if k.tsStore == nil {
		return sm.Result{}, &ServiceNotConfiguredError{Service: "timeseries"}
	}

	p, err := protocol.DecodeTSSetRetention(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "ts set retention: " + err.Error()}
	}

	if retErr := k.tsStore.SetRetentionPolicy(p.Metric, p.DurationNs, p.ResolutionNs, p.Aggregation); retErr != nil {
		return sm.Result{}, &StorageError{Op: "ts-set-retention", Inner: retErr}
	}

	return sm.Result{Value: revision}, nil
}
