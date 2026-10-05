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
	"bytes"
	"strconv"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// This file is the NATIVE transaction and batch plane of the state machines in
// this package: protocol.OpTransaction and protocol.OpBatch decided against,
// and committed to, the state machine's OWN keyspace — the one Put, Get,
// PutIfAbsent and the guarded single-key verbs already use.
//
// It is the model the default pebble state machine (pkg/store,
// pebbleHandleTransaction) and the KV engine (pkg/engine,
// KVEngine.handleTransaction) use, and it deliberately matches them compare
// for compare, so a transaction behaves the same whatever raft.sm_type serves
// it (owner ruling 2026-09-29, go-qrdb#136):
//
//   - VALUE compares against the stored value. An absent key compares as the
//     empty value, which is why the guarded-batch adapters pair a value guard
//     with an existence compare (lifecycle.guardCompares).
//   - VERSION compares degrade honestly: 0 when the key is absent, 1 when it
//     is present. That is exactly enough for "must be absent" (version == 0),
//     the fence every guarded create is built on.
//   - CREATE_REVISION and MOD_REVISION compares are REFUSED, with the same
//     application error pebble returns. These state machines keep no
//     revisions, and answering "revision 0" for every key would let two
//     read-then-CAS writers both win and lose an update with no signal.
//
// What these state machines do NOT use is a separate MVCC keyspace. An
// earlier plan composed an mvccadapter.Adapter beside the backend; its keys
// live under their own prefixes, so a transaction's compare could not see a
// value a plain Put had written and a plain Get could not see a transaction's
// writes — a guarded batch fenced on a claim written by PutIfAbsent would
// fence on nothing. When an MVCCStore IS injected, the state machine still
// delegates to it; the native plane serves every state machine that has none.
//
// Atomicity: the compares and the commit run inside one handler call on the
// apply goroutine, which go-quicraft never runs concurrently with itself, so
// no other entry lands between them. The commit is Backend.ApplyAtomic, so no
// concurrent Lookup sees half a transaction and a crash leaves all of it or
// none.

// keyspace is what the native transaction and batch plane needs from a state
// machine: a point read of its own keyspace and an all-or-nothing commit to
// it. Every Backend is one, and RegularStateMachine adapts its map to it.
type keyspace interface {
	Get(key []byte) ([]byte, error)
	ApplyAtomic(muts []Mutation) error
}

// leaseKeyspace is a keyspace that keeps leases: a put in a batch or a
// transaction may name one, and every committed write binds or releases its
// key in the same commit. KVStateMachine is one (leasedKeyspace);
// RegularStateMachine is not, and refuses a lease-bearing operation.
type leaseKeyspace interface {
	keyspace

	// hasLease reports whether the keyspace holds lease id.
	hasLease(id int64) bool

	// commitLeased applies muts all or nothing with each key's binding:
	// a put to leases[i], a delete to none.
	commitLeased(muts []Mutation, leases []int64) error
}

// leasedKeyspace adapts a KVStateMachine to leaseKeyspace.
type leasedKeyspace struct {
	k *KVStateMachine
}

func (ks leasedKeyspace) Get(key []byte) ([]byte, error)    { return ks.k.backend.Get(key) }
func (ks leasedKeyspace) ApplyAtomic(muts []Mutation) error { return ks.k.commitLeased(muts, nil) }
func (ks leasedKeyspace) hasLease(id int64) bool            { return ks.k.lessor.Has(id) }
func (ks leasedKeyspace) commitLeased(muts []Mutation, leases []int64) error {
	return ks.k.commitLeased(muts, leases)
}

// Compile-time interface check.
var _ leaseKeyspace = leasedKeyspace{}

// leaseRefusedError reports that an operation names a lease the keyspace does
// not hold. The handler answers it with the lease-not-found application error
// every replica returns alike; it never reaches Raft as an error.
type leaseRefusedError struct {
	ID int64
}

// Error returns a human-readable description.
func (e *leaseRefusedError) Error() string {
	return "statemachine: lease " + strconv.FormatInt(e.ID, 10) + " not found"
}

// commitOps commits a decoded operation set: through commitLeased on a keyspace
// that keeps leases, so every key's binding commits with it, and through
// ApplyAtomic otherwise.
func commitOps(ks keyspace, muts []Mutation, leases []int64) error {
	if lk, ok := ks.(leaseKeyspace); ok {
		return lk.commitLeased(muts, leases)
	}
	return ks.ApplyAtomic(muts)
}

// Transaction compare targets and results on the wire. They mirror
// lock.CompareTarget / lock.CompareResult (and pkg/store's copies) in iota
// order; protocol.DecodedCompare carries them as raw bytes.
const (
	txnTargetVersion        byte = 0
	txnTargetCreateRevision byte = 1
	txnTargetModRevision    byte = 2
	txnTargetValue          byte = 3

	txnResultEqual    byte = 0
	txnResultGreater  byte = 1
	txnResultLess     byte = 2
	txnResultNotEqual byte = 3
)

