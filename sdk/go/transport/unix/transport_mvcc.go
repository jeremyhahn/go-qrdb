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
		Key:       key,
		Value:     value,
		LeaseId:   leaseID,
		WriteMode: protoWriteMode(opts...),
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
		Key:         key,
		Consistency: protoConsistency(opts...),
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
		Key:         key,
		Revision:    revision,
		Consistency: protoConsistency(opts...),
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
		Start:       start,
		End:         end,
		Limit:       rangeOpts.Limit,
		Revision:    rangeOpts.Revision,
		CountOnly:   rangeOpts.CountOnly,
		KeysOnly:    rangeOpts.KeysOnly,
		SortOrder:   sortOrderToProto[rangeOpts.SortOrder],
		SortTarget:  sortTargetToProto[rangeOpts.SortTarget],
		Consistency: protoConsistency(opts...),
	})
	if err != nil {
		return nil, mapGRPCError("Range", err)
	}

	kvs := make([]*transport.MVCCKeyValue, len(resp.GetKvs()))
	for i, kv := range resp.GetKvs() {
		kvs[i] = protoToMVCCKeyValue(kv)
	}

	return &transport.RangeResponse{
		Kvs:      kvs,
		Count:    resp.GetCount(),
		Revision: resp.GetRevision(),
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

// RestoreToRevision returns a bounded set of keys to the value they held at
// an earlier MVCC revision -- a logical undo, never a Raft rewind. Exactly
// one of req.Prefix and req.Keys must be set.
func (t *Transport) RestoreToRevision(ctx context.Context, req *transport.RestoreRevisionRequest) (*transport.RestoreRevisionResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "RestoreToRevision", Err: &transport.NilRequestError{}}
	}

	keys := make([][]byte, len(req.Keys))
	copy(keys, req.Keys)

	resp, err := t.client.RestoreToRevision(ctx, &pb.RestoreRevisionRequest{
		Revision: req.Revision,
		Prefix:   req.Prefix,
		Keys:     keys,
		DryRun:   req.DryRun,
		Force:    req.Force,
	})
	if err != nil {
		return nil, mapGRPCError("RestoreToRevision", err)
	}

	return protoToRestoreResult(resp), nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// restoreActionFromProto converts proto RestoreAction to transport
// RestoreAction.
var restoreActionFromProto = map[pb.RestoreAction]transport.RestoreAction{
	pb.RestoreAction_RESTORE_ACTION_REVERT:   transport.RestoreActionRevert,
	pb.RestoreAction_RESTORE_ACTION_RECREATE: transport.RestoreActionRecreate,
	pb.RestoreAction_RESTORE_ACTION_DELETE:   transport.RestoreActionDelete,
}

// protoToRestoreChange converts a proto RestoreKeyChange to its transport
// representation.
func protoToRestoreChange(c *pb.RestoreKeyChange) *transport.RestoreKeyChange {
	if c == nil {
		return nil
	}
	return &transport.RestoreKeyChange{
		Key:          c.GetKey(),
		Action:       restoreActionFromProto[c.GetAction()],
		FromRevision: c.GetFromRevision(),
		ToRevision:   c.GetToRevision(),
		ValueBytes:   c.GetValueBytes(),
	}
}

// protoToRestoreChanges converts a slice of proto RestoreKeyChange to their
// transport representation.
func protoToRestoreChanges(changes []*pb.RestoreKeyChange) []*transport.RestoreKeyChange {
	out := make([]*transport.RestoreKeyChange, len(changes))
	for i, c := range changes {
		out[i] = protoToRestoreChange(c)
	}
	return out
}

// protoToRestoreResult converts a proto RestoreRevisionResponse to the
// transport RestoreRevisionResult.
func protoToRestoreResult(resp *pb.RestoreRevisionResponse) *transport.RestoreRevisionResult {
	return &transport.RestoreRevisionResult{
		Revision:   resp.GetRevision(),
		Changes:    protoToRestoreChanges(resp.GetChanges()),
		Conflicts:  protoToRestoreChanges(resp.GetConflicts()),
		Applied:    int(resp.GetApplied()),
		Skipped:    int(resp.GetSkipped()),
		Conflicted: int(resp.GetConflicted()),
		DryRun:     resp.GetDryRun(),
	}
}

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
