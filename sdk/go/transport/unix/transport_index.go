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

package unix

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.IndexClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// IndexClient implementation
// ---------------------------------------------------------------------------

// IndexCreate creates a new secondary index on the specified field.
func (t *Transport) IndexCreate(ctx context.Context, name, fieldName string, config transport.IndexConfig) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.IndexCreate(ctx, &pb.IndexCreateRequest{
		Name:            name,
		FieldName:       fieldName,
		Unique:          config.Unique,
		Sparse:          config.Sparse,
		Numeric:         config.Numeric,
		CaseInsensitive: config.CaseInsensitive,
	})
	if err != nil {
		return mapGRPCError("IndexCreate", err)
	}
	return nil
}

// IndexDrop removes a secondary index by name.
func (t *Transport) IndexDrop(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.IndexDrop(ctx, &pb.IndexDropRequest{
		Name: name,
	})
	if err != nil {
		return mapGRPCError("IndexDrop", err)
	}
	return nil
}

// IndexQuery returns entity keys matching an exact value in a secondary index.
func (t *Transport) IndexQuery(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.IndexQuery(ctx, &pb.IndexQueryRequest{
		IndexName: indexName,
		Value:     []byte(value),
	})
	if err != nil {
		return nil, mapIndexAbsent("IndexQuery", indexName, err)
	}

	keys := make([]string, len(resp.GetKeys()))
	for i, k := range resp.GetKeys() {
		keys[i] = string(k)
	}

	return &transport.IndexQueryResponse{
		Keys: keys,
	}, nil
}

// IndexScan returns index entries within a value range.
func (t *Transport) IndexScan(ctx context.Context, indexName string, start, end *string) (*transport.IndexScanResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.IndexScan(ctx, &pb.IndexScanRequest{
		IndexName: indexName,
		Start:     transport.OptionalBytes(start),
		End:       transport.OptionalBytes(end),
	})
	if err != nil {
		return nil, mapIndexAbsent("IndexScan", indexName, err)
	}

	entries := make([]transport.IndexScanEntry, len(resp.GetEntries()))
	for i, e := range resp.GetEntries() {
		keys := make([]string, len(e.GetKeys()))
		for j, k := range e.GetKeys() {
			keys[j] = string(k)
		}
		entries[i] = transport.IndexScanEntry{
			Value: e.GetValue(),
			Keys:  keys,
		}
	}

	return &transport.IndexScanResponse{
		Entries: entries,
	}, nil
}

// IndexList returns the names of all registered secondary indexes.
func (t *Transport) IndexList(ctx context.Context) (*transport.IndexListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.IndexList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("IndexList", err)
	}

	return &transport.IndexListResponse{
		Names: resp.GetNames(),
	}, nil
}

// mapIndexAbsent turns the server's codes.NotFound on an index query or scan
// into the typed IndexNotFoundError every transport returns for an index that
// does not exist; any other error takes the generic mapping.
func mapIndexAbsent(method, indexName string, err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return &transport.IndexNotFoundError{Name: indexName}
	}
	return mapGRPCError(method, err)
}
