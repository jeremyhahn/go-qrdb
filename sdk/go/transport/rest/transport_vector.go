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
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VectorClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Namespace operations
// ---------------------------------------------------------------------------

// CreateNamespace creates a new vector namespace. Maps to
// POST /vector/namespace with a JSON body.
func (t *Transport) CreateNamespace(ctx context.Context, req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.CreateNamespaceResponse
	if err := t.postJSONDecode(ctx, "CreateNamespace", "/vector/namespace", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateNamespace changes a namespace's index parameters and metadata in
// place via POST /vector/namespace/update, keeping every vector.
func (t *Transport) UpdateNamespace(ctx context.Context, req *transport.UpdateNamespaceRequest) (*transport.UpdateNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.UpdateNamespaceResponse
	if err := t.postJSONDecode(ctx, "UpdateNamespace", "/vector/namespace/update", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DeleteNamespace removes a namespace and all its vectors. Maps to
// POST /vector/namespace/delete with a JSON body.
func (t *Transport) DeleteNamespace(ctx context.Context, req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.DeleteNamespaceResponse
	if err := t.postJSONDecode(ctx, "DeleteNamespace", "/vector/namespace/delete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListNamespaces returns all vector namespaces. Maps to
// GET /vector/namespaces with optional shard_id query parameter.
func (t *Transport) ListNamespaces(ctx context.Context, req *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.ShardID > 0 {
		params.Set("shard_id", strconv.FormatUint(req.ShardID, 10))
	}

	reqURL := t.baseURL + "/vector/namespaces"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListNamespaces", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListNamespaces", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListNamespaces", resp)
	}

	var wireResp transport.ListNamespacesResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "ListNamespaces", Err: &jsonDecodeError{Err: err}}
	}
	for _, ns := range wireResp.Namespaces {
		ns.IndexType = transport.ParseVectorIndexType(ns.IndexType)
	}

	return &wireResp, nil
}

// GetNamespaceInfo returns metadata about a namespace. Maps to
// GET /vector/namespace/info?namespace=X with optional shard_id.
func (t *Transport) GetNamespaceInfo(ctx context.Context, req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("namespace", req.Namespace)
	if req.ShardID > 0 {
		params.Set("shard_id", strconv.FormatUint(req.ShardID, 10))
	}

	reqURL := t.baseURL + "/vector/namespace/info?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetNamespaceInfo", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetNamespaceInfo", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetNamespaceInfo", resp)
	}

	var wireResp transport.GetNamespaceInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "GetNamespaceInfo", Err: &jsonDecodeError{Err: err}}
	}
	wireResp.IndexType = transport.ParseVectorIndexType(wireResp.IndexType)
	if wireResp.DistanceMetric, err = transport.ParseDistanceMetric(wireResp.DistanceMetric); err != nil {
		return nil, err
	}

	return &wireResp, nil
}

// ---------------------------------------------------------------------------
// Vector CRUD operations
// ---------------------------------------------------------------------------

// UpsertVector inserts or updates a vector in a namespace. Maps to
// POST /vector/upsert with a JSON body.
func (t *Transport) UpsertVector(ctx context.Context, req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.UpsertVectorResponse
	if err := t.postJSONDecode(ctx, "UpsertVector", "/vector/upsert", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetVector retrieves a single vector by ID. Maps to
// GET /vector/get?namespace=X&id=N with optional query parameters.
func (t *Transport) GetVector(ctx context.Context, req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("namespace", req.Namespace)
	params.Set("id", strconv.FormatUint(req.ID, 10))
	if req.IncludeVector {
		params.Set("include_vector", "true")
	}
	if req.IncludeMetadata {
		params.Set("include_metadata", "true")
	}
	if req.Consistency > 0 {
		params.Set("consistency", strconv.FormatUint(uint64(req.Consistency), 10))
	}
	if req.ShardID > 0 {
		params.Set("shard_id", strconv.FormatUint(req.ShardID, 10))
	}

	reqURL := t.baseURL + "/vector/get?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetVector", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetVector", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetVector", resp)
	}

	var wireResp transport.GetVectorResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "GetVector", Err: &jsonDecodeError{Err: err}}
	}

	return &wireResp, nil
}

