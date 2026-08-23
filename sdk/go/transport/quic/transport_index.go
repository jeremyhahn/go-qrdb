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

package quic

import (
	"context"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.IndexClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// IndexClient implementation
// ---------------------------------------------------------------------------

// IndexCreate creates a new secondary index with the given name, field,
// and configuration through the QUIC transport.
func (t *Transport) IndexCreate(ctx context.Context, name, fieldName string, config transport.IndexConfig) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.IndexCreateRequest{
		Name:            name,
		FieldName:       fieldName,
		Unique:          config.Unique,
		Sparse:          config.Sparse,
		Numeric:         config.Numeric,
		CaseInsensitive: config.CaseInsensitive,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "index.Create", payload)
	return err
}

// IndexDrop removes a secondary index by name through the QUIC transport.
func (t *Transport) IndexDrop(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.IndexDropRequest{
		Name: name,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "index.Drop", payload)
	return err
}

// IndexQuery returns entity keys matching an exact value in an index
// through the QUIC transport.
func (t *Transport) IndexQuery(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.IndexQueryRequest{
		IndexName: indexName,
		Value:     []byte(value),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "index.Query", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.IndexQueryResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	keys := make([]string, len(pbResp.GetKeys()))
	for i, k := range pbResp.GetKeys() {
		keys[i] = string(k)
	}

	return &transport.IndexQueryResponse{
		Keys: keys,
	}, nil
}

// IndexScan returns entries in an index within a value range [start, end)
// through the QUIC transport.
func (t *Transport) IndexScan(ctx context.Context, indexName, start, end string) (*transport.IndexScanResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.IndexScanRequest{
		IndexName: indexName,
		Start:     []byte(start),
		End:       []byte(end),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "index.Scan", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.IndexScanResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	entries := make([]transport.IndexScanEntry, len(pbResp.GetEntries()))
	for i, e := range pbResp.GetEntries() {
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

// IndexList returns the names of all registered indexes through the
// QUIC transport.
func (t *Transport) IndexList(ctx context.Context) (*transport.IndexListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&emptypb.Empty{})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "index.List", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.IndexListResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return &transport.IndexListResponse{
		Names: pbResp.GetNames(),
	}, nil
}
