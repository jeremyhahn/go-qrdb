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
	"sort"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.IndexClient = (*Transport)(nil)

// IndexCreate creates a new secondary index on a specified field. Calls
// the qrdb_index_create MCP tool.
func (t *Transport) IndexCreate(ctx context.Context, name, fieldName string, config transport.IndexConfig) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	params := map[string]any{
		"name":       name,
		"field_name": fieldName,
	}
	if config.Unique {
		params["unique"] = true
	}
	if config.Sparse {
		params["sparse"] = true
	}
	if config.Numeric {
		params["numeric"] = true
	}
	if config.CaseInsensitive {
		params["case_insensitive"] = true
	}

	_, err := t.callTool(ctx, "qrdb_index_create", params)
	return err
}

// IndexDrop removes a secondary index by name. Calls the
// qrdb_index_drop MCP tool.
func (t *Transport) IndexDrop(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.callTool(ctx, "qrdb_index_drop", map[string]any{
		"name": name,
	})
	return err
}

// IndexQuery queries a secondary index for keys matching an exact
// value. Calls the qrdb_index_query MCP tool and parses the returned
// JSON array of key strings.
func (t *Transport) IndexQuery(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_index_query", map[string]any{
		"index_name": indexName,
		"value":      value,
	})
	if err != nil {
		return nil, err
	}

	var keys []string
	if jsonErr := json.Unmarshal([]byte(text), &keys); jsonErr != nil {
		return nil, &transport.RequestError{Method: "IndexQuery", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.IndexQueryResponse{Keys: keys}, nil
}

// IndexScan scans a secondary index for entries whose values fall in
// the half-open interval [start, end). Calls the qrdb_index_scan MCP
// tool and parses the returned JSON map of value-to-keys entries.
func (t *Transport) IndexScan(ctx context.Context, indexName string, start, end *string) (*transport.IndexScanResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	// Only present bounds are sent; an absent argument is an OPEN bound, which
	// the tool handler distinguishes from a present-but-empty one (go-qrdb#36).
	args := map[string]any{"index_name": indexName}
	if start != nil {
		args["start"] = *start
	}
	if end != nil {
		args["end"] = *end
	}
	text, err := t.callTool(ctx, "qrdb_index_scan", args)
	if err != nil {
		return nil, err
	}

	var raw map[string][]string
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "IndexScan", Err: &jsonDecodeError{Err: jsonErr}}
	}

	entries := make([]transport.IndexScanEntry, 0, len(raw))
	for val, keys := range raw {
		entries = append(entries, transport.IndexScanEntry{
			Value: val,
			Keys:  keys,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Value < entries[j].Value
	})

	return &transport.IndexScanResponse{Entries: entries}, nil
}

// IndexList returns the names of all registered secondary indexes.
// Calls the qrdb_index_list MCP tool and parses the returned JSON
// array of name strings.
func (t *Transport) IndexList(ctx context.Context) (*transport.IndexListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	text, err := t.callTool(ctx, "qrdb_index_list", nil)
	if err != nil {
		return nil, err
	}

	var names []string
	if jsonErr := json.Unmarshal([]byte(text), &names); jsonErr != nil {
		return nil, &transport.RequestError{Method: "IndexList", Err: &jsonDecodeError{Err: jsonErr}}
	}

	return &transport.IndexListResponse{Names: names}, nil
}
