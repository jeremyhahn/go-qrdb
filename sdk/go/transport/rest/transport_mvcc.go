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
// for key-value responses with MVCC metadata. The server encodes Key and
// Value as []byte, which encoding/json represents as base64 (RFC 4648) on
// the wire, so arbitrary binary values round-trip byte-identical.
type mvccKVWireResponse struct {
	Key            []byte `json:"key"`
	Value          []byte `json:"value"`
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

// mvccRestoreWireRequest matches the JSON payload for POST /kv/restore.
// Prefix and Keys are []byte / [][]byte, not string, so an arbitrary
// byte-valued key or prefix round-trips byte-for-byte through the base64
// (RFC 4648) encoding encoding/json applies to []byte.
type mvccRestoreWireRequest struct {
	Revision int64    `json:"revision"`
	Prefix   []byte   `json:"prefix,omitempty"`
	Keys     [][]byte `json:"keys,omitempty"`
	DryRun   bool     `json:"dry_run,omitempty"`
	Force    bool     `json:"force,omitempty"`
}

// mvccRestoreKeyChangeWireResponse matches the JSON shape of one restore
// plan entry returned by POST /kv/restore.
type mvccRestoreKeyChangeWireResponse struct {
	Key          []byte                  `json:"key"`
	Action       transport.RestoreAction `json:"action"`
	FromRevision int64                   `json:"from_revision,omitempty"`
	ToRevision   int64                   `json:"to_revision,omitempty"`
	ValueBytes   int64                   `json:"value_bytes,omitempty"`
}

// mvccRestoreWireResponse matches the JSON shape returned by
// POST /kv/restore.
type mvccRestoreWireResponse struct {
	Revision   int64                               `json:"revision"`
	Changes    []*mvccRestoreKeyChangeWireResponse `json:"changes"`
	Conflicts  []*mvccRestoreKeyChangeWireResponse `json:"conflicts,omitempty"`
	Applied    int                                 `json:"applied"`
	Skipped    int                                 `json:"skipped"`
	Conflicted int                                 `json:"conflicted"`
	DryRun     bool                                `json:"dry_run"`
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
// PUT /kv/{key}?lease-id=N. When the lease expires, the key is automatically
// deleted. An optional ?write_mode= query parameter is appended when a write
// mode was explicitly set.
func (t *Transport) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...transport.WriteOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key) + "?lease-id=" + strconv.FormatInt(leaseID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(value))
	if err != nil {
		return nil, &transport.RequestError{Method: "PutWithLease", Err: err}
	}

	applyWriteModeQueryParam(req, opts...)

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
// key does not exist. An optional ?consistency= query parameter is appended
// when a consistency level was set.
func (t *Transport) GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key) + "?revision=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRevision", Err: err}
	}

	applyReadQueryParams(req, opts...)

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
// Maps to GET /kv/{key}?at-revision=N. Returns RevisionCompactedError when
// the revision has been compacted. An optional ?consistency= query parameter
// is appended when a consistency level was set.
func (t *Transport) GetAtRevision(ctx context.Context, key string, revision int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/kv/" + url.PathEscape(key) + "?at-revision=" + strconv.FormatInt(revision, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetAtRevision", Err: err}
	}

	applyReadQueryParams(req, opts...)

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

	applyReadQueryParams(req, opts...)

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
		Kvs:      kvs,
		Count:    wireResp.Count,
		Revision: wireResp.Revision,
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

// RestoreToRevision returns a bounded set of keys to the value they held at
// an earlier MVCC revision -- a logical undo, never a Raft rewind. Maps to
// POST /kv/restore with a JSON body. Exactly one of req.Prefix and req.Keys
// must be set.
func (t *Transport) RestoreToRevision(ctx context.Context, req *transport.RestoreRevisionRequest) (*transport.RestoreRevisionResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: &transport.NilRequestError{}}
	}

	payload := mvccRestoreWireRequest{
		Revision: req.Revision,
		Prefix:   req.Prefix,
		Keys:     req.Keys,
		DryRun:   req.DryRun,
		Force:    req.Force,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/kv/restore", bytes.NewReader(data))
	if err != nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("RestoreToRevision", resp)
	}

	var wireResp mvccRestoreWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: &jsonDecodeError{Err: err}}
	}

	return wireRestoreResultToTransport(&wireResp), nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// wireRestoreChangeToTransport converts a wire restore key-change response
// to the transport RestoreKeyChange type.
func wireRestoreChangeToTransport(c *mvccRestoreKeyChangeWireResponse) *transport.RestoreKeyChange {
	if c == nil {
		return nil
	}
	return &transport.RestoreKeyChange{
		Key:          c.Key,
		Action:       c.Action,
		FromRevision: c.FromRevision,
		ToRevision:   c.ToRevision,
		ValueBytes:   c.ValueBytes,
	}
}

// wireRestoreChangesToTransport converts a slice of wire restore key-change
// responses to their transport representation.
func wireRestoreChangesToTransport(changes []*mvccRestoreKeyChangeWireResponse) []*transport.RestoreKeyChange {
	out := make([]*transport.RestoreKeyChange, len(changes))
	for i, c := range changes {
		out[i] = wireRestoreChangeToTransport(c)
	}
	return out
}

// wireRestoreResultToTransport converts a wire restore response to the
// transport RestoreRevisionResult type.
func wireRestoreResultToTransport(resp *mvccRestoreWireResponse) *transport.RestoreRevisionResult {
	return &transport.RestoreRevisionResult{
		Revision:   resp.Revision,
		Changes:    wireRestoreChangesToTransport(resp.Changes),
		Conflicts:  wireRestoreChangesToTransport(resp.Conflicts),
		Applied:    resp.Applied,
		Skipped:    resp.Skipped,
		Conflicted: resp.Conflicted,
		DryRun:     resp.DryRun,
	}
}

// wireKVToTransport converts an MVCC wire key-value response to the
// transport MVCCKeyValue type.
func wireKVToTransport(wkv *mvccKVWireResponse) *transport.MVCCKeyValue {
	if wkv == nil {
		return nil
	}
	return &transport.MVCCKeyValue{
		Key:            wkv.Key,
		Value:          wkv.Value,
		CreateRevision: wkv.CreateRevision,
		ModRevision:    wkv.ModRevision,
		Version:        wkv.Version,
		LeaseID:        wkv.LeaseID,
	}
}
