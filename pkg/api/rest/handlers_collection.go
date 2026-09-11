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

package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Collection request/response types
// ---------------------------------------------------------------------------

// createCollectionRequest is the JSON payload for POST /v1/collections.
type createCollectionRequest struct {
	Name       string `json:"name"`
	Database   string `json:"database"`
	IDStrategy string `json:"id_strategy,omitempty"`
	FieldHash  string `json:"field_hash,omitempty"`
}

// insertDocumentRequest is the JSON payload for
// POST /v1/collections/{database}/{name}/documents.
type insertDocumentRequest struct {
	Document map[string]interface{} `json:"document"`
}

// updateDocumentRequest is the JSON payload for
// PUT /v1/collections/{database}/{name}/documents/{id}.
type updateDocumentRequest struct {
	Document map[string]interface{} `json:"document"`
}

// collectionInfoResponse is the JSON representation of a collection.
type collectionInfoResponse struct {
	Name          string            `json:"name"`
	Database      string            `json:"database"`
	IDStrategy    string            `json:"id_strategy"`
	FieldHash     string            `json:"field_hash,omitempty"`
	Schema        map[string]string `json:"schema,omitempty"`
	DocumentCount int64             `json:"document_count"`
	CreatedAt     string            `json:"created_at"`
}

// listCollectionsResponse is the JSON payload returned by
// GET /v1/collections/list.
type listCollectionsResponse struct {
	Collections []collectionInfoResponse `json:"collections"`
}

// documentResponse is the JSON representation of a document.
type documentResponse struct {
	ID       uint64                 `json:"id,string"`
	Document map[string]interface{} `json:"document"`
}

// insertDocumentResponse is the JSON payload returned after inserting
// a document.
type insertDocumentResponse struct {
	ID uint64 `json:"id,string"`
}

// updateDocumentResponse is the JSON payload returned after updating
// a document.
type updateDocumentResponse struct {
	ID uint64 `json:"id,string"`
}

// deleteDocumentResponse is the JSON payload returned after deleting
// a document.
type deleteDocumentResponse struct {
	Deleted bool `json:"deleted"`
}

// listDocumentsResponse is the JSON payload returned by
// GET /v1/collections/{database}/{name}/documents.
type listDocumentsResponse struct {
	Documents     []documentResponse `json:"documents"`
	NextPageToken string             `json:"next_page_token,omitempty"`
	TotalCount    int64              `json:"total_count"`
}

// ---------------------------------------------------------------------------
// Collection handlers
// ---------------------------------------------------------------------------

