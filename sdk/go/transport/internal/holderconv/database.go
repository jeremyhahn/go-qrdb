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

package holderconv

import (
	"context"

	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// DatabaseHolderRPC is the slice of the QRDBService client the database
// share-holder calls make. pb.QRDBServiceClient satisfies it, which is what
// the gRPC and Unix transports bring; the QUIC transport satisfies it by
// framing the same messages to its db.Barrier* handlers.
type DatabaseHolderRPC interface {
	DatabaseBarrierOfficerRegister(ctx context.Context, in *pb.DatabaseBarrierOfficerRegisterRequest, opts ...grpc.CallOption) (*pb.BarrierOfficerRegisterResponse, error)
	DatabaseBarrierOfficerList(ctx context.Context, in *pb.DatabaseBarrierOfficerListRequest, opts ...grpc.CallOption) (*pb.BarrierOfficerListResponse, error)
	DatabaseBarrierShamirInitialize(ctx context.Context, in *pb.DatabaseBarrierShamirInitializeRequest, opts ...grpc.CallOption) (*pb.BarrierShamirOperationResponse, error)
	DatabaseBarrierShareClaimBegin(ctx context.Context, in *pb.DatabaseBarrierShareClaimBeginRequest, opts ...grpc.CallOption) (*pb.BarrierShareClaimBeginResponse, error)
	DatabaseBarrierShareClaimComplete(ctx context.Context, in *pb.DatabaseBarrierShareClaimCompleteRequest, opts ...grpc.CallOption) (*pb.BarrierShareClaimCompleteResponse, error)
	DatabaseBarrierQuorumBegin(ctx context.Context, in *pb.DatabaseBarrierQuorumBeginRequest, opts ...grpc.CallOption) (*pb.BarrierQuorumChallenge, error)
	DatabaseBarrierQuorumSubmit(ctx context.Context, in *pb.DatabaseBarrierQuorumSubmitRequest, opts ...grpc.CallOption) (*pb.BarrierQuorumOutcome, error)
	DatabaseBarrierRekeyStart(ctx context.Context, in *pb.DatabaseBarrierRekeyStartRequest, opts ...grpc.CallOption) (*pb.BarrierQuorumRound, error)
	DatabaseBarrierOfficerRevoke(ctx context.Context, in *pb.DatabaseBarrierOfficerRevokeRequest, opts ...grpc.CallOption) (*pb.BarrierQuorumRound, error)
}

var _ DatabaseHolderRPC = pb.QRDBServiceClient(nil)

// DatabaseHolders makes the database share-holder calls. The gRPC, Unix and
// QUIC transports carry the same protobuf messages and differ only in the
// client and the error mapping they bring, so the calls live here once.
type DatabaseHolders struct {
	// Client carries the calls.
	Client DatabaseHolderRPC
	// MapErr maps a gRPC error from method into the transport's errors.
	MapErr func(method string, err error) error
}

// Register enrols a share holder of a database's barrier.
func (d DatabaseHolders) Register(ctx context.Context, req *transport.DatabaseOfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRegister"}
	}
	officer, err := RegisterToPB(&req.OfficerRegisterRequest)
	if err != nil {
		return nil, err
	}
	resp, err := d.Client.DatabaseBarrierOfficerRegister(ctx, &pb.DatabaseBarrierOfficerRegisterRequest{Database: req.Database, Officer: officer})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierOfficerRegister", err)
	}
	return RegisterFromPB(resp), nil
}

// List lists a database barrier's holders.
func (d DatabaseHolders) List(ctx context.Context, name string) ([]*transport.OfficerStatus, error) {
	if name == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerList"}
	}
	resp, err := d.Client.DatabaseBarrierOfficerList(ctx, &pb.DatabaseBarrierOfficerListRequest{Database: name})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierOfficerList", err)
	}
	return StatusesFromPB(resp.GetOfficers()), nil
}

