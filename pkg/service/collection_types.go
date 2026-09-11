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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ID strategy constants for collection document ID generation.
const (
	// IDStrategyUnixNano generates IDs from Unix nanosecond timestamps.
	IDStrategyUnixNano = "unix_nano"

	// IDStrategyIncrement generates monotonically increasing sequential IDs.
	IDStrategyIncrement = "increment"

	// IDStrategyFieldHash generates deterministic IDs by hashing a document field.
	IDStrategyFieldHash = "field_hash"
)

// collectionMetadataPrefix is the key prefix for collection metadata entries.
const collectionMetadataPrefix = "__sys:collections/"

// collectionDataPrefix is the key prefix for collection document entries.
const collectionDataPrefix = "__col:"

// IndexDefinition defines a secondary index on a collection field.
type IndexDefinition struct {
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique,omitempty"`
	Sparse    bool   `json:"sparse,omitempty"`
}

// CollectionInfo describes a collection and its configuration.
type CollectionInfo struct {
	Name          string            `json:"name"`
	Database      string            `json:"database"`
	IDStrategy    string            `json:"id_strategy"`
	FieldHash     string            `json:"field_hash,omitempty"`
	Schema        map[string]string `json:"schema,omitempty"`
	Indexes       []IndexDefinition `json:"indexes,omitempty"`
	DocumentCount int64             `json:"document_count"`
	StorageCodec  string            `json:"storage_codec,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

// CreateCollectionRequest contains parameters for creating a collection.
type CreateCollectionRequest struct {
	Name         string            `json:"name"`
	Database     string            `json:"database"`
	IDStrategy   string            `json:"id_strategy,omitempty"`
	FieldHash    string            `json:"field_hash,omitempty"`
	Indexes      []IndexDefinition `json:"indexes,omitempty"`
	StorageCodec string            `json:"storage_codec,omitempty"`
}

// DropCollectionRequest contains parameters for dropping a collection.
type DropCollectionRequest struct {
	Name     string `json:"name"`
	Database string `json:"database"`
}

// ListCollectionsRequest contains parameters for listing collections.
type ListCollectionsRequest struct {
	Database string `json:"database"`
}

// ListCollectionsResponse is returned with the list of collections.
type ListCollectionsResponse struct {
	Collections []CollectionInfo `json:"collections"`
}

// InsertDocumentRequest contains parameters for inserting a document.
type InsertDocumentRequest struct {
	Collection  string                 `json:"collection"`
	Database    string                 `json:"database"`
	Document    map[string]interface{} `json:"document"`
	Consistency consistency.Level      `json:"consistency,omitempty"`
	WriteMode   writemode.Mode         `json:"write_mode,omitempty"`
}

// InsertDocumentResponse is returned after a successful document insert.
type InsertDocumentResponse struct {
	ID uint64 `json:"id"`
}

// GetDocumentRequest contains parameters for retrieving a document.
type GetDocumentRequest struct {
	Collection  string            `json:"collection"`
	Database    string            `json:"database"`
	ID          uint64            `json:"id"`
	Consistency consistency.Level `json:"consistency,omitempty"`
}

// GetDocumentResponse is returned with the retrieved document.
type GetDocumentResponse struct {
	ID       uint64                 `json:"id"`
	Document map[string]interface{} `json:"document"`
}

// UpdateDocumentRequest contains parameters for updating a document.
type UpdateDocumentRequest struct {
	Collection  string                 `json:"collection"`
	Database    string                 `json:"database"`
	ID          uint64                 `json:"id"`
	Document    map[string]interface{} `json:"document"`
	Consistency consistency.Level      `json:"consistency,omitempty"`
	WriteMode   writemode.Mode         `json:"write_mode,omitempty"`
}

// UpdateDocumentResponse is returned after a successful document update.
type UpdateDocumentResponse struct {
	ID uint64 `json:"id"`
}

// DeleteDocumentRequest contains parameters for deleting a document.
type DeleteDocumentRequest struct {
	Collection string         `json:"collection"`
	Database   string         `json:"database"`
	ID         uint64         `json:"id"`
	WriteMode  writemode.Mode `json:"write_mode,omitempty"`
}

// DeleteDocumentResponse is returned after a successful document deletion.
type DeleteDocumentResponse struct {
	Deleted bool `json:"deleted"`
}

// ListDocumentsRequest contains parameters for listing documents.
type ListDocumentsRequest struct {
	Collection  string            `json:"collection"`
	Database    string            `json:"database"`
	PageSize    int32             `json:"page_size,omitempty"`
	PageToken   string            `json:"page_token,omitempty"`
	Consistency consistency.Level `json:"consistency,omitempty"`
}

// ListDocumentsResponse is returned with a page of documents.
type ListDocumentsResponse struct {
	Documents     []GetDocumentResponse `json:"documents"`
	NextPageToken string                `json:"next_page_token,omitempty"`
	TotalCount    int64                 `json:"total_count"`
}

// collectionMetadataKey returns the storage key for collection metadata.
func collectionMetadataKey(database, name string) string {
	return collectionMetadataPrefix + database + "/" + name
}

// collectionDataEntityType returns the entity type prefix for collection
// documents, used as the key prefix in the KV store.
func collectionDataEntityType(database, collection string) string {
	return collectionDataPrefix + database + ":" + collection
}

// collectionIndexName returns the secondary-index name for a field of a
// collection, in the database-scoped hierarchical form
// "{database}.{collection}.{field}" the rest of the index subsystem uses
// (see docs/secondary-indexes/architecture.md, "Index naming").
//
// It is the single formula shared by CreateCollection and DropCollection.
// The two derived the name independently only in the sense that the drop
// derived none at all: it never dropped anything, so the indexes a
// collection created outlived it (go-qrdb#399). Keeping one function means a
// future change to the naming scheme cannot desynchronise them again.
func collectionIndexName(database, collection, field string) string {
	return database + "." + collection + "." + field
}
