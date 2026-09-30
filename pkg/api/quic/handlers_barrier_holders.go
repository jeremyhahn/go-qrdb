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

package quic

import (
	"context"
	"encoding/hex"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Share-holder QUIC handlers. Frames carry the same protobuf messages as the
// gRPC and Unix transports, converted by pkg/api/holderpb, so a holder
// message means the same thing on every protobuf-carrying transport.

// quicCertThumbprint returns the verified TLS client certificate's
// thumbprint, or a service.ClientCertRequiredError when the stream carried
// none.
func quicCertThumbprint(ctx context.Context) (string, error) {
	tlsState := auth.TLSStateFromContext(ctx)
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return "", &service.ClientCertRequiredError{}
	}
	return mtls.Thumbprint(tlsState.PeerCertificates[0].Raw), nil
}

// quicOptionalThumbprint returns the TLS client certificate's thumbprint, or
// "" when there is none. The factor proof authenticates a quorum submission;
// a certificate, when present, is additionally bound by the mtls factor.
func quicOptionalThumbprint(ctx context.Context) string {
	thumb, err := quicCertThumbprint(ctx)
	if err != nil {
		return ""
	}
	return thumb
}

// marshalReply encodes a protobuf reply.
func marshalReply(m proto.Message) ([]byte, error) {
	out, err := proto.Marshal(m)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// registerHolder serves both registration frames.
func (s *Server) registerHolder(ctx context.Context, domain seal.QuorumDomain, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierOfficerRegisterRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	svcReq, err := holderpb.RegisterFromPB(&req)
	if err != nil {
		return nil, &InvalidArgumentError{Reason: err.Error()}
	}
	result, err := s.barrierSvc.BarrierOfficerRegister(ctx, domain, svcReq)
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RegisterToPB(result))
}

// handleBarrierOfficerRegister enrols an unseal-key share holder.
func (s *Server) handleBarrierOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	return s.registerHolder(ctx, seal.DomainUnseal, payload)
}

// handleBarrierRecoveryOfficerRegister enrols a recovery-key holder.
func (s *Server) handleBarrierRecoveryOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	return s.registerHolder(ctx, seal.DomainRecovery, payload)
}

// handleBarrierOfficerList lists the unseal-key domain's holders.
func (s *Server) handleBarrierOfficerList(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	officers, err := s.barrierSvc.BarrierOfficerList(ctx, seal.DomainUnseal)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierOfficerListResponse{Officers: holderpb.StatusesToPB(officers)})
}

// claimBegin serves both claim-begin frames.
func (s *Server) claimBegin(ctx context.Context, domain seal.QuorumDomain, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierShareClaimBeginRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}
	challenge, err := s.barrierSvc.BarrierShareClaimBegin(ctx, domain, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierShareClaimBeginResponse{NonceHex: challenge.NonceHex, ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339)})
}

// claimComplete serves both claim-complete frames. The envelope is the only
// share material any frame carries, and it is sealed to the holder.
func (s *Server) claimComplete(ctx context.Context, domain seal.QuorumDomain, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierShareClaimCompleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.barrierSvc.BarrierShareClaimComplete(ctx, domain, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.BarrierShareClaimCompleteResponse{ShareIndex: int32(result.ShareIndex), Envelope: result.Envelope}) //nolint:gosec
}

// handleBarrierShareClaimBegin issues the claim nonce to an unseal-key holder.
func (s *Server) handleBarrierShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
	return s.claimBegin(ctx, seal.DomainUnseal, payload)
}

// handleBarrierShareClaimComplete delivers an unseal-key holder's envelope.
func (s *Server) handleBarrierShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
	return s.claimComplete(ctx, seal.DomainUnseal, payload)
}

// handleBarrierRecoveryShareClaimBegin issues the claim nonce to a recovery holder.
func (s *Server) handleBarrierRecoveryShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
	return s.claimBegin(ctx, seal.DomainRecovery, payload)
}

// handleBarrierRecoveryShareClaimComplete delivers a recovery holder's envelope.
func (s *Server) handleBarrierRecoveryShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
	return s.claimComplete(ctx, seal.DomainRecovery, payload)
}

// handleBarrierQuorumBegin issues a quorum challenge.
func (s *Server) handleBarrierQuorumBegin(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierQuorumBeginRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	ch, err := s.barrierSvc.BarrierQuorumBegin(ctx, &service.QuorumBeginRequest{
		Domain:   seal.QuorumDomain(req.GetDomain()),
		Op:       seal.QuorumOp(req.GetOp()),
		Username: req.GetUsername(),
		Factor:   req.GetFactor(),
	})
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.ChallengeToPB(ch))
}

// handleBarrierQuorumSubmit submits one authenticated holder's share.
func (s *Server) handleBarrierQuorumSubmit(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierQuorumSubmitRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	// The decoded share is this handler's to destroy on every path.
	defer secure.Wipe(req.GetShare())
	out, err := s.barrierSvc.BarrierQuorumSubmit(ctx, holderpb.SubmitFromPB(&req, quicOptionalThumbprint(ctx)))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.OutcomeToPB(out))
}

