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
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Shamir custody of a database barrier (docs/databases/api.md). Frames carry
// the same protobuf messages as the gRPC and Unix transports.

// databaseHolders resolves the database service's share-holder custody.
func (s *Server) databaseHolders() (service.DatabaseHolderServicer, error) {
	hs, ok := s.databaseSvc.(service.DatabaseHolderServicer)
	if !ok {
		return nil, &ServiceNotConfiguredError{Service: "database share holders"}
	}
	return hs, nil
}

// decodeDatabaseHolder resolves the custody and decodes payload into req.
func (s *Server) decodeDatabaseHolder(payload []byte, req proto.Message) (service.DatabaseHolderServicer, error) {
	hs, err := s.databaseHolders()
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(payload, req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	return hs, nil
}

// handleDatabaseBarrierOfficerRegister enrols a share holder of a database's
// barrier.
func (s *Server) handleDatabaseBarrierOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierOfficerRegisterRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	svcReq, err := holderpb.RegisterFromPB(req.GetOfficer())
	if err != nil {
		return nil, err
	}
	result, err := hs.DatabaseBarrierOfficerRegister(ctx, req.GetDatabase(), svcReq)
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RegisterToPB(result))
}

// handleDatabaseBarrierOfficerList lists a database barrier's holders.
func (s *Server) handleDatabaseBarrierOfficerList(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierOfficerListRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	officers, err := hs.DatabaseBarrierOfficerList(ctx, req.GetDatabase())
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierOfficerListResponse{Officers: holderpb.StatusesToPB(officers)})
}

// handleDatabaseBarrierShamirInitialize splits a database's root key to its
// enrolled holders.
func (s *Server) handleDatabaseBarrierShamirInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierShamirInitializeRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	info, err := hs.DatabaseBarrierShamirInitialize(ctx, req.GetDatabase(), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),   //nolint:gosec
		TotalShares: int32(info.TotalShares), //nolint:gosec
		ShareCount:  int32(info.ShareCount),  //nolint:gosec
	})
}

// handleDatabaseBarrierShareClaimBegin issues a claim nonce to a database
// holder presenting its certificate on the stream.
func (s *Server) handleDatabaseBarrierShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierShareClaimBeginRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}
	challenge, err := hs.DatabaseBarrierShareClaimBegin(ctx, req.GetDatabase(), req.GetClaim().GetUsername(), thumbprint)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339)})
}

// handleDatabaseBarrierShareClaimComplete delivers a database holder's
// envelope exactly once.
func (s *Server) handleDatabaseBarrierShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierShareClaimCompleteRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}
	claim := req.GetClaim()
	result, err := hs.DatabaseBarrierShareClaimComplete(ctx, req.GetDatabase(), claim.GetUsername(), claim.GetNonceHex(), claim.GetSignature(), thumbprint)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierShareClaimCompleteResponse{ShareIndex: int32(result.ShareIndex), Envelope: result.Envelope}) //nolint:gosec
}

// handleDatabaseBarrierQuorumBegin issues a database holder's quorum
// challenge.
func (s *Server) handleDatabaseBarrierQuorumBegin(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierQuorumBeginRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	begin := req.GetBegin()
	ch, err := hs.DatabaseBarrierQuorumBegin(ctx, req.GetDatabase(), &service.QuorumBeginRequest{
		Op: seal.QuorumOp(begin.GetOp()), Username: begin.GetUsername(), Factor: begin.GetFactor(),
	})
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.ChallengeToPB(ch))
}

// handleDatabaseBarrierQuorumSubmit submits a database holder's share; the
// decoded share is destroyed on every path.
func (s *Server) handleDatabaseBarrierQuorumSubmit(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierQuorumSubmitRequest
	defer func() { secure.Wipe(req.GetSubmit().GetShare()) }()
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	out, err := hs.DatabaseBarrierQuorumSubmit(ctx, req.GetDatabase(), holderpb.SubmitFromPB(req.GetSubmit(), quicOptionalThumbprint(ctx)))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.OutcomeToPB(out))
}

// handleDatabaseBarrierRekeyStart opens a rekey round on a database barrier.
func (s *Server) handleDatabaseBarrierRekeyStart(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierRekeyStartRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	round, err := hs.DatabaseBarrierRekeyStart(ctx, req.GetDatabase(), int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RoundToPB(round))
}

// handleDatabaseBarrierOfficerRevoke revokes a database holder.
func (s *Server) handleDatabaseBarrierOfficerRevoke(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DatabaseBarrierOfficerRevokeRequest
	hs, err := s.decodeDatabaseHolder(payload, &req)
	if err != nil {
		return nil, err
	}
	round, err := hs.DatabaseBarrierOfficerRevoke(ctx, req.GetDatabase(), req.GetUsername(), int(req.GetThreshold()))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RoundToPB(round))
}
