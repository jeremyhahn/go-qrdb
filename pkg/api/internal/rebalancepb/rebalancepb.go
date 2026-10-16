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

// Package rebalancepb converts a rebalance execution result to its protobuf
// wire form, shared by the gRPC and QUIC servers so both report it alike.
package rebalancepb

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ExecutionToProto converts r to its wire form. A nil r converts to nil.
func ExecutionToProto(r *service.RebalanceExecutionResult) *pb.RebalanceExecutionResponse {
	if r == nil {
		return nil
	}
	resp := &pb.RebalanceExecutionResponse{
		MovesCompleted: int32(r.MovesCompleted),
		MovesFailed:    int32(r.MovesFailed),
		DurationNs:     int64(r.Duration),
		FinalImbalance: r.FinalImbalance,
	}
	if len(r.Failures) > 0 {
		resp.Failures = make([]*pb.RebalanceMoveFailure, len(r.Failures))
		for i, f := range r.Failures {
			resp.Failures[i] = &pb.RebalanceMoveFailure{
				ShardId:  f.ShardID,
				FromNode: f.FromNode,
				ToNode:   f.ToNode,
				Step:     f.Step,
				Error:    f.Error,
			}
		}
	}
	return resp
}
