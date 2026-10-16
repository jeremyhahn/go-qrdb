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

// Package rebalanceconv converts a rebalance execution result from its
// protobuf wire form, shared by the gRPC, QUIC and Unix-socket transports so
// each reports it alike.
package rebalanceconv

import (
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ResultFromProto converts resp to the transport's result. A nil resp
// converts to a zero result, as its getters read.
func ResultFromProto(resp *pb.RebalanceExecutionResponse) *transport.RebalanceResult {
	r := &transport.RebalanceResult{
		MovesCompleted: int(resp.GetMovesCompleted()),
		MovesFailed:    int(resp.GetMovesFailed()),
		Duration:       time.Duration(resp.GetDurationNs()),
		FinalImbalance: resp.GetFinalImbalance(),
	}
	if fs := resp.GetFailures(); len(fs) > 0 {
		r.Failures = make([]transport.RebalanceMoveFailure, len(fs))
		for i, f := range fs {
			r.Failures[i] = transport.RebalanceMoveFailure{
				ShardID:  f.GetShardId(),
				FromNode: f.GetFromNode(),
				ToNode:   f.GetToNode(),
				Step:     f.GetStep(),
				Error:    f.GetError(),
			}
		}
	}
	return r
}
