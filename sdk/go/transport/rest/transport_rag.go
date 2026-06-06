//go:build qrdb_vector

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
	"context"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.RAGClient = (*Transport)(nil)

// RAGIngestDocument ingests a document. Maps to POST /v1/rag/ingest. The REST
// JSON shapes match the transport request/response contracts exactly.
func (t *Transport) RAGIngestDocument(ctx context.Context, req *transport.RAGIngestRequest) (*transport.RAGIngestResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.RAGIngestResponse
	if err := t.postJSONDecode(ctx, "RAGIngestDocument", "/v1/rag/ingest", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RAGQuery retrieves semantically similar passages. Maps to POST /v1/rag/query.
func (t *Transport) RAGQuery(ctx context.Context, req *transport.RAGQueryRequest) (*transport.RAGQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.RAGQueryResponse
	if err := t.postJSONDecode(ctx, "RAGQuery", "/v1/rag/query", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RAGDeleteDocument removes a document. Maps to POST /v1/rag/documents/delete.
func (t *Transport) RAGDeleteDocument(ctx context.Context, req *transport.RAGDeleteDocumentRequest) (*transport.RAGDeleteDocumentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var resp transport.RAGDeleteDocumentResponse
	if err := t.postJSONDecode(ctx, "RAGDeleteDocument", "/v1/rag/documents/delete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RAGListDocuments lists ingested documents. Maps to
// GET /v1/rag/documents?namespace=&limit=&offset=.
func (t *Transport) RAGListDocuments(ctx context.Context, req *transport.RAGListDocumentsRequest) (*transport.RAGListDocumentsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	q := url.Values{}
	if req.Namespace != "" {
		q.Set("namespace", req.Namespace)
	}
	if req.Limit != 0 {
		q.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset != 0 {
		q.Set("offset", strconv.Itoa(req.Offset))
	}
	path := "/v1/rag/documents"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp transport.RAGListDocumentsResponse
	if err := t.getJSONDecode(ctx, "RAGListDocuments", path, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
