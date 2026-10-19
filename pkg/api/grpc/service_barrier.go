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
	"log/slog"
	"time"

	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Barrier handlers
// ---------------------------------------------------------------------------

// BarrierInitialize initializes the storage barrier with a secret credential.
// PINs the request supplies are provisioned on the pin.PINBackend before the
// barrier is initialized (see pin.Provision); PINs supplied to a server with
// no backend are refused, never ignored.
func (s *qrdbServiceServer) BarrierInitialize(ctx context.Context, req *pb.BarrierInitializeRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}

	// The decoded credential is this handler's to own and to destroy. The
	// service layer and pkg/seal borrow it for the call and retain nothing,
	// so a deferred wipe covers every exit path including the error ones.
	defer secure.Wipe(req.GetSecret())

	if err := service.ProvisionInitializePINs(ctx, s.barrierSvc, s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, mapServiceError(err)
	}

	if err := s.barrierSvc.BarrierInitialize(ctx, req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierUnseal unseals the storage barrier with a secret credential.
func (s *qrdbServiceServer) BarrierUnseal(ctx context.Context, req *pb.BarrierUnsealRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}

	defer secure.Wipe(req.GetSecret())

	if err := s.barrierSvc.BarrierUnseal(ctx, req.GetSecret()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierSeal transitions the storage barrier to the sealed state -- on every
// node once the cluster has activated cluster-seal -- under the configured SO
// PIN, verified by the PIN backend exactly as on every other transport;
// without a PIN backend it fails closed.
func (s *qrdbServiceServer) BarrierSeal(ctx context.Context, req *pb.BarrierSealRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}

	if err := pin.RequireSOPIN(ctx, s.pinBackend, req.GetSoPin()); err != nil {
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}

	info, err := s.barrierSvc.BarrierStatus(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return BarrierStatusToProto(info), nil
}

// BarrierStatusToProto converts a barrier status to its wire form, shared by
// the gRPC and native QUIC servers. Initialized is how a client tells a node
// waiting for its first boot from an initialized one, as on REST and MCP; a
// sealed node reports it and Sealed and nothing else.
func BarrierStatusToProto(info *service.BarrierStatusInfo) *pb.BarrierStatusResponse {
	out := &pb.BarrierStatusResponse{
		Sealed:           info.Sealed,
		Initialized:      info.Initialized,
		Strategy:         info.Strategy,
		HardwareBacked:   info.HardwareBacked,
		ShamirThreshold:  int32(info.ShamirThreshold),
		ShamirTotal:      int32(info.ShamirTotal),
		ClusterSeal:      ClusterSealToProto(info.ClusterSeal),
		ClusterMigration: ClusterMigrationToProto(info.ClusterMigration),
	}
	if !info.InitializedAt.IsZero() {
		out.InitializedAt = info.InitializedAt.Format(time.RFC3339Nano)
	}
	return out
}

// BarrierShamirInitialize initializes the barrier under Shamir custody: the
// unseal key is split threshold-of-total_shares to the enrolled share
// holders, each share sealed into an envelope to its holder. Supplied PINs are
// provisioned (or verified) through the PINBackend first. No share material
// is returned.
func (s *qrdbServiceServer) BarrierShamirInitialize(ctx context.Context, req *pb.BarrierShamirInitializeRequest) (*pb.BarrierShamirOperationResponse, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}
	if err := service.ProvisionInitializePINs(ctx, s.barrierSvc, s.pinBackend, req.GetSoPin(), req.GetUserPin()); err != nil {
		return nil, mapServiceError(err)
	}
	info, err := s.barrierSvc.BarrierInitializeShamir(ctx, int(req.GetThreshold()), int(req.GetTotalShares()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierShamirOperationResponse{
		Threshold:   int32(info.Threshold),   //nolint:gosec
		TotalShares: int32(info.TotalShares), //nolint:gosec
		ShareCount:  int32(info.ShareCount),  //nolint:gosec
	}, nil
}

// grpcMigrationAcceptWindow bounds how long the handler waits for
// EncryptionMigrator.EnableEncryption to return before giving up on
// delivering its outcome synchronously. Mirrors migrationAcceptWindow in
// pkg/api/rest/handlers_barrier.go — see its doc comment for the full
// rationale: every synchronously-knowable rejection returns well within this
// window, before the destructive Quiesce phase (which for a system-scope
// migration recycles the serving gRPC server) ever runs.
const grpcMigrationAcceptWindow = 750 * time.Millisecond

// BarrierEnableEncryption migrates a previously-unencrypted barrier to a keyed
// strategy. It validates synchronously, then invokes the migrator and races
// its result against grpcMigrationAcceptWindow: a synchronous rejection (bad
// target, shared-barrier-immutable, insufficient officers, entitlement
// denied, provider unavailable) is returned to the caller as a proper gRPC
// status error, while a migration still running once the window elapses is
// acknowledged with "migration_started"; clients poll BarrierStatus.
func (s *qrdbServiceServer) BarrierEnableEncryption(ctx context.Context, req *pb.BarrierEnableEncryptionRequest) (*pb.BarrierEnableEncryptionResponse, error) {
	// The method is identity-gated: which permission authorizes the call
	// depends on the barrier the request names, so it is decided here,
	// before anything else. No tenant named is the node's own barrier, a
	// platform-operator act under seal:enable_encryption; a named tenant is
	// that tenant's barrier, authorized under tenant_barrier:enable_encryption
	// for THAT tenant, exactly like every other tenant-barrier operation.
	scope := service.BarrierScope{Kind: service.ScopeSystem}
	target, perm := tenantid.System, permission.SealEnableEncryption
	if req.GetTenantId() != "" {
		scope = service.BarrierScope{Kind: service.ScopeTenant, TenantID: req.GetTenantId()}
		target, perm = req.GetTenantId(), permission.TenantBarrierEnableEncryption
	}
	if err := authorizeTenantBarrierOp(ctx, s.authMW, target, perm); err != nil {
		return nil, authzError(err)
	}
	if s.encMigrator == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "encryption-migration"})
	}

	mechanism := seal.MechanismSwap
	if req.GetMechanism() == string(seal.MechanismInPlace) {
		mechanism = seal.MechanismInPlace
	}
	availability := seal.AvailabilityQuiesced
	if req.GetAvailability() == string(seal.AvailabilityOnline) {
		availability = seal.AvailabilityOnline
	}

	// The request's passphrase is copied into protected memory, and the
	// migration reads only that credential. The decoded copy is wiped when
	// this handler returns, not at once: a follower forwards the unchanged
	// request to the leader, and a wiped passphrase would seal the leader's
	// barrier under zeros.
	creds, err := seal.CredentialsFor(ctx, req.GetPassphrase())
	defer secure.Wipe(req.GetPassphrase())
	if err != nil {
		return nil, mapServiceError(err)
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
		seal.DestroyCredentials(creds)
		return nil, mapServiceError(err)
	}

	// The migration runs on the request's context without its cancellation:
	// the request context is cancelled once this call returns, and the
	// migration must continue independent of its lifetime, while still
	// carrying the request's values (the caller's identity and tenant), as
	// on every transport.
	migrationCtx := context.WithoutCancel(ctx)
	migrator := s.encMigrator
	resultCh := make(chan error, 1)
	go func() {
		// The migration OWNS the credential from here: it outlives this
		// request, so destroying it in the handler would pull the
		// passphrase out from under a running migration. The destroy
		// belongs to the last reader, and that is this goroutine.
		defer seal.DestroyCredentials(creds)
		resultCh <- migrator.EnableEncryption(migrationCtx, scope, opts)
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			return s.enableEncryptionRefused(ctx, req, err)
		}
		// Completed within the window with no error: an unusually fast
		// migration (e.g. an empty barrier) already finished. Fall through
		// and report "migration_started" — BarrierStatus already reflects
		// the committed strategy, so the wording is accurate either way.
	case <-time.After(grpcMigrationAcceptWindow):
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

	return &pb.BarrierEnableEncryptionResponse{
		Status: "migration_started",
		Scope:  string(scope.Kind),
	}, nil
}

