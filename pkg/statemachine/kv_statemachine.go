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
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
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

	// batch is held shared by every Update while it applies a batch of
	// entries, and exclusively by GetHash while it hashes the state.
	// go-quicraft's concurrent tier runs Lookup and GetHash concurrently with
	// Update; replicas compare the hash to detect divergence, so it must be of
	// a state between batches -- one every replica passes through -- never of
	// a batch half applied. Holding it shared leaves applies as concurrent as
	// they were; Lookup takes no part in it, so a read may run during a batch.
	batch sync.RWMutex

	// lessor holds the state machine's leases. It is never nil: every
	// KVStateMachine serves leases, durably (kv_lease.go). WithKVLessor
	// injects the composition root's, so its expiry sweeper sees the same
	// leases; otherwise the state machine has its own.
	lessor *lease.Lessor

	// Optional subsystem managers.
	watchManager WatchManager
	indexManager IndexManager
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
	if k.lessor == nil {
		k.lessor = lease.NewLessor()
	}

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

		// A write wrapped with a guard evaluated at this entry's log
		// position -- the drop fence rides on it. See guarded_write.go.
		protocol.OpGuarded: k.handleGuarded,

		// A replicated delete of every key in [start, end), gated by the
		// range-delete feature. See delete_range.go.
		protocol.OpDeleteRange: k.handleDeleteRange,

		// MVCC
		protocol.OpMVCCPut:    k.handleMVCCPut,
		protocol.OpMVCCDelete: k.handleMVCCDelete,
		protocol.OpMVCCRange:  k.handleMVCCRange,

		// Transactions
		protocol.OpTransaction: k.handleTransaction,

		// Leases
		protocol.OpLeaseGrant:      k.handleLeaseGrant,
		protocol.OpLeaseRevoke:     k.handleLeaseRevoke,
		protocol.OpLeaseKeepAlive:  k.handleLeaseKeepAlive,
		protocol.OpLeaseCheckpoint: k.handleLeaseCheckpoint,

		// Chunked import session staging (pkg/importstage), lease-bound and
		// never reachable from a client KV verb -- see kv_import_stage.go.
		protocol.OpImportStage: k.handleImportStage,

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
// ResolveApplyError is the single place that draws that line, shared with
// the regular, fast and concurrent state machines, so the rule is stated
// once rather than re-derived in four dispatch loops. An application-level
// condition becomes a result whose Data carries the encoded error and
// processing continues; the proposer inspects Data (via checkResultError)
// and surfaces a typed refusal without poisoning Raft.
func (k *KVStateMachine) Update(entries []sm.Entry, results []sm.Result) error {
	k.batch.RLock()
	defer k.batch.RUnlock()
	if err := k.applyEntries(entries, results); err != nil {
		return err
	}
	return k.stampApplied(entries)
}

// stampApplied records the index of the last entry in the batch under
// diskMetaKeyLastApplied, so the applied index travels with the records in
// snapshots and the state digest reads both under one lock. The disk
// adapter stamps its own marker durably and does not come through here.
func (k *KVStateMachine) stampApplied(entries []sm.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	return k.backend.Put([]byte(diskMetaKeyLastApplied), encodeAppliedIndex(entries[len(entries)-1].Index))
}

// applyEntries is Update's dispatch loop without the batch lock: the caller
// must already hold at least batch.RLock() for the whole span entries
// belongs to. Update takes that lock once around one full call's entries;
// DiskAdapter.Update (go-qrdb#584 B1) takes it once around its OWN loop,
// which applies one entry per call so each entry's write lands in its own
// storage batch, and calls this directly so the lock is held for that
// WHOLE loop rather than reacquired every entry -- reacquiring it let
// GetHash observe state between entries within what DiskAdapter's own
// caller sees as one batch, exactly the half-applied state the lock exists
// to keep GetHash from hashing.
func (k *KVStateMachine) applyEntries(entries []sm.Entry, results []sm.Result) error {
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
			k.lessor.Applying(entries[i].Index)
			revision := k.revision.Add(1)
			result, err = handler(cmd[1:], revision)
		} else {
			err = &InvalidOpcodeError{Op: op}
		}
		// #nosec G602 -- i ranges over entries.
		resolved, stop, applyErr := ResolveApplyError(k.logger, entries[i].Index, result, err)
		if stop {
			return applyErr
		}
		// #nosec G602 -- the sm.StateMachine Update contract pre-allocates results with len(results) == len(entries), and i ranges over entries.
		results[i] = resolved
	}
	return nil
}

// handlePut decodes a KV Put command and writes the key-value pair via
// the backend.
// Payload format: [keyLen:2 LE][key][value].
func (k *KVStateMachine) handlePut(payload []byte, revision uint64) (sm.Result, error) {
	key, value, err := decodeKeyValuePayload(payload, "put")
	if err != nil {
		return sm.Result{}, err
	}
	if kerr := validateClientKey("put", key); kerr != nil {
		return sm.Result{}, kerr
	}

	if err := k.putKey(key, value, false); err != nil {
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
	key, _, err := decodeKeyValuePayload(payload, "delete")
	if err != nil {
		return sm.Result{}, err
	}

	if kerr := validateClientKey("delete", key); kerr != nil {
		return sm.Result{}, kerr
	}
	if err := k.deleteKey(key, false); err != nil {
		return sm.Result{}, &StorageError{Op: "delete", Inner: err}
	}

	if k.watchManager != nil {
		k.watchManager.Notify(string(key), nil, revision, true)
	}

	return sm.Result{Value: revision}, nil
}

// putKey writes one key with no lease. A key bound to a lease leaves it: the
// write and the release commit together (commitLeased). Otherwise the write
// takes the backend's ordinary path -- Put, or PutDurable for the guarded
// verbs, whose answer must survive a restart.
func (k *KVStateMachine) putKey(key, value []byte, durable bool) error {
	if k.bound(key) {
		return k.commitLeased([]Mutation{{Key: key, Value: value}}, nil)
	}
	if durable {
		return k.backend.PutDurable(key, value)
	}
	return k.backend.Put(key, value)
}

// deleteKey removes one key, releasing it from any lease it is bound to in the
// same commit; see putKey.
func (k *KVStateMachine) deleteKey(key []byte, durable bool) error {
	if k.bound(key) {
		return k.commitLeased([]Mutation{{Key: key, Delete: true}}, nil)
	}
	if durable {
		return k.backend.DeleteDurable(key)
	}
	return k.backend.Delete(key)
}

// handleBatch applies a batch of put and delete operations all or nothing,
// through Backend.ApplyAtomic: an unsupported operation refuses the whole
// batch before any of it is applied, no concurrent Lookup sees part of it, and
// a crash leaves all of it or none. Watchers are notified after the commit.
func (k *KVStateMachine) handleBatch(payload []byte, revision uint64) (sm.Result, error) {
	muts, err := applyNativeBatch(leasedKeyspace{k}, payload)
	var refused *leaseRefusedError
	if errors.As(err, &refused) {
		return sm.Result{Data: protocol.EncodeLeaseNotFoundError(refused.ID)}, nil
	}
	if err != nil {
		return sm.Result{}, err
	}
	notifyMutations(k.watchManager, muts, revision)
	return sm.Result{Value: revision}, nil
}
