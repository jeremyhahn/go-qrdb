// Copyright (c) 2026 Jeremy Hahn
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
	"context"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// optionalPeerThumbprint returns the verified TLS client certificate's
// thumbprint, or "" when the connection carried none. A quorum submission
// does not require mTLS — the factor proof authenticates the holder — but
// when a certificate is present the mtls factor binds to it.
func optionalPeerThumbprint(ctx context.Context) string {
	thumb, err := peerCertThumbprint(ctx)
	if err != nil {
		return ""
	}
	return thumb
}

// barrierUnavailable is the reply when no barrier service is wired.
func barrierUnavailable() error {
	return mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
}

// registerHolder serves both the unseal and recovery registration RPCs.
func (s *qrdbServiceServer) registerHolder(ctx context.Context, domain seal.QuorumDomain, req *pb.BarrierOfficerRegisterRequest) (*pb.BarrierOfficerRegisterResponse, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	svcReq, err := holderpb.RegisterFromPB(req)
	if err != nil {
		return nil, mapServiceError(err)
	}
	result, err := s.barrierSvc.BarrierOfficerRegister(ctx, domain, svcReq)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RegisterToPB(result), nil
}

// BarrierOfficerRegister enrols a share holder (Security Officer or key
// custodian) of the unseal-key domain.
func (s *qrdbServiceServer) BarrierOfficerRegister(ctx context.Context, req *pb.BarrierOfficerRegisterRequest) (*pb.BarrierOfficerRegisterResponse, error) {
	return s.registerHolder(ctx, seal.DomainUnseal, req)
}

// BarrierRecoveryOfficerRegister enrols a recovery-key holder.
func (s *qrdbServiceServer) BarrierRecoveryOfficerRegister(ctx context.Context, req *pb.BarrierOfficerRegisterRequest) (*pb.BarrierOfficerRegisterResponse, error) {
	return s.registerHolder(ctx, seal.DomainRecovery, req)
}

// BarrierOfficerList lists the unseal-key domain's holders.
func (s *qrdbServiceServer) BarrierOfficerList(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierOfficerListResponse, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	officers, err := s.barrierSvc.BarrierOfficerList(ctx, seal.DomainUnseal)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierOfficerListResponse{Officers: holderpb.StatusesToPB(officers)}, nil
}

// claimBegin serves both claim-begin RPCs.
func (s *qrdbServiceServer) claimBegin(ctx context.Context, domain seal.QuorumDomain, req *pb.BarrierShareClaimBeginRequest) (*pb.BarrierShareClaimBeginResponse, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	challenge, err := s.barrierSvc.BarrierShareClaimBegin(ctx, domain, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339)}, nil
}

// claimComplete serves both claim-complete RPCs.
func (s *qrdbServiceServer) claimComplete(ctx context.Context, domain seal.QuorumDomain, req *pb.BarrierShareClaimCompleteRequest) (*pb.BarrierShareClaimCompleteResponse, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	result, err := s.barrierSvc.BarrierShareClaimComplete(ctx, domain, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierShareClaimCompleteResponse{ShareIndex: int32(result.ShareIndex), Envelope: result.Envelope}, nil //nolint:gosec
}

// BarrierShareClaimBegin issues the claim nonce to an unseal-key holder.
func (s *qrdbServiceServer) BarrierShareClaimBegin(ctx context.Context, req *pb.BarrierShareClaimBeginRequest) (*pb.BarrierShareClaimBeginResponse, error) {
	return s.claimBegin(ctx, seal.DomainUnseal, req)
}

// BarrierShareClaimComplete delivers an unseal-key holder's envelope once.
func (s *qrdbServiceServer) BarrierShareClaimComplete(ctx context.Context, req *pb.BarrierShareClaimCompleteRequest) (*pb.BarrierShareClaimCompleteResponse, error) {
	return s.claimComplete(ctx, seal.DomainUnseal, req)
}

// BarrierRecoveryShareClaimBegin issues the claim nonce to a recovery holder.
func (s *qrdbServiceServer) BarrierRecoveryShareClaimBegin(ctx context.Context, req *pb.BarrierShareClaimBeginRequest) (*pb.BarrierShareClaimBeginResponse, error) {
	return s.claimBegin(ctx, seal.DomainRecovery, req)
}

// BarrierRecoveryShareClaimComplete delivers a recovery holder's envelope once.
func (s *qrdbServiceServer) BarrierRecoveryShareClaimComplete(ctx context.Context, req *pb.BarrierShareClaimCompleteRequest) (*pb.BarrierShareClaimCompleteResponse, error) {
	return s.claimComplete(ctx, seal.DomainRecovery, req)
}

// BarrierQuorumBegin issues a quorum challenge. It is reachable while the
// barrier is sealed.
func (s *qrdbServiceServer) BarrierQuorumBegin(ctx context.Context, req *pb.BarrierQuorumBeginRequest) (*pb.BarrierQuorumChallenge, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	ch, err := s.barrierSvc.BarrierQuorumBegin(ctx, &service.QuorumBeginRequest{
		Domain:   seal.QuorumDomain(req.GetDomain()),
		Op:       seal.QuorumOp(req.GetOp()),
		Username: req.GetUsername(),
		Factor:   req.GetFactor(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.ChallengeToPB(ch), nil
}

// BarrierQuorumSubmit submits one authenticated holder's share. The TLS peer
// certificate, when present, is taken from the connection — never from the
// request.
func (s *qrdbServiceServer) BarrierQuorumSubmit(ctx context.Context, req *pb.BarrierQuorumSubmitRequest) (*pb.BarrierQuorumOutcome, error) {
	// The decoded share is this handler's to destroy on every path.
	defer secure.Wipe(req.GetShare())
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	out, err := s.barrierSvc.BarrierQuorumSubmit(ctx, holderpb.SubmitFromPB(req, optionalPeerThumbprint(ctx)))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.OutcomeToPB(out), nil
}

// BarrierRekeyStart opens a rekey round under SO PIN authorization.
func (s *qrdbServiceServer) BarrierRekeyStart(ctx context.Context, req *pb.BarrierRekeyStartRequest) (*pb.BarrierQuorumRound, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	round, err := s.barrierSvc.BarrierRekeyStart(ctx, seal.QuorumDomain(req.GetDomain()), int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()), req.GetSoPin())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RoundToPB(round), nil
}

// BarrierOfficerRevoke revokes a holder through a quorum-gated rekey, under
// SO PIN authorization.
func (s *qrdbServiceServer) BarrierOfficerRevoke(ctx context.Context, req *pb.BarrierOfficerRevokeRequest) (*pb.BarrierQuorumRound, error) {
	if s.barrierSvc == nil {
		return nil, barrierUnavailable()
	}
	round, err := s.barrierSvc.BarrierOfficerRevoke(ctx, seal.QuorumDomain(req.GetDomain()), req.GetUsername(), int(req.GetThreshold()), req.GetSoPin())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RoundToPB(round), nil
}
