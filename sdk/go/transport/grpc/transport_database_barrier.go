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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealDatabase seals a database's encryption barrier via the gRPC DatabaseSeal RPC.
func (t *Transport) SealDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.DatabaseSeal(ctx, &pb.DatabaseSealRequest{Name: name}); err != nil {
		return mapGRPCError("SealDatabase", err)
	}
	return nil
}

// UnsealDatabase unseals a database's encryption barrier via the gRPC DatabaseUnseal RPC.
func (t *Transport) UnsealDatabase(ctx context.Context, name, secret string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.DatabaseUnseal(ctx, &pb.DatabaseUnsealRequest{Name: name, Secret: secret}); err != nil {
		return mapGRPCError("UnsealDatabase", err)
	}
	return nil
}

// DatabaseBarrierStatus returns a database barrier's status via the gRPC RPC.
func (t *Transport) DatabaseBarrierStatus(ctx context.Context, name string) (*transport.DatabaseBarrierStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.DatabaseBarrierStatus(ctx, &pb.DatabaseBarrierStatusRequest{Name: name})
	if err != nil {
		return nil, mapGRPCError("DatabaseBarrierStatus", err)
	}
	return &transport.DatabaseBarrierStatus{
		Sealed:         resp.GetSealed(),
		Strategy:       resp.GetStrategy(),
		HardwareBacked: resp.GetHardwareBacked(),
		InitializedAt:  time.Unix(resp.GetInitializedAtUnix(), 0),
		ClusterSeal:    sealconv.FromProto(resp.GetClusterSeal()),
	}, nil
}
