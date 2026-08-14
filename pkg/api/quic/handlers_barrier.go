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
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleBarrierInitialize initializes the storage barrier with a secret
// credential by delegating to the barrier service layer.
func (s *Server) handleBarrierInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := applyBarrierInitializePINs(s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, err
	}

	if err := s.barrierSvc.BarrierInitialize(ctx, req.GetSecret()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierUnseal unseals the storage barrier with a secret credential
// by delegating to the barrier service layer.
func (s *Server) handleBarrierUnseal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierUnsealRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierUnseal(ctx, req.GetSecret()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierSeal transitions the storage barrier to the sealed state
// by delegating to the barrier service layer.
func (s *Server) handleBarrierSeal(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	if err := s.barrierSvc.BarrierSeal(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierStatus returns the current barrier status including seal
// state, strategy, hardware-backed flag, and initialization timestamp.
func (s *Server) handleBarrierStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	info, err := s.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierStatusResponse{
		Sealed:          info.Sealed,
		Strategy:        info.Strategy,
		HardwareBacked:  info.HardwareBacked,
		InitializedAt:   info.InitializedAt.Format(time.RFC3339Nano),
		ShamirThreshold: int32(info.ShamirThreshold),
		ShamirTotal:     int32(info.ShamirTotal),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirInitialize initializes the barrier with Shamir secret
// sharing, splitting the root key into threshold-of-totalShares shares.
func (s *Server) handleBarrierShamirInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := applyBarrierInitializePINs(s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, err
	}

	info, err := s.barrierSvc.BarrierInitializeShamir(ctx, req.GetSecret(), int(req.GetThreshold()), int(req.GetTotalShares()))
	if err != nil {
		return nil, err
	}

	// INV-1: share bytes never appear in the init response.
	resp, err := proto.Marshal(&pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),
		TotalShares: int32(info.TotalShares),
		ShareCount:  int32(info.ShareCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirUnsealShare submits a single Shamir share toward the
// quorum threshold and returns progress information.
func (s *Server) handleBarrierShamirUnsealShare(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirUnsealShareRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	progress, err := s.barrierSvc.BarrierUnsealWithShare(ctx, req.GetShare())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShamirUnsealShareResponse{
		Required:  int32(progress.Required),
		Submitted: int32(progress.Submitted),
		Complete:  progress.Complete,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirUnsealShares unseals the barrier using a complete set
// of Shamir shares in a single call.
func (s *Server) handleBarrierShamirUnsealShares(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirUnsealSharesRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierUnsealWithShares(ctx, req.GetShares()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierRekey generates new Shamir shares for the existing root key
// with the specified threshold and total parameters.
func (s *Server) handleBarrierRekey(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRekeyRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, err
	}

	info, err := s.barrierSvc.BarrierRekey(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, err
	}

	// INV-1: share bytes never appear in the rekey response.
	resp, err := proto.Marshal(&pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),
		TotalShares: int32(info.TotalShares),
		ShareCount:  int32(info.ShareCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// quicMigrationAcceptWindow bounds how long the handler waits for
// EncryptionMigrator.EnableEncryption to return before giving up on
// delivering its outcome synchronously. Mirrors migrationAcceptWindow in
// pkg/api/rest/handlers_barrier.go / grpcMigrationAcceptWindow in
// pkg/api/grpc/service_barrier.go — see the former's doc comment for the
// full rationale: every synchronously-knowable rejection returns well within
// this window, before the destructive Quiesce phase (which for a
// system-scope migration recycles the serving QUIC server) ever runs.
const quicMigrationAcceptWindow = 750 * time.Millisecond

// handleBarrierEnableEncryption migrates a previously-unencrypted barrier to
// a keyed strategy. Like the REST/gRPC handlers it validates synchronously,
// then invokes the migrator and races its result against
// quicMigrationAcceptWindow: a synchronous rejection (bad target,
// shared-barrier-immutable, insufficient officers, entitlement denied,
// provider unavailable) is returned to the caller as a real error, while a
// migration still running once the window elapses is acknowledged with
// "migration_started"; clients poll BarrierStatus. Forwards every field
// including tenant, strategy, mechanism, availability, passphrase, and
// Shamir threshold/total.
func (s *Server) handleBarrierEnableEncryption(_ context.Context, payload []byte) ([]byte, error) {
	if s.encMigrator == nil {
		return nil, &ServiceNotConfiguredError{Service: "encryption-migration"}
	}

	var req pb.BarrierEnableEncryptionRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	scope := service.BarrierScope{Kind: service.ScopeSystem}
	if req.GetTenantId() != "" {
		scope = service.BarrierScope{Kind: service.ScopeTenant, TenantID: req.GetTenantId()}
	}

	mechanism := seal.MechanismSwap
	if req.GetMechanism() == string(seal.MechanismInPlace) {
		mechanism = seal.MechanismInPlace
	}
	availability := seal.AvailabilityQuiesced
	if req.GetAvailability() == string(seal.AvailabilityOnline) {
		availability = seal.AvailabilityOnline
	}

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyID(req.GetStrategy()),
		Mechanism:       mechanism,
		Availability:    availability,
		Credentials:     seal.Credentials{Secret: []byte(req.GetPassphrase())},
		ShamirThreshold: int(req.GetShamirThreshold()),
		ShamirTotal:     int(req.GetShamirTotal()),
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	// A background context is used because the request context is cancelled
	// once the response returns, and the migration must continue independent
	// of this request's lifetime.
	migrator := s.encMigrator
	resultCh := make(chan error, 1)
	// #nosec G118 -- detaching from the request context is the point: the
	// migration must survive the response returning.
	go func() {
		resultCh <- migrator.EnableEncryption(context.Background(), scope, opts)
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			return nil, err
		}
		// Completed within the window with no error: an unusually fast
		// migration (e.g. an empty barrier) already finished. Fall through
		// and report "migration_started" — BarrierStatus already reflects
		// the committed strategy, so the wording is accurate either way.
	case <-time.After(quicMigrationAcceptWindow):
		// Still running past acceptance: no client is left to deliver the
		// eventual outcome to, so only log it.
		go func() {
			if err := <-resultCh; err != nil {
				slog.Error("enable-encryption migration failed",
					slog.String("scope", string(scope.Kind)),
					slog.String("tenant", scope.TenantID),
					slog.String("strategy", string(opts.TargetStrategy)),
					slog.Any("error", err))
			}
		}()
	}

	resp, err := proto.Marshal(&pb.BarrierEnableEncryptionResponse{
		Status: "migration_started",
		Scope:  string(scope.Kind),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryGenerate generates a set of Shamir recovery shares
// from the root key for disaster recovery.
func (s *Server) handleBarrierRecoveryGenerate(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRecoveryGenerateRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.GetSoPin()); err != nil {
		return nil, err
	}

	info, err := s.barrierSvc.BarrierGenerateRecoveryKeys(ctx, int(req.GetThreshold()), int(req.GetTotal()))
	if err != nil {
		return nil, err
	}

	// INV-1: recovery share bytes never appear in the generate response.
	resp, err := proto.Marshal(&pb.BarrierRecoveryKeysResponse{
		Threshold:  int32(info.Threshold),
		Total:      int32(info.Total),
		ShareCount: int32(info.ShareCount),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryRecover unseals the barrier using recovery shares
// that were previously generated by BarrierGenerateRecoveryKeys.
func (s *Server) handleBarrierRecoveryRecover(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRecoveryRecoverRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.barrierSvc.BarrierRecoverWithKeys(ctx, req.GetKeys()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierRecoveryDelete removes recovery key metadata from storage.
func (s *Server) handleBarrierRecoveryDelete(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(ctx); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierRecoveryStatus reports whether recovery key metadata exists
// in storage.
func (s *Server) handleBarrierRecoveryStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	has, err := s.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierRecoveryStatusResponse{
		HasRecoveryKeys: has,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRootToken creates a new root token by proving knowledge of
// the master key through Shamir shares.
func (s *Server) handleBarrierRootToken(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierRootTokenRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	info, err := s.barrierSvc.BarrierGenerateRootToken(ctx, req.GetShares())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierRootTokenResponse{
		Token:     info.Token,
		CreatedAt: info.CreatedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Barrier Security Officer QUIC handlers
// ---------------------------------------------------------------------------

// quicCertThumbprint extracts the mTLS peer certificate thumbprint from the
// context. Returns a ClientCertRequiredError when no peer certificate is present.
func quicCertThumbprint(ctx context.Context) (string, error) {
	tlsState := auth.TLSStateFromContext(ctx)
	if tlsState == nil || len(tlsState.PeerCertificates) == 0 {
		return "", &ClientCertRequiredError{}
	}
	return ceremony.CertThumbprint(tlsState.PeerCertificates[0].Raw), nil
}

// handleBarrierOfficerRegister registers a Security Officer for the system barrier.
func (s *Server) handleBarrierOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierOfficerRegisterRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.barrierSvc.BarrierOfficerRegister(ctx, &service.BarrierOfficerRegisterRequest{
		Username:          req.GetUsername(),
		CSRPEM:            []byte(req.GetCsrPem()),
		AuthenticatorType: req.GetAuthenticatorType(),
		AuthenticatorRef:  req.GetAuthenticatorRef(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierOfficerRegisterResponse{
		CertPem:    result.CertPEM,
		ShareIndex: int32(result.ShareIndex),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierOfficerList returns all enrolled Security Officers for the system barrier.
func (s *Server) handleBarrierOfficerList(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	officers, err := s.barrierSvc.BarrierOfficerList(ctx)
	if err != nil {
		return nil, err
	}

	pbOfficers := quicBarrierOfficerStatusesToProto(officers)
	resp, err := proto.Marshal(&pb.BarrierOfficerListResponse{Officers: pbOfficers})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShareClaimBegin issues a challenge nonce to a Security Officer.
func (s *Server) handleBarrierShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
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

	challenge, err := s.barrierSvc.BarrierShareClaimBegin(ctx, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShareClaimBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShareClaimComplete verifies the officer's nonce signature and
// returns the share bytes exactly once (INV-1, INV-11).
func (s *Server) handleBarrierShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
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

	// Signature is transmitted as raw bytes in the proto wire format.
	result, err := s.barrierSvc.BarrierShareClaimComplete(ctx, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, err
	}

	// INV-11: share_bytes appears only here, in the claiming officer's own session.
	resp, err := proto.Marshal(&pb.BarrierShareClaimCompleteResponse{
		ShareIndex: int32(result.ShareIndex),
		ShareBytes: result.ShareBytes,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierOfficerClaimStatus returns a per-officer claim status snapshot.
func (s *Server) handleBarrierOfficerClaimStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	officers, err := s.barrierSvc.BarrierOfficerClaimStatus(ctx)
	if err != nil {
		return nil, err
	}

	pbOfficers := quicBarrierOfficerStatusesToProto(officers)
	resp, err := proto.Marshal(&pb.BarrierOfficerClaimStatusResponse{Officers: pbOfficers})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierShamirSealVote submits a Shamir share as a seal vote.
func (s *Server) handleBarrierShamirSealVote(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirSealVoteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	progress, err := s.barrierSvc.BarrierShamirSealVote(ctx, req.GetShare())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShamirSealVoteResponse{
		Required:  int32(progress.Required),
		Submitted: int32(progress.Submitted),
		Complete:  progress.Complete,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryOfficerRegister registers a Security Officer for
// recovery key distribution.
func (s *Server) handleBarrierRecoveryOfficerRegister(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierOfficerRegisterRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	result, err := s.barrierSvc.BarrierRecoveryOfficerRegister(ctx, &service.BarrierOfficerRegisterRequest{
		Username:          req.GetUsername(),
		CSRPEM:            []byte(req.GetCsrPem()),
		AuthenticatorType: req.GetAuthenticatorType(),
		AuthenticatorRef:  req.GetAuthenticatorRef(),
	})
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierOfficerRegisterResponse{
		CertPem:    result.CertPEM,
		ShareIndex: int32(result.ShareIndex),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryShareClaimBegin issues a challenge nonce to a
// recovery officer.
func (s *Server) handleBarrierRecoveryShareClaimBegin(ctx context.Context, payload []byte) ([]byte, error) {
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

	challenge, err := s.barrierSvc.BarrierRecoveryShareClaimBegin(ctx, req.GetUsername(), thumbprint)
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.BarrierShareClaimBeginResponse{
		NonceHex:  challenge.NonceHex,
		ExpiresAt: challenge.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleBarrierRecoveryShareClaimComplete verifies a recovery officer's nonce
// signature and returns the recovery share bytes exactly once (INV-1, INV-11).
func (s *Server) handleBarrierRecoveryShareClaimComplete(ctx context.Context, payload []byte) ([]byte, error) {
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

	// Signature is transmitted as raw bytes in the proto wire format.
	result, err := s.barrierSvc.BarrierRecoveryShareClaimComplete(ctx, req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, err
	}

	// INV-11: share_bytes appears only here, in the claiming officer's own session.
	resp, err := proto.Marshal(&pb.BarrierShareClaimCompleteResponse{
		ShareIndex: int32(result.ShareIndex),
		ShareBytes: result.ShareBytes,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// quicBarrierOfficerStatusesToProto converts a slice of BarrierOfficerStatus to
// proto OfficerStatus messages.
func quicBarrierOfficerStatusesToProto(officers []*service.BarrierOfficerStatus) []*pb.OfficerStatus {
	out := make([]*pb.OfficerStatus, len(officers))
	for i, o := range officers {
		out[i] = &pb.OfficerStatus{
			Username:       o.Username,
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     int32(o.ShareIndex),
			AuthFactor:     o.AuthFactor,
			SharePending:   o.SharePending,
			EnrolledAt:     o.EnrolledAt.Format(time.RFC3339),
		}
		if !o.ClaimedAt.IsZero() {
			out[i].ClaimedAt = o.ClaimedAt.Format(time.RFC3339)
		}
	}
	return out
}
