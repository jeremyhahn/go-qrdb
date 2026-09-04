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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// batchOpSupported reports whether the batch handlers in this package can
// execute op. It is the single definition of that set, so the pre-flight check
// below and each handleBatch dispatch cannot drift apart — a divergence would
// silently reintroduce the partial-write hole validateBatchOps exists to close.
// TestBatchValidationMatchesDispatch pins the two together over every opcode.
func batchOpSupported(op byte) bool {
	switch op {
	case kv.OpPut, kv.OpDelete:
		return true
	default:
		return false
	}
}

// validateBatchOps rejects a batch naming an operation these state machines
// cannot execute, BEFORE any of it is applied.
//
// The batch handlers walk their operations applying each in turn, so without
// this they refuse only on REACHING the unknown one, having already written
// every operation before it and delivered its watch notifications. While such
// a refusal reached Raft as an Apply error that was the lesser of two problems
// — the shard was poisoned anyway. Now that an unrecognised opcode is an
// application-level refusal the shard survives (go-qrdb#129), a partial write
// would become a lie the caller acts on: told the batch was refused, with half
// of it committed.
//
// A batch is atomic or it is nothing, so an opcode this state machine does not
// implement refuses the whole batch and mutates nothing.
func validateBatchOps(ops []protocol.DecodedOp) error {
	for _, op := range ops {
		if !batchOpSupported(op.Type) {
			return &InvalidOpcodeError{Op: op.Type}
		}
	}
	return nil
}

// resolveApplyError converts a handler error into the right action for the
// SM.Update contract. When the error is an APPLICATION-level condition the
// caller MUST NOT return it to Raft, because Raft treats a non-nil Update
// error as a fatal Apply failure that prevents lastApplied from advancing.
// This stalls every subsequent SyncRead on the shard until the daemon
// restarts.
//
// Two conditions are application-level, and they share one cause: the
// proposer asked for something THIS state machine was not built to do.
// A third, GuardedWriteUnsupportedError, was classified here between
// go-qrdb#175 and go-qrdb#216, when a guarded verb could be refused for
// naming a backend whose writes could not carry its result. Backend now
// requires the durable pair of every implementation, so no backend refuses
// and the error is gone; a mechanism that cannot fire is an invitation to
// re-arm it.
//
//   - ServiceNotConfiguredError — the verb exists but its subsystem is not
//     wired into this instance.
//   - InvalidOpcodeError — the verb does not exist here at all, either as the
//     leading op byte of an entry or as a sub-operation inside a batch.
//
// Neither says anything about the health of the storage underneath, so
// neither may halt Apply. go-qrdb#129: the unrecognised-opcode case used to
// be returned. Measured consequence on a single-replica raft node running
// sm_type=memory, from ONE ordinary authorized CompareAndDelete — an opcode
// no state machine in this package implements — was that go-quicraft tripped
// the shard's circuit breaker, after which every later proposal failed,
// including plain Put, and a linearizable Get with no deadline never
// returned. A verb this state machine does not implement must be refused;
// refusing it must not cost the cluster the shard.
//
// Instead, the error is encoded in sm.Result.Data so the proposer can surface
// it to its caller without poisoning consensus. All other errors are returned
// verbatim so genuine infrastructure failures still halt Apply.
//
// Returns (result, stop). When stop is true the outer loop should return the
// (possibly nil) error to Raft immediately. When stop is false the outer loop
// should assign the returned result to results[i] and continue.
func resolveApplyError(log *slog.Logger, entryIndex uint64, handlerResult sm.Result, handlerErr error) (sm.Result, bool, error) {
	if handlerErr == nil {
		return handlerResult, false, nil
	}
	var sncErr *ServiceNotConfiguredError
	var opErr *InvalidOpcodeError
	switch {
	case errors.As(handlerErr, &sncErr):
		if log != nil {
			log.Error("statemachine: subsystem not configured, encoding as result",
				slog.String("service", sncErr.Service),
				slog.Uint64("entry_index", entryIndex))
		}
	case errors.As(handlerErr, &opErr):
		if log != nil {
			log.Error("statemachine: unrecognised opcode, encoding as result",
				slog.Int("opcode", int(opErr.Op)),
				slog.Uint64("entry_index", entryIndex))
		}
	default:
		return sm.Result{}, true, handlerErr
	}

	// EncodeAppError, not a bare string. checkResultError recognises an
	// application error ONLY by that prefix, so an unprefixed payload never
	// decodes: the proposer reads the error text as an ordinary result and
	// reports the transaction as a failed COMPARE with a nil error. For a
	// caller, PutIfAbsent returning (false, nil) means "the key already
	// exists" — so a misconfigured subsystem is reported as another writer
	// having won the race, and the caller goes on to read a key that was
	// never written (go-qrdb#62). Every other producer in the tree already
	// encodes through this function.
	// EncodeCapabilityError, not the generic encoder: both branches above are
	// specifically "this state machine was not built to do that", and carrying
	// that distinction is what lets the service layer hand the caller a typed
	// CapabilityUnsupportedError instead of something indistinguishable from a
	// unique-index violation (go-qrdb#141). Every other application error in
	// the tree — constraint violations, invalid TTLs, a closed engine — stays
	// on EncodeAppError, because those were understood and refused on their
	// merits.
	return sm.Result{Data: protocol.EncodeCapabilityError(handlerErr.Error())}, false, nil
}
