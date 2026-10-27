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
	"encoding/json"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CollectionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Server-side JSON decode types for collection responses
// ---------------------------------------------------------------------------

// serverCollectionInfo mirrors service.CollectionInfo
// (pkg/service/collection_types.go) for JSON decoding from the MCP
// server's qrdb_collection_create/info/list tool results. CreatedAt is
// decoded as a plain string: Go's default time.Time JSON encoding is an
// RFC3339-quoted string, which unmarshals cleanly into a string field
// without requiring a time import here, matching
// transport.CollectionInfo's own string-typed CreatedAt.
type serverCollectionInfo struct {
	Name          string                                `json:"name"`
	Database      string                                `json:"database"`
	IDStrategy    string                                `json:"id_strategy"`
	FieldHash     string                                `json:"field_hash,omitempty"`
	Schema        map[string]string                     `json:"schema,omitempty"`
	Indexes       []transport.CollectionIndexDefinition `json:"indexes,omitempty"`
	DocumentCount int64                                 `json:"document_count"`
	StorageCodec  string                                `json:"storage_codec,omitempty"`
	CreatedAt     string                                `json:"created_at"`
	State         string                                `json:"state,omitempty"`
	StateSince    string                                `json:"state_since,omitempty"`
}

// serverCollectionInfoToTransport converts a server-side CollectionInfo to
// the SDK transport representation.
func serverCollectionInfoToTransport(sc *serverCollectionInfo) *transport.CollectionInfo {
	return &transport.CollectionInfo{
		Name:          sc.Name,
		Database:      sc.Database,
		IDStrategy:    sc.IDStrategy,
		FieldHash:     sc.FieldHash,
		Schema:        sc.Schema,
		Indexes:       sc.Indexes,
		DocumentCount: sc.DocumentCount,
		StorageCodec:  sc.StorageCodec,
		CreatedAt:     sc.CreatedAt,
		State:         sc.State,
		StateSince:    sc.StateSince,
	}
}

// serverListCollectionsResponse mirrors service.ListCollectionsResponse for
// JSON decoding from the qrdb_collection_list tool result.
type serverListCollectionsResponse struct {
	Collections []serverCollectionInfo `json:"collections"`
}

// serverDocumentResponse mirrors service.GetDocumentResponse -- the shape
// shared by the qrdb_collection_get and qrdb_collection_list_documents tool
// results -- for JSON decoding. The ID is a decimal string, as the server
// writes it: a JSON number above 2^53 does not survive a float64 reader, and
// a number is refused rather than read.
type serverDocumentResponse struct {
	ID       uint64                 `json:"id,string"`
	Document map[string]interface{} `json:"document"`
}

// serverDocumentResponseToTransport converts a server-side
// GetDocumentResponse to the SDK transport representation.
func serverDocumentResponseToTransport(sd *serverDocumentResponse) *transport.DocumentResponse {
	return &transport.DocumentResponse{
		ID:       sd.ID,
		Document: sd.Document,
	}
}

// serverInsertDocumentResponse mirrors service.InsertDocumentResponse for
// JSON decoding from the qrdb_collection_insert tool result.
type serverInsertDocumentResponse struct {
	ID uint64 `json:"id,string"`
}

// serverListDocumentsResponse mirrors service.ListDocumentsResponse for
// JSON decoding from the qrdb_collection_list_documents tool result.
type serverListDocumentsResponse struct {
	Documents     []serverDocumentResponse `json:"documents"`
	NextPageToken string                   `json:"next_page_token,omitempty"`
	TotalCount    int64                    `json:"total_count"`
}

// ---------------------------------------------------------------------------
// CollectionClient implementation
// ---------------------------------------------------------------------------

// CreateCollection creates a new collection within a database. Calls the
// qrdb_collection_create MCP tool.
func (t *Transport) CreateCollection(ctx context.Context, req transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := map[string]any{
		"database": req.Database,
		"name":     req.Name,
	}
	if req.IDStrategy != "" {
		params["id_strategy"] = req.IDStrategy
	}
	if req.FieldHash != "" {
		params["field_hash"] = req.FieldHash
	}
	if req.StorageCodec != "" {
		params["storage_codec"] = req.StorageCodec
	}
	if len(req.Indexes) > 0 {
		// Array-valued tool arguments travel as a JSON string on this
		// surface; see the server's parseCollectionIndexes.
		encoded, encErr := json.Marshal(req.Indexes)
		if encErr != nil {
			return nil, &transport.RequestError{Method: "CreateCollection", Err: encErr}
		}
		params["indexes"] = string(encoded)
	}

	text, err := t.callTool(ctx, "qrdb_collection_create", params)
	if err != nil {
		return nil, err
	}

	var raw serverCollectionInfo
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "CreateCollection", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverCollectionInfoToTransport(&raw), nil
}

