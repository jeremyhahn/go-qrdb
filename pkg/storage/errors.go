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

	// ErrStorageClosed is returned when operating on a closed engine.
	ErrStorageClosed = &StorageError{Code: "storage_closed", Message: "storage engine is closed"}

	// ErrEmptyKey is returned when an empty key is provided.
	ErrEmptyKey = &StorageError{Code: "empty_key", Message: "key must not be empty"}

	// ErrInvalidPath is returned when a file storage path is invalid.
	ErrInvalidPath = &StorageError{Code: "invalid_path", Message: "storage path is invalid"}
)
