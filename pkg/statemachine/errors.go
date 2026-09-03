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

import "strconv"

// InvalidOpcodeError is returned when the state machine receives an entry
// with an unrecognized operation code.
type InvalidOpcodeError struct {
	Op byte
}

// Error returns a human-readable description of the unknown op code.
func (e *InvalidOpcodeError) Error() string {
	return "statemachine: unknown opcode: " + strconv.Itoa(int(e.Op))
}

// IncompleteDataError is returned when a command payload is truncated
// or missing required fields.
type IncompleteDataError struct {
	Reason string
}

// Error returns a human-readable description of the data deficiency.
func (e *IncompleteDataError) Error() string {
	return "statemachine: incomplete data: " + e.Reason
}

// SnapshotError is returned when a snapshot save or recovery operation fails.
type SnapshotError struct {
	Op    string
	Inner error
}

// Error returns a human-readable description of the snapshot failure.
func (e *SnapshotError) Error() string {
	return "statemachine: snapshot " + e.Op + ": " + e.Inner.Error()
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *SnapshotError) Unwrap() error {
	return e.Inner
}

// StorageError is returned when the backing storage layer returns an error.
type StorageError struct {
	Op    string
	Inner error
}

// Error returns a human-readable description of the storage failure.
func (e *StorageError) Error() string {
	return "statemachine: storage " + e.Op + ": " + e.Inner.Error()
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *StorageError) Unwrap() error {
	return e.Inner
}

// ServiceNotConfiguredError is returned when a handler is invoked for a
// service subsystem (e.g. lease, index) that has not been wired into the
// state machine via its corresponding Option.
type ServiceNotConfiguredError struct {
	Service string
}

// Error returns a human-readable description of the missing service.
func (e *ServiceNotConfiguredError) Error() string {
	return "statemachine: " + e.Service + " not configured"
}

// GuardedWriteUnsupportedError is returned when a guarded verb is applied on a
// state machine whose backend cannot carry what the verb's result PROMISES: a
// write that is durable when it is acknowledged, whose failure is reported,
// and which no earlier write to the same key can overwrite.
//
// It is a REFUSAL, deliberately, and not a silent absence of the opcode.
// "This state machine was not built with that verb" and "this backend will not
// honour it" are different facts and a caller must be able to tell them apart:
// the first is a build/configuration choice, the second says the operator
// picked a backend whose write path cannot support the guarantee, and only the
// second is fixed by changing raft.sm_type.
//
// It is classified as an APPLICATION error by resolveApplyError, exactly as
// ServiceNotConfiguredError is, so it reaches the proposer as a typed
// capability refusal and never halts Apply. Returning it to Raft would trip
// the shard's circuit breaker (go-qrdb#129).
//
// The alternative — executing the verb anyway and reporting success — is what
// the local Codex review on go-qrdb#175 blocked. Its specific hazard, an older
// pending write landing on disk after a newer one, was fixed by go-qrdb#224:
// FastBackend now orders its persistence through persistOrder.
//
// The refusal stands on what is still true there: the write is acknowledged
// before it is durable, and a storage failure is logged rather than returned.
// A caller told "you released the claim" can still be contradicted by the
// disk, which is the failure the guarded plane exists to prevent. Supplying
// the missing promises is go-qrdb#216.
type GuardedWriteUnsupportedError struct {
	// Op is the refused operation code.
	Op byte

	// Backend names the backend that cannot honour the guarantee, so an
	// operator reading the refusal knows which knob to turn.
	Backend string
}

// Error returns a human-readable description naming the opcode, the backend and
// the promise the backend cannot keep.
//
// It names the CURRENT reason, which is not the reason this error was written
// with. It originally cited unordered persistence as well; go-qrdb#224 fixed
// that, and a refusal that keeps citing a repaired defect sends an operator to
// look at the wrong thing. What remains is the pair go-qrdb#216 covers.
//
// The tracker ID stays in THIS comment and out of the message. No other error
// string in the tree carries one, and a number an operator cannot resolve is
// noise in the one sentence they are given — the same reason docs/<feature>/
// is publication-bound.
func (e *GuardedWriteUnsupportedError) Error() string {
	return "statemachine: guarded opcode " + strconv.Itoa(int(e.Op)) +
		" is not supported on the " + e.Backend +
		" backend: it acknowledges a write before it is durable and logs a storage " +
		"failure instead of returning it"
}
