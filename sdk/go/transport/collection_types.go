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

package transport

import "context"

// ---------------------------------------------------------------------------
// Collection management types
// ---------------------------------------------------------------------------

// CollectionInfo is the transport representation of a collection.
type CollectionInfo struct {
	Name          string            `json:"name"`
	Database      string            `json:"database"`
	IDStrategy    string            `json:"id_strategy"`
	FieldHash     string            `json:"field_hash,omitempty"`
	Schema        map[string]string `json:"schema,omitempty"`
	DocumentCount int64             `json:"document_count"`
	StorageCodec  string            `json:"storage_codec,omitempty"`
	CreatedAt     string            `json:"created_at"`
}

// CreateCollectionRequest represents a request to create a collection
// within a database.
type CreateCollectionRequest struct {
	Database     string `json:"database"`
	Name         string `json:"name"`
	IDStrategy   string `json:"id_strategy,omitempty"`
	FieldHash    string `json:"field_hash,omitempty"`
	StorageCodec string `json:"storage_codec,omitempty"`
}

// ListCollectionsResponse holds the list of collections returned by a
// list operation.
type ListCollectionsResponse struct {
	Collections []CollectionInfo `json:"collections"`
}

// ---------------------------------------------------------------------------
// Document types
// ---------------------------------------------------------------------------

// DocumentResponse represents a document returned from the store.
// The ID is serialized as a JSON string to prevent precision loss in
// JavaScript clients (Number.MAX_SAFE_INTEGER < 2^63-1).
type DocumentResponse struct {
	ID       uint64                 `json:"id,string"`
	Document map[string]interface{} `json:"document"`
}

// InsertDocumentRequest represents a request to insert a document into
// a collection.
type InsertDocumentRequest struct {
	Database   string                 `json:"database"`
	Collection string                 `json:"collection"`
	Document   map[string]interface{} `json:"document"`
}

// InsertDocumentResponse holds the ID assigned to a newly inserted
// document.
type InsertDocumentResponse struct {
	ID uint64 `json:"id,string"`
}

// UpdateDocumentRequest represents a request to update an existing
// document in a collection.
type UpdateDocumentRequest struct {
	Database   string                 `json:"database"`
	Collection string                 `json:"collection"`
	ID         uint64                 `json:"id,string"`
	Document   map[string]interface{} `json:"document"`
}

// ListDocumentsRequest represents a paginated request to list documents
// in a collection.
type ListDocumentsRequest struct {
	Database   string `json:"database"`
	Collection string `json:"collection"`
	PageSize   int32  `json:"page_size,omitempty"`
	PageToken  string `json:"page_token,omitempty"`
}

// ListDocumentsResponse holds a page of documents returned by a list
// operation.
type ListDocumentsResponse struct {
	Documents     []DocumentResponse `json:"documents"`
	NextPageToken string             `json:"next_page_token,omitempty"`
	TotalCount    int64              `json:"total_count"`
}

// ---------------------------------------------------------------------------
// Collection management operations
// ---------------------------------------------------------------------------

// CollectionClient provides collection and document management operations.
type CollectionClient interface {
	// CreateCollection creates a new collection within a database.
	CreateCollection(ctx context.Context, req CreateCollectionRequest) (*CollectionInfo, error)

	// GetCollectionInfo returns information about a specific collection.
	GetCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error)

	// ListCollections returns all collections within a database.
	ListCollections(ctx context.Context, database string) (*ListCollectionsResponse, error)

	// DropCollection removes a collection from a database by name.
	DropCollection(ctx context.Context, database, name string) error

	// InsertDocument inserts a document into a collection and returns
	// the assigned document ID.
	InsertDocument(ctx context.Context, req InsertDocumentRequest) (*InsertDocumentResponse, error)

	// GetDocument returns a single document by its ID.
	GetDocument(ctx context.Context, database, collection string, id uint64) (*DocumentResponse, error)

	// UpdateDocument replaces an existing document in a collection.
	UpdateDocument(ctx context.Context, req UpdateDocumentRequest) error

	// DeleteDocument removes a document from a collection by its ID.
	DeleteDocument(ctx context.Context, database, collection string, id uint64) error

	// ListDocuments returns a paginated list of documents in a collection.
	ListDocuments(ctx context.Context, req ListDocumentsRequest) (*ListDocumentsResponse, error)
}
