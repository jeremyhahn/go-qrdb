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

// Package mvcc provides a multi-version concurrency control engine backed
// by PebbleDB. Each Put creates a new revision while retaining historical
// versions for point-in-time reads and compaction.
package mvcc

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// KeyNotFoundError is returned when a key does not exist or has been
// deleted at the requested revision.
type KeyNotFoundError struct {
	Key string
}

// Error returns a human-readable description of the missing key.
func (e *KeyNotFoundError) Error() string {
	return "mvcc: key not found: " + e.Key
}

// KeyNotFound returns the key name, satisfying the cross-package error
// interface used by the service layer for cycle-free error translation.
func (e *KeyNotFoundError) KeyNotFound() string {
	return e.Key
}

// RevisionCompactedError is returned when a read targets a revision
// that has been compacted.
type RevisionCompactedError struct {
	Requested int64
	Compacted int64
}

// Error returns a human-readable description of the compacted revision.
func (e *RevisionCompactedError) Error() string {
	return "mvcc: revision " + strconv.FormatInt(e.Requested, 10) +
		" has been compacted (earliest: " +
		strconv.FormatInt(e.Compacted, 10) + ")"
}

// RevisionCompacted returns the requested and compacted revisions,
// satisfying the cross-package error interface used by the service
// layer for cycle-free error translation.
func (e *RevisionCompactedError) RevisionCompacted() (requested, compacted int64) {
	return e.Requested, e.Compacted
}

// RevisionNotFoundError is returned when the requested revision does
// not exist for a given key.
type RevisionNotFoundError struct {
	Key      string
	Revision int64
}

// Error returns a human-readable description of the missing revision.
func (e *RevisionNotFoundError) Error() string {
	return "mvcc: revision " + strconv.FormatInt(e.Revision, 10) +
		" not found for key: " + e.Key
}

// RevisionNotFound returns the key and missing revision, satisfying
// the cross-package error interface used by the service layer for
// cycle-free error translation.
func (e *RevisionNotFoundError) RevisionNotFound() (key string, revision int64) {
	return e.Key, e.Revision
}

// StoreError is returned when a storage-level operation fails.
type StoreError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the storage failure.
func (e *StoreError) Error() string {
	return "mvcc: " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StoreError) Unwrap() error {
	return e.Err
}

// EncodingError is returned when key or metadata encoding/decoding fails.
type EncodingError struct {
	Reason string
}

// Error returns a human-readable description of the encoding failure.
func (e *EncodingError) Error() string {
	return "mvcc: encoding error: " + e.Reason
}

// CompactionError is returned when a compaction operation fails.
type CompactionError struct {
	Err error
}

// Error returns a human-readable description of the compaction failure.
func (e *CompactionError) Error() string {
	return "mvcc: compaction failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CompactionError) Unwrap() error {
	return e.Err
}

// TransactionConflictError is returned when a transaction's compare
// preconditions fail.
type TransactionConflictError struct {
	Key string
}

// Error returns a human-readable description of the conflict.
func (e *TransactionConflictError) Error() string {
	return "mvcc: transaction conflict on key: " + e.Key
}

// UnsupportedSortTargetError is returned when a range query requests a
// service.SortTarget value outside the set of targets the store knows how
// to sort by.
type UnsupportedSortTargetError struct {
	Target service.SortTarget
}

// Error returns a human-readable description of the unsupported target.
func (e *UnsupportedSortTargetError) Error() string {
	return "mvcc: unsupported sort target: " + strconv.FormatUint(uint64(e.Target), 10)
}