// Initialize splits a database's root key to its enrolled holders.
func (d DatabaseHolders) Initialize(ctx context.Context, req *transport.DatabaseShamirInitializeRequest) (*transport.BarrierInitializeShamirResponse, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShamirInitialize"}
	}
	resp, err := d.Client.DatabaseBarrierShamirInitialize(ctx, &pb.DatabaseBarrierShamirInitializeRequest{Database: req.Database, Policy: req.Policy})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierShamirInitialize", err)
	}
	return &transport.BarrierInitializeShamirResponse{
		Threshold:   int(resp.GetThreshold()),
		TotalShares: int(resp.GetTotalShares()),
		ShareCount:  int(resp.GetShareCount()),
	}, nil
}

// ClaimBegin issues a claim nonce to a database holder.
func (d DatabaseHolders) ClaimBegin(ctx context.Context, req *transport.DatabaseShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimBegin"}
	}
	resp, err := d.Client.DatabaseBarrierShareClaimBegin(ctx, &pb.DatabaseBarrierShareClaimBeginRequest{
		Database: req.Database,
		Claim:    &pb.BarrierShareClaimBeginRequest{Username: req.Username},
	})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierShareClaimBegin", err)
	}
	return ClaimBeginFromPB(resp.GetNonceHex(), resp.GetExpiresAt()), nil
}

// ClaimComplete delivers a database holder's envelope exactly once.
func (d DatabaseHolders) ClaimComplete(ctx context.Context, req *transport.DatabaseShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierShareClaimComplete"}
	}
	resp, err := d.Client.DatabaseBarrierShareClaimComplete(ctx, &pb.DatabaseBarrierShareClaimCompleteRequest{
		Database: req.Database,
		Claim:    &pb.BarrierShareClaimCompleteRequest{Username: req.Username, NonceHex: req.NonceHex, Signature: req.Signature},
	})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierShareClaimComplete", err)
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: int(resp.GetShareIndex()), Envelope: resp.GetEnvelope()}, nil
}

// QuorumBegin issues a database holder's quorum challenge.
func (d DatabaseHolders) QuorumBegin(ctx context.Context, req *transport.DatabaseQuorumBeginRequest) (*transport.QuorumChallenge, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumBegin"}
	}
	resp, err := d.Client.DatabaseBarrierQuorumBegin(ctx, &pb.DatabaseBarrierQuorumBeginRequest{Database: req.Database, Begin: BeginToPB(&req.QuorumBeginRequest)})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierQuorumBegin", err)
	}
	return ChallengeFromPB(resp), nil
}

// QuorumSubmit submits a database holder's share.
func (d DatabaseHolders) QuorumSubmit(ctx context.Context, req *transport.DatabaseQuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierQuorumSubmit"}
	}
	resp, err := d.Client.DatabaseBarrierQuorumSubmit(ctx, &pb.DatabaseBarrierQuorumSubmitRequest{Database: req.Database, Submit: SubmitToPB(&req.QuorumSubmitRequest)})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierQuorumSubmit", err)
	}
	return OutcomeFromPB(resp), nil
}

// RekeyStart opens a rekey round on a database barrier.
func (d DatabaseHolders) RekeyStart(ctx context.Context, req *transport.DatabaseRekeyStartRequest) (*transport.QuorumRound, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierRekeyStart"}
	}
	resp, err := d.Client.DatabaseBarrierRekeyStart(ctx, &pb.DatabaseBarrierRekeyStartRequest{
		Database: req.Database, Threshold: int32(req.Threshold), Policy: req.Policy, //nolint:gosec
	})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierRekeyStart", err)
	}
	return RoundFromPB(resp), nil
}

// Revoke revokes a database holder.
func (d DatabaseHolders) Revoke(ctx context.Context, req *transport.DatabaseOfficerRevokeRequest) (*transport.QuorumRound, error) {
	if req == nil || req.Database == "" {
		return nil, &transport.DatabaseNameRequiredError{Method: "DatabaseBarrierOfficerRevoke"}
	}
	resp, err := d.Client.DatabaseBarrierOfficerRevoke(ctx, &pb.DatabaseBarrierOfficerRevokeRequest{
		Database: req.Database, Username: req.Username, Threshold: int32(req.Threshold), //nolint:gosec
	})
	if err != nil {
		return nil, d.MapErr("DatabaseBarrierOfficerRevoke", err)
	}
	return RoundFromPB(resp), nil
}
