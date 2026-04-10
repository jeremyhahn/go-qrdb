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

package etcd

import (
	"bytes"
	"context"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// kvServer implements etcdserverpb.KVServer by delegating to qrdb's
// MVCC and transaction service layers.
type kvServer struct {
	pb.UnimplementedKVServer
	mvccSvc   service.MVCCKVServicer
	txSvc     service.TransactionServicer
	clusterID uint64
	memberID  uint64
}

// Range handles etcd Range (get/list/scan) requests.
func (s *kvServer) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	if len(req.Key) == 0 {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}

	rev, _ := s.mvccSvc.CurrentRevision(ctx)

	// Single-key get: no RangeEnd.
	if len(req.RangeEnd) == 0 {
		kv, err := s.mvccSvc.GetRevision(ctx, string(req.Key))
		if err != nil {
			// Key not found returns an empty result, not an error in etcd semantics.
			return &pb.RangeResponse{
				Header: responseHeader(s.clusterID, s.memberID, rev, 0),
				Count:  0,
			}, nil
		}
		return &pb.RangeResponse{
			Header: responseHeader(s.clusterID, s.memberID, rev, 0),
			Kvs:    kvsToEtcd([]*service.KeyValue{kv}),
			Count:  1,
		}, nil
	}

	// Build range options.
	var opts []service.RangeOption
	if req.Limit > 0 {
		opts = append(opts, service.WithLimit(req.Limit))
	}
	if req.Revision > 0 {
		opts = append(opts, service.WithRevision(req.Revision))
	}
	if req.CountOnly {
		opts = append(opts, service.WithCountOnly())
	}
	if req.KeysOnly {
		opts = append(opts, service.WithKeysOnly())
	}

	// Determine start/end keys.
	start := req.Key
	end := req.RangeEnd

	// Handle all-keys range: key="\x00", rangeEnd="\x00".
	if isAllKeys(start, end) {
		start = []byte{0}
		end = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	}

	kvs, count, err := s.mvccSvc.Range(ctx, start, end, opts...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "range: %v", err)
	}

	return &pb.RangeResponse{
		Header: responseHeader(s.clusterID, s.memberID, rev, 0),
		Kvs:    kvsToEtcd(kvs),
		Count:  count,
	}, nil
}

// Put handles etcd Put requests.
func (s *kvServer) Put(ctx context.Context, req *pb.PutRequest) (*pb.PutResponse, error) {
	if len(req.Key) == 0 {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}

	// Get previous key-value if requested.
	var prevKV *service.KeyValue
	if req.PrevKv {
		prevKV, _ = s.mvccSvc.GetRevision(ctx, string(req.Key))
	}

	// Use PutWithLease for all puts (lease 0 = no lease).
	kv, err := s.mvccSvc.PutWithLease(ctx, string(req.Key), req.Value, req.Lease)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "put: %v", err)
	}

	rev := kv.ModRevision

	resp := &pb.PutResponse{
		Header: responseHeader(s.clusterID, s.memberID, rev, 0),
	}
	if req.PrevKv && prevKV != nil {
		resp.PrevKv = kvToEtcd(prevKV)
	}

	return resp, nil
}

// DeleteRange handles etcd DeleteRange requests.
func (s *kvServer) DeleteRange(ctx context.Context, req *pb.DeleteRangeRequest) (*pb.DeleteRangeResponse, error) {
	if len(req.Key) == 0 {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}

	// Single key delete.
	if len(req.RangeEnd) == 0 {
		var prevKV *service.KeyValue
		if req.PrevKv {
			prevKV, _ = s.mvccSvc.GetRevision(ctx, string(req.Key))
		}

		err := s.mvccSvc.Delete(ctx, string(req.Key))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "delete: %v", err)
		}

		rev, _ := s.mvccSvc.CurrentRevision(ctx)
		resp := &pb.DeleteRangeResponse{
			Header:  responseHeader(s.clusterID, s.memberID, rev, 0),
			Deleted: 1,
		}
		if req.PrevKv && prevKV != nil {
			resp.PrevKvs = kvsToEtcd([]*service.KeyValue{prevKV})
		}
		return resp, nil
	}

	// Range delete: first get all keys in range, then delete each.
	kvs, _, err := s.mvccSvc.Range(ctx, req.Key, req.RangeEnd)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "delete range scan: %v", err)
	}

	var prevKvs []*service.KeyValue
	if req.PrevKv {
		prevKvs = kvs
	}

	for _, kv := range kvs {
		if err := s.mvccSvc.Delete(ctx, string(kv.Key)); err != nil {
			return nil, status.Errorf(codes.Internal, "delete range: %v", err)
		}
	}

	rev, _ := s.mvccSvc.CurrentRevision(ctx)
	resp := &pb.DeleteRangeResponse{
		Header:  responseHeader(s.clusterID, s.memberID, rev, 0),
		Deleted: int64(len(kvs)),
	}
	if req.PrevKv {
		resp.PrevKvs = kvsToEtcd(prevKvs)
	}
	return resp, nil
}