// GetCollectionInfo returns information about a specific collection. Calls
// the qrdb_collection_info MCP tool.
func (t *Transport) GetCollectionInfo(ctx context.Context, database, name string) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_collection_info", map[string]any{
		"database": database,
		"name":     name,
	})
	if err != nil {
		return nil, err
	}

	var raw serverCollectionInfo
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetCollectionInfo", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverCollectionInfoToTransport(&raw), nil
}

// ListCollections returns all collections within a database. Calls the
// qrdb_collection_list MCP tool.
func (t *Transport) ListCollections(ctx context.Context, database string) (*transport.ListCollectionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_collection_list", map[string]any{
		"database": database,
	})
	if err != nil {
		return nil, err
	}

	var raw serverListCollectionsResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListCollections", Err: &jsonDecodeError{Err: jsonErr}}
	}

	collections := make([]transport.CollectionInfo, len(raw.Collections))
	for i := range raw.Collections {
		collections[i] = *serverCollectionInfoToTransport(&raw.Collections[i])
	}

	return &transport.ListCollectionsResponse{Collections: collections}, nil
}

// DropCollection removes a collection from a database by name. Calls the
// qrdb_collection_drop MCP tool.
func (t *Transport) DropCollection(ctx context.Context, database, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_collection_drop", map[string]any{
		"database": database,
		"name":     name,
	})
	return err
}

// InsertDocument inserts a document into a collection and returns the
// assigned document ID. Calls the qrdb_collection_insert MCP tool.
func (t *Transport) InsertDocument(ctx context.Context, req transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	docJSON, jsonErr := json.Marshal(req.Document)
	if jsonErr != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: &jsonEncodeError{Err: jsonErr}}
	}

	text, err := t.callTool(ctx, "qrdb_collection_insert", map[string]any{
		"database":   req.Database,
		"collection": req.Collection,
		"document":   string(docJSON),
	})
	if err != nil {
		return nil, err
	}

	var raw serverInsertDocumentResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.InsertDocumentResponse{ID: raw.ID}, nil
}

// GetDocument returns a single document by its ID. Calls the
// qrdb_collection_get MCP tool.
func (t *Transport) GetDocument(ctx context.Context, database, collection string, id uint64) (*transport.DocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_collection_get", map[string]any{
		"database":   database,
		"collection": collection,
		"id":         strconv.FormatUint(id, 10),
	})
	if err != nil {
		return nil, err
	}

	var raw serverDocumentResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetDocument", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverDocumentResponseToTransport(&raw), nil
}

// UpdateDocument replaces an existing document in a collection. Calls the
// qrdb_collection_update MCP tool.
func (t *Transport) UpdateDocument(ctx context.Context, req transport.UpdateDocumentRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	docJSON, jsonErr := json.Marshal(req.Document)
	if jsonErr != nil {
		return &transport.RequestError{Method: "UpdateDocument", Err: &jsonEncodeError{Err: jsonErr}}
	}

	_, err := t.callTool(ctx, "qrdb_collection_update", map[string]any{
		"database":   req.Database,
		"collection": req.Collection,
		"id":         strconv.FormatUint(req.ID, 10),
		"document":   string(docJSON),
	})
	return err
}

// DeleteDocument removes a document from a collection by its ID. Calls the
// qrdb_collection_delete MCP tool.
func (t *Transport) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_collection_delete", map[string]any{
		"database":   database,
		"collection": collection,
		"id":         strconv.FormatUint(id, 10),
	})
	return err
}

// ListDocuments returns a paginated list of documents in a collection.
// Calls the qrdb_collection_list_documents MCP tool.
func (t *Transport) ListDocuments(ctx context.Context, req transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := map[string]any{
		"database":   req.Database,
		"collection": req.Collection,
	}
	if req.PageSize > 0 {
		params["page_size"] = req.PageSize
	}
	if req.PageToken != "" {
		params["page_token"] = req.PageToken
	}

	text, err := t.callTool(ctx, "qrdb_collection_list_documents", params)
	if err != nil {
		return nil, err
	}

	var raw serverListDocumentsResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ListDocuments", Err: &jsonDecodeError{Err: jsonErr}}
	}

	documents := make([]transport.DocumentResponse, len(raw.Documents))
	for i := range raw.Documents {
		documents[i] = *serverDocumentResponseToTransport(&raw.Documents[i])
	}

	return &transport.ListDocumentsResponse{
		Documents:     documents,
		NextPageToken: raw.NextPageToken,
		TotalCount:    raw.TotalCount,
	}, nil
}
