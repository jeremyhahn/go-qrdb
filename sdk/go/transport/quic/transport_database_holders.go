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

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/holderconv"
)

// Shamir custody of a database barrier (docs/databases/api.md). The QUIC
// db.Barrier* handlers carry the same protobuf messages as the gRPC RPCs, so
// the calls are holderconv.DatabaseHolders' over a frame-backed client.

// databaseHolderRPC frames the database share-holder messages to the QUIC
// db.Barrier* handlers.
type databaseHolderRPC struct {
	t *Transport
}

var _ holderconv.DatabaseHolderRPC = databaseHolderRPC{}

// callHolder makes one db.Barrier<verb> call, decoding the reply into a new
// Out.
func callHolder[Out any, PO interface {
	*Out
	proto.Message
}](ctx context.Context, t *Transport, verb string, in proto.Message) (PO, error) {
	out := PO(new(Out))
	if err := t.callPB(ctx, "db.Barrier"+verb, in, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DatabaseBarrierOfficerRegister frames the request to db.BarrierOfficerRegister.
func (r databaseHolderRPC) DatabaseBarrierOfficerRegister(ctx context.Context, in *pb.DatabaseBarrierOfficerRegisterRequest, _ ...grpc.CallOption) (*pb.BarrierOfficerRegisterResponse, error) {
	return callHolder[pb.BarrierOfficerRegisterResponse](ctx, r.t, "OfficerRegister", in)
}

// DatabaseBarrierOfficerList frames the request to db.BarrierOfficerList.
func (r databaseHolderRPC) DatabaseBarrierOfficerList(ctx context.Context, in *pb.DatabaseBarrierOfficerListRequest, _ ...grpc.CallOption) (*pb.BarrierOfficerListResponse, error) {
	return callHolder[pb.BarrierOfficerListResponse](ctx, r.t, "OfficerList", in)
}

// DatabaseBarrierShamirInitialize frames the request to db.BarrierShamirInitialize.
func (r databaseHolderRPC) DatabaseBarrierShamirInitialize(ctx context.Context, in *pb.DatabaseBarrierShamirInitializeRequest, _ ...grpc.CallOption) (*pb.BarrierShamirOperationResponse, error) {
	return callHolder[pb.BarrierShamirOperationResponse](ctx, r.t, "ShamirInitialize", in)
}

// DatabaseBarrierShareClaimBegin frames the request to db.BarrierShareClaimBegin.
func (r databaseHolderRPC) DatabaseBarrierShareClaimBegin(ctx context.Context, in *pb.DatabaseBarrierShareClaimBeginRequest, _ ...grpc.CallOption) (*pb.BarrierShareClaimBeginResponse, error) {
	return callHolder[pb.BarrierShareClaimBeginResponse](ctx, r.t, "ShareClaimBegin", in)
}

// DatabaseBarrierShareClaimComplete frames the request to db.BarrierShareClaimComplete.
func (r databaseHolderRPC) DatabaseBarrierShareClaimComplete(ctx context.Context, in *pb.DatabaseBarrierShareClaimCompleteRequest, _ ...grpc.CallOption) (*pb.BarrierShareClaimCompleteResponse, error) {
	return callHolder[pb.BarrierShareClaimCompleteResponse](ctx, r.t, "ShareClaimComplete", in)
}

// DatabaseBarrierQuorumBegin frames the request to db.BarrierQuorumBegin.
func (r databaseHolderRPC) DatabaseBarrierQuorumBegin(ctx context.Context, in *pb.DatabaseBarrierQuorumBeginRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumChallenge, error) {
	return callHolder[pb.BarrierQuorumChallenge](ctx, r.t, "QuorumBegin", in)
}

// DatabaseBarrierQuorumSubmit frames the request to db.BarrierQuorumSubmit.
func (r databaseHolderRPC) DatabaseBarrierQuorumSubmit(ctx context.Context, in *pb.DatabaseBarrierQuorumSubmitRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumOutcome, error) {
	return callHolder[pb.BarrierQuorumOutcome](ctx, r.t, "QuorumSubmit", in)
}

// DatabaseBarrierRekeyStart frames the request to db.BarrierRekeyStart.
func (r databaseHolderRPC) DatabaseBarrierRekeyStart(ctx context.Context, in *pb.DatabaseBarrierRekeyStartRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumRound, error) {
	return callHolder[pb.BarrierQuorumRound](ctx, r.t, "RekeyStart", in)
}

// DatabaseBarrierOfficerRevoke frames the request to db.BarrierOfficerRevoke.
func (r databaseHolderRPC) DatabaseBarrierOfficerRevoke(ctx context.Context, in *pb.DatabaseBarrierOfficerRevokeRequest, _ ...grpc.CallOption) (*pb.BarrierQuorumRound, error) {
	return callHolder[pb.BarrierQuorumRound](ctx, r.t, "OfficerRevoke", in)
}

// DatabaseBarrierOfficerUnenrol frames the request to db.BarrierOfficerUnenrol.
func (r databaseHolderRPC) DatabaseBarrierOfficerUnenrol(ctx context.Context, in *pb.DatabaseBarrierOfficerUnenrolRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return callHolder[emptypb.Empty](ctx, r.t, "OfficerUnenrol", in)
}

// passthroughError returns err unchanged: the QUIC client's errors are typed
// where they are raised.
func passthroughError(_ string, err error) error { return err }

// databaseHolders binds the database share-holder calls to this transport.
func (t *Transport) databaseHolders() holderconv.DatabaseHolders {
	return holderconv.DatabaseHolders{Client: databaseHolderRPC{t: t}, MapErr: passthroughError}
}

// DatabaseBarrierOfficerRegister enrols a share holder of a database's barrier.
func (t *Transport) DatabaseBarrierOfficerRegister(ctx context.Context, req *transport.DatabaseOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().Register(ctx, req)
}

// DatabaseBarrierOfficerList lists a database barrier's holders.
func (t *Transport) DatabaseBarrierOfficerList(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().List(ctx, name)
}

// DatabaseBarrierShamirInitialize splits a database's root key to its enrolled holders.
func (t *Transport) DatabaseBarrierShamirInitialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().Initialize(ctx, req)
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder presenting its certificate.
func (t *Transport) DatabaseBarrierShareClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().ClaimBegin(ctx, req)
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope exactly once.
func (t *Transport) DatabaseBarrierShareClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().ClaimComplete(ctx, req)
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
func (t *Transport) DatabaseBarrierQuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().QuorumBegin(ctx, req)
}

// DatabaseBarrierQuorumSubmit submits a database holder's share.
func (t *Transport) DatabaseBarrierQuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().QuorumSubmit(ctx, req)
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (t *Transport) DatabaseBarrierRekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().RekeyStart(ctx, req)
}

// DatabaseBarrierOfficerRevoke revokes a database holder.
func (t *Transport) DatabaseBarrierOfficerRevoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return t.databaseHolders().Revoke(ctx, req)
}

// DatabaseBarrierOfficerUnenrol removes a database holder before the
// database's custody is initialized.
func (t *Transport) DatabaseBarrierOfficerUnenrol(ctx context.Context, req *transport.DatabaseOfficerUnenrolRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.databaseHolders().Unenrol(ctx, req)
}
