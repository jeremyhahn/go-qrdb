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

// Package sealconv converts a barrier's cluster seal state into the
// transport-neutral transport.ClusterSeal: from the protobuf the gRPC, Unix
// and QUIC transports exchange, and from the service report the embedded
// transport holds. It is internal: only the transport implementations may
// import it.
package sealconv

import (
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// FromProto converts a protobuf ClusterSealState; nil is the inactive state.
func FromProto(s *pb.ClusterSealState) *transport.ClusterSeal {
	if s == nil {
		return &transport.ClusterSeal{}
	}
	return &transport.ClusterSeal{
		Active:          s.GetActive(),
		Sealed:          s.GetSealed(),
		Epoch:           s.GetEpoch(),
		Principal:       s.GetPrincipal(),
		PrincipalTenant: s.GetPrincipalTenant(),
		Node:            s.GetNode(),
		At:              s.GetAt(),
		Local:           s.GetLocal(),
	}
}

// FromReport converts a service cluster seal report; nil is the inactive
// state.
func FromReport(r *clusterseal.Report) *transport.ClusterSeal {
	if r == nil {
		return &transport.ClusterSeal{}
	}
	out := &transport.ClusterSeal{
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
