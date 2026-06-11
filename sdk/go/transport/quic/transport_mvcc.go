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
	"io"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.MVCCClient        = (*Transport)(nil)
	_ transport.TransactionClient = (*Transport)(nil)
	_ transport.BatchClient       = (*Transport)(nil)
	_ transport.LeaseClient       = (*Transport)(nil)
)

// ---------------------------------------------------------------------------
// Typed errors
// ---------------------------------------------------------------------------

// marshalError is returned when protobuf serialization fails.
type marshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *marshalError) Error() string {
	return "quic: failed to marshal request: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *marshalError) Unwrap() error {
	return e.Err
}

// unmarshalError is returned when protobuf deserialization fails.
type unmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *unmarshalError) Error() string {
	return "quic: failed to unmarshal response: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *unmarshalError) Unwrap() error {
	return e.Err
}

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

// vectorConsistencyToProto maps a transport.ConsistencyLevel stored as a
// uint8 in vector request types to its proto ConsistencyLevel equivalent.
// The zero value (ConsistencyStrong) maps to STRONG so that the server
// preserves the same behavior clients received when the field was int32.
func vectorConsistencyToProto(level uint8) pb.ConsistencyLevel {
	switch transport.ConsistencyLevel(level) {
	case transport.ConsistencyLinearizable:
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE
	case transport.ConsistencyEventual:
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	case transport.ConsistencyFollower:
		// No dedicated wire value yet; remote clients receive the closest
		// non-blocking semantic (local read) instead of a leader round-trip.
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	default:
		// ConsistencyStrong (0) and any unrecognized value → STRONG.
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG
	}
}

// protoConsistency resolves read options into the proto ConsistencyLevel
// carried in the request body. UNSPECIFIED is returned when no consistency
// was requested, letting the server apply its default. An explicit
// WithAllowStale (without a level) maps to EVENTUAL.
func protoConsistency(opts ...transport.ReadOption) pb.ConsistencyLevel {
	ro := transport.ResolveReadOptions(opts...)
	if ro.ConsistencySet {
		switch ro.Consistency {
		case transport.ConsistencyStrong:
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG
		case transport.ConsistencyLinearizable:
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE
		case transport.ConsistencyEventual:
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
		case transport.ConsistencyFollower:
			// No dedicated wire value yet; map to the closest non-blocking
			// semantic so remote clients never hang on a leader round-trip.
			return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
		}
	}
	if ro.AllowStale {
		return pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL
	}
	return pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED
}

// protoWriteMode resolves write options into the proto WriteMode carried in
// the request body. UNSPECIFIED is returned when no mode was requested.
func protoWriteMode(opts ...transport.WriteOption) pb.WriteMode {
	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		switch wo.Mode {
		case transport.WriteModeSync:
			return pb.WriteMode_WRITE_MODE_SYNC
		case transport.WriteModeWALDurable:
			return pb.WriteMode_WRITE_MODE_WAL_DURABLE
		case transport.WriteModeBatched:
			return pb.WriteMode_WRITE_MODE_BATCHED
		}
	}
	return pb.WriteMode_WRITE_MODE_UNSPECIFIED
}

// compareTargetToProto converts transport CompareTarget to proto CompareTarget.
var compareTargetToProto = map[transport.CompareTarget]pb.CompareTarget{
	transport.CompareVersion:        pb.CompareTarget_COMPARE_VERSION,
	transport.CompareCreateRevision: pb.CompareTarget_COMPARE_CREATE_REVISION,
	transport.CompareModRevision:    pb.CompareTarget_COMPARE_MOD_REVISION,
	transport.CompareValue:          pb.CompareTarget_COMPARE_VALUE,
}

// compareResultToProto converts transport CompareResult to proto CompareResult.
var compareResultToProto = map[transport.CompareResult]pb.CompareResult{
	transport.CompareEqual:    pb.CompareResult_COMPARE_EQUAL,
	transport.CompareGreater:  pb.CompareResult_COMPARE_GREATER,
	transport.CompareLess:     pb.CompareResult_COMPARE_LESS,
	transport.CompareNotEqual: pb.CompareResult_COMPARE_NOT_EQUAL,
}

// opTypeToProto converts transport OpType to proto OpType.
var opTypeToProto = map[transport.OpType]pb.OpType{
	transport.OpPut:    pb.OpType_OP_PUT,
	transport.OpGet:    pb.OpType_OP_GET,
	transport.OpDelete: pb.OpType_OP_DELETE,
	transport.OpRange:  pb.OpType_OP_RANGE,
}

// ---------------------------------------------------------------------------
// MVCCClient implementation
// ---------------------------------------------------------------------------