// Txn handles etcd Txn (transaction) requests.
func (s *kvServer) Txn(ctx context.Context, req *pb.TxnRequest) (*pb.TxnResponse, error) {
	if s.txSvc == nil {
		return nil, status.Error(codes.Unimplemented, "transactions not available")
	}

	// Evaluate compares locally to determine which branch to execute.
	// etcd evaluates compares server-side, then applies success or failure ops.
	succeeded := true
	for _, cmp := range req.Compare {
		kv, err := s.mvccSvc.GetRevision(ctx, string(cmp.Key))
		if err != nil {
			// Key not found: comparison fails for most operations.
			succeeded = false
			break
		}
		if !evaluateCompare(cmp, kv) {
			succeeded = false
			break
		}
	}

	var ops []*pb.RequestOp
	if succeeded {
		ops = req.Success
	} else {
		ops = req.Failure
	}

	// Execute the selected branch.
	var responses []*pb.ResponseOp
	for _, op := range ops {
		respOp, err := s.executeOp(ctx, op)
		if err != nil {
			return nil, err
		}
		responses = append(responses, respOp)
	}

	rev, _ := s.mvccSvc.CurrentRevision(ctx)
	return &pb.TxnResponse{
		Header:    responseHeader(s.clusterID, s.memberID, rev, 0),
		Succeeded: succeeded,
		Responses: responses,
	}, nil
}

// Compact handles etcd Compact requests.
func (s *kvServer) Compact(ctx context.Context, req *pb.CompactionRequest) (*pb.CompactionResponse, error) {
	if err := s.mvccSvc.Compact(ctx, req.Revision); err != nil {
		return nil, status.Errorf(codes.Internal, "compact: %v", err)
	}

	return &pb.CompactionResponse{
		Header: responseHeader(s.clusterID, s.memberID, req.Revision, 0),
	}, nil
}

// executeOp executes a single RequestOp and returns the corresponding ResponseOp.
func (s *kvServer) executeOp(ctx context.Context, op *pb.RequestOp) (*pb.ResponseOp, error) {
	switch o := op.Request.(type) {
	case *pb.RequestOp_RequestRange:
		resp, err := s.Range(ctx, o.RequestRange)
		if err != nil {
			return nil, err
		}
		return &pb.ResponseOp{Response: &pb.ResponseOp_ResponseRange{ResponseRange: resp}}, nil
	case *pb.RequestOp_RequestPut:
		resp, err := s.Put(ctx, o.RequestPut)
		if err != nil {
			return nil, err
		}
		return &pb.ResponseOp{Response: &pb.ResponseOp_ResponsePut{ResponsePut: resp}}, nil
	case *pb.RequestOp_RequestDeleteRange:
		resp, err := s.DeleteRange(ctx, o.RequestDeleteRange)
		if err != nil {
			return nil, err
		}
		return &pb.ResponseOp{Response: &pb.ResponseOp_ResponseDeleteRange{ResponseDeleteRange: resp}}, nil
	default:
		return nil, status.Error(codes.Unimplemented, "unknown operation type")
	}
}

// evaluateCompare evaluates a single etcd Compare against a key-value.
func evaluateCompare(cmp *pb.Compare, kv *service.KeyValue) bool {
	var result int

	switch cmp.Target {
	case pb.Compare_VERSION:
		result = compareInt64(kv.Version, cmp.GetVersion())
	case pb.Compare_CREATE:
		result = compareInt64(kv.CreateRevision, cmp.GetCreateRevision())
	case pb.Compare_MOD:
		result = compareInt64(kv.ModRevision, cmp.GetModRevision())
	case pb.Compare_VALUE:
		result = bytes.Compare(kv.Value, cmp.GetValue())
	default:
		return false
	}

	switch cmp.Result {
	case pb.Compare_EQUAL:
		return result == 0
	case pb.Compare_GREATER:
		return result > 0
	case pb.Compare_LESS:
		return result < 0
	case pb.Compare_NOT_EQUAL:
		return result != 0
	default:
		return false
	}
}

// compareInt64 returns -1, 0, or 1.
func compareInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
