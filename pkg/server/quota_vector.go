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

package server

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// quotaVectorService wraps a VectorServicer to enforce a tenant's storage-byte
// quota. Vector embeddings and their metadata are independent tenant payload
// written outside the KV/MVCC servicers, so each mutating call is admitted
// through the Accountant first: an upsert that would push the tenant over its
// MaxStorageBytes is rejected with *quota.ExceededError before it reaches
// consensus, and the reserved bytes are released if the write fails downstream.
// All namespace, search, list, index, and compaction operations embed the inner
// servicer and pass straight through with no accounting.
//
// Vector size measure: a vector is accounted as
//
//	len(vector)*vectorFloatBytes + metadataBytes(metadata)
//
// where vectorFloatBytes is the 4-byte float32 element width (the embedding is
// the dominant payload), and metadataBytes sums each metadata key length plus a
// per-value size: the exact length for string and []byte values, and a fixed
// metadataScalarBytes estimate for numeric/boolean/other scalar values (which
// the engine persists in a fixed-width msgpack encoding). This avoids
// re-serializing the metadata map on the hot path. Consistent with the KV quota
// path, overwrites (re-upserts, metadata replaces) are counted by full size
// rather than a delta and deletes are not credited; the background reconciler
// corrects the persisted counter for volume tenants.
type quotaVectorService struct {
	service.VectorServicer
	acct     *quota.Accountant
	tenantID string
	limit    int64
}

// Compile-time interface check.
var _ service.VectorServicer = (*quotaVectorService)(nil)

// newQuotaVectorService wraps inner with storage-byte quota enforcement for the
// given tenant and limit (MaxStorageBytes).
func newQuotaVectorService(inner service.VectorServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaVectorService {
	return &quotaVectorService{
		VectorServicer: inner,
		acct:           acct,
		tenantID:       tenantID,
		limit:          limit,
	}
}

const (
	// vectorFloatBytes is the byte width of a single float32 embedding element.
	vectorFloatBytes = 4

	// metadataScalarBytes is the accounted size of a non-string, non-byte
	// metadata value (number, boolean, etc.), matching the engine's fixed-width
	// scalar encoding.
	metadataScalarBytes = 8
)

// metadataValueBytes returns the accounted byte size of a single metadata value.
func metadataValueBytes(v interface{}) int {
	switch t := v.(type) {
	case string:
		return len(t)
	case []byte:
		return len(t)
	default:
		return metadataScalarBytes
	}
}

// metadataBytes returns the accounted byte size of a metadata map: each key
// length plus its value size.
func metadataBytes(meta map[string]interface{}) int {
	var n int
	for k, v := range meta {
		n += len(k) + metadataValueBytes(v)
	}
	return n
}

// vectorPayloadBytes returns the accounted byte size of a vector embedding and
// its metadata.
func vectorPayloadBytes(vec []float32, meta map[string]interface{}) int {
	return len(vec)*vectorFloatBytes + metadataBytes(meta)
}

// reserve admits n bytes against the tenant's quota.
func (s *quotaVectorService) reserve(ctx context.Context, n int) error {
	return s.acct.Reserve(ctx, s.tenantID, s.limit, int64(n))
}

// UpsertVector admits the vector's payload bytes against the quota, then
// forwards.
func (s *quotaVectorService) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	var n int
	if req != nil {
		n = vectorPayloadBytes(req.Vector, req.Metadata)
	}
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.VectorServicer.UpsertVector(ctx, req)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// BatchUpsert admits the sum of the batch's vector payload bytes against the
// quota, then forwards.
func (s *quotaVectorService) BatchUpsert(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	var n int
	if req != nil {
		for _, v := range req.Vectors {
			if v != nil {
				n += vectorPayloadBytes(v.Vector, v.Metadata)
			}
		}
	}
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.VectorServicer.BatchUpsert(ctx, req)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}

// UpdateMetadata admits the new metadata's bytes against the quota, then
// forwards. The vector values are unchanged, so only the metadata payload is
// accounted.
func (s *quotaVectorService) UpdateMetadata(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	var n int
	if req != nil {
		n = metadataBytes(req.Metadata)
	}
	if err := s.reserve(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.VectorServicer.UpdateMetadata(ctx, req)
	if err != nil {
		s.acct.Release(s.tenantID, int64(n))
		return nil, err
	}
	return resp, nil
}
