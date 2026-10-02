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
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
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
	// The decoded credential is this handler's to own and to destroy. The
	// service layer and pkg/seal borrow it for the call and retain nothing,
	// so a deferred wipe covers every exit path including the error ones.
	defer secure.Wipe(req.GetSecret())

	if err := service.ProvisionInitializePINs(ctx, s.barrierSvc, s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
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
	defer secure.Wipe(req.GetSecret())

	if err := s.barrierSvc.BarrierUnseal(ctx, req.GetSecret()); err != nil {
		return nil, err
	}

	return nil, nil
}

// handleBarrierSeal transitions the storage barrier to the sealed state
// by delegating to the barrier service layer.
func (s *Server) handleBarrierSeal(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	// The configured barrier SO PIN, exactly as REST and MCP verify it. The
	// handler used to ignore the request body, so the PIN was skipped.
	var req pb.BarrierSealRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := pin.RequireSOPIN(ctx, s.pinBackend, req.GetSoPin()); err != nil {
		return nil, err
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
// custody, splitting the unseal key threshold-of-totalShares to the enrolled
// share holders.
func (s *Server) handleBarrierShamirInitialize(ctx context.Context, payload []byte) ([]byte, error) {
	if s.barrierSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "barrier"}
	}

	var req pb.BarrierShamirInitializeRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := service.ProvisionInitializePINs(ctx, s.barrierSvc, s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, err
	}

	info, err := s.barrierSvc.BarrierInitializeShamir(ctx, int(req.GetThreshold()), int(req.GetTotalShares()), seal.FactorPolicy(req.GetPolicy()))
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

	// The request's passphrase is copied into protected memory and the
	// decoded copy wiped at once — the credential is what the migration reads
	// from here on, so nothing needs the request's plaintext past this line.
	creds, err := seal.CredentialsFor(req.GetPassphrase())
	secure.Wipe(req.GetPassphrase())
	if err != nil {
		return nil, err
	}
	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyID(req.GetStrategy()),
		Mechanism:       mechanism,
		Availability:    availability,
		Credentials:     creds,
		ShamirThreshold: int(req.GetShamirThreshold()),
		ShamirTotal:     int(req.GetShamirTotal()),
	}
	if err := opts.Validate(); err != nil {
		creds.Secret.Destroy()
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
		// The migration OWNS the credential from here: it outlives this
		// request, so destroying it in the handler would pull the
		// passphrase out from under a running migration. The destroy
		// belongs to the last reader, and that is this goroutine.
		defer creds.Secret.Destroy()
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

	if err := pin.RequireSOPIN(ctx, s.pinBackend, req.GetSoPin()); err != nil {
		return nil, err
	}

	info, err := s.barrierSvc.BarrierGenerateRecoveryKeys(ctx, int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
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