// handleBarrierRekeyStart opens a rekey round under SO PIN authorization.
func (s *Server) handleBarrierRekeyStart(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierRekeyStartRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := pin.RequireSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, err
	}
	round, err := s.barrierSvc.BarrierRekeyStart(ctx, seal.QuorumDomain(req.GetDomain()), int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RoundToPB(round))
}

// handleBarrierOfficerRevoke revokes a holder through a quorum-gated rekey.
func (s *Server) handleBarrierOfficerRevoke(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}
	var req pb.BarrierOfficerRevokeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := pin.RequireSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, err
	}
	round, err := s.barrierSvc.BarrierOfficerRevoke(ctx, seal.QuorumDomain(req.GetDomain()), req.GetUsername(), int(req.GetThreshold()))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RoundToPB(round))
}

// ---------------------------------------------------------------------------
// Tenant barrier holder frames
// ---------------------------------------------------------------------------

// tenantHolderReady refuses a tenant holder frame when no tenant service is
// wired or the tenant id is missing.
func (s *Server) tenantHolderReady(tenantID string) error {
	if s.tenantSvc == nil {
		return &ServiceNotConfiguredError{Service: "tenant"}
	}
	if tenantID == "" {
		return &InvalidArgumentError{Reason: (&missingTenantIDError{}).Error()}
	}
	return nil
}

// handleTenantBarrierOfficerRegister enrols a tenant share holder.
func (s *Server) handleTenantBarrierOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierOfficerRegisterRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerRegister); err != nil {
		return nil, err
	}
	regReq, err := holderpb.TenantRegisterFromPB(&req)
	if err != nil {
		return nil, &InvalidArgumentError{Reason: err.Error()}
	}
	svcReq := service.HolderRegistration(ctx, regReq)
	result, err := s.tenantSvc.TenantBarrierOfficerRegister(ctx, req.GetTenantId(), &svcReq)
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.TenantRegisterToPB(result))
}

// handleTenantBarrierOfficerList lists a tenant barrier's holders.
func (s *Server) handleTenantBarrierOfficerList(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierOfficerListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerList); err != nil {
		return nil, err
	}
	officers, err := s.tenantSvc.TenantBarrierOfficerList(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.TenantBarrierOfficerListResponse{Officers: holderpb.StatusesToPB(officers)})
}

// handleTenantBarrierShareClaimBegin issues the claim nonce to a tenant holder.
func (s *Server) handleTenantBarrierShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierShareClaimBeginRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, err
	}
	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}
	challenge, err := s.tenantSvc.TenantBarrierShareClaimBegin(ctx, req.GetTenantId(), req.GetUsername(), thumbprint)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.TenantBarrierShareClaimBeginResponse{NonceHex: hex.EncodeToString(challenge.Nonce), Expires: challenge.ExpiresAt.Unix()})
}

// handleTenantBarrierShareClaimComplete delivers a tenant holder's envelope.
func (s *Server) handleTenantBarrierShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierShareClaimCompleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, err
	}
	thumbprint, err := quicCertThumbprint(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.tenantSvc.TenantBarrierShareClaimComplete(ctx, req.GetTenantId(), req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, err
	}
	return marshalReply(&pb.TenantBarrierShareClaimCompleteResponse{ShareIndex: int32(result.ShareIndex), Envelope: result.Envelope}) //nolint:gosec
}

// handleTenantBarrierQuorumBegin issues a tenant quorum challenge.
func (s *Server) handleTenantBarrierQuorumBegin(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierQuorumBeginRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, err
	}
	ch, err := s.tenantSvc.TenantBarrierQuorumBegin(ctx, req.GetTenantId(), seal.QuorumOp(req.GetOp()), req.GetUsername(), req.GetFactor())
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.ChallengeToPB(ch))
}

// handleTenantBarrierQuorumSubmit submits one authenticated tenant holder's share.
func (s *Server) handleTenantBarrierQuorumSubmit(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierQuorumSubmitRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	// The decoded share is this handler's to destroy on every path.
	defer secure.Wipe(req.GetShare())
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, err
	}
	out, err := s.tenantSvc.TenantBarrierQuorumSubmit(ctx, req.GetTenantId(), holderpb.TenantSubmitFromPB(&req, quicOptionalThumbprint(ctx)))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.OutcomeToPB(service.QuorumOutcomeFrom(out)))
}

// handleTenantBarrierRekeyStart opens a rekey round on a tenant barrier.
func (s *Server) handleTenantBarrierRekeyStart(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierRekeyStartRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierRekey); err != nil {
		return nil, err
	}
	round, err := s.tenantSvc.TenantBarrierRekeyStart(ctx, req.GetTenantId(), int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RoundToPB(round))
}

// handleTenantBarrierOfficerRevoke revokes a tenant holder.
func (s *Server) handleTenantBarrierOfficerRevoke(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.TenantBarrierOfficerRevokeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.tenantHolderReady(req.GetTenantId()); err != nil {
		return nil, err
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerRevoke); err != nil {
		return nil, err
	}
	round, err := s.tenantSvc.TenantBarrierOfficerRevoke(ctx, req.GetTenantId(), req.GetUsername(), int(req.GetThreshold()))
	if err != nil {
		return nil, err
	}
	return marshalReply(holderpb.RoundToPB(round))
}
