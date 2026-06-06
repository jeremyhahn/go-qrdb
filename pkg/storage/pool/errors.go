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

package pool

import "fmt"

// NilStorageError is returned by NewPebbleStateStore when the supplied
// storage is nil.
type NilStorageError struct{}

// Error implements the error interface.
func (e *NilStorageError) Error() string {
	return "qrdb/storage/pool: storage must not be nil"
}

// EmptyKeyError is returned when Get or Put is invoked with an empty
// logical key.
type EmptyKeyError struct{}

// Error implements the error interface.
func (e *EmptyKeyError) Error() string {
	return "qrdb/storage/pool: key must not be empty"
}

// GetError wraps an underlying Pebble Get failure with the logical
// key that triggered it.
type GetError struct {
	Key string
	Err error
}

// Error implements the error interface.
func (e *GetError) Error() string {
	return fmt.Sprintf("qrdb/storage/pool: get %q: %v", e.Key, e.Err)
}

// Unwrap returns the underlying error.
func (e *GetError) Unwrap() error { return e.Err }

// PutError wraps an underlying Pebble Set failure with the logical
// key that triggered it.
type PutError struct {
	Key string
	Err error
}

// Error implements the error interface.
func (e *PutError) Error() string {
	return fmt.Sprintf("qrdb/storage/pool: put %q: %v", e.Key, e.Err)
}

// Unwrap returns the underlying error.
func (e *PutError) Unwrap() error { return e.Err }