// handleCreateCollection handles POST /v1/collections -- creates a new
// collection in the specified database.
func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	if !s.collectionSurfaceConfigured() {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "collection"})
		return
	}

	var req createCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// The database is resolved from the request body, which is where this
	// route names it; collectionSvcFor writes the response on failure.
	collectionSvc := s.collectionSvcFor(w, r.Context(), req.Database)
	if collectionSvc == nil {
		return
	}

	info, err := collectionSvc.CreateCollection(r.Context(), &service.CreateCollectionRequest{
		Name:       req.Name,
		Database:   req.Database,
		IDStrategy: req.IDStrategy,
		FieldHash:  req.FieldHash,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(collectionInfoToResponse(info))
}

// handleListCollections handles GET /v1/collections/list?database={db} --
// returns all collections in the specified database.
func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	if !s.collectionSurfaceConfigured() {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "collection"})
		return
	}

	database := r.URL.Query().Get("database")
	if database == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "database"})
		return
	}

	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	resp, err := collectionSvc.ListCollections(r.Context(), &service.ListCollectionsRequest{
		Database: database,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	collections := make([]collectionInfoResponse, len(resp.Collections))
	for i, c := range resp.Collections {
		collections[i] = collectionInfoToResponse(&c)
	}

	writeJSON(w, listCollectionsResponse{Collections: collections})
}

// handleCollectionByPath routes requests under /v1/collections/{database}/{name}
// to the appropriate handler. It parses the database, collection name, and
// optional /documents and /documents/{id} sub-resources from the URL path.
//
// Supported routes:
//
//	GET    /v1/collections/{database}/{name}                    -> GetCollectionInfo
//	DELETE /v1/collections/{database}/{name}                    -> DropCollection
//	POST   /v1/collections/{database}/{name}/documents          -> InsertDocument
//	GET    /v1/collections/{database}/{name}/documents          -> ListDocuments
//	GET    /v1/collections/{database}/{name}/documents/{id}     -> GetDocument
//	PUT    /v1/collections/{database}/{name}/documents/{id}     -> UpdateDocument
//	DELETE /v1/collections/{database}/{name}/documents/{id}     -> DeleteDocument
func (s *Server) handleCollectionByPath(w http.ResponseWriter, r *http.Request) {
	// Whether the surface exists at all is knowable here; WHICH servicer
	// serves it is not, because the database this request addresses is in the
	// path and the path has not been parsed yet. So this reports the absent
	// surface, and each leaf handler resolves its own scope through
	// collectionSvcFor once it knows the database.
	if !s.collectionSurfaceConfigured() {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "collection"})
		return
	}

	// Extract remainder after /v1/collections/
	remainder := strings.TrimPrefix(r.URL.Path, "/v1/collections/")
	if remainder == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "database"})
		return
	}

	// Split into database / name / subPath...
	parts := strings.SplitN(remainder, "/", 4)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	database := parts[0]
	name := parts[1]

	// No sub-resource: GET (info) or DELETE (drop) on the collection itself.
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			s.handleGetCollectionInfo(w, r, database, name)
		case http.MethodDelete:
			s.handleDropCollection(w, r, database, name)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Sub-resource must be "documents".
	subResource := parts[2]
	if subResource != "documents" {
		http.NotFound(w, r)
		return
	}

	// /v1/collections/{database}/{name}/documents (no document ID).
	if len(parts) == 3 || parts[3] == "" {
		switch r.Method {
		case http.MethodPost:
			s.handleInsertDocument(w, r, database, name)
		case http.MethodGet:
			s.handleListDocuments(w, r, database, name)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// /v1/collections/{database}/{name}/documents/{id}
	docIDStr := parts[3]
	docID, err := strconv.ParseUint(docIDStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "id", Err: err})
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetDocument(w, r, database, name, docID)
	case http.MethodPut:
		s.handleUpdateDocument(w, r, database, name, docID)
	case http.MethodDelete:
		s.handleDeleteDocument(w, r, database, name, docID)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetCollectionInfo handles GET /v1/collections/{database}/{name} --
// returns metadata about a specific collection.
func (s *Server) handleGetCollectionInfo(w http.ResponseWriter, r *http.Request, database, name string) {
	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	info, err := collectionSvc.GetCollectionInfo(r.Context(), database, name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, collectionInfoToResponse(info))
}

// handleDropCollection handles DELETE /v1/collections/{database}/{name} --
// removes a collection and all its documents.
func (s *Server) handleDropCollection(w http.ResponseWriter, r *http.Request, database, name string) {
	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	if err := collectionSvc.DropCollection(r.Context(), &service.DropCollectionRequest{
		Name:     name,
		Database: database,
	}); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleInsertDocument handles POST /v1/collections/{database}/{name}/documents --
// inserts a new document into the specified collection.
func (s *Server) handleInsertDocument(w http.ResponseWriter, r *http.Request, database, name string) {
	var req insertDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	resp, err := collectionSvc.InsertDocument(r.Context(), &service.InsertDocumentRequest{
		Collection: name,
		Database:   database,
		Document:   req.Document,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(insertDocumentResponse{ID: resp.ID})
}

// handleGetDocument handles GET /v1/collections/{database}/{name}/documents/{id} --
// retrieves a document by its ID from the specified collection.
func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request, database, name string, id uint64) {
	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	resp, err := collectionSvc.GetDocument(r.Context(), &service.GetDocumentRequest{
		Collection: name,
		Database:   database,
		ID:         id,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, documentResponse{
		ID:       resp.ID,
		Document: resp.Document,
	})
}

// handleUpdateDocument handles PUT /v1/collections/{database}/{name}/documents/{id} --
// updates an existing document in the specified collection.
func (s *Server) handleUpdateDocument(w http.ResponseWriter, r *http.Request, database, name string, id uint64) {
	var req updateDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	resp, err := collectionSvc.UpdateDocument(r.Context(), &service.UpdateDocumentRequest{
		Collection: name,
		Database:   database,
		ID:         id,
		Document:   req.Document,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, updateDocumentResponse{ID: resp.ID})
}

// handleDeleteDocument handles DELETE /v1/collections/{database}/{name}/documents/{id} --
// removes a document from the specified collection.
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request, database, name string, id uint64) {
	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	resp, err := collectionSvc.DeleteDocument(r.Context(), &service.DeleteDocumentRequest{
		Collection: name,
		Database:   database,
		ID:         id,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, deleteDocumentResponse{Deleted: resp.Deleted})
}

// handleListDocuments handles GET /v1/collections/{database}/{name}/documents --
// lists documents in a collection with optional pagination via page_size
// and page_token query parameters.
func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request, database, name string) {
	q := r.URL.Query()

	var pageSize int32
	if ps := q.Get("page_size"); ps != "" {
		parsed, err := strconv.ParseInt(ps, 10, 32)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "page_size", Err: err})
			return
		}
		pageSize = int32(parsed)
	}

	collectionSvc := s.collectionSvcFor(w, r.Context(), database)
	if collectionSvc == nil {
		return
	}

	resp, err := collectionSvc.ListDocuments(r.Context(), &service.ListDocumentsRequest{
		Collection: name,
		Database:   database,
		PageSize:   pageSize,
		PageToken:  q.Get("page_token"),
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	docs := make([]documentResponse, len(resp.Documents))
	for i, d := range resp.Documents {
		docs[i] = documentResponse{
			ID:       d.ID,
			Document: d.Document,
		}
	}

	writeJSON(w, listDocumentsResponse{
		Documents:     docs,
		NextPageToken: resp.NextPageToken,
		TotalCount:    resp.TotalCount,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// collectionInfoToResponse converts a service.CollectionInfo to its JSON
// response representation.
func collectionInfoToResponse(info *service.CollectionInfo) collectionInfoResponse {
	return collectionInfoResponse{
		Name:          info.Name,
		Database:      info.Database,
		IDStrategy:    info.IDStrategy,
		FieldHash:     info.FieldHash,
		Schema:        info.Schema,
		DocumentCount: info.DocumentCount,
		CreatedAt:     info.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
