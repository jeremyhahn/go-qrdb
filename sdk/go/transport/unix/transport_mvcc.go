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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.MVCCClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Proto-to-transport conversion maps
// ---------------------------------------------------------------------------

// sortOrderToProto converts transport SortOrder to proto SortOrder.
var sortOrderToProto = map[transport.SortOrder]pb.SortOrder{
	transport.SortNone:    pb.SortOrder_SORT_NONE,
	transport.SortAscend:  pb.SortOrder_SORT_ASCEND,
	transport.SortDescend: pb.SortOrder_SORT_DESCEND,
}

// sortTargetToProto converts transport SortTarget to proto SortTarget.
var sortTargetToProto = map[transport.SortTarget]pb.SortTarget{
	transport.SortByKey:            pb.SortTarget_SORT_BY_KEY,
	transport.SortByVersion:        pb.SortTarget_SORT_BY_VERSION,
	transport.SortByCreateRevision: pb.SortTarget_SORT_BY_CREATE_REVISION,
	transport.SortByModRevision:    pb.SortTarget_SORT_BY_MOD_REVISION,
	transport.SortByValue:          pb.SortTarget_SORT_BY_VALUE,
}

// ---------------------------------------------------------------------------
// MVCCClient implementation
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease. When
// the lease expires, the key is automatically deleted.
func (t *Transport) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...transport.WriteOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyWriteMetadata(ctx, opts...)

	resp, err := t.client.PutWithLease(ctx, &pb.PutWithLeaseRequest{
		Key:     key,
		Value:   value,
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, mapGRPCError("PutWithLease", err)
	}

	return protoToMVCCKeyValue(resp.GetKv()), nil
}

// GetRevision retrieves a key-value pair with full MVCC revision
// metadata. Returns KeyNotFoundError when the key does not exist.
func (t *Transport) GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyReadMetadata(ctx, opts...)

	resp, err := t.client.GetRevision(ctx, &pb.GetRevisionRequest{
		Key: key,
	})
	if err != nil {
		return nil, mapGRPCError("GetRevision", err)
	}

	return protoToMVCCKeyValue(resp.GetKv()), nil
}

// GetAtRevision retrieves a key-value pair at a historical revision.
// Returns RevisionCompactedError when the revision has been compacted.
func (t *Transport) GetAtRevision(ctx context.Context, key string, revision int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyReadMetadata(ctx, opts...)

	resp, err := t.client.GetAtRevision(ctx, &pb.GetAtRevisionRequest{
		Key:      key,
		Revision: revision,
	})
	if err != nil {
		return nil, mapGRPCError("GetAtRevision", err)
	}

	return protoToMVCCKeyValue(resp.GetKv()), nil
}

// Range returns key-value pairs in the half-open interval [start, end).
// It supports limit, revision, count-only, keys-only, and sort options.
func (t *Transport) Range(ctx context.Context, start, end []byte, rangeOpts transport.RangeOptions, opts ...transport.ReadOption) (*transport.RangeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyReadMetadata(ctx, opts...)

	resp, err := t.client.Range(ctx, &pb.RangeRequest{
		Start:      start,
		End:        end,
		Limit:      rangeOpts.Limit,
		Revision:   rangeOpts.Revision,
		CountOnly:  rangeOpts.CountOnly,
		KeysOnly:   rangeOpts.KeysOnly,
		SortOrder:  sortOrderToProto[rangeOpts.SortOrder],
		SortTarget: sortTargetToProto[rangeOpts.SortTarget],
	})
	if err != nil {
		return nil, mapGRPCError("Range", err)
	}

	kvs := make([]*transport.MVCCKeyValue, len(resp.GetKvs()))
	for i, kv := range resp.GetKvs() {
		kvs[i] = protoToMVCCKeyValue(kv)
	}

	return &transport.RangeResponse{
		Kvs:   kvs,
		Count: resp.GetCount(),
	}, nil
}

// Compact discards all MVCC revisions older than the specified revision.
// Compacted revisions can no longer be read via GetAtRevision.
func (t *Transport) Compact(ctx context.Context, revision int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.Compact(ctx, &pb.CompactRequest{
		Revision: revision,
	})
	if err != nil {
		return mapGRPCError("Compact", err)
	}
	return nil
}

// CurrentRevision returns the latest committed MVCC revision.
func (t *Transport) CurrentRevision(ctx context.Context) (int64, error) {
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}

	resp, err := t.client.CurrentRevision(ctx, &emptypb.Empty{})
	if err != nil {
		return 0, mapGRPCError("CurrentRevision", err)
	}

	return resp.GetRevision(), nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// protoToMVCCKeyValue converts a proto KeyValue to a transport MVCCKeyValue.
func protoToMVCCKeyValue(kv *pb.KeyValue) *transport.MVCCKeyValue {
	if kv == nil {
		return nil
	}
	return &transport.MVCCKeyValue{
		Key:            kv.GetKey(),
		Value:          kv.GetValue(),
		CreateRevision: kv.GetCreateRevision(),
		ModRevision:    kv.GetModRevision(),
		Version:        kv.GetVersion(),
		LeaseID:        kv.GetLeaseId(),
	}
}
