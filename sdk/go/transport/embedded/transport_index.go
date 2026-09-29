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

package embedded

import (
	"context"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.IndexClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// IndexClient implementation
// ---------------------------------------------------------------------------

// IndexCreate creates a new secondary index by delegating to the injected
// IndexServicer.
func (t *Transport) IndexCreate(ctx context.Context, name, fieldName string, config transport.IndexConfig) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.indexSvc == nil {
		return &NilServiceError{Service: "IndexServicer"}
	}
	valueType := index.IndexValueTypeString
	if config.Numeric {
		valueType = index.IndexValueTypeNumeric
	}
	idxConfig := index.IndexConfig{
		Unique:    config.Unique,
		Sparse:    config.Sparse,
		ValueType: valueType,
		// index.IndexConfig names the field for the DEFAULT, so its zero value
		// is case-INSENSITIVE. Set it from the transport's positive claim
		// rather than leaving it zero, or an ordinary index created over this
		// transport folds case (go-qrdb#34).
		CaseSensitive: !config.CaseInsensitive,
	}
	return translateError(t.indexSvc.CreateIndex(ctx, name, fieldName, idxConfig))
}

// IndexDrop removes a secondary index by name by delegating to the
// injected IndexServicer.
func (t *Transport) IndexDrop(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.indexSvc == nil {
		return &NilServiceError{Service: "IndexServicer"}
	}
	return translateError(t.indexSvc.DropOwnedIndex(ctx, name, index.ExplicitOwner))
}

// IndexQuery returns entity keys matching an exact value in an index by
// delegating to the injected IndexServicer.
func (t *Transport) IndexQuery(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.indexSvc == nil {
		return nil, &NilServiceError{Service: "IndexServicer"}
	}
	rawKeys, err := t.indexSvc.Query(ctx, indexName, []byte(value))
	if err != nil {
		return nil, translateError(err)
	}
	keys := make([]string, len(rawKeys))
	for i, k := range rawKeys {
		keys[i] = string(k)
	}
	return &transport.IndexQueryResponse{Keys: keys}, nil
}

// IndexScan returns entries in an index within a value range [start, end)
// by delegating to the injected IndexServicer.
func (t *Transport) IndexScan(ctx context.Context, indexName string, start, end *string) (*transport.IndexScanResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.indexSvc == nil {
		return nil, &NilServiceError{Service: "IndexServicer"}
	}
	rawEntries, err := t.indexSvc.Scan(ctx, indexName, transport.OptionalBytes(start), transport.OptionalBytes(end))
	if err != nil {
		return nil, translateError(err)
	}
	entries := make([]transport.IndexScanEntry, 0, len(rawEntries))
	for val, rawKeys := range rawEntries {
		keys := make([]string, len(rawKeys))
		for i, k := range rawKeys {
			keys[i] = string(k)
		}
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

// IndexList returns the names of all registered indexes by delegating to
// the injected IndexServicer.
func (t *Transport) IndexList(ctx context.Context) (*transport.IndexListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.indexSvc == nil {
		return nil, &NilServiceError{Service: "IndexServicer"}
	}
	names, err := t.indexSvc.ListIndexes(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return &transport.IndexListResponse{Names: names}, nil
}

// ---------------------------------------------------------------------------
// Request dispatch handlers
// ---------------------------------------------------------------------------

func handleIndexCreate(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*indexCreateRequest)
	if !ok {
		return &InvalidRequestError{Method: "IndexCreate"}
	}
	return t.IndexCreate(ctx, r.Name, r.FieldName, r.Config)
}

func handleIndexDrop(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*indexDropRequest)
	if !ok {
		return &InvalidRequestError{Method: "IndexDrop"}
	}
	return t.IndexDrop(ctx, r.Name)
}

func handleIndexQuery(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*indexQueryRequest)
	if !ok {
		return &InvalidRequestError{Method: "IndexQuery"}
	}
	result, err := t.IndexQuery(ctx, r.IndexName, r.Value)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.IndexQueryResponse); ok {
		*out = result
	}
	return nil
}

func handleIndexScan(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*indexScanRequest)
	if !ok {
		return &InvalidRequestError{Method: "IndexScan"}
	}
	result, err := t.IndexScan(ctx, r.IndexName, r.Start, r.End)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.IndexScanResponse); ok {
		*out = result
	}
	return nil
}

func handleIndexList(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.IndexList(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.IndexListResponse); ok {
		*out = result
	}
	return nil
}

// ---------------------------------------------------------------------------
// Generic request types for Request dispatch
// ---------------------------------------------------------------------------

type indexCreateRequest struct {
	Name      string
	FieldName string
	Config    transport.IndexConfig
}

type indexDropRequest struct {
	Name string
}

type indexQueryRequest struct {
	IndexName string
	Value     string
}

type indexScanRequest struct {
	IndexName string
	Start     *string
	End       *string
}
