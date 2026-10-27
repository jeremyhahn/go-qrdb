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
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// collectionTools returns the MCP tool registrations for document collection
// operations.
func collectionTools(svc service.CollectionServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolCollectionCreate(),
			Perm:    permission.CollectionsCreate,
			Handler: handleCollectionCreate(svc),
		},
		{
			Tool:    toolCollectionDrop(),
			Perm:    permission.CollectionsDrop,
			Handler: handleCollectionDrop(svc),
		},
		{
			Tool:    toolCollectionList(),
			Perm:    permission.CollectionsList,
			Handler: handleCollectionList(svc),
		},
		{
			Tool:    toolCollectionInfo(),
			Perm:    permission.CollectionsRead,
			Handler: handleCollectionInfo(svc),
		},
		{
			Tool:    toolCollectionInsert(),
			Perm:    permission.CollectionsInsert,
			Handler: handleCollectionInsert(svc),
		},
		{
			Tool:    toolCollectionGet(),
			Perm:    permission.CollectionsRead,
			Handler: handleCollectionGet(svc),
		},
		{
			Tool:    toolCollectionUpdate(),
			Perm:    permission.CollectionsUpdate,
			Handler: handleCollectionUpdate(svc),
		},
		{
			Tool:    toolCollectionDelete(),
			Perm:    permission.CollectionsDelete,
			Handler: handleCollectionDelete(svc),
		},
		{
			Tool:    toolCollectionListDocuments(),
			Perm:    permission.CollectionsList,
			Handler: handleCollectionListDocuments(svc),
		},
	}
}

// toolCollectionCreate defines the qrdb_collection_create tool schema.
func toolCollectionCreate() mcp.Tool {
	return mcp.NewTool("qrdb_collection_create",
		mcp.WithDescription("Create a new document collection in the specified database."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database to create the collection in."),
		),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The collection name."),
		),
		mcp.WithString("id_strategy",
			mcp.Description("Document ID generation strategy: unix_nano, increment, or field_hash. Defaults to unix_nano."),
		),
		mcp.WithString("field_hash",
			mcp.Description("The document field to hash for ID generation. Required when id_strategy is field_hash."),
		),
		mcp.WithString("storage_codec",
			mcp.Description("Document storage codec for this collection. Defaults to the database's codec."),
		),
		mcp.WithString("indexes",
			mcp.Description(`JSON array of secondary index definitions, e.g. [{"field_name":"email","unique":true}]. The collection is active only once every index exists.`),
		),
	)
}

// handleCollectionCreate returns a tool handler that delegates to
// svc.CreateCollection.
func handleCollectionCreate(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		idStrategy := mcp.ParseString(req, "id_strategy", "unix_nano")
		fieldHash := mcp.ParseString(req, "field_hash", "")

		if idStrategy == "field_hash" && fieldHash == "" {
			return mcp.NewToolResultError("field_hash is required when id_strategy is field_hash"), nil
		}

		indexes, idxErr := parseCollectionIndexes(req)
		if idxErr != nil {
			return mcp.NewToolResultError("indexes must be a JSON array of {field_name, unique, sparse} objects"), nil
		}

		resp, err := svc.CreateCollection(ctx, &service.CreateCollectionRequest{
			Name:         name,
			Database:     database,
			IDStrategy:   idStrategy,
			FieldHash:    fieldHash,
			StorageCodec: mcp.ParseString(req, "storage_codec", ""),
			Indexes:      indexes,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_create", Err: err}
		}

		data, jsonErr := json.Marshal(collectionInfoResultOf(resp))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_create", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionDrop defines the qrdb_collection_drop tool schema.
func toolCollectionDrop() mcp.Tool {
	return mcp.NewTool("qrdb_collection_drop",
		mcp.WithDescription("Drop a collection and all its documents from the specified database."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The collection name to drop."),
		),
	)
}

// handleCollectionDrop returns a tool handler that delegates to
// svc.DropCollection.
func handleCollectionDrop(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		if err := svc.DropCollection(ctx, &service.DropCollectionRequest{
			Name:     name,
			Database: database,
		}); err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_drop", Err: err}
		}

		return mcp.NewToolResultText("ok"), nil
	}
}

// toolCollectionList defines the qrdb_collection_list tool schema.
func toolCollectionList() mcp.Tool {
	return mcp.NewTool("qrdb_collection_list",
		mcp.WithDescription("List all collections in the specified database."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database to list collections for."),
		),
	)
}

