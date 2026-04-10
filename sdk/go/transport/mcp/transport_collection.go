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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CollectionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CollectionClient implementation (pending MCP tool definitions)
// ---------------------------------------------------------------------------

// CreateCollection creates a new collection within a database. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) CreateCollection(_ context.Context, _ transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	return nil, &transport.NotImplementedError{Method: "CreateCollection"}
}

// GetCollectionInfo returns information about a specific collection. This
// method is not yet implemented for the MCP transport and requires
// corresponding MCP tool definitions.
func (t *Transport) GetCollectionInfo(_ context.Context, _, _ string) (*transport.CollectionInfo, error) {
	return nil, &transport.NotImplementedError{Method: "GetCollectionInfo"}
}

// ListCollections returns all collections within a database. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) ListCollections(_ context.Context, _ string) (*transport.ListCollectionsResponse, error) {
	return nil, &transport.NotImplementedError{Method: "ListCollections"}
}

// DropCollection removes a collection from a database by name. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) DropCollection(_ context.Context, _, _ string) error {
	return &transport.NotImplementedError{Method: "DropCollection"}
}

// InsertDocument inserts a document into a collection and returns the assigned
// document ID. This method is not yet implemented for the MCP transport and
// requires corresponding MCP tool definitions.
func (t *Transport) InsertDocument(_ context.Context, _ transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	return nil, &transport.NotImplementedError{Method: "InsertDocument"}
}

// GetDocument returns a single document by its ID. This method is not yet
// implemented for the MCP transport and requires corresponding MCP tool
// definitions.
func (t *Transport) GetDocument(_ context.Context, _, _ string, _ uint64) (*transport.DocumentResponse, error) {
	return nil, &transport.NotImplementedError{Method: "GetDocument"}
}

// UpdateDocument replaces an existing document in a collection. This method is
// not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) UpdateDocument(_ context.Context, _ transport.UpdateDocumentRequest) error {
	return &transport.NotImplementedError{Method: "UpdateDocument"}
}

// DeleteDocument removes a document from a collection by its ID. This method
// is not yet implemented for the MCP transport and requires corresponding MCP
// tool definitions.
func (t *Transport) DeleteDocument(_ context.Context, _, _ string, _ uint64) error {
	return &transport.NotImplementedError{Method: "DeleteDocument"}
}

// ListDocuments returns a paginated list of documents in a collection. This
// method is not yet implemented for the MCP transport and requires
// corresponding MCP tool definitions.
func (t *Transport) ListDocuments(_ context.Context, _ transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	return nil, &transport.NotImplementedError{Method: "ListDocuments"}
}
