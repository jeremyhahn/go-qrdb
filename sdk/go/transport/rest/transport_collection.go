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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.CollectionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CollectionClient implementation
// ---------------------------------------------------------------------------

// CreateCollection creates a new collection within a database.
// Maps to POST /v1/collections with a JSON request body.
func (t *Transport) CreateCollection(ctx context.Context, req transport.CreateCollectionRequest) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var info transport.CollectionInfo
	if err := t.postJSONDecode(ctx, "CreateCollection", "/v1/collections", req, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// GetCollectionInfo returns information about a specific collection.
// Maps to GET /v1/collections/{database}/{name}.
func (t *Transport) GetCollectionInfo(ctx context.Context, database, name string) (*transport.CollectionInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(database) + "/" + url.PathEscape(name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetCollectionInfo", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetCollectionInfo", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetCollectionInfo", resp)
	}

	var info transport.CollectionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, &transport.RequestError{Method: "GetCollectionInfo", Err: &jsonDecodeError{Err: err}}
	}

	return &info, nil
}

// ListCollections returns all collections within a database.
// Maps to GET /v1/collections/list?database={database}.
func (t *Transport) ListCollections(ctx context.Context, database string) (*transport.ListCollectionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("database", database)
	reqURL := t.baseURL + "/v1/collections/list?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListCollections", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListCollections", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListCollections", resp)
	}

	var listResp transport.ListCollectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "ListCollections", Err: &jsonDecodeError{Err: err}}
	}

	return &listResp, nil
}

// DropCollection removes a collection from a database by name.
// Maps to DELETE /v1/collections/{database}/{name}.
func (t *Transport) DropCollection(ctx context.Context, database, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(database) + "/" + url.PathEscape(name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "DropCollection", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DropCollection", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("DropCollection", resp)
}

// InsertDocument inserts a document into a collection and returns the
// assigned document ID. Maps to POST /v1/collections/{database}/{collection}/documents
// with a JSON body containing only the document field.
func (t *Transport) InsertDocument(ctx context.Context, req transport.InsertDocumentRequest) (*transport.InsertDocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	body := struct {
		Document map[string]interface{} `json:"document"`
	}{
		Document: req.Document,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: &jsonEncodeError{Err: err}}
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(req.Database) + "/" + url.PathEscape(req.Collection) + "/documents"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, t.decodeErrorResponse("InsertDocument", resp)
	}

	var insertResp transport.InsertDocumentResponse
	if err := json.NewDecoder(resp.Body).Decode(&insertResp); err != nil {
		return nil, &transport.RequestError{Method: "InsertDocument", Err: &jsonDecodeError{Err: err}}
	}

	return &insertResp, nil
}

// GetDocument returns a single document by its ID.
// Maps to GET /v1/collections/{database}/{collection}/documents/{id}.
func (t *Transport) GetDocument(ctx context.Context, database, collection string, id uint64) (*transport.DocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(database) + "/" + url.PathEscape(collection) + "/documents/" + strconv.FormatUint(id, 10)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetDocument", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetDocument", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetDocument", resp)
	}

	var doc transport.DocumentResponse
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, &transport.RequestError{Method: "GetDocument", Err: &jsonDecodeError{Err: err}}
	}

	return &doc, nil
}

// UpdateDocument replaces an existing document in a collection.
// Maps to PUT /v1/collections/{database}/{collection}/documents/{id}
// with a JSON body containing only the document field.
func (t *Transport) UpdateDocument(ctx context.Context, req transport.UpdateDocumentRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	body := struct {
		Document map[string]interface{} `json:"document"`
	}{
		Document: req.Document,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return &transport.RequestError{Method: "UpdateDocument", Err: &jsonEncodeError{Err: err}}
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(req.Database) + "/" + url.PathEscape(req.Collection) + "/documents/" + strconv.FormatUint(req.ID, 10)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: "UpdateDocument", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "UpdateDocument", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("UpdateDocument", resp)
}

// DeleteDocument removes a document from a collection by its ID.
// Maps to DELETE /v1/collections/{database}/{collection}/documents/{id}.
func (t *Transport) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(database) + "/" + url.PathEscape(collection) + "/documents/" + strconv.FormatUint(id, 10)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "DeleteDocument", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DeleteDocument", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("DeleteDocument", resp)
}

// ListDocuments returns a paginated list of documents in a collection.
// Maps to GET /v1/collections/{database}/{collection}/documents?page_size=N&page_token=T.
func (t *Transport) ListDocuments(ctx context.Context, req transport.ListDocumentsRequest) (*transport.ListDocumentsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.PageSize > 0 {
		params.Set("page_size", strconv.FormatInt(int64(req.PageSize), 10))
	}
	if req.PageToken != "" {
		params.Set("page_token", req.PageToken)
	}

	reqURL := t.baseURL + "/v1/collections/" + url.PathEscape(req.Database) + "/" + url.PathEscape(req.Collection) + "/documents"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDocuments", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListDocuments", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListDocuments", resp)
	}

	var listResp transport.ListDocumentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "ListDocuments", Err: &jsonDecodeError{Err: err}}
	}

	return &listResp, nil
}