// RevisionCompareUnsupportedMessage is the application error a state machine
// without MVCC revisions returns for a create_revision or mod_revision
// compare. It is pebble's text, so the refusal reads the same on every
// state machine that keeps no revisions.
const RevisionCompareUnsupportedMessage = "transaction: create_revision/mod_revision compares require " +
	"an MVCC state machine; this node runs a non-MVCC backend (set raft.sm_type: mvcc). " +
	"Compare on value or version instead."

// txnOutcome is what applyNativeTransaction decided.
type txnOutcome struct {
	// refused is set when the transaction was refused as a whole, as an
	// application error, and nothing was written.
	refused bool

	// succeeded reports which branch ran: the success operations when every
	// compare held, the failure operations otherwise.
	succeeded bool

	// applied is the mutation set that was committed, for watch
	// notification.
	applied []Mutation
}

// applyNativeTransaction evaluates a transaction's compares against ks and
// commits the selected branch through ks.ApplyAtomic.
//
// A read fault while evaluating a compare is returned as a Go error — never
// folded into "the compare failed", which would let this replica take the
// failure branch while replicas whose reads succeeded took the other one.
func applyNativeTransaction(ks keyspace, payload []byte) (txnOutcome, error) {
	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return txnOutcome{}, &IncompleteDataError{Reason: "transaction: " + err.Error()}
	}

	// Every key the transaction names — compares and BOTH branches — is
	// checked before anything is evaluated, so an invalid key refuses the
	// transaction whichever branch it would take, identically on every
	// replica, instead of reaching storage and halting Apply.
	for _, dc := range txn.Compares {
		if err := validateClientKey("transaction compare", dc.Key); err != nil {
			return txnOutcome{}, err
		}
	}
	for _, ops := range [][]protocol.DecodedOp{txn.SuccessOps, txn.FailureOps} {
		for _, op := range ops {
			if err := validateClientKey("transaction op", op.Key); err != nil {
				return txnOutcome{}, err
			}
		}
	}

	// Refused BEFORE anything is evaluated, exactly as pebble does.
	for _, dc := range txn.Compares {
		if dc.Target == txnTargetCreateRevision || dc.Target == txnTargetModRevision {
			return txnOutcome{refused: true}, nil
		}
	}

	succeeded := true
	for _, dc := range txn.Compares {
		held, cerr := evaluateNativeCompare(ks, dc)
		if cerr != nil {
			return txnOutcome{}, cerr
		}
		if !held {
			succeeded = false
			break
		}
	}

	ops := txn.SuccessOps
	if !succeeded {
		ops = txn.FailureOps
	}
	muts, leases, err := batchMutations(ks, ops)
	if err != nil {
		return txnOutcome{}, err
	}
	if err := commitOps(ks, muts, leases); err != nil {
		return txnOutcome{}, &StorageError{Op: "transaction", Inner: err}
	}
	return txnOutcome{succeeded: succeeded, applied: muts}, nil
}

// evaluateNativeCompare decides one compare against the current value of its
// key. It is pebbleEvaluateCompare's semantics over a keyspace.
func evaluateNativeCompare(ks keyspace, dc protocol.DecodedCompare) (bool, error) {
	val, err := ks.Get(dc.Key)
	if err != nil {
		return false, &StorageError{Op: "transaction_compare_get", Inner: err}
	}
	switch dc.Target {
	case txnTargetValue:
		if val == nil {
			// An absent key compares as the empty value: only "equal to
			// empty" holds.
			return dc.Result == txnResultEqual && len(dc.Value) == 0, nil
		}
		return compareOutcome(dc.Result, bytes.Compare(val, dc.Value)), nil
	case txnTargetVersion:
		var version int64
		if val != nil {
			version = 1
		}
		switch {
		case version < dc.Version:
			return compareOutcome(dc.Result, -1), nil
		case version > dc.Version:
			return compareOutcome(dc.Result, 1), nil
		default:
			return compareOutcome(dc.Result, 0), nil
		}
	default:
		// Revision targets are refused before evaluation; anything else is
		// not a target at all. Fail closed.
		return false, nil
	}
}

// compareOutcome maps an ordering (negative, zero, positive) to whether the
// compare's result operator holds. An unknown operator never holds.
func compareOutcome(result byte, cmp int) bool {
	switch result {
	case txnResultEqual:
		return cmp == 0
	case txnResultGreater:
		return cmp > 0
	case txnResultLess:
		return cmp < 0
	case txnResultNotEqual:
		return cmp != 0
	default:
		return false
	}
}

