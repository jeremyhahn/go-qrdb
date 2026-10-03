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
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// ClusterSealToProto converts a barrier's cluster seal state to its wire
// form. A nil report is the inactive state. QUIC frames reuse it.
func ClusterSealToProto(r *clusterseal.Report) *pb.ClusterSealState {
	if r == nil {
		return &pb.ClusterSealState{}
	}
	out := &pb.ClusterSealState{
		Active:          r.Active,
		Sealed:          r.Sealed,
		Epoch:           r.Epoch,
		Principal:       r.Principal,
		PrincipalTenant: r.PrincipalTenant,
		Node:            r.Node,
		Local:           string(r.Local),
	}
	if !r.At.IsZero() {
		out.At = r.At.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// BarrierClusterUnseal clears a cluster-wide seal of the node barrier. It
// unseals nothing; the node must already have unsealed itself.
func (s *qrdbServiceServer) BarrierClusterUnseal(ctx context.Context, _ *pb.BarrierClusterUnsealRequest) (*pb.ClusterSealState, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}
	report, err := s.barrierSvc.BarrierClusterUnseal(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return ClusterSealToProto(report), nil
}