// PutWithLease stores a key-value pair with an attached lease through
// the QUIC transport. When opts specify a write mode, the WriteMode proto
// field is set on the request so the server routes to the appropriate write
// path. When the lease expires, the key is automatically deleted.
func (t *Transport) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64, opts ...transport.WriteOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.PutWithLeaseRequest{
		Key:       key,
		Value:     value,
		LeaseId:   leaseID,
		WriteMode: protoWriteMode(opts...),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "mvcc.PutWithLease", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.PutWithLeaseResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToMVCCKeyValue(pbResp.GetKv()), nil
}

// GetRevision retrieves a key-value pair with full MVCC revision metadata
// at the latest committed revision through the QUIC transport. When opts
// specify a consistency level, the ConsistencyLevel proto field is set on
// the request.
func (t *Transport) GetRevision(ctx context.Context, key string, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.GetRevisionRequest{
		Key:         key,
		Consistency: protoConsistency(opts...),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "mvcc.GetRevision", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.GetRevisionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToMVCCKeyValue(pbResp.GetKv()), nil
}

// GetAtRevision retrieves a key-value pair at a historical MVCC revision
// through the QUIC transport. When opts specify a consistency level, the
// ConsistencyLevel proto field is set on the request. Returns
// RevisionCompactedError when the revision has been compacted.
func (t *Transport) GetAtRevision(ctx context.Context, key string, revision int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.GetAtRevisionRequest{
		Key:         key,
		Revision:    revision,
		Consistency: protoConsistency(opts...),
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "mvcc.GetAtRevision", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.GetAtRevisionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToMVCCKeyValue(pbResp.GetKv()), nil
}

// Range returns key-value pairs in the half-open interval [start, end)
// through the QUIC transport. When opts specify a consistency level, the
// ConsistencyLevel proto field is set on the request. It supports limit,
// revision, count-only, keys-only, and sort options.
func (t *Transport) Range(ctx context.Context, start, end []byte, rangeOpts transport.RangeOptions, opts ...transport.ReadOption) (*transport.RangeResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.RangeRequest{
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
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "mvcc.Range", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.RangeResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	kvs := make([]*transport.MVCCKeyValue, len(pbResp.GetKvs()))
	for i, kv := range pbResp.GetKvs() {
		kvs[i] = protoToMVCCKeyValue(kv)
	}

	return &transport.RangeResponse{
		Kvs:   kvs,
		Count: pbResp.GetCount(),
	}, nil
}

// Compact discards all MVCC revisions older than the specified revision
// through the QUIC transport. Compacted revisions can no longer be read
// via GetAtRevision.
func (t *Transport) Compact(ctx context.Context, revision int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.CompactRequest{
		Revision: revision,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "mvcc.Compact", payload)
	return err
}

// CurrentRevision returns the latest committed MVCC revision through
// the QUIC transport.
func (t *Transport) CurrentRevision(ctx context.Context) (int64, error) {
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}

	resp, err := t.call(ctx, "mvcc.CurrentRevision", nil)
	if err != nil {
		return 0, err
	}

	var pbResp pb.CurrentRevisionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return 0, &unmarshalError{Err: err}
	}

	return pbResp.GetRevision(), nil
}

// ---------------------------------------------------------------------------
// TransactionClient implementation
// ---------------------------------------------------------------------------

// ExecuteTransaction atomically evaluates compares and executes the
// corresponding operation set through the QUIC transport. When all
// compares succeed, success ops are applied; otherwise failure ops
// are applied.
func (t *Transport) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbCompares := make([]*pb.Compare, len(req.Compares))
	for i, c := range req.Compares {
		pbCompares[i] = compareToProto(c)
	}

	pbSuccessOps := make([]*pb.Op, len(req.SuccessOps))
	for i, op := range req.SuccessOps {
		pbSuccessOps[i] = opToProto(op)
	}

	pbFailureOps := make([]*pb.Op, len(req.FailureOps))
	for i, op := range req.FailureOps {
		pbFailureOps[i] = opToProto(op)
	}

	payload, err := proto.Marshal(&pb.TransactionRequest{
		Compares:   pbCompares,
		SuccessOps: pbSuccessOps,
		FailureOps: pbFailureOps,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "tx.Transaction", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.TransactionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	responses := make([]transport.OpResponse, len(pbResp.GetResponses()))
	for i, r := range pbResp.GetResponses() {
		responses[i] = protoToOpResponse(r)
	}

	return &transport.TransactionResponse{
		Succeeded: pbResp.GetSucceeded(),
		Revision:  pbResp.GetRevision(),
		Responses: responses,
	}, nil
}

// ---------------------------------------------------------------------------
// BatchClient implementation
// ---------------------------------------------------------------------------

// ExecuteBatch applies the given operations atomically through the QUIC
// transport. All operations succeed or fail together.
func (t *Transport) ExecuteBatch(ctx context.Context, ops []transport.Op, opts ...transport.WriteOption) (*transport.BatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbOps := make([]*pb.Op, len(ops))
	for i, op := range ops {
		pbOps[i] = opToProto(op)
	}

	payload, err := proto.Marshal(&pb.BatchRequest{
		Ops: pbOps,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "batch.Batch", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.BatchResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	responses := make([]transport.OpResponse, len(pbResp.GetResponses()))
	for i, r := range pbResp.GetResponses() {
		responses[i] = protoToOpResponse(r)
	}

	return &transport.BatchResponse{
		Responses: responses,
	}, nil
}

// ImportFile is not yet implemented for the QUIC transport.
func (t *Transport) ImportFile(_ context.Context, _ string, _ io.Reader, _ transport.ImportOptions) (*transport.ImportResult, error) {
	return nil, &transport.NotImplementedError{Method: "ImportFile"}
}

// ---------------------------------------------------------------------------
// LeaseClient implementation
// ---------------------------------------------------------------------------

// LeaseGrant creates a new lease with the specified TTL in seconds
// through the QUIC transport.
func (t *Transport) LeaseGrant(ctx context.Context, ttl int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LeaseGrantRequest{
		Ttl: ttl,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lease.Grant", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LeaseGrantResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLeaseInfo(pbResp.GetLease()), nil
}

// LeaseRevoke immediately expires a lease and deletes attached keys
// through the QUIC transport.
func (t *Transport) LeaseRevoke(ctx context.Context, leaseID int64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LeaseRevokeRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	_, err = t.call(ctx, "lease.Revoke", payload)
	return err
}

// LeaseKeepAlive resets the TTL on an existing lease through the QUIC
// transport.
func (t *Transport) LeaseKeepAlive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LeaseKeepAliveRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lease.KeepAlive", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LeaseKeepAliveResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLeaseInfo(pbResp.GetLease()), nil
}

// LeaseTimeToLive returns the current state of a lease through the
// QUIC transport.
func (t *Transport) LeaseTimeToLive(ctx context.Context, leaseID int64) (*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.LeaseTimeToLiveRequest{
		LeaseId: leaseID,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	resp, err := t.call(ctx, "lease.TimeToLive", payload)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LeaseTimeToLiveResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	return protoToLeaseInfo(pbResp.GetLease()), nil
}

// LeaseList returns all active leases through the QUIC transport.
func (t *Transport) LeaseList(ctx context.Context) ([]*transport.LeaseInfo, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.call(ctx, "lease.List", nil)
	if err != nil {
		return nil, err
	}

	var pbResp pb.LeaseListResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	leases := make([]*transport.LeaseInfo, len(pbResp.GetLeases()))
	for i, l := range pbResp.GetLeases() {
		leases[i] = protoToLeaseInfo(l)
	}

	return leases, nil
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

// compareToProto converts a transport Compare to a proto Compare.
func compareToProto(c transport.Compare) *pb.Compare {
	pbCmp := &pb.Compare{
		Key:    c.Key,
		Target: compareTargetToProto[c.Target],
		Result: compareResultToProto[c.Result],
	}

	switch c.Target {
	case transport.CompareVersion:
		pbCmp.TargetUnion = &pb.Compare_Version{Version: c.Version}
	case transport.CompareCreateRevision:
		pbCmp.TargetUnion = &pb.Compare_CreateRevision{CreateRevision: c.CreateRevision}
	case transport.CompareModRevision:
		pbCmp.TargetUnion = &pb.Compare_ModRevision{ModRevision: c.ModRevision}
	case transport.CompareValue:
		pbCmp.TargetUnion = &pb.Compare_Value{Value: c.Value}
	}

	return pbCmp
}

// opToProto converts a transport Op to a proto Op.
func opToProto(op transport.Op) *pb.Op {
	return &pb.Op{
		Type:     opTypeToProto[op.Type],
		Key:      op.Key,
		Value:    op.Value,
		RangeEnd: op.RangeEnd,
		LeaseId:  op.LeaseID,
	}
}

// protoToOpResponse converts a proto OpResponse to a transport OpResponse.
func protoToOpResponse(r *pb.OpResponse) transport.OpResponse {
	kvs := make([]*transport.MVCCKeyValue, len(r.GetKvs()))
	for i, kv := range r.GetKvs() {
		kvs[i] = protoToMVCCKeyValue(kv)
	}
	return transport.OpResponse{
		Kvs:       kvs,
		Count:     r.GetCount(),
		Succeeded: r.GetSucceeded(),
	}
}

// protoToLeaseInfo converts a proto Lease to a transport LeaseInfo.
func protoToLeaseInfo(l *pb.Lease) *transport.LeaseInfo {
	if l == nil {
		return nil
	}
	return &transport.LeaseInfo{
		ID:           l.GetId(),
		TTL:          l.GetTtl(),
		GrantedTTL:   l.GetGrantedTtl(),
		RemainingTTL: l.GetRemainingTtl(),
		Keys:         l.GetKeys(),
	}
}
