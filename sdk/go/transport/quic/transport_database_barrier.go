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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"
)

// SealDatabase seals a database's encryption barrier via the QUIC db.Seal handler.
func (t *Transport) SealDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.DatabaseSealRequest{Name: name})
	if err != nil {
		return &transport.RequestError{Method: "SealDatabase", Err: err}
	}
	if _, err := t.call(ctx, "db.Seal", payload); err != nil {
		return &transport.RequestError{Method: "SealDatabase", Err: err}
	}
	return nil
}

// UnsealDatabase unseals a database's encryption barrier via the QUIC db.Unseal handler.
func (t *Transport) UnsealDatabase(ctx context.Context, name, secret string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.DatabaseUnsealRequest{Name: name, Secret: secret})
	if err != nil {
		return &transport.RequestError{Method: "UnsealDatabase", Err: err}
	}
	if _, err := t.call(ctx, "db.Unseal", payload); err != nil {
		return &transport.RequestError{Method: "UnsealDatabase", Err: err}
	}
	return nil
}

// DatabaseBarrierStatus returns a database barrier's status via the QUIC
// db.BarrierStatus handler.
func (t *Transport) DatabaseBarrierStatus(ctx context.Context, name string) (*transport.DatabaseBarrierStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: name})
	if err != nil {
		return nil, &transport.RequestError{Method: "DatabaseBarrierStatus", Err: err}
	}
	respBytes, err := t.call(ctx, "db.BarrierStatus", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "DatabaseBarrierStatus", Err: err}
	}
	var resp pb.DatabaseBarrierStatusResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &transport.RequestError{Method: "DatabaseBarrierStatus", Err: err}
	}
	return &transport.DatabaseBarrierStatus{
		Sealed:         resp.GetSealed(),
		Strategy:       resp.GetStrategy(),
		HardwareBacked: resp.GetHardwareBacked(),
		InitializedAt:  time.Unix(resp.GetInitializedAtUnix(), 0),
		ClusterSeal:    sealconv.FromProto(resp.GetClusterSeal()),
	}, nil
}
