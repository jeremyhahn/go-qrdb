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

package service

// unsupportedQueryTypeErr matches the refusal a Lookup switch returns when
// it has no case at all for the query's dynamic type, as distinct from a
// more specific invalid-query refusal that names a resource (an unknown
// index, a missing lease). It is the shape every MVCC-only query — an as-of
// read, a revision listing, a change-index scan — takes when the shard's
// state machine keeps no MVCC revision index, which is the case whenever
// raft.sm_type is left at its default ("pebble") instead of "mvcc".
//
// Matched structurally, without importing pkg/engine, because pkg/engine
// already imports pkg/service (KVEngine and MVCCEngine both build their
// results from service-layer types) and the reverse import would cycle.
// pkg/engine.InvalidQueryError satisfies this interface.
type unsupportedQueryTypeErr interface {
	error
	// UnsupportedQueryType reports whether this is the "no case for this
	// query type at all" refusal.
	UnsupportedQueryType() bool
}

// MVCCBackendRequiredError is returned when an MVCC-only operation — an
// as-of read (GetAtRevision), a revision listing (CurrentRevision), a range
// read pinned to a historical revision, a change-index scan, or
// RestoreToRevision — is attempted against a shard whose configured state
// machine keeps no MVCC revision history.
//
// The default raft.sm_type is "pebble", a plain key-value state machine
// with no revision index; only raft.sm_type: mvcc maintains the history
// these operations read. Without this type the refusal a caller sees is the
// state machine's generic "unsupported query type", which names the
// symptom and not the fix. Every transport maps this error to 501 Not
// Implemented / codes.Unimplemented so the caller learns the operation is
// unavailable on this deployment, not merely that this one call failed.
type MVCCBackendRequiredError struct {
	// Err is the underlying refusal from the state machine.
	Err error
}

// Error returns a human-readable, actionable description.
func (e *MVCCBackendRequiredError) Error() string {
	msg := "service: this operation requires an MVCC-backed shard " +
		"(configure raft.sm_type: mvcc); the configured state machine keeps no MVCC revision history"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *MVCCBackendRequiredError) Unwrap() error {
	return e.Err
}
