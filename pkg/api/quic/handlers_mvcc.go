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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// protoRestoreActionMap converts service RestoreAction values to proto
// RestoreAction values.
var protoRestoreActionMap = map[service.RestoreAction]pb.RestoreAction{
	service.RestoreActionRevert:   pb.RestoreAction_RESTORE_ACTION_REVERT,
	service.RestoreActionRecreate: pb.RestoreAction_RESTORE_ACTION_RECREATE,
	service.RestoreActionDelete:   pb.RestoreAction_RESTORE_ACTION_DELETE,
}

// restoreChangesToProto converts a slice of service RestoreKeyChange to
// their protobuf representation.
func restoreChangesToProto(changes []service.RestoreKeyChange) []*pb.RestoreKeyChange {
	out := make([]*pb.RestoreKeyChange, len(changes))
	for i, c := range changes {
		out[i] = &pb.RestoreKeyChange{
			Key:          []byte(c.Key),
			Action:       protoRestoreActionMap[c.Action],
			FromRevision: c.FromRevision,
			ToRevision:   c.ToRevision,
			ValueBytes:   c.ValueBytes,
		}
	}
	return out
}

// handlePutWithLease stores a key-value pair with an attached lease through
// the MVCC service layer. When the request carries a non-UNSPECIFIED
// WriteMode, PutWithLeaseAndMode is used; otherwise the plain PutWithLease
// method is called. The lease must have been previously granted; when it
// expires the key is automatically deleted.
func (s *Server) handlePutWithLease(ctx context.Context, payload []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.PutWithLeaseRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	var (
		result *service.KeyValue
		err    error
	)
	if mode, ok := writeModeFromWire(req.GetWriteMode()); ok {
		result, err = mvccSvc.PutWithLeaseAndMode(ctx, string(req.GetKey()), req.GetValue(), req.GetLeaseId(), mode)
	} else {
		result, err = mvccSvc.PutWithLease(ctx, string(req.GetKey()), req.GetValue(), req.GetLeaseId())
	}
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.PutWithLeaseResponse{Kv: kvToProto(result)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleGetRevision retrieves a key-value pair with full MVCC revision
// metadata at the latest committed revision. When the request carries a
// non-UNSPECIFIED ConsistencyLevel, GetRevisionWithConsistency is used;
// otherwise the plain GetRevision method is called.
func (s *Server) handleGetRevision(ctx context.Context, payload []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.GetRevisionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	var (
		result *service.KeyValue
		err    error
	)
	if opts, ok := readOptsFromWire(req.GetConsistency()); ok {
		result, err = mvccSvc.GetRevisionWithConsistency(ctx, string(req.GetKey()), opts)
	} else {
		result, err = mvccSvc.GetRevision(ctx, string(req.GetKey()))
	}
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.GetRevisionResponse{Kv: kvToProto(result)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleGetAtRevision retrieves a key-value pair as it existed at a
// specific historical MVCC revision. When the request carries a
// non-UNSPECIFIED ConsistencyLevel, GetAtRevisionWithConsistency is used;
// otherwise the plain GetAtRevision method is called.
func (s *Server) handleGetAtRevision(ctx context.Context, payload []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.GetAtRevisionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	var (
		result *service.KeyValue
		err    error
	)
	if opts, ok := readOptsFromWire(req.GetConsistency()); ok {
		result, err = mvccSvc.GetAtRevisionWithConsistency(ctx, string(req.GetKey()), req.GetRevision(), opts)
	} else {
		result, err = mvccSvc.GetAtRevision(ctx, string(req.GetKey()), req.GetRevision())
	}
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.GetAtRevisionResponse{Kv: kvToProto(result)})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRange returns key-value pairs in the half-open interval [start, end).
// It supports limit, revision, count-only, keys-only, and sort options
// matching the gRPC Range RPC semantics. When the request carries a
// non-UNSPECIFIED ConsistencyLevel, RangeWithConsistency is used; otherwise
// the plain Range method is called.
func (s *Server) handleRange(ctx context.Context, payload []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.RangeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	// An absent range start must be rejected rather than forwarded: a nil lower
	// bound otherwise denotes the start of the shared store, which would scan
	// across the (tenant, db) namespace boundary. This matches the REST handler,
	// which rejects an empty start with 400.
	if len(req.GetStart()) == 0 {
		return nil, &InvalidArgumentError{Reason: "start is required"}
	}

	var opts []service.RangeOption

	if req.GetLimit() > 0 {
		opts = append(opts, service.WithLimit(req.GetLimit()))
	}
	if req.GetRevision() > 0 {
		opts = append(opts, service.WithRevision(req.GetRevision()))
	}
	if req.GetCountOnly() {
		opts = append(opts, service.WithCountOnly())
	}
	if req.GetKeysOnly() {
		opts = append(opts, service.WithKeysOnly())
	}
	if req.GetSortOrder() != pb.SortOrder_SORT_NONE || req.GetSortTarget() != pb.SortTarget_SORT_BY_KEY {
		order := protoSortOrderMap[req.GetSortOrder()]
		target := protoSortTargetMap[req.GetSortTarget()]
		opts = append(opts, service.WithSort(order, target))
	}

	var (
		kvs      []*service.KeyValue
		count    int64
		revision int64
		err      error
	)
	if ropts, ok := readOptsFromWire(req.GetConsistency()); ok {
		kvs, count, revision, err = mvccSvc.RangeWithConsistency(ctx, req.GetStart(), req.GetEnd(), ropts, opts...)
	} else {
		kvs, count, revision, err = mvccSvc.Range(ctx, req.GetStart(), req.GetEnd(), opts...)
	}
	if err != nil {
		return nil, err
	}

	pbKvs := make([]*pb.KeyValue, len(kvs))
	for i, k := range kvs {
		pbKvs[i] = kvToProto(k)
	}

	resp, err := proto.Marshal(&pb.RangeResponse{Kvs: pbKvs, Count: count, Revision: revision})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCompact discards all MVCC revisions older than the specified
// revision, reclaiming storage space. Compacted revisions can no longer
// be read via GetAtRevision.
func (s *Server) handleCompact(ctx context.Context, payload []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.CompactRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := mvccSvc.Compact(ctx, req.GetRevision()); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleCurrentRevision returns the latest committed MVCC revision number.
// This handler accepts no payload.
func (s *Server) handleCurrentRevision(ctx context.Context, _ []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	rev, err := mvccSvc.CurrentRevision(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CurrentRevisionResponse{Revision: rev})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleRestoreToRevision returns a bounded set of keys to the value they
// held at an earlier MVCC revision by proposing new writes through the
// service layer -- a logical undo, never a Raft rewind.
func (s *Server) handleRestoreToRevision(ctx context.Context, payload []byte) ([]byte, error) {
	mvccSvc, svcErr := s.mvccSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var req pb.RestoreRevisionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	keys := make([]string, len(req.GetKeys()))
	for i, k := range req.GetKeys() {
		keys[i] = string(k)
	}

	result, err := mvccSvc.RestoreToRevision(ctx, &service.RestoreRevisionRequest{
		Revision: req.GetRevision(),
		Prefix:   string(req.GetPrefix()),
		Keys:     keys,
		DryRun:   req.GetDryRun(),
		Force:    req.GetForce(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.RestoreRevisionResponse{
		Revision:   result.Revision,
		Changes:    restoreChangesToProto(result.Changes),
		Conflicts:  restoreChangesToProto(result.Conflicts),
		Applied:    int64(result.Applied),
		Skipped:    int64(result.Skipped),
		Conflicted: int64(result.Conflicted),
		DryRun:     result.DryRun,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
