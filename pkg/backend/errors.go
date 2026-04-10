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

import "strconv"

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

// BackendClosedError is returned when an operation is attempted on a
// LocalBackend that has already been closed.
type BackendClosedError struct{}

// Error returns a human-readable description.
func (e *BackendClosedError) Error() string {
	return "backend: closed"
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
