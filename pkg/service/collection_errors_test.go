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

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollectionAlreadyExistsError(t *testing.T) {
	err := &CollectionAlreadyExistsError{Database: "mydb", Collection: "users"}
	assert.Equal(t, "service: collection already exists: mydb/users", err.Error())
}

func TestCollectionAlreadyExistsError_EmptyFields(t *testing.T) {
	err := &CollectionAlreadyExistsError{}
	assert.Equal(t, "service: collection already exists: /", err.Error())
}

func TestCollectionNotFoundError(t *testing.T) {
	err := &CollectionNotFoundError{Database: "mydb", Collection: "users"}
	assert.Equal(t, "service: collection not found: mydb/users", err.Error())
}

func TestCollectionNotFoundError_EmptyFields(t *testing.T) {
	err := &CollectionNotFoundError{}
	assert.Equal(t, "service: collection not found: /", err.Error())
}

func TestCollectionEmptyNameError(t *testing.T) {
	err := &CollectionEmptyNameError{}
	assert.Equal(t, "service: collection name must not be empty", err.Error())
}

func TestCollectionEmptyDatabaseError(t *testing.T) {
	err := &CollectionEmptyDatabaseError{}
	assert.Equal(t, "service: database name must not be empty for collection operation", err.Error())
}

func TestCollectionInvalidIDStrategyError(t *testing.T) {
	err := &CollectionInvalidIDStrategyError{Strategy: "random"}
	assert.Equal(t, "service: invalid collection ID strategy: random", err.Error())
}

func TestCollectionInvalidIDStrategyError_EmptyStrategy(t *testing.T) {
	err := &CollectionInvalidIDStrategyError{}
	assert.Equal(t, "service: invalid collection ID strategy: ", err.Error())
}

func TestCollectionMissingFieldHashError(t *testing.T) {
	err := &CollectionMissingFieldHashError{}
	assert.Equal(t, "service: field_hash ID strategy requires a field_hash field name", err.Error())
}

func TestCollectionDocumentNotFoundError(t *testing.T) {
	err := &CollectionDocumentNotFoundError{Database: "mydb", Collection: "users", ID: 42}
	assert.Equal(t, "service: document not found: mydb/users/42", err.Error())
}

func TestCollectionDocumentNotFoundError_Zero(t *testing.T) {
	err := &CollectionDocumentNotFoundError{Database: "db", Collection: "col", ID: 0}
	assert.Equal(t, "service: document not found: db/col/0", err.Error())
}

func TestCollectionEmptyDocumentError(t *testing.T) {
	err := &CollectionEmptyDocumentError{}
	assert.Equal(t, "service: document must not be empty", err.Error())
}

func TestCollectionInvalidDocumentIDError(t *testing.T) {
	err := &CollectionInvalidDocumentIDError{}
	assert.Equal(t, "service: document ID must not be zero", err.Error())
}

func TestCollectionMetadataError(t *testing.T) {
	cause := errors.New("disk full")
	err := &CollectionMetadataError{Op: "save", Err: cause}
	assert.Equal(t, "service: collection metadata save failed: disk full", err.Error())
}

func TestCollectionMetadataError_Unwrap(t *testing.T) {
	cause := errors.New("timeout")
	err := &CollectionMetadataError{Op: "load", Err: cause}
	assert.Equal(t, cause, errors.Unwrap(err))
}

func TestCollectionMetadataError_ErrorsIs(t *testing.T) {
	cause := errors.New("not found")
	err := &CollectionMetadataError{Op: "get", Err: cause}
	assert.True(t, errors.Is(err, cause))
}
