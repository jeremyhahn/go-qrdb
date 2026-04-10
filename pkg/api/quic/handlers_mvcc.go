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

// handlePutWithLease stores a key-value pair with an attached lease through
// the MVCC service layer. The lease must have been previously granted; when
// it expires the key is automatically deleted.
func (s *Server) handlePutWithLease(ctx context.Context, payload []byte) ([]byte, error) {
	if s.mvccSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	var req pb.PutWithLeaseRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.mvccSvc.PutWithLease(ctx, string(req.GetKey()), req.GetValue(), req.GetLeaseId())
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
// metadata at the latest committed revision.
func (s *Server) handleGetRevision(ctx context.Context, payload []byte) ([]byte, error) {
	if s.mvccSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	var req pb.GetRevisionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.mvccSvc.GetRevision(ctx, string(req.GetKey()))
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
// specific historical MVCC revision.
func (s *Server) handleGetAtRevision(ctx context.Context, payload []byte) ([]byte, error) {
	if s.mvccSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	var req pb.GetAtRevisionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.mvccSvc.GetAtRevision(ctx, string(req.GetKey()), req.GetRevision())
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
// matching the gRPC Range RPC semantics.
func (s *Server) handleRange(ctx context.Context, payload []byte) ([]byte, error) {
	if s.mvccSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	var req pb.RangeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
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
	if req.GetSortOrder() != pb.SortOrder_SORT_NONE {
		order := protoSortOrderMap[req.GetSortOrder()]
		target := protoSortTargetMap[req.GetSortTarget()]
		opts = append(opts, service.WithSort(order, target))
	}

	kvs, count, err := s.mvccSvc.Range(ctx, req.GetStart(), req.GetEnd(), opts...)
	if err != nil {
		return nil, err
	}

	pbKvs := make([]*pb.KeyValue, len(kvs))
	for i, k := range kvs {
		pbKvs[i] = kvToProto(k)
	}

	resp, err := proto.Marshal(&pb.RangeResponse{Kvs: pbKvs, Count: count})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleCompact discards all MVCC revisions older than the specified
// revision, reclaiming storage space. Compacted revisions can no longer
// be read via GetAtRevision.
func (s *Server) handleCompact(ctx context.Context, payload []byte) ([]byte, error) {
	if s.mvccSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	var req pb.CompactRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.mvccSvc.Compact(ctx, req.GetRevision()); err != nil {
		return nil, err
	}

	return []byte{}, nil
}

// handleCurrentRevision returns the latest committed MVCC revision number.
// This handler accepts no payload.
func (s *Server) handleCurrentRevision(ctx context.Context, _ []byte) ([]byte, error) {
	if s.mvccSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "mvcc"}
	}

	rev, err := s.mvccSvc.CurrentRevision(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.CurrentRevisionResponse{Revision: rev})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
