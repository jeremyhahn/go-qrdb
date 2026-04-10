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
	"net/http"
	"net/url"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.MVCCClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for MVCC REST communication
// ---------------------------------------------------------------------------

// mvccKVWireResponse matches the JSON shape returned by the REST server
// for key-value responses with MVCC metadata. The server encodes key and
// value as plain strings (not base64).
type mvccKVWireResponse struct {
	Key            string `json:"key"`
	Value          string `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// mvccRangeWireResponse matches the JSON shape returned by GET /kv/range.
type mvccRangeWireResponse struct {
	Kvs      []*mvccKVWireResponse `json:"kvs"`
	Count    int64                 `json:"count"`
	Revision int64                 `json:"revision,omitempty"`
}

// mvccRevisionWireResponse matches the JSON shape returned by
// GET /kv/revision.
type mvccRevisionWireResponse struct {
	Revision int64 `json:"revision"`
}

// mvccCompactWireRequest matches the JSON payload for POST /kv/compact.
type mvccCompactWireRequest struct {
	Revision int64 `json:"revision"`
}

// ---------------------------------------------------------------------------
// Sort order/target string maps
// ---------------------------------------------------------------------------

// sortOrderStringMap converts transport SortOrder to the query parameter
// string expected by the REST server.
var sortOrderStringMap = map[transport.SortOrder]string{
	transport.SortNone:    "none",
	transport.SortAscend:  "ascend",
	transport.SortDescend: "descend",
}

// sortTargetStringMap converts transport SortTarget to the query parameter
// string expected by the REST server.
var sortTargetStringMap = map[transport.SortTarget]string{
	transport.SortByKey:            "key",
	transport.SortByVersion:        "version",
	transport.SortByCreateRevision: "create_revision",
	transport.SortByModRevision:    "mod_revision",
	transport.SortByValue:          "value",
}

// ---------------------------------------------------------------------------
// MVCCClient implementation
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease. Maps to
// PUT /kv/{key}?lease-id=N. When the lease expires, the key is
// automatically deleted.
func (t *Transport) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...transport.WriteOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key) + "?lease-id=" + strconv.FormatInt(leaseID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(value))
	if err != nil {
		return nil, &transport.RequestError{Method: "PutWithLease", Err: err}
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		req.Header.Set("X-QRDB-Write-Mode", wo.Mode.String())
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "PutWithLease", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, t.decodeErrorResponse("PutWithLease", resp)
	}

	var wireKV mvccKVWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireKV); err != nil {
		return nil, &transport.RequestError{Method: "PutWithLease", Err: &jsonDecodeError{Err: err}}
	}

	return wireKVToTransport(&wireKV), nil
}

// GetRevision retrieves a key-value pair with full MVCC revision metadata.
// Maps to GET /kv/{key}?revision=true. Returns KeyNotFoundError when the
// key does not exist.
func (t *Transport) GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key) + "?revision=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRevision", Err: err}
	}

	applyReadHeaders(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRevision", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.KeyNotFoundError{Key: key}
	}

	if resp.StatusCode >= 400 {
		return nil, t.decodeErrorResponse("GetRevision", resp)
	}

	var wireKV mvccKVWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireKV); err != nil {
		return nil, &transport.RequestError{Method: "GetRevision", Err: &jsonDecodeError{Err: err}}
	}

	return wireKVToTransport(&wireKV), nil
}

// GetAtRevision retrieves a key-value pair at a historical MVCC revision.
// Maps to GET /kv/{key}?at-revision=N. Returns RevisionCompactedError
// when the revision has been compacted.
func (t *Transport) GetAtRevision(ctx context.Context, key string, revision int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key) + "?at-revision=" + strconv.FormatInt(revision, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetAtRevision", Err: err}
	}

	applyReadHeaders(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetAtRevision", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.KeyNotFoundError{Key: key}
	}

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return nil, &transport.RevisionCompactedError{Revision: revision}
	}

	if resp.StatusCode >= 400 {
		return nil, t.decodeErrorResponse("GetAtRevision", resp)
	}

	var wireKV mvccKVWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireKV); err != nil {
		return nil, &transport.RequestError{Method: "GetAtRevision", Err: &jsonDecodeError{Err: err}}
	}

	return wireKVToTransport(&wireKV), nil
}

// Range returns key-value pairs in the half-open interval [start, end).
// Maps to GET /kv/range with query parameters for start, end, limit,
// revision, count-only, keys-only, sort-order, and sort-target.
func (t *Transport) Range(ctx context.Context, start, end []byte, rangeOpts transport.RangeOptions, opts ...transport.ReadOption) (*transport.RangeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("start", string(start))
	if len(end) > 0 {
		params.Set("end", string(end))
	}
	if rangeOpts.Limit > 0 {
		params.Set("limit", strconv.FormatInt(rangeOpts.Limit, 10))
	}
	if rangeOpts.Revision > 0 {
		params.Set("revision", strconv.FormatInt(rangeOpts.Revision, 10))
	}
	if rangeOpts.CountOnly {
		params.Set("count-only", "true")
	}
	if rangeOpts.KeysOnly {
		params.Set("keys-only", "true")
	}
	if rangeOpts.SortOrder != transport.SortNone {
		params.Set("sort-order", sortOrderStringMap[rangeOpts.SortOrder])
	}
	if rangeOpts.SortTarget != transport.SortByKey {
		params.Set("sort-target", sortTargetStringMap[rangeOpts.SortTarget])
	}

	reqURL := t.baseURL + "/kv/range?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Range", Err: err}
	}

	applyReadHeaders(req, opts...)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transport.RequestError{Method: "Range", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("Range", resp)
	}

	var wireResp mvccRangeWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "Range", Err: &jsonDecodeError{Err: err}}
	}

	kvs := make([]*transport.MVCCKeyValue, len(wireResp.Kvs))
	for i, wkv := range wireResp.Kvs {
		kvs[i] = wireKVToTransport(wkv)
	}

	return &transport.RangeResponse{
		Kvs:   kvs,
		Count: wireResp.Count,
	}, nil
}

// Compact discards all MVCC revisions older than the specified revision.
// Maps to POST /kv/compact with JSON body containing the revision.
func (t *Transport) Compact(ctx context.Context, revision int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := mvccCompactWireRequest{Revision: revision}
	return t.postJSON(ctx, "Compact", "/kv/compact", payload)
}

// CurrentRevision returns the latest committed MVCC revision. Maps to
// GET /kv/revision.
func (t *Transport) CurrentRevision(ctx context.Context) (int64, error) {
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/kv/revision", nil)
	if err != nil {
		return 0, &transport.RequestError{Method: "CurrentRevision", Err: err}
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return 0, &transport.RequestError{Method: "CurrentRevision", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, t.decodeErrorResponse("CurrentRevision", resp)
	}

	var wireResp mvccRevisionWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return 0, &transport.RequestError{Method: "CurrentRevision", Err: &jsonDecodeError{Err: err}}
	}

	return wireResp.Revision, nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// wireKVToTransport converts an MVCC wire key-value response to the
// transport MVCCKeyValue type.
func wireKVToTransport(wkv *mvccKVWireResponse) *transport.MVCCKeyValue {
	if wkv == nil {
		return nil
	}
	return &transport.MVCCKeyValue{
		Key:            []byte(wkv.Key),
		Value:          []byte(wkv.Value),
		CreateRevision: wkv.CreateRevision,
		ModRevision:    wkv.ModRevision,
		Version:        wkv.Version,
		LeaseID:        wkv.LeaseID,
	}
}
