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

package backend

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ClusterOperationError is returned when a cluster-only operation
// (AddNode, RemoveNode, StartShard, etc.) is called on a LocalBackend
// that does not support Raft consensus.
type ClusterOperationError struct {
	Op string
}

// Error returns a human-readable description.
func (e *ClusterOperationError) Error() string {
	return "backend: cluster operation not supported: " + e.Op
}

// UnsupportedOperationError is returned when the SimpleStateMachine
// receives an opcode it cannot handle (e.g., MVCC, lease, or index
// operations that require the full PebbleStateMachine).
type UnsupportedOperationError struct {
	Op byte
}

// Error returns a human-readable description.
func (e *UnsupportedOperationError) Error() string {
	return "backend: unsupported operation: 0x" + strconv.FormatUint(uint64(e.Op), 16)
}

// Capability names the absent verb, declaring this an application-level
// refusal rather than a fault. See protocol.CapabilityError.
func (e *UnsupportedOperationError) Capability() string {
	return "opcode 0x" + strconv.FormatUint(uint64(e.Op), 16)
}

// UnsupportedOperationError is an absent capability, not a fault: a state
// machine that has no handler for an opcode must answer the proposer, not halt.
var _ protocol.CapabilityError = (*UnsupportedOperationError)(nil)

// BackendClosedError is returned when an operation is attempted on a
// LocalBackend that has already been closed.
type BackendClosedError struct{}

// Error returns a human-readable description.
func (e *BackendClosedError) Error() string {
	return "backend: closed"
}

// Unwrap returns storage.ErrStorageClosed.
//
// Closing a LocalBackend closes the state machine and with it the store, so
// from a caller's side this reports the same fact as a backend's own
// ClosedError: the store you were using was closed under you. Whether the
// racing call lost at the backend's gate or at the storage engine's is an
// internal detail, and a caller forced to distinguish them ends up matching
// two error strings instead of one sentinel (go-qrdb#371).
func (e *BackendClosedError) Unwrap() error {
	return storage.ErrStorageClosed
}

// LookupTypeError is returned when a Lookup query has an unsupported
// type that the SimpleStateMachine cannot handle.
type LookupTypeError struct {
	Type string
}

// Error returns a human-readable description.
func (e *LookupTypeError) Error() string {
	return "backend: unsupported lookup type: " + e.Type
}

// UndecodableCommandError reports a committed command SimpleStateMachine
// cannot decode as a base KV command. It is refused in the entry's result,
// not returned from Update: the bytes are the same on every replica, so the
// refusal is too.
type UndecodableCommandError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UndecodableCommandError) Error() string {
	return "backend: undecodable command: " + e.Err.Error()
}

// Unwrap returns the decode error.
func (e *UndecodableCommandError) Unwrap() error {
	return e.Err
}
