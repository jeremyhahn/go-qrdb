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
	"encoding/binary"
	"log/slog"
	"sync/atomic"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// KVStateMachine is a unified key-value state machine that delegates
// storage operations to a Backend interface. This eliminates dispatch
// table duplication across RegularStateMachine, ConcurrentStateMachine,
// and FastStateMachine by consolidating all protocol handlers into a
// single implementation.
//
// The KVStateMachine is protocol-aware but storage-agnostic: it decodes
// Raft log entries, dispatches to the appropriate handler, and uses the
// Backend for all data operations. Adapters (StandardAdapter,
// ConcurrentAdapter, DiskAdapter) wrap KVStateMachine to implement the
// specific go-quicraft state machine interfaces.
type KVStateMachine struct {
	backend  Backend
	revision atomic.Uint64
	handlers map[byte]updateHandler
	logger   *slog.Logger

	// guardedWrites records whether the backend can carry what a guarded
	// verb's result promises — see GuardedWriteBackend. Resolved ONCE at
	// construction rather than per apply: it is a property of the composition,
	// it cannot change while the state machine runs, and the guarded verbs are
	// on the apply path.
	guardedWrites bool

	// backendName names the backend in a refusal, so an operator reading
	// "not supported on the fast backend" knows which knob to turn.
	backendName string

	// Optional subsystem managers.
	watchManager WatchManager
	indexManager IndexManager
	leaseManager LeaseManager
	tsStore      TimeSeriesStore
	mvccStore    MVCCStore
}

// NewKVStateMachine creates a new KVStateMachine backed by the given
// Backend with optional configuration.
func NewKVStateMachine(backend Backend, opts ...KVOption) *KVStateMachine {
	k := &KVStateMachine{
		backend: backend,
		logger:  slog.Default(),
	}

	for _, opt := range opts {
		opt(k)
	}

	k.guardedWrites = backendSupportsGuardedWrites(backend)
	k.backendName = backendName(backend)

	k.handlers = map[byte]updateHandler{
		// Basic KV
		kv.OpPut:         k.handlePut,
		kv.OpPutIfAbsent: k.handlePutIfAbsent,
		kv.OpDelete:      k.handleDelete,
		protocol.OpBatch: k.handleBatch,

		// Guarded single-key verbs. They complete the set kv.OpPutIfAbsent
		// opens: a guarded create with no guarded release or edit is a
		// primitive an unguarded Delete or Put can un-decide (go-qrdb#41,
		// #56). pkg/store and both pkg/engine engines have dispatched them
		// since they were added; these four maps did not, so every sm_type
		// served by this package refused them with "unknown opcode: 66"
		// (go-qrdb#175).
		protocol.OpCompareAndDelete: k.handleCompareAndDelete,
		protocol.OpCompareAndSwap:   k.handleCompareAndSwap,

		// MVCC
		protocol.OpMVCCPut:     k.handleMVCCPut,
		protocol.OpMVCCDelete:  k.handleMVCCDelete,
		protocol.OpMVCCRange:   k.handleMVCCRange,
		protocol.OpMVCCCompact: k.handleMVCCCompact,

		// Transactions
		protocol.OpTransaction: k.handleTransaction,

		// Leases
		protocol.OpLeaseGrant:     k.handleLeaseGrant,
		protocol.OpLeaseRevoke:    k.handleLeaseRevoke,
		protocol.OpLeaseKeepAlive: k.handleLeaseKeepAlive,

		// Secondary indexes
		protocol.OpIndexCreate: k.handleIndexCreate,
		protocol.OpIndexDrop:   k.handleIndexDrop,

		// Time series
		protocol.OpTSWrite:        k.handleTSWrite,
		protocol.OpTSWriteBatch:   k.handleTSWriteBatch,
		protocol.OpTSWritePoint:   k.handleTSWritePoint,
		protocol.OpTSDeleteSeries: k.handleTSDeleteSeries,
		protocol.OpTSSetRetention: k.handleTSSetRetention,
	}

	return k
}