// DeleteVector removes a single vector by ID. Maps to
// POST /vector/delete with a JSON body.
func (t *Transport) DeleteVector(ctx context.Context, req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.DeleteVectorResponse
	if err := t.postJSONDecode(ctx, "DeleteVector", "/vector/delete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BatchUpsert inserts or updates multiple vectors. Maps to
// POST /vector/batch/upsert with a JSON body.
func (t *Transport) BatchUpsert(ctx context.Context, req *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.BatchUpsertResponse
	if err := t.postJSONDecode(ctx, "BatchUpsert", "/vector/batch/upsert", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BatchDeleteVectors removes multiple vectors by ID. Maps to
// POST /vector/batch/delete with a JSON body.
func (t *Transport) BatchDeleteVectors(ctx context.Context, req *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.BatchDeleteVectorsResponse
	if err := t.postJSONDecode(ctx, "BatchDeleteVectors", "/vector/batch/delete", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListVectors returns a paginated list of vectors. Maps to
// GET /vector/list?namespace=X with optional query parameters.
func (t *Transport) ListVectors(ctx context.Context, req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("namespace", req.Namespace)
	if req.Limit > 0 {
		params.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		params.Set("cursor", req.Cursor)
	}
	if req.IDPrefix != "" {
		params.Set("id_prefix", req.IDPrefix)
	}
	if req.IncludeVectors {
		params.Set("include_vectors", "true")
	}
	if req.IncludeMetadata {
		params.Set("include_metadata", "true")
	}
	if req.Consistency > 0 {
		params.Set("consistency", strconv.FormatUint(uint64(req.Consistency), 10))
	}
	if req.ShardID > 0 {
		params.Set("shard_id", strconv.FormatUint(req.ShardID, 10))
	}

	reqURL := t.baseURL + "/vector/list?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListVectors", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListVectors", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListVectors", resp)
	}

	var wireResp transport.ListVectorsResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "ListVectors", Err: &jsonDecodeError{Err: err}}
	}

	return &wireResp, nil
}

// UpdateMetadata updates the metadata for an existing vector. Maps to
// POST /vector/metadata with a JSON body.
func (t *Transport) UpdateMetadata(ctx context.Context, req *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.UpdateVectorMetadataResponse
	if err := t.postJSONDecode(ctx, "UpdateMetadata", "/vector/metadata", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Search operations
// ---------------------------------------------------------------------------

// SearchSimilar performs a k-nearest-neighbor search. Maps to
// POST /vector/search/similar with a JSON body.
func (t *Transport) SearchSimilar(ctx context.Context, req *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.SearchSimilarResponse
	if err := t.postJSONDecode(ctx, "SearchSimilar", "/vector/search/similar", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SearchByMetadata returns vectors matching metadata filters. Maps to
// POST /vector/search/metadata with a JSON body.
func (t *Transport) SearchByMetadata(ctx context.Context, req *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.SearchByMetadataResponse
	if err := t.postJSONDecode(ctx, "SearchByMetadata", "/vector/search/metadata", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// HybridSearch combines vector similarity and metadata filtering.
// Maps to POST /vector/search/hybrid with a JSON body.
func (t *Transport) HybridSearch(ctx context.Context, req *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.HybridSearchResponse
	if err := t.postJSONDecode(ctx, "HybridSearch", "/vector/search/hybrid", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SearchByID searches using an existing vector's ID as the query.
// Maps to POST /vector/search/by-id with a JSON body.
func (t *Transport) SearchByID(ctx context.Context, req *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.SearchByIDResponse
	if err := t.postJSONDecode(ctx, "SearchByID", "/vector/search/by-id", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Index management operations
// ---------------------------------------------------------------------------

// RebuildIndex triggers an asynchronous index rebuild. Maps to
// POST /vector/index/rebuild with a JSON body.
func (t *Transport) RebuildIndex(ctx context.Context, req *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.RebuildIndexResponse
	if err := t.postJSONDecode(ctx, "RebuildIndex", "/vector/index/rebuild", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetRebuildProgress returns the current progress of an index rebuild.
// Maps to GET /vector/index/rebuild/progress with query parameters.
func (t *Transport) GetRebuildProgress(ctx context.Context, req *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.Namespace != "" {
		params.Set("namespace", req.Namespace)
	}
	if req.JobID != "" {
		params.Set("job_id", req.JobID)
	}

	reqURL := t.baseURL + "/vector/index/rebuild/progress"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRebuildProgress", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRebuildProgress", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetRebuildProgress", resp)
	}

	var wireResp transport.GetRebuildProgressResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "GetRebuildProgress", Err: &jsonDecodeError{Err: err}}
	}

	return &wireResp, nil
}

// GetIndexStats returns statistics about a namespace's vector index.
// Maps to GET /vector/index/stats?namespace=X with optional shard_id.
func (t *Transport) GetIndexStats(ctx context.Context, req *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("namespace", req.Namespace)
	if req.ShardID > 0 {
		params.Set("shard_id", strconv.FormatUint(req.ShardID, 10))
	}

	reqURL := t.baseURL + "/vector/index/stats?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetIndexStats", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetIndexStats", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetIndexStats", resp)
	}

	var wireResp transport.GetIndexStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "GetIndexStats", Err: &jsonDecodeError{Err: err}}
	}
	wireResp.IndexType = transport.ParseVectorIndexType(wireResp.IndexType)

	return &wireResp, nil
}

// ---------------------------------------------------------------------------
// Compaction operations
// ---------------------------------------------------------------------------

// CompactNamespace triggers compaction of a namespace's storage. Maps to
// POST /vector/compact with a JSON body.
func (t *Transport) CompactNamespace(ctx context.Context, req *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var resp transport.CompactNamespaceResponse
	if err := t.postJSONDecode(ctx, "CompactNamespace", "/vector/compact", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetCompactionProgress returns the current progress of a compaction.
// Maps to GET /vector/compact/progress with query parameters.
func (t *Transport) GetCompactionProgress(ctx context.Context, req *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.Namespace != "" {
		params.Set("namespace", req.Namespace)
	}
	if req.OperationID != "" {
		params.Set("operation_id", req.OperationID)
	}
	if req.ShardID > 0 {
		params.Set("shard_id", strconv.FormatUint(req.ShardID, 10))
	}

	reqURL := t.baseURL + "/vector/compact/progress"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetCompactionProgress", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetCompactionProgress", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetCompactionProgress", resp)
	}

	var wireResp transport.GetCompactionProgressResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "GetCompactionProgress", Err: &jsonDecodeError{Err: err}}
	}

	return &wireResp, nil
}
