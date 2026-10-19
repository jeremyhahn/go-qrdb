// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
)

// ClusterMigrationToProto converts a barrier's cluster migration report to
// its wire form, shared by the gRPC and native QUIC servers. nil, no
// migration in progress, stays nil: the field is absent.
func ClusterMigrationToProto(r *clustermig.Report) *pb.ClusterMigrationState {
	if r == nil {
		return nil
	}
	out := &pb.ClusterMigrationState{
		Unconfirmed: r.Unconfirmed,
		Target:      string(r.Target),
		Generation:  r.Generation,
		Acked:       int32(r.Acked),
		Total:       int32(r.Total),
	}
	for _, n := range r.Nodes {
		out.Nodes = append(out.Nodes, &pb.ClusterMigrationNode{
			NodeId:   n.NodeID,
			Step:     string(n.Step),
			Phase:    string(n.Phase),
			Attempts: int32(n.Attempts),
			Detail:   n.Detail,
		})
	}
	return out
}
