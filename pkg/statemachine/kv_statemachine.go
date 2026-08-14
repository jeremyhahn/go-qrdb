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
	"errors"
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

	k.handlers = map[byte]updateHandler{
		// Basic KV
		kv.OpPut:         k.handlePut,
		kv.OpDelete:      k.handleDelete,
		protocol.OpBatch: k.handleBatch,

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
// application-level conditions (e.g. a subsystem not being wired into
// this SM instance). Returning a non-nil error prevents the RSM layer
// from advancing lastApplied, causing all subsequent linearizable reads
// to block indefinitely. Only unrecoverable infrastructure errors (disk
// full, corrupt storage) may be returned.
//
// When a handler returns ServiceNotConfiguredError, the entry is encoded
// as a result with a non-nil Data field carrying the error message, and
// processing continues. The proposer receives this result and may inspect
// result.Data to surface the error to the caller without poisoning Raft.
func (k *KVStateMachine) Update(entries []sm.Entry, results []sm.Result) error {
	for i := range entries {
		cmd := entries[i].Cmd
		if len(cmd) == 0 {
			results[i] = sm.Result{}
			continue
		}

		op := cmd[0]
		handler, ok := k.handlers[op]
		if !ok {
			return &InvalidOpcodeError{Op: op}
		}

		revision := k.revision.Add(1)
		result, err := handler(cmd[1:], revision)
		if err != nil {
			var sncErr *ServiceNotConfiguredError
			if errors.As(err, &sncErr) {
				// Application-level error: the subsystem is not configured on
				// this SM instance. Encode the error in Data so the proposer
				// can surface it, but do NOT return the error to Raft — that
				// would prevent lastApplied from advancing and stall all reads.
				k.logger.Error("statemachine: subsystem not configured, encoding as result",
					slog.String("service", sncErr.Service),
					slog.Uint64("entry_index", entries[i].Index))
				results[i] = sm.Result{Data: []byte(err.Error())}
				continue
			}
			return err
		}
		results[i] = result
	}
	return nil
}

// handlePut decodes a KV Put command and writes the key-value pair via
// the backend.
// Payload format: [keyLen:2 LE][key][value]
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
// Payload format: [keyLen:2 LE][key]
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
// Payload format: [opCount:2 LE][ops...]
func (k *KVStateMachine) handleBatch(payload []byte, revision uint64) (sm.Result, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "batch: " + err.Error()}
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
