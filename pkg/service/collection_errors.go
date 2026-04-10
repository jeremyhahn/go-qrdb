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

import "strconv"

// CollectionAlreadyExistsError is returned when attempting to create a
// collection that already exists.
type CollectionAlreadyExistsError struct {
	Database   string
	Collection string
}

// Error returns a human-readable description.
func (e *CollectionAlreadyExistsError) Error() string {
	return "service: collection already exists: " + e.Database + "/" + e.Collection
}

// CollectionNotFoundError is returned when an operation references a
// collection that does not exist.
type CollectionNotFoundError struct {
	Database   string
	Collection string
}

// Error returns a human-readable description.
func (e *CollectionNotFoundError) Error() string {
	return "service: collection not found: " + e.Database + "/" + e.Collection
}

// CollectionEmptyNameError is returned when a collection name is empty.
type CollectionEmptyNameError struct{}

// Error returns a human-readable description.
func (e *CollectionEmptyNameError) Error() string {
	return "service: collection name must not be empty"
}

// CollectionEmptyDatabaseError is returned when a database name is empty
// in a collection operation.
type CollectionEmptyDatabaseError struct{}

// Error returns a human-readable description.
func (e *CollectionEmptyDatabaseError) Error() string {
	return "service: database name must not be empty for collection operation"
}

// CollectionInvalidIDStrategyError is returned when an unsupported ID
// strategy is specified.
type CollectionInvalidIDStrategyError struct {
	Strategy string
}

// Error returns a human-readable description.
func (e *CollectionInvalidIDStrategyError) Error() string {
	return "service: invalid collection ID strategy: " + e.Strategy
}

// CollectionMissingFieldHashError is returned when the field_hash ID
// strategy is specified but no field name is provided.
type CollectionMissingFieldHashError struct{}

// Error returns a human-readable description.
func (e *CollectionMissingFieldHashError) Error() string {
	return "service: field_hash ID strategy requires a field_hash field name"
}

// CollectionDocumentNotFoundError is returned when a document is not
// found within a collection.
type CollectionDocumentNotFoundError struct {
	Database   string
	Collection string
	ID         uint64
}

// Error returns a human-readable description.
func (e *CollectionDocumentNotFoundError) Error() string {
	return "service: document not found: " + e.Database + "/" +
		e.Collection + "/" + strconv.FormatUint(e.ID, 10)
}

// CollectionEmptyDocumentError is returned when an insert or update
// request contains an empty document.
type CollectionEmptyDocumentError struct{}

// Error returns a human-readable description.
func (e *CollectionEmptyDocumentError) Error() string {
	return "service: document must not be empty"
}

// CollectionInvalidDocumentIDError is returned when a document ID of 0
// is provided for an operation that requires a valid ID.
type CollectionInvalidDocumentIDError struct{}

// Error returns a human-readable description.
func (e *CollectionInvalidDocumentIDError) Error() string {
	return "service: document ID must not be zero"
}

// CollectionMetadataError is returned when a collection metadata
// operation fails.
type CollectionMetadataError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *CollectionMetadataError) Error() string {
	return "service: collection metadata " + e.Op + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CollectionMetadataError) Unwrap() error {
	return e.Err
}

// CollectionInvalidCodecError is returned when a storage codec is specified
// that is not registered in the current build.
type CollectionInvalidCodecError struct {
	Codec string
}

// Error returns a human-readable description.
func (e *CollectionInvalidCodecError) Error() string {
	return "service: invalid or unsupported storage codec: " + strconv.Quote(e.Codec)
}
