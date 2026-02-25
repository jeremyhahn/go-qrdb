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

package index

import "strconv"

// ClosedError is returned when an operation is attempted on a closed index.
type ClosedError struct {
	Name string
}

// Error returns a human-readable description of the closed index.
func (e *ClosedError) Error() string {
	return "index: closed: " + e.Name
}

// NilDBError is returned when a nil PebbleDB is passed to the constructor.
type NilDBError struct{}

// Error returns a human-readable description of the nil database.
func (e *NilDBError) Error() string {
	return "index: pebble db is nil"
}

// EmptyNameError is returned when an empty index name is provided.
type EmptyNameError struct{}

// Error returns a human-readable description of the empty name.
func (e *EmptyNameError) Error() string {
	return "index: name must not be empty"
}

// NilFieldValueError is returned when a nil field value is passed.
type NilFieldValueError struct {
	Op string
}

// Error returns a human-readable description of the nil field value.
func (e *NilFieldValueError) Error() string {
	return "index: nil field value in " + e.Op
}

// UniqueViolationError is returned when a unique index constraint is violated.
type UniqueViolationError struct {
	IndexName  string
	FieldValue []byte
	ExistingID uint64
	NewID      uint64
}

// Error returns a human-readable description of the unique constraint violation.
func (e *UniqueViolationError) Error() string {
	return "index: unique violation on " + e.IndexName +
		": field value already mapped to entity " +
		strconv.FormatUint(e.ExistingID, 10) +
		", cannot map to " +
		strconv.FormatUint(e.NewID, 10)
}

// KeyTooShortError is returned when an index key is too short to parse.
type KeyTooShortError struct{}

// Error returns a human-readable description of the short key.
func (e *KeyTooShortError) Error() string {
	return "index: key too short to extract entity ID"
}

// KeyPrefixMismatchError is returned when a key doesn't match the expected prefix.
type KeyPrefixMismatchError struct{}

// Error returns a human-readable description of the prefix mismatch.
func (e *KeyPrefixMismatchError) Error() string {
	return "index: key prefix does not match index"
}
