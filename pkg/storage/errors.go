// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import "errors"

// StorageError is a typed error for storage operations.
type StorageError struct {
	Code    string
	Message string
}

// Error implements the error interface.
func (e *StorageError) Error() string {
	return "qrdb/storage: " + e.Code + ": " + e.Message
}

var (
	// ErrKeyNotFound is returned when a key does not exist in storage.
	ErrKeyNotFound = &StorageError{Code: "key_not_found", Message: "key does not exist"}

	// ErrStorageClosed is the sentinel for "this storage engine has been
	// closed". It is THE identity of the condition across the whole stack:
	// every closed-store failure a backend reports unwraps to it, whether the
	// backend returns this value directly or a ClosedError, and whether the
	// caller receives it bare or inside an OpError, BatchError, SnapshotError
	// or a service/transport wrapper. One errors.Is covers all of them.
	ErrStorageClosed = &StorageError{Code: "storage_closed", Message: "storage engine is closed"}

	// ErrEmptyKey is returned when an empty key is provided.
	ErrEmptyKey = &StorageError{Code: "empty_key", Message: "key must not be empty"}

	// ErrInvalidPath is returned when a file storage path is invalid.
	ErrInvalidPath = &StorageError{Code: "invalid_path", Message: "storage path is invalid"}
)

// OpError wraps an error with the storage operation that caused it.
// Use OpError for errors from Storage interface methods (Get, Set, Delete).
type OpError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the operation failure.
func (e *OpError) Error() string {
	return "storage " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *OpError) Unwrap() error {
	return e.Err
}

// KeyNotFoundError is returned when a specific key does not exist in storage.
// Unlike the ErrKeyNotFound sentinel, KeyNotFoundError carries the missing key.
type KeyNotFoundError struct {
	Key []byte
}

// Error returns a human-readable description of the missing key.
func (e *KeyNotFoundError) Error() string {
	return "key not found: " + string(e.Key)
}

// ClosedError is returned when operating on a closed storage instance.
//
// It carries no data, so ErrStorageClosed is the value callers match against;
// ClosedError is the shape the backends construct. The two are not rival
// concepts: Unwrap ties every ClosedError to the sentinel.
type ClosedError struct{}

// Error returns a human-readable description indicating the storage is closed.
func (e *ClosedError) Error() string {
	return "storage is closed"
}

// Unwrap returns ErrStorageClosed so that a closed-store failure is matchable
// with errors.Is at any depth. Without it a caller has only errors.As against
// this concrete type, which fails the moment a backend returns the sentinel
// instead — and go-quicbaas resorted to matching the string "pebble: closed"
// for exactly that reason.
func (e *ClosedError) Unwrap() error {
	return ErrStorageClosed
}

// IsClosed reports whether err is, or wraps, the storage engine having been
// closed. Prefer it over comparing error strings.
func IsClosed(err error) bool {
	return errors.Is(err, ErrStorageClosed)
}

// BatchError wraps an error that occurred during a batch operation.
type BatchError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the batch operation failure.
func (e *BatchError) Error() string {
	return "storage batch " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BatchError) Unwrap() error {
	return e.Err
}

// IteratorError wraps an error that occurred during iteration.
type IteratorError struct {
	Err error
}

// Error returns a human-readable description of the iterator failure.
func (e *IteratorError) Error() string {
	return "storage iterator: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *IteratorError) Unwrap() error {
	return e.Err
}

// SnapshotError wraps an error that occurred during a snapshot operation.
type SnapshotError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the snapshot failure.
func (e *SnapshotError) Error() string {
	return "storage snapshot " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *SnapshotError) Unwrap() error {
	return e.Err
}
