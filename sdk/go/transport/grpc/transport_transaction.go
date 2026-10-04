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

package grpc

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TransactionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Enum conversion maps
// ---------------------------------------------------------------------------

// sdkCompareTargetToProto converts transport CompareTarget to proto
// CompareTarget.
var sdkCompareTargetToProto = map[transport.CompareTarget]pb.CompareTarget{
	transport.CompareVersion:        pb.CompareTarget_COMPARE_VERSION,
	transport.CompareCreateRevision: pb.CompareTarget_COMPARE_CREATE_REVISION,
	transport.CompareModRevision:    pb.CompareTarget_COMPARE_MOD_REVISION,
	transport.CompareValue:          pb.CompareTarget_COMPARE_VALUE,
}

// sdkCompareResultToProto converts transport CompareResult to proto
// CompareResult.
var sdkCompareResultToProto = map[transport.CompareResult]pb.CompareResult{
	transport.CompareEqual:    pb.CompareResult_COMPARE_EQUAL,
	transport.CompareGreater:  pb.CompareResult_COMPARE_GREATER,
	transport.CompareLess:     pb.CompareResult_COMPARE_LESS,
	transport.CompareNotEqual: pb.CompareResult_COMPARE_NOT_EQUAL,
}

// sdkOpTypeToProto converts transport OpType to proto OpType.
var sdkOpTypeToProto = map[transport.OpType]pb.OpType{
	transport.OpPut:    pb.OpType_OP_PUT,
	transport.OpGet:    pb.OpType_OP_GET,
	transport.OpDelete: pb.OpType_OP_DELETE,
	transport.OpRange:  pb.OpType_OP_RANGE,
}

// ---------------------------------------------------------------------------
// TransactionClient implementation
// ---------------------------------------------------------------------------

// ExecuteTransaction atomically evaluates compares and executes the
// corresponding operation set through the gRPC transport. When all
// compares succeed, success ops are applied; otherwise failure ops
// are applied.
func (t *Transport) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ctx = applyWriteMetadata(ctx, opts...)

	pbCompares := make([]*pb.Compare, len(req.Compares))
	for i, c := range req.Compares {
		pbCompares[i] = sdkCompareToProto(c)
	}

	pbSuccessOps := make([]*pb.Op, len(req.SuccessOps))
	for i, op := range req.SuccessOps {
		pbSuccessOps[i] = sdkOpToProto(op)
	}

	pbFailureOps := make([]*pb.Op, len(req.FailureOps))
	for i, op := range req.FailureOps {
		pbFailureOps[i] = sdkOpToProto(op)
	}

	resp, err := t.client.Transaction(ctx, &pb.TransactionRequest{
		Compares:   pbCompares,
		SuccessOps: pbSuccessOps,
		FailureOps: pbFailureOps,
	})
	if err != nil {
		ops := append(append([]transport.Op{}, req.SuccessOps...), req.FailureOps...)
		return nil, mapOpsNotFoundError("ExecuteTransaction", ops, err)
	}

	responses := make([]transport.OpResponse, len(resp.GetResponses()))
	for i, r := range resp.GetResponses() {
		responses[i] = protoOpResponseToSDK(r)
	}

	return &transport.TransactionResponse{
		Succeeded: resp.GetSucceeded(),
		Revision:  resp.GetRevision(),
		Responses: responses,
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// sdkCompareToProto converts a transport Compare to a proto Compare,
// including the appropriate target_union oneof field based on the
// compare target type.
func sdkCompareToProto(c transport.Compare) *pb.Compare {
	pbCmp := &pb.Compare{
		Key:    c.Key,
		Target: sdkCompareTargetToProto[c.Target],
		Result: sdkCompareResultToProto[c.Result],
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

// sdkOpToProto converts a transport Op to a proto Op.
func sdkOpToProto(op transport.Op) *pb.Op {
	return &pb.Op{
		Type:     sdkOpTypeToProto[op.Type],
		Key:      op.Key,
		Value:    op.Value,
		RangeEnd: op.RangeEnd,
		LeaseId:  op.LeaseID,
	}
}

// protoOpResponseToSDK converts a proto OpResponse to a transport
// OpResponse, including MVCC key-value conversion for each result.
func protoOpResponseToSDK(r *pb.OpResponse) transport.OpResponse {
	kvs := make([]*transport.MVCCKeyValue, len(r.GetKvs()))
	for i, kv := range r.GetKvs() {
		kvs[i] = protoToMVCCKV(kv)
	}
	return transport.OpResponse{
		Kvs:       kvs,
		Count:     r.GetCount(),
		Succeeded: r.GetSucceeded(),
	}
}
