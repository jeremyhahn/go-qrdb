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

// Package store provides persistent storage backends for QRDB state machines.
package store

// OpenError is returned when the PebbleDB database cannot be opened.
type OpenError struct {
	Dir string
	Err error
}

// Error returns a human-readable description of the open failure.
func (e *OpenError) Error() string {
	return "pebbledb: open failed: " + e.Dir + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *OpenError) Unwrap() error {
	return e.Err
}

// WriteError is returned when a write operation fails.
type WriteError struct {
	Err error
}

// Error returns a human-readable description of the write failure.
func (e *WriteError) Error() string {
	return "pebbledb: write failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *WriteError) Unwrap() error {
	return e.Err
}

// ReadError is returned when a read operation fails.
type ReadError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the read failure.
func (e *ReadError) Error() string {
	return "pebbledb: read failed: " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ReadError) Unwrap() error {
	return e.Err
}

// SnapshotError is returned when a snapshot operation fails.
type SnapshotError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the snapshot failure.
func (e *SnapshotError) Error() string {
	return "pebbledb: snapshot " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *SnapshotError) Unwrap() error {
	return e.Err
}

// InvalidQueryError is returned when a Lookup receives an unsupported query type.
type InvalidQueryError struct {
	Reason string
}

// Error returns a human-readable description of the invalid query.
func (e *InvalidQueryError) Error() string {
	return "pebbledb: invalid query: " + e.Reason
}

// UnsupportedQueryType reports whether this refusal is the fallback the Lookup
// switch returns when the query's dynamic type has no case at all, as opposed
// to a specific invalid-query refusal that names a resource (an unknown index,
// a lessor that is not configured). Only the fallback is safe to reinterpret as
// "this state machine has no MVCC revision index".
//
// PebbleStateMachine is the DEFAULT state machine (raft.sm_type: pebble) and it
// keeps no revisions, so every MVCC-only query — as-of reads, current revision,
// change-index scans, and the reads behind RestoreToRevision — lands here. This
// mirrors the identical marker on pkg/engine's InvalidQueryError; pkg/service
// duck-types both through one unexported interface, so without this method the
// default backend surfaces a bare "unsupported query type" instead of the
// actionable error naming raft.sm_type: mvcc.
func (e *InvalidQueryError) UnsupportedQueryType() bool {
	return e.Reason == "unsupported query type"
}

// NotOpenError is returned when operations are attempted on an unopened store.
type NotOpenError struct{}

// Error returns a human-readable description indicating the store is not open.
func (e *NotOpenError) Error() string {
	return "pebbledb: store not open"
}