// Update applies committed log entries to the state machine by
// dispatching each entry through the handler map.
//
// Raft correctness contract: Update must never return an error for
// application-level conditions — a subsystem not wired into this SM
// instance, or an opcode this SM does not implement at all. Returning a
// non-nil error prevents the RSM layer from advancing lastApplied,
// causing all subsequent linearizable reads to block indefinitely. Only
// unrecoverable infrastructure errors (disk full, corrupt storage) may
// be returned.
//
// resolveApplyError is the single place that draws that line, shared with
// the regular, fast and concurrent state machines, so the rule is stated
// once rather than re-derived in four dispatch loops. An application-level
// condition becomes a result whose Data carries the encoded error and
// processing continues; the proposer inspects Data (via checkResultError)
// and surfaces a typed refusal without poisoning Raft.
func (k *KVStateMachine) Update(entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		cmd := entries[i].Cmd
		if len(cmd) == 0 {
			results[i] = sm.Result{}
			continue
		}

		op := cmd[0]
		var result sm.Result
		var err error
		if handler, ok := k.handlers[op]; ok {
			revision := k.revision.Add(1)
			result, err = handler(cmd[1:], revision)
		} else {
			err = &InvalidOpcodeError{Op: op}
		}
		resolved, stop, applyErr := resolveApplyError(k.logger, entries[i].Index, result, err)
		if stop {
			return applyErr
		}
		results[i] = resolved
	}
	return nil
}

// handlePut decodes a KV Put command and writes the key-value pair via
// the backend.
// Payload format: [keyLen:2 LE][key][value].
func (k *KVStateMachine) handlePut(payload []byte, revision uint64) (sm.Result, error) {
	if len(payload) < 2 {
		return sm.Result{}, &IncompleteDataError{Reason: "put: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return sm.Result{}, &IncompleteDataError{Reason: "put: key length exceeds payload"}
	}
	key := payload[2 : 2+keyLen]
	value := payload[2+keyLen:]

	if err := k.backend.Put(key, value); err != nil {
		return sm.Result{}, &StorageError{Op: "put", Inner: err}
	}

	if k.watchManager != nil {
		k.watchManager.Notify(string(key), value, revision, false)
	}

	return sm.Result{Value: revision}, nil
}

// handleDelete decodes a KV Delete command and removes the key via the
// backend.
// Payload format: [keyLen:2 LE][key].
func (k *KVStateMachine) handleDelete(payload []byte, revision uint64) (sm.Result, error) {
	if len(payload) < 2 {
		return sm.Result{}, &IncompleteDataError{Reason: "delete: payload too short"}
	}
	keyLen := binary.LittleEndian.Uint16(payload[0:2])
	if int(2+keyLen) > len(payload) {
		return sm.Result{}, &IncompleteDataError{Reason: "delete: key length exceeds payload"}
	}
	key := payload[2 : 2+keyLen]

	if err := k.backend.Delete(key); err != nil {
		return sm.Result{}, &StorageError{Op: "delete", Inner: err}
	}

	if k.watchManager != nil {
		k.watchManager.Notify(string(key), nil, revision, true)
	}

	return sm.Result{Value: revision}, nil
}

// handleBatch decodes and applies a batch of operations via the backend.
// Payload format: [opCount:2 LE][ops...].
func (k *KVStateMachine) handleBatch(payload []byte, revision uint64) (sm.Result, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "batch: " + err.Error()}
	}

	// Refuse the whole batch before applying any of it: the loop below mutates
	// as it goes, so validating inside it would leave earlier operations
	// committed and notified for a batch the caller is told was refused.
	if opErr := validateBatchOps(ops); opErr != nil {
		return sm.Result{}, opErr
	}

	for _, op := range ops {
		switch op.Type {
		case kv.OpPut:
			if putErr := k.backend.Put(op.Key, op.Value); putErr != nil {
				return sm.Result{}, &StorageError{Op: "batch-put", Inner: putErr}
			}
			if k.watchManager != nil {
				k.watchManager.Notify(string(op.Key), op.Value, revision, false)
			}
		case kv.OpDelete:
			if delErr := k.backend.Delete(op.Key); delErr != nil {
				return sm.Result{}, &StorageError{Op: "batch-delete", Inner: delErr}
			}
			if k.watchManager != nil {
				k.watchManager.Notify(string(op.Key), nil, revision, true)
			}
		default:
			return sm.Result{}, &InvalidOpcodeError{Op: op.Type}
		}
	}

	return sm.Result{Value: revision}, nil
}