// batchMutations converts a decoded operation set into the mutations it
// commits and the lease each one binds its key to, refusing the WHOLE set
// before anything is applied when any operation is one ks cannot execute.
//
// A lease-bearing put needs a keyspace that keeps leases. On one that does,
// a lease it does not hold refuses the set (*leaseRefusedError). On one that
// does not, the put is refused rather than applied without its lease: a write
// that silently dropped the lease would make a key meant to expire live
// forever.
func batchMutations(ks keyspace, ops []protocol.DecodedOp) ([]Mutation, []int64, error) {
	if err := validateBatchOps(ops); err != nil {
		return nil, nil, err
	}
	lk, keepsLeases := ks.(leaseKeyspace)
	muts := make([]Mutation, len(ops))
	leases := make([]int64, len(ops))
	for i, op := range ops {
		if err := validateClientKey("batch op", op.Key); err != nil {
			return nil, nil, err
		}
		if op.LeaseID != 0 {
			if !keepsLeases {
				return nil, nil, &ServiceNotConfiguredError{Service: "lease attachment"}
			}
			if !lk.hasLease(op.LeaseID) {
				return nil, nil, &leaseRefusedError{ID: op.LeaseID}
			}
		}
		muts[i] = Mutation{Key: op.Key, Value: op.Value, Delete: batchOpIsDelete(op.Type)}
		leases[i] = op.LeaseID
	}
	return muts, leases, nil
}

// applyNativeBatch commits an OpBatch payload to ks all or nothing.
func applyNativeBatch(ks keyspace, payload []byte) ([]Mutation, error) {
	ops, err := protocol.DecodeBatch(payload)
	if err != nil {
		return nil, &IncompleteDataError{Reason: "batch: " + err.Error()}
	}
	muts, leases, err := batchMutations(ks, ops)
	if err != nil {
		return nil, err
	}
	if err := commitOps(ks, muts, leases); err != nil {
		return nil, &StorageError{Op: "batch", Inner: err}
	}
	return muts, nil
}

// transactionResult encodes a native transaction's outcome the way every
// transaction handler in the tree does: the revision in Value and the branch
// taken in Data[0], or pebble's application error for a refused revision
// compare.
func transactionResult(out txnOutcome, revision uint64) sm.Result {
	if out.refused {
		return CapabilityResult(RevisionCompareUnsupportedMessage)
	}
	flag := byte(0)
	if out.succeeded {
		flag = 1
	}
	return sm.Result{Value: revision, Data: []byte{flag}}
}

// notifyMutations delivers one watch event per committed mutation, after the
// commit, so no watcher learns of a write the caller was told had failed.
func notifyMutations(wm WatchManager, muts []Mutation, revision uint64) {
	if wm == nil {
		return
	}
	for _, m := range muts {
		if m.Delete {
			wm.Notify(string(m.Key), nil, revision, true)
			continue
		}
		wm.Notify(string(m.Key), m.Value, revision, false)
	}
}

// batchOpIsDelete reports whether a supported batch opcode removes its key.
// Both spellings are accepted: the KV opcodes, and the MVCC opcodes the
// service layer's batch and transaction encoders emit.
func batchOpIsDelete(op byte) bool {
	return op == kv.OpDelete || op == protocol.OpMVCCDelete
}

// reservedKeys are keys the state machines in this package keep for their
// own bookkeeping. The disk state machine stores its applied-index marker in
// the same keyspace clients write; a client write to that key would move the
// index a restart resumes from.
var reservedKeys = map[string]struct{}{
	diskMetaKeyLastApplied: {},
}

// validateClientKey refuses a key no client may name: an empty key, which no
// storage backend can hold and which used to reach storage and halt Apply on
// every replica; a key over protocol.MaxClientKeyLength, the same global
// cap every encoder that frames a key wider than that checks before
// proposing (go-qrdb#592) — checked again here, at Apply, so a key's
// validity never depends on which replica's raft.sm_storage happens to see
// it first, nor on a caller that reached this state machine some other way
// than those encoders; and a reserved key. It is refused on every state
// machine alike, so a request is valid or invalid regardless of sm_type.
func validateClientKey(op string, key []byte) error {
	if len(key) == 0 {
		return &InvalidKeyError{Op: op, Reason: "empty key"}
	}
	if len(key) > protocol.MaxClientKeyLength {
		return &InvalidKeyError{Op: op, Reason: "key length " + strconv.Itoa(len(key)) +
			" exceeds the maximum of " + strconv.Itoa(protocol.MaxClientKeyLength) + " bytes"}
	}
	if isStateMachineKey(key) {
		return &InvalidKeyError{Op: op, Reason: "key is reserved for the state machine's own bookkeeping"}
	}
	return nil
}

// isStateMachineKey reports whether key is one of the state machine's own
// records -- a reserved key or a lease-table record -- rather than client data.
// Clients may not write one, and enumeration does not return one.
func isStateMachineKey(key []byte) bool {
	if _, reserved := reservedKeys[string(key)]; reserved {
		return true
	}
	return lease.IsTableKey(key)
}

// reachesStateMachineKeys reports whether an enumeration rooted at prefix can
// match a state machine key. Every one begins with 0x00, so only the empty
// prefix and prefixes that begin with it can.
func reachesStateMachineKeys(prefix []byte) bool {
	return len(prefix) == 0 || prefix[0] == 0x00
}