// handleCollectionList returns a tool handler that delegates to
// svc.ListCollections.
func handleCollectionList(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}

		resp, err := svc.ListCollections(ctx, &service.ListCollectionsRequest{
			Database: database,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_list", Err: err}
		}

		data, jsonErr := json.Marshal(collectionListResultOf(resp))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionInfo defines the qrdb_collection_info tool schema.
func toolCollectionInfo() mcp.Tool {
	return mcp.NewTool("qrdb_collection_info",
		mcp.WithDescription("Get metadata and statistics for a specific collection."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The collection name to retrieve info for."),
		),
	)
}

// handleCollectionInfo returns a tool handler that delegates to
// svc.GetCollectionInfo.
func handleCollectionInfo(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		resp, err := svc.GetCollectionInfo(ctx, database, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_info", Err: err}
		}

		data, jsonErr := json.Marshal(collectionInfoResultOf(resp))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_info", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionInsert defines the qrdb_collection_insert tool schema.
func toolCollectionInsert() mcp.Tool {
	return mcp.NewTool("qrdb_collection_insert",
		mcp.WithDescription("Insert a document into a collection. The document is a JSON object."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("collection",
			mcp.Required(),
			mcp.Description("The collection to insert the document into."),
		),
		mcp.WithString("document",
			mcp.Required(),
			mcp.Description("The document as a JSON string."),
		),
	)
}

// handleCollectionInsert returns a tool handler that delegates to
// svc.InsertDocument.
func handleCollectionInsert(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		collection := mcp.ParseString(req, "collection", "")
		if collection == "" {
			return mcp.NewToolResultError("collection is required"), nil
		}
		docStr := mcp.ParseString(req, "document", "")
		if docStr == "" {
			return mcp.NewToolResultError("document is required"), nil
		}

		var doc map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(docStr), &doc); jsonErr != nil {
			return mcp.NewToolResultError("invalid document JSON: " + jsonErr.Error()), nil
		}

		resp, err := svc.InsertDocument(ctx, &service.InsertDocumentRequest{
			Collection: collection,
			Database:   database,
			Document:   doc,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_insert", Err: err}
		}

		data, jsonErr := json.Marshal(documentIDResult{ID: resp.ID})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_insert", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionGet defines the qrdb_collection_get tool schema.
func toolCollectionGet() mcp.Tool {
	return mcp.NewTool("qrdb_collection_get",
		mcp.WithDescription("Get a document by ID from a collection."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("collection",
			mcp.Required(),
			mcp.Description("The collection containing the document."),
		),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The document ID to retrieve, in decimal. A string, not a JSON number: an ID is a uint64, and a number above 2^53 does not survive a JSON number's float64."),
		),
	)
}

// handleCollectionGet returns a tool handler that delegates to
// svc.GetDocument.
func handleCollectionGet(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		collection := mcp.ParseString(req, "collection", "")
		if collection == "" {
			return mcp.NewToolResultError("collection is required"), nil
		}
		id, bad := parseDocumentID(req)
		if bad != nil {
			return bad, nil
		}

		resp, err := svc.GetDocument(ctx, &service.GetDocumentRequest{
			Collection: collection,
			Database:   database,
			ID:         id,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_get", Err: err}
		}

		data, jsonErr := json.Marshal(documentResultOf(resp))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_get", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionUpdate defines the qrdb_collection_update tool schema.
func toolCollectionUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_collection_update",
		mcp.WithDescription("Update an existing document in a collection."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("collection",
			mcp.Required(),
			mcp.Description("The collection containing the document."),
		),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The document ID to update, in decimal. A string, not a JSON number: an ID is a uint64, and a number above 2^53 does not survive a JSON number's float64."),
		),
		mcp.WithString("document",
			mcp.Required(),
			mcp.Description("The updated document as a JSON string."),
		),
	)
}

// handleCollectionUpdate returns a tool handler that delegates to
// svc.UpdateDocument.
func handleCollectionUpdate(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		collection := mcp.ParseString(req, "collection", "")
		if collection == "" {
			return mcp.NewToolResultError("collection is required"), nil
		}
		id, bad := parseDocumentID(req)
		if bad != nil {
			return bad, nil
		}
		docStr := mcp.ParseString(req, "document", "")
		if docStr == "" {
			return mcp.NewToolResultError("document is required"), nil
		}

		var doc map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(docStr), &doc); jsonErr != nil {
			return mcp.NewToolResultError("invalid document JSON: " + jsonErr.Error()), nil
		}

		resp, err := svc.UpdateDocument(ctx, &service.UpdateDocumentRequest{
			Collection: collection,
			Database:   database,
			ID:         id,
			Document:   doc,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_update", Err: err}
		}

		data, jsonErr := json.Marshal(documentIDResult{ID: resp.ID})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_update", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionDelete defines the qrdb_collection_delete tool schema.
func toolCollectionDelete() mcp.Tool {
	return mcp.NewTool("qrdb_collection_delete",
		mcp.WithDescription("Delete a document by ID from a collection."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("collection",
			mcp.Required(),
			mcp.Description("The collection containing the document."),
		),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The document ID to delete, in decimal. A string, not a JSON number: an ID is a uint64, and a number above 2^53 does not survive a JSON number's float64."),
		),
	)
}

// handleCollectionDelete returns a tool handler that delegates to
// svc.DeleteDocument.
func handleCollectionDelete(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		collection := mcp.ParseString(req, "collection", "")
		if collection == "" {
			return mcp.NewToolResultError("collection is required"), nil
		}
		id, bad := parseDocumentID(req)
		if bad != nil {
			return bad, nil
		}

		resp, err := svc.DeleteDocument(ctx, &service.DeleteDocumentRequest{
			Collection: collection,
			Database:   database,
			ID:         id,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_delete", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_delete", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCollectionListDocuments defines the qrdb_collection_list_documents
// tool schema.
func toolCollectionListDocuments() mcp.Tool {
	return mcp.NewTool("qrdb_collection_list_documents",
		mcp.WithDescription("List documents in a collection with pagination."),
		mcp.WithString("database",
			mcp.Required(),
			mcp.Description("The database containing the collection."),
		),
		mcp.WithString("collection",
			mcp.Required(),
			mcp.Description("The collection to list documents from."),
		),
		mcp.WithNumber("page_size",
			mcp.Description("Maximum number of documents to return per page. Defaults to 25."),
		),
		mcp.WithString("page_token",
			mcp.Description("Pagination token from a previous list response."),
		),
	)
}

// handleCollectionListDocuments returns a tool handler that delegates to
// svc.ListDocuments.
func handleCollectionListDocuments(svc service.CollectionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := mcp.ParseString(req, "database", "")
		if database == "" {
			return mcp.NewToolResultError("database is required"), nil
		}
		svc, rerr := resolveCollection(ctx, svc, database)
		if rerr != nil {
			return nil, rerr
		}
		collection := mcp.ParseString(req, "collection", "")
		if collection == "" {
			return mcp.NewToolResultError("collection is required"), nil
		}

		pageSize := int32(mcp.ParseInt(req, "page_size", 0))
		pageToken := mcp.ParseString(req, "page_token", "")

		resp, err := svc.ListDocuments(ctx, &service.ListDocumentsRequest{
			Collection: collection,
			Database:   database,
			PageSize:   pageSize,
			PageToken:  pageToken,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_collection_list_documents", Err: err}
		}

		data, jsonErr := json.Marshal(documentListResultOf(resp))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_collection_list_documents", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// parseCollectionIndexes reads the optional JSON-encoded "indexes" argument.
// Like every array-valued argument on this surface it travels as a JSON
// string (see parseStringArray); unlike most, a malformed value is refused
// rather than read as absent, because creating the collection WITHOUT the
// indexes -- and the unique constraints -- the caller declared would be a
// silent substitution.
func parseCollectionIndexes(req mcp.CallToolRequest) ([]service.IndexDefinition, error) {
	raw := mcp.ParseString(req, "indexes", "")
	if raw == "" {
		return nil, nil
	}
	var defs []service.IndexDefinition
	if err := json.Unmarshal([]byte(raw), &defs); err != nil {
		return nil, err
	}
	return defs, nil
}

// parseDocumentID reads the required "id" argument, a document ID in
// decimal. It travels as a string because a document ID is a uint64 and the
// default unix_nano IDs exceed 2^53, past which a JSON number, read as a
// float64, no longer names the document. A malformed or zero ID is refused
// with the tool error result to return.
func parseDocumentID(req mcp.CallToolRequest) (uint64, *mcp.CallToolResult) {
	arg, present := req.GetArguments()["id"]
	if !present {
		return 0, mcp.NewToolResultError("id is required")
	}
	// mcp.ParseString would stringify a JSON number; the number form is the
	// lossy one, so it is refused rather than read.
	raw, isString := arg.(string)
	if !isString {
		return 0, mcp.NewToolResultError("id must be a decimal string, not a JSON number")
	}
	id, err := parseUint64(raw)
	if err != nil || id == 0 {
		return 0, mcp.NewToolResultError("id must be a non-zero decimal uint64, got " + strconv.Quote(raw))
	}
	return id, nil
}

// collectionInfoResult is a collection as the MCP tools report it: the
// service record without the server's own bookkeeping. IndexOwner and
// Incarnation are persisted on the metadata so the server can tell its own
// indexes and incarnations apart; they are not part of the collection
// surface, so they are left out rather than published by marshalling the
// stored record wholesale.
type collectionInfoResult struct {
	Name          string                    `json:"name"`
	Database      string                    `json:"database"`
	IDStrategy    string                    `json:"id_strategy"`
	FieldHash     string                    `json:"field_hash,omitempty"`
	Schema        map[string]string         `json:"schema,omitempty"`
	Indexes       []service.IndexDefinition `json:"indexes,omitempty"`
	DocumentCount int64                     `json:"document_count"`
	StorageCodec  string                    `json:"storage_codec,omitempty"`
	CreatedAt     time.Time                 `json:"created_at"`
	State         string                    `json:"state,omitempty"`
	StateSince    time.Time                 `json:"state_since,omitzero"`
}

// collectionInfoResultOf projects a service record onto the tool result.
func collectionInfoResultOf(info *service.CollectionInfo) collectionInfoResult {
	return collectionInfoResult{
		Name:          info.Name,
		Database:      info.Database,
		IDStrategy:    info.IDStrategy,
		FieldHash:     info.FieldHash,
		Schema:        info.Schema,
		Indexes:       info.Indexes,
		DocumentCount: info.DocumentCount,
		StorageCodec:  info.StorageCodec,
		CreatedAt:     info.CreatedAt,
		State:         info.State,
		StateSince:    info.StateSince,
	}
}

// documentIDResult is the result of qrdb_collection_insert and
// qrdb_collection_update. The ID is a decimal string, as the id argument of
// the get, update and delete tools is: a document ID is a uint64, the default
// unix_nano IDs exceed 2^53, and a JSON number is read as a float64 by most
// MCP clients, which rounds it to a different document.
type documentIDResult struct {
	ID uint64 `json:"id,string"`
}

// documentResult is one document as qrdb_collection_get and
// qrdb_collection_list_documents return it, its ID a decimal string for the
// reason documentIDResult gives.
type documentResult struct {
	ID       uint64                 `json:"id,string"`
	Document map[string]interface{} `json:"document"`
}

// documentListResult is the result of qrdb_collection_list_documents.
type documentListResult struct {
	Documents     []documentResult `json:"documents"`
	NextPageToken string           `json:"next_page_token,omitempty"`
	TotalCount    int64            `json:"total_count"`
}

// documentResultOf converts a service document to its tool result.
func documentResultOf(doc *service.GetDocumentResponse) documentResult {
	return documentResult{ID: doc.ID, Document: doc.Document}
}

// documentListResultOf converts a service document page to its tool result.
func documentListResultOf(page *service.ListDocumentsResponse) documentListResult {
	docs := make([]documentResult, len(page.Documents))
	for i := range page.Documents {
		docs[i] = documentResultOf(&page.Documents[i])
	}
	return documentListResult{
		Documents:     docs,
		NextPageToken: page.NextPageToken,
		TotalCount:    page.TotalCount,
	}
}

// collectionListResult is the qrdb_collection_list tool result.
type collectionListResult struct {
	Collections []collectionInfoResult `json:"collections"`
}

// collectionListResultOf projects a service listing onto the tool result.
func collectionListResultOf(resp *service.ListCollectionsResponse) collectionListResult {
	out := collectionListResult{Collections: make([]collectionInfoResult, len(resp.Collections))}
	for i := range resp.Collections {
		out.Collections[i] = collectionInfoResultOf(&resp.Collections[i])
	}
	return out
}
