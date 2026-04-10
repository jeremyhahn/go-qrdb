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
	"sort"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.IndexClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for index REST communication
// ---------------------------------------------------------------------------

// indexCreateWireRequest matches the JSON payload for POST /index/create.
type indexCreateWireRequest struct {
	Name      string `json:"name"`
	FieldName string `json:"field_name"`
	Unique    bool   `json:"unique"`
	Sparse    bool   `json:"sparse"`
}

// indexDropWireRequest matches the JSON payload for POST /index/drop.
type indexDropWireRequest struct {
	Name string `json:"name"`
}

// indexQueryWireResponse matches the JSON payload returned by GET /index/query.
type indexQueryWireResponse struct {
	Keys []string `json:"keys"`
}

// indexScanWireResponse matches the JSON payload returned by GET /index/scan.
// The server encodes entries as a map of value to keys.
type indexScanWireResponse struct {
	Entries map[string][]string `json:"entries"`
}

// indexListWireResponse matches the JSON payload returned by GET /index/list.
type indexListWireResponse struct {
	Names []string `json:"names"`
}

// ---------------------------------------------------------------------------
// IndexClient implementation
// ---------------------------------------------------------------------------

// IndexCreate creates a new secondary index with the given name, field, and
// configuration. Maps to POST /index/create with a JSON body.
func (t *Transport) IndexCreate(ctx context.Context, name, fieldName string, config transport.IndexConfig) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := indexCreateWireRequest{
		Name:      name,
		FieldName: fieldName,
		Unique:    config.Unique,
		Sparse:    config.Sparse,
	}

	return t.postJSON(ctx, "IndexCreate", "/index/create", payload)
}

// IndexDrop removes a secondary index by name. Maps to POST /index/drop
// with a JSON body containing the index name.
func (t *Transport) IndexDrop(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := indexDropWireRequest{Name: name}
	return t.postJSON(ctx, "IndexDrop", "/index/drop", payload)
}

// IndexQuery returns entity keys matching an exact value in an index.
// Maps to GET /index/query?index_name=...&value=...
func (t *Transport) IndexQuery(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/index/query?" + url.Values{
		"index_name": {indexName},
		"value":      {value},
	}.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "IndexQuery", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "IndexQuery", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("IndexQuery", resp)
	}

	var wireResp indexQueryWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "IndexQuery", Err: &jsonDecodeError{Err: err}}
	}

	return &transport.IndexQueryResponse{Keys: wireResp.Keys}, nil
}

// IndexScan returns entries in an index within a value range [start, end).
// Maps to GET /index/scan?index_name=...&start=...&end=...
func (t *Transport) IndexScan(ctx context.Context, indexName, start, end string) (*transport.IndexScanResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/index/scan?" + url.Values{
		"index_name": {indexName},
		"start":      {start},
		"end":        {end},
	}.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "IndexScan", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "IndexScan", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("IndexScan", resp)
	}

	var wireResp indexScanWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "IndexScan", Err: &jsonDecodeError{Err: err}}
	}

	return wireScanToTransport(&wireResp), nil
}

// IndexList returns the names of all registered indexes. Maps to
// GET /index/list.
func (t *Transport) IndexList(ctx context.Context) (*transport.IndexListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/index/list", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "IndexList", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "IndexList", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("IndexList", resp)
	}

	var wireResp indexListWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "IndexList", Err: &jsonDecodeError{Err: err}}
	}

	return &transport.IndexListResponse{Names: wireResp.Names}, nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// wireScanToTransport converts the server's map-based scan response to the
// SDK's slice-based IndexScanResponse. Keys are sorted for deterministic
// ordering.
func wireScanToTransport(w *indexScanWireResponse) *transport.IndexScanResponse {
	if w == nil || len(w.Entries) == 0 {
		return &transport.IndexScanResponse{}
	}

	entries := make([]transport.IndexScanEntry, 0, len(w.Entries))
	for val, keys := range w.Entries {
		entries = append(entries, transport.IndexScanEntry{
			Value: val,
			Keys:  keys,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Value < entries[j].Value
	})

	return &transport.IndexScanResponse{Entries: entries}
}
