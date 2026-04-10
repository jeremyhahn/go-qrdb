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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.MVCCClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Server-side JSON decode types for MVCC responses
// ---------------------------------------------------------------------------

// serverKeyValue mirrors kv.KeyValue for JSON decoding from the MCP
// server. The server-side struct has no JSON tags, so Go's default
// PascalCase encoding is used.
type serverKeyValue struct {
	Key            []byte `json:"Key"`
	Value          []byte `json:"Value"`
	CreateRevision int64  `json:"CreateRevision"`
	ModRevision    int64  `json:"ModRevision"`
	Version        int64  `json:"Version"`
	LeaseID        int64  `json:"LeaseID"`
}

// serverRangeResponse mirrors the mvccRangeResponse envelope returned
// by the qrdb_mvcc_range tool.
type serverRangeResponse struct {
	KeyValues json.RawMessage `json:"key_values"`
	Count     int64           `json:"count"`
}

// serverCurrentRevisionResponse mirrors the JSON envelope returned by
// the qrdb_mvcc_current_revision tool.
type serverCurrentRevisionResponse struct {
	Revision int64 `json:"revision"`
}

// serverKeyValueToTransport converts a server-side KeyValue to the SDK
// transport representation.
func serverKeyValueToTransport(skv *serverKeyValue) *transport.MVCCKeyValue {
	return &transport.MVCCKeyValue{
		Key:            skv.Key,
		Value:          skv.Value,
		CreateRevision: skv.CreateRevision,
		ModRevision:    skv.ModRevision,
		Version:        skv.Version,
		LeaseID:        skv.LeaseID,
	}
}

// ---------------------------------------------------------------------------
// MVCCClient implementation
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease through
// MVCC-aware Raft consensus. When the lease expires, the key is
// automatically deleted. Calls the qrdb_mvcc_put_with_lease MCP tool.
func (t *Transport) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...transport.WriteOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"key":      key,
		"value":    string(value),
		"lease_id": leaseID,
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		args["write_mode"] = wo.Mode.String()
	}

	text, err := t.callTool(ctx, "qrdb_mvcc_put_with_lease", args)
	if err != nil {
		return nil, err
	}

	var raw serverKeyValue
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "PutWithLease", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverKeyValueToTransport(&raw), nil
}

// GetRevision retrieves a key-value pair with full MVCC revision
// metadata at the latest revision. Calls the qrdb_mvcc_get_revision
// MCP tool. Returns KeyNotFoundError when the key does not exist.
func (t *Transport) GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"key": key,
	}

	applyReadArgs(args, opts...)

	text, err := t.callTool(ctx, "qrdb_mvcc_get_revision", args)
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: key}
		}
		return nil, err
	}

	var raw serverKeyValue
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetRevision", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverKeyValueToTransport(&raw), nil
}

// GetAtRevision retrieves a key-value pair as it existed at a specific
// historical MVCC revision. Calls the qrdb_mvcc_get_at_revision MCP
// tool. Returns RevisionCompactedError when the revision has been
// compacted.
func (t *Transport) GetAtRevision(ctx context.Context, key string, revision int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"key":      key,
		"revision": revision,
	}

	applyReadArgs(args, opts...)

	text, err := t.callTool(ctx, "qrdb_mvcc_get_at_revision", args)
	if err != nil {
		if isNotFoundError(err) {
			return nil, &transport.KeyNotFoundError{Key: key}
		}
		return nil, err
	}

	var raw serverKeyValue
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "GetAtRevision", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return serverKeyValueToTransport(&raw), nil
}

// Range retrieves key-value pairs in the half-open interval [start, end)
// with MVCC revision metadata. Calls the qrdb_mvcc_range MCP tool and
// parses the JSON envelope containing key-values and count.
func (t *Transport) Range(ctx context.Context, start, end []byte, rangeOpts transport.RangeOptions, opts ...transport.ReadOption) (*transport.RangeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	args := map[string]any{
		"start": string(start),
		"end":   string(end),
	}
	if rangeOpts.Limit > 0 {
		args["limit"] = rangeOpts.Limit
	}
	if rangeOpts.Revision > 0 {
		args["revision"] = rangeOpts.Revision
	}
	if rangeOpts.CountOnly {
		args["count_only"] = true
	}
	if rangeOpts.KeysOnly {
		args["keys_only"] = true
	}

	applyReadArgs(args, opts...)

	text, err := t.callTool(ctx, "qrdb_mvcc_range", args)
	if err != nil {
		return nil, err
	}

	var raw serverRangeResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "Range", Err: &jsonDecodeError{Err: jsonErr}}
	}

	var serverKVs []serverKeyValue
	if len(raw.KeyValues) > 0 {
		if jsonErr := json.Unmarshal(raw.KeyValues, &serverKVs); jsonErr != nil {
			return nil, &transport.RequestError{Method: "Range", Err: &jsonDecodeError{Err: jsonErr}}
		}
	}

	kvs := make([]*transport.MVCCKeyValue, len(serverKVs))
	for i := range serverKVs {
		kvs[i] = serverKeyValueToTransport(&serverKVs[i])
	}

	return &transport.RangeResponse{
		Kvs:   kvs,
		Count: raw.Count,
	}, nil
}

// Compact discards all MVCC revisions older than the specified revision
// to reclaim storage. Calls the qrdb_mvcc_compact MCP tool.
func (t *Transport) Compact(ctx context.Context, revision int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_mvcc_compact", map[string]any{
		"revision": revision,
	})
	return err
}

// CurrentRevision returns the latest committed MVCC revision. Calls the
// qrdb_mvcc_current_revision MCP tool and parses the JSON envelope.
func (t *Transport) CurrentRevision(ctx context.Context) (int64, error) {
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_mvcc_current_revision", nil)
	if err != nil {
		return 0, err
	}

	var raw serverCurrentRevisionResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return 0, &transport.RequestError{Method: "CurrentRevision", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return raw.Revision, nil
}
