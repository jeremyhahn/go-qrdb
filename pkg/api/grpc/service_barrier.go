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
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Barrier handlers
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the storage barrier with a secret credential.
// When the request supplies a non-empty SoPin / UserPin and the server has a
// pin.PINBackend configured, the PINs are persisted on the backend before
// the barrier is initialized. With no backend the PIN fields are ignored.
func (s *qrdbServiceServer) BarrierInitialize(ctx context.Context, req *pb.BarrierInitializeRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := applyBarrierInitializePINs(s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, mapPINError(err)
	}

	if err := s.barrierSvc.BarrierInitialize(ctx, req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierUnseal unseals the storage barrier with a secret credential.
func (s *qrdbServiceServer) BarrierUnseal(ctx context.Context, req *pb.BarrierUnsealRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierUnseal(ctx, req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierSeal transitions the storage barrier to the sealed state. The
// proto RPC accepts Empty so this transport does not surface SO PIN gating
// at the wire (it would require a breaking proto change). The same SO PIN
// enforcement is available through the REST surface which carries the
// barrierSealRequest JSON body; QUIC, MCP and Unix mirror REST semantics
// for parity.
func (s *qrdbServiceServer) BarrierSeal(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierSeal(ctx); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierStatus returns the current barrier status including sealed state,
// strategy, hardware backing, and initialization timestamp.
func (s *qrdbServiceServer) BarrierStatus(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierStatusResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	info, err := s.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierStatusResponse{
		Sealed:          info.Sealed,
		Strategy:        info.Strategy,
		HardwareBacked:  info.HardwareBacked,
		InitializedAt:   info.InitializedAt.Format(time.RFC3339Nano),
		ShamirThreshold: int32(info.ShamirThreshold),
		ShamirTotal:     int32(info.ShamirTotal),
	}, nil
}

// BarrierShamirInitialize initializes the barrier with Shamir secret sharing,
// splitting the root key into threshold-of-totalShares shares. When the
// request supplies SoPin / UserPin and a pin.PINBackend is configured,
// the PINs are persisted on the backend before initialization.
// The response is share-free: shares are distributed to officers via the
// BarrierOfficerRegister / BarrierShareClaimBegin / BarrierShareClaimComplete flow.
func (s *qrdbServiceServer) BarrierShamirInitialize(ctx context.Context, req *pb.BarrierShamirInitializeRequest) (*pb.BarrierShamirOperationResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := applyBarrierInitializePINs(s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, mapPINError(err)
	}

	info, err := s.barrierSvc.BarrierInitializeShamir(ctx, req.GetSecret(), int(req.GetThreshold()), int(req.GetTotalShares()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),   //nolint:gosec
		TotalShares: int32(info.TotalShares), //nolint:gosec
		ShareCount:  int32(info.ShareCount),  //nolint:gosec
	}, nil
}

// BarrierShamirUnsealShare submits a single Shamir share toward the quorum
// threshold and reports current unseal progress.
func (s *qrdbServiceServer) BarrierShamirUnsealShare(ctx context.Context, req *pb.BarrierShamirUnsealShareRequest) (*pb.BarrierShamirUnsealShareResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	progress, err := s.barrierSvc.BarrierUnsealWithShare(ctx, req.GetShare())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierShamirUnsealShareResponse{
		Required:  int32(progress.Required),  //nolint:gosec
		Submitted: int32(progress.Submitted), //nolint:gosec
		Complete:  progress.Complete,
	}, nil
}

// BarrierShamirUnsealShares unseals the barrier using a complete set of
// Shamir shares in a single call.
func (s *qrdbServiceServer) BarrierShamirUnsealShares(ctx context.Context, req *pb.BarrierShamirUnsealSharesRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierUnsealWithShares(ctx, req.GetShares()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRekey generates new Shamir shares for the existing root key with
// the specified threshold and total parameters. SO PIN verification is
// enforced when a pin.PINBackend is configured AND a SO PIN has been set.
// The response is share-free: new shares are sealed to officers via
// the claim flow.
func (s *qrdbServiceServer) BarrierRekey(ctx context.Context, req *pb.BarrierRekeyRequest) (*pb.BarrierShamirOperationResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, mapPINError(err)
	}

	info, err := s.barrierSvc.BarrierRekey(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),   //nolint:gosec
		TotalShares: int32(info.TotalShares), //nolint:gosec
		ShareCount:  int32(info.ShareCount),  //nolint:gosec
	}, nil
}

// BarrierRecoveryGenerate generates a set of Shamir recovery shares from the
// root key for disaster recovery. SO PIN verification is enforced when a
// pin.PINBackend is configured AND a SO PIN has been set.
// The response is share-free: recovery shares are sealed to recovery officers
// and claimed via the BarrierRecoveryShareClaimBegin / BarrierRecoveryShareClaimComplete flow.
func (s *qrdbServiceServer) BarrierRecoveryGenerate(ctx context.Context, req *pb.BarrierRecoveryGenerateRequest) (*pb.BarrierRecoveryKeysResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, mapPINError(err)
	}

	info, err := s.barrierSvc.BarrierGenerateRecoveryKeys(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRecoveryKeysResponse{
		Threshold:  int32(info.Threshold),  //nolint:gosec
		Total:      int32(info.Total),      //nolint:gosec
		ShareCount: int32(info.ShareCount), //nolint:gosec
	}, nil
}

// BarrierRecoveryRecover unseals the barrier using recovery shares that were
// previously generated by BarrierRecoveryGenerate.
func (s *qrdbServiceServer) BarrierRecoveryRecover(ctx context.Context, req *pb.BarrierRecoveryRecoverRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierRecoverWithKeys(ctx, req.GetKeys()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRecoveryDelete removes recovery key metadata from storage.
func (s *qrdbServiceServer) BarrierRecoveryDelete(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(ctx); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRecoveryStatus reports whether recovery key metadata exists in storage.
func (s *qrdbServiceServer) BarrierRecoveryStatus(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierRecoveryStatusResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	has, err := s.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRecoveryStatusResponse{
		HasRecoveryKeys: has,
	}, nil
}

// BarrierRootToken creates a new root token by proving knowledge of the
// master key through Shamir shares.
func (s *qrdbServiceServer) BarrierRootToken(ctx context.Context, req *pb.BarrierRootTokenRequest) (*pb.BarrierRootTokenResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	info, err := s.barrierSvc.BarrierGenerateRootToken(ctx, req.GetShares())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRootTokenResponse{
		Token:     info.Token,
		CreatedAt: info.CreatedAt.Format(time.RFC3339Nano),
	}, nil
}

// ---------------------------------------------------------------------------
// Security Officer management handlers (new: share-distribution model)
// ---------------------------------------------------------------------------

// peerCertThumbprint extracts the mTLS leaf certificate thumbprint from the
// gRPC peer context. Returns an error if no verified client certificate is
// available on the connection.
func peerCertThumbprint(ctx context.Context) (string, error) {
	p, ok := grpcpeer.FromContext(ctx)
	if !ok {
		return "", &ClientCertRequiredError{}
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", &ClientCertRequiredError{}
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", &ClientCertRequiredError{}
	}
	return ceremony.CertThumbprint(tlsInfo.State.PeerCertificates[0].Raw), nil
}

// BarrierOfficerRegister registers a Security Officer for the system barrier.
// The officer receives a signed certificate; their Shamir share is distributed
// at init / rekey time and claimed via the share-claim flow.
func (s *qrdbServiceServer) BarrierOfficerRegister(ctx context.Context, req *pb.BarrierOfficerRegisterRequest) (*pb.BarrierOfficerRegisterResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	svcReq := &service.BarrierOfficerRegisterRequest{
		Username:          req.GetUsername(),
		CSRPEM:            req.GetCsrPem(),
		AuthenticatorType: req.GetAuthenticatorType(),
		AuthenticatorRef:  req.GetAuthenticatorRef(),
	}
	result, err := s.barrierSvc.BarrierOfficerRegister(ctx, svcReq)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierOfficerRegisterResponse{
		CertPem:    result.CertPEM,
		ShareIndex: int32(result.ShareIndex), //nolint:gosec
	}, nil
}

// BarrierOfficerList lists all registered Security Officers for the system barrier.
// The response contains only public officer metadata — no share bytes.
func (s *qrdbServiceServer) BarrierOfficerList(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierOfficerListResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	officers, err := s.barrierSvc.BarrierOfficerList(ctx)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierOfficerListResponse{
		Officers: barrierOfficerStatusesToProto(officers),
	}, nil
}

// BarrierShareClaimBegin starts the share-claim handshake for the calling
// Security Officer. The SO must present their mTLS certificate; the server
// issues a challenge nonce the SO must sign to prove key possession.
func (s *qrdbServiceServer) BarrierShareClaimBegin(ctx context.Context, req *pb.BarrierShareClaimBeginRequest) (*pb.BarrierShareClaimBeginResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	challenge, err := s.barrierSvc.BarrierShareClaimBegin(ctx, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierShareClaimBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	}, nil
}

// BarrierShareClaimComplete completes the share-claim handshake and returns
// the Shamir share exactly once over the SO's own mTLS session. The share bytes
// are never logged and are the caller's responsibility to wipe after use.
func (s *qrdbServiceServer) BarrierShareClaimComplete(ctx context.Context, req *pb.BarrierShareClaimCompleteRequest) (*pb.BarrierShareClaimCompleteResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	result, err := s.barrierSvc.BarrierShareClaimComplete(ctx, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierShareClaimCompleteResponse{
		ShareIndex: int32(result.ShareIndex), //nolint:gosec
		ShareBytes: result.ShareBytes,
	}, nil
}

// BarrierOfficerClaimStatus returns per-officer claim status for the system
// barrier. The response contains only public metadata — no share bytes.
func (s *qrdbServiceServer) BarrierOfficerClaimStatus(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierOfficerClaimStatusResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	officers, err := s.barrierSvc.BarrierOfficerClaimStatus(ctx)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierOfficerClaimStatusResponse{
		Officers: barrierOfficerStatusesToProto(officers),
	}, nil
}

// BarrierShamirSealVote submits a Shamir share as a seal vote. When the
// threshold is reached the system barrier is sealed and all vote enclaves
// are destroyed. Returns ErrShamirBarrierRequiresQuorum if the barrier is
// not in Shamir mode.
func (s *qrdbServiceServer) BarrierShamirSealVote(ctx context.Context, req *pb.BarrierShamirSealVoteRequest) (*pb.BarrierShamirSealVoteResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	progress, err := s.barrierSvc.BarrierShamirSealVote(ctx, req.GetShare())
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierShamirSealVoteResponse{
		Required:  int32(progress.Required),  //nolint:gosec
		Submitted: int32(progress.Submitted), //nolint:gosec
		Complete:  progress.Complete,
	}, nil
}

// BarrierRecoveryOfficerRegister registers a Security Officer for recovery
// key distribution. Recovery officers use the same claim flow as unseal officers
// but against the recovery scope.
func (s *qrdbServiceServer) BarrierRecoveryOfficerRegister(ctx context.Context, req *pb.BarrierOfficerRegisterRequest) (*pb.BarrierOfficerRegisterResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	svcReq := &service.BarrierOfficerRegisterRequest{
		Username:          req.GetUsername(),
		CSRPEM:            req.GetCsrPem(),
		AuthenticatorType: req.GetAuthenticatorType(),
		AuthenticatorRef:  req.GetAuthenticatorRef(),
	}
	result, err := s.barrierSvc.BarrierRecoveryOfficerRegister(ctx, svcReq)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierOfficerRegisterResponse{
		CertPem:    result.CertPEM,
		ShareIndex: int32(result.ShareIndex), //nolint:gosec
	}, nil
}

// BarrierRecoveryShareClaimBegin starts the recovery-share claim handshake
// for the calling Security Officer.
func (s *qrdbServiceServer) BarrierRecoveryShareClaimBegin(ctx context.Context, req *pb.BarrierShareClaimBeginRequest) (*pb.BarrierShareClaimBeginResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	challenge, err := s.barrierSvc.BarrierRecoveryShareClaimBegin(ctx, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierShareClaimBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	}, nil
}

// BarrierRecoveryShareClaimComplete completes the recovery-share claim
// handshake and returns the recovery share exactly once over the SO's own
// mTLS session. The share bytes are never logged.
func (s *qrdbServiceServer) BarrierRecoveryShareClaimComplete(ctx context.Context, req *pb.BarrierShareClaimCompleteRequest) (*pb.BarrierShareClaimCompleteResponse, error) {
	if s.barrierSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "barrier"}).Error())
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	result, err := s.barrierSvc.BarrierRecoveryShareClaimComplete(ctx, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapBarrierCeremonyError(err)
	}

	return &pb.BarrierShareClaimCompleteResponse{
		ShareIndex: int32(result.ShareIndex), //nolint:gosec
		ShareBytes: result.ShareBytes,
	}, nil
}

// ---------------------------------------------------------------------------
// Error mapping helpers
// ---------------------------------------------------------------------------

// mapBarrierCeremonyError translates ceremony and seal errors that arise from
// the Security Officer share-distribution flow into appropriate gRPC status
// codes.
func mapBarrierCeremonyError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, service.ErrCeremonyNotConfigured) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, seal.ErrShamirBarrierRequiresQuorum) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, seal.ErrSealVoteVerificationFailed) {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if errors.Is(err, seal.ErrUnsealKeyMismatch) {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if errors.Is(err, seal.ErrWrappedRootKeyCorrupt) {
		return status.Error(codes.Internal, err.Error())
	}
	if errors.Is(err, ceremony.ErrCertThumbprintMismatch) {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if errors.Is(err, ceremony.ErrOfficerNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	if errors.Is(err, ceremony.ErrShareAlreadyClaimed) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, ceremony.ErrNoPendingShare) {
		return status.Error(codes.NotFound, err.Error())
	}
	if errors.Is(err, ceremony.ErrInsufficientOfficers) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, ceremony.ErrInvalidAuthenticatorType) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	return mapServiceError(err)
}

// barrierOfficerStatusesToProto converts service-layer BarrierOfficerStatus
// slices to proto OfficerStatus messages.
func barrierOfficerStatusesToProto(officers []*service.BarrierOfficerStatus) []*pb.OfficerStatus {
	out := make([]*pb.OfficerStatus, len(officers))
	for i, o := range officers {
		var claimedAt string
		if !o.ClaimedAt.IsZero() {
			claimedAt = o.ClaimedAt.Format(time.RFC3339)
		}
		out[i] = &pb.OfficerStatus{
			Username:       o.Username,
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     int32(o.ShareIndex), //nolint:gosec
			AuthFactor:     o.AuthFactor,
			EnrolledAt:     o.EnrolledAt.Format(time.RFC3339),
			ClaimedAt:      claimedAt,
			SharePending:   o.SharePending,
		}
	}
	return out
}
