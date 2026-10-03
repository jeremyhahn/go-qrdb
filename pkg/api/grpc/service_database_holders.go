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

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Shamir custody of a database barrier (docs/databases/api.md). Each RPC
// names the database and nests the system holder request; the database
// service confines it to a database in the caller's tenant.

// databaseHolders resolves the database service's share-holder custody.
func (s *qrdbServiceServer) databaseHolders() (service.DatabaseHolderServicer, error) {
	hs, ok := s.databaseSvc.(service.DatabaseHolderServicer)
	if !ok {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "database share holders"})
	}
	return hs, nil
}

// DatabaseBarrierOfficerRegister enrols a share holder of a database's
// barrier.
func (s *qrdbServiceServer) DatabaseBarrierOfficerRegister(ctx context.Context, req *pb.DatabaseBarrierOfficerRegisterRequest) (*pb.BarrierOfficerRegisterResponse, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	svcReq, err := holderpb.RegisterFromPB(req.GetOfficer())
	if err != nil {
		return nil, mapServiceError(err)
	}
	result, err := hs.DatabaseBarrierOfficerRegister(ctx, req.GetDatabase(), svcReq)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RegisterToPB(result), nil
}

// DatabaseBarrierOfficerList lists a database barrier's holders.
func (s *qrdbServiceServer) DatabaseBarrierOfficerList(ctx context.Context, req *pb.DatabaseBarrierOfficerListRequest) (*pb.BarrierOfficerListResponse, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	officers, err := hs.DatabaseBarrierOfficerList(ctx, req.GetDatabase())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierOfficerListResponse{Officers: holderpb.StatusesToPB(officers)}, nil
}

// DatabaseBarrierShamirInitialize splits a database's root key to its
// enrolled holders.
func (s *qrdbServiceServer) DatabaseBarrierShamirInitialize(ctx context.Context, req *pb.DatabaseBarrierShamirInitializeRequest) (*pb.BarrierShamirOperationResponse, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	info, err := hs.DatabaseBarrierShamirInitialize(ctx, req.GetDatabase(), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),   //nolint:gosec
		TotalShares: int32(info.TotalShares), //nolint:gosec
		ShareCount:  int32(info.ShareCount),  //nolint:gosec
	}, nil
}

// DatabaseBarrierShareClaimBegin issues a claim nonce to a database holder
// presenting its certificate on the connection.
func (s *qrdbServiceServer) DatabaseBarrierShareClaimBegin(ctx context.Context, req *pb.DatabaseBarrierShareClaimBeginRequest) (*pb.BarrierShareClaimBeginResponse, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	challenge, err := hs.DatabaseBarrierShareClaimBegin(ctx, req.GetDatabase(), req.GetClaim().GetUsername(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339)}, nil
}

// DatabaseBarrierShareClaimComplete delivers a database holder's envelope
// exactly once.
func (s *qrdbServiceServer) DatabaseBarrierShareClaimComplete(ctx context.Context, req *pb.DatabaseBarrierShareClaimCompleteRequest) (*pb.BarrierShareClaimCompleteResponse, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	claim := req.GetClaim()
	result, err := hs.DatabaseBarrierShareClaimComplete(ctx, req.GetDatabase(), claim.GetUsername(), claim.GetNonceHex(), claim.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierShareClaimCompleteResponse{ShareIndex: int32(result.ShareIndex), Envelope: result.Envelope}, nil //nolint:gosec
}

// DatabaseBarrierQuorumBegin issues a database holder's quorum challenge.
func (s *qrdbServiceServer) DatabaseBarrierQuorumBegin(ctx context.Context, req *pb.DatabaseBarrierQuorumBeginRequest) (*pb.BarrierQuorumChallenge, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	begin := req.GetBegin()
	ch, err := hs.DatabaseBarrierQuorumBegin(ctx, req.GetDatabase(), &service.QuorumBeginRequest{
		Op:       seal.QuorumOp(begin.GetOp()),
		Username: begin.GetUsername(),
		Factor:   begin.GetFactor(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.ChallengeToPB(ch), nil
}

// DatabaseBarrierQuorumSubmit submits a database holder's share. The TLS
// peer certificate, when present, is taken from the connection.
func (s *qrdbServiceServer) DatabaseBarrierQuorumSubmit(ctx context.Context, req *pb.DatabaseBarrierQuorumSubmitRequest) (*pb.BarrierQuorumOutcome, error) {
	// The decoded share is this handler's to destroy on every path.
	defer secure.Wipe(req.GetSubmit().GetShare())
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	out, err := hs.DatabaseBarrierQuorumSubmit(ctx, req.GetDatabase(), holderpb.SubmitFromPB(req.GetSubmit(), optionalPeerThumbprint(ctx)))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.OutcomeToPB(out), nil
}

// DatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (s *qrdbServiceServer) DatabaseBarrierRekeyStart(ctx context.Context, req *pb.DatabaseBarrierRekeyStartRequest) (*pb.BarrierQuorumRound, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	round, err := hs.DatabaseBarrierRekeyStart(ctx, req.GetDatabase(), int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RoundToPB(round), nil
}

// DatabaseBarrierOfficerRevoke revokes a database holder.
func (s *qrdbServiceServer) DatabaseBarrierOfficerRevoke(ctx context.Context, req *pb.DatabaseBarrierOfficerRevokeRequest) (*pb.BarrierQuorumRound, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	round, err := hs.DatabaseBarrierOfficerRevoke(ctx, req.GetDatabase(), req.GetUsername(), int(req.GetThreshold()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RoundToPB(round), nil
}

// DatabaseBarrierOfficerUnenrol removes a database holder before the
// database's custody is initialized.
func (s *qrdbServiceServer) DatabaseBarrierOfficerUnenrol(ctx context.Context, req *pb.DatabaseBarrierOfficerUnenrolRequest) (*emptypb.Empty, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	if err := hs.DatabaseBarrierOfficerUnenrol(ctx, req.GetDatabase(), req.GetUsername()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}
