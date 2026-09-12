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
	"log/slog"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ResolveApplyError converts a handler error into the right action for the
// SM.Update contract, and is THE classification seam for this repository.
// Every Update implementation in the tree routes its handler errors through
// this one function — the state machines in this package, the KV and MVCC
// engines, the Pebble state machine and the simple backend state machine —
// and test/buildgates asserts that they do.
//
// When the error is an APPLICATION-level condition the caller MUST NOT return
// it to Raft, because Raft treats a non-nil Update error as a fatal Apply
// failure that prevents lastApplied from advancing. This stalls every
// subsequent SyncRead on the shard until the daemon restarts.
//
// An application-level condition is one the error DECLARES, by implementing
// protocol.CapabilityError: the proposer asked for something this state
// machine was not built to do. That covers a verb whose subsystem is not wired
// into this instance (ServiceNotConfiguredError), a verb that does not exist
// here at all (InvalidOpcodeError, protocol.UnknownOpError), and a subsystem
// compiled out of this binary (timeseries.NotCompiledError). A fourth,
// GuardedWriteUnsupportedError, was classified here between go-qrdb#175 and
// go-qrdb#216, when a guarded verb could be refused for naming a backend whose
// writes could not carry its result. Backend now requires the durable pair of
// every implementation, so no backend refuses and the error is gone; a
// mechanism that cannot fire is an invitation to re-arm it.
//
// None of those say anything about the health of the storage underneath, so
// none may halt Apply. go-qrdb#129: the unrecognised-opcode case used to be
// returned. Measured consequence on a single-replica raft node running
// sm_type=memory, from ONE ordinary authorized CompareAndDelete — an opcode
// no state machine in this package implements — was that go-quicraft tripped
// the shard's circuit breaker, after which every later proposal failed,
// including plain Put, and a linearizable Get with no deadline never
// returned. go-qrdb#520 measured the same shape on the MVCC engine, which had
// no classification at all: one refused time-series write, and a plain KV.Get
// on a key read successfully microseconds earlier burned the caller's whole
// deadline and reported `quicraft: timeout`. A verb this state machine does
// not implement must be refused; refusing it must not cost the cluster the
// shard.
//
// Instead, the error is encoded in sm.Result.Data so the proposer can surface
// it to its caller without poisoning consensus. All other errors are returned
// verbatim so genuine infrastructure failures still halt Apply — a replica
// that has actually diverged must stop rather than serve wrong data, so
// silence means fatal and only a declaration means benign.
//
// Returns (result, stop). When stop is true the outer loop should return the
// (possibly nil) error to Raft immediately. When stop is false the outer loop
// should assign the returned result to results[i] and continue.
func ResolveApplyError(log *slog.Logger, entryIndex uint64, handlerResult sm.Result, handlerErr error) (sm.Result, bool, error) {
	if handlerErr == nil {
		return handlerResult, false, nil
	}
	capErr, ok := protocol.AsCapabilityError(handlerErr)
	if !ok {
		return sm.Result{}, true, handlerErr
	}
	if log != nil {
		log.Error("statemachine: absent capability, encoding as result",
			slog.String("capability", capErr.Capability()),
			slog.String("error", handlerErr.Error()),
			slog.Uint64("entry_index", entryIndex))
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
	// EncodeCapabilityError, not the generic encoder: a CapabilityError is
	// specifically "this state machine was not built to do that", and carrying
	// that distinction is what lets the service layer hand the caller a typed
	// CapabilityUnsupportedError instead of something indistinguishable from a
	// unique-index violation (go-qrdb#141). Every other application error in
	// the tree — constraint violations, invalid TTLs, a closed engine — stays
	// on EncodeAppError, because those were understood and refused on their
	// merits.
	return sm.Result{Data: protocol.EncodeCapabilityError(handlerErr.Error())}, false, nil
}