// enableEncryptionRefused answers a BarrierEnableEncryption the migrator
// refused. A follower's not-leader refusal forwards the identical request to
// the system-shard leader, as CeremonyInitialize does, while the passphrase
// is still intact: the handler wipes it on return, so the replica-forwarding
// interceptor, which runs after the handler, must never replay it. When the
// request cannot be forwarded the refusal is answered Unavailable without the
// leader's address, which is what would arm that replay.
func (s *qrdbServiceServer) enableEncryptionRefused(ctx context.Context, req *pb.BarrierEnableEncryptionRequest, err error) (*pb.BarrierEnableEncryptionResponse, error) {
	var notLeader *service.NotLeaderError
	if !errors.As(err, &notLeader) {
		return nil, mapServiceError(err)
	}
	if s.replicaForwarder != nil && !forwardedIn(ctx) {
		if reply, ferr, forwarded := s.replicaForwarder.forwardAdminWrite(ctx, pb.QRDBService_BarrierEnableEncryption_FullMethodName, req, notLeader.LeaderAddress); forwarded {
			if ferr != nil {
				return nil, ferr
			}
			if resp, ok := reply.(*pb.BarrierEnableEncryptionResponse); ok {
				return resp, nil
			}
		}
	}
	return nil, mapServiceError(&service.NotLeaderError{ShardID: notLeader.ShardID, LeaderID: notLeader.LeaderID})
}

// BarrierRecoveryGenerate creates the independent recovery-key domain, split
// to the enrolled recovery holders, under SO PIN authorization. No share
// material is returned.
func (s *qrdbServiceServer) BarrierRecoveryGenerate(ctx context.Context, req *pb.BarrierRecoveryGenerateRequest) (*pb.BarrierRecoveryKeysResponse, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}
	if err := pin.RequireSOPIN(ctx, s.pinBackend, req.GetSoPin()); err != nil {
		return nil, mapServiceError(err)
	}
	info, err := s.barrierSvc.BarrierGenerateRecoveryKeys(ctx, int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.BarrierRecoveryKeysResponse{
		Threshold:  int32(info.Threshold),  //nolint:gosec
		Total:      int32(info.Total),      //nolint:gosec
		ShareCount: int32(info.ShareCount), //nolint:gosec
	}, nil
}

// BarrierRecoveryDelete removes recovery key metadata from storage.
func (s *qrdbServiceServer) BarrierRecoveryDelete(ctx context.Context, _ *pb.BarrierRecoveryDeleteRequest) (*emptypb.Empty, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(ctx); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// BarrierRecoveryStatus reports whether recovery key metadata exists in storage.
func (s *qrdbServiceServer) BarrierRecoveryStatus(ctx context.Context, _ *emptypb.Empty) (*pb.BarrierRecoveryStatusResponse, error) {
	if s.barrierSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "barrier"})
	}

	has, err := s.barrierSvc.BarrierHasRecoveryKeys(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.BarrierRecoveryStatusResponse{
		HasRecoveryKeys: has,
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
		return "", &service.ClientCertRequiredError{}
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", &service.ClientCertRequiredError{}
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", &service.ClientCertRequiredError{}
	}
	return mtls.Thumbprint(tlsInfo.State.PeerCertificates[0].Raw), nil
}

// ---------------------------------------------------------------------------
// Error mapping helpers
// ---------------------------------------------------------------------------
