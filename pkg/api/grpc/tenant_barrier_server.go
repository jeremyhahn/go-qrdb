// Copyright 2026 Jeremy Hahn
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
	"encoding/hex"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Per-tenant barrier gRPC service (native protobuf: qrdb.v1.TenantBarrierService)
//
// Handlers wrap the tenantpkg.TenantServicer implementation and translate
// domain errors via mapTenantError. The fine-grained, tenant-scoped
// authorization gate is enforced against the TRUE target tenant carried in the
// request (ScopeTenantOnly denies the system tenant and any cross-tenant
// access). The same TenantBarrierService descriptor is served over both the
// gRPC and Unix (gRPC-over-UDS) transports.
// ---------------------------------------------------------------------------

// tenantBarrierServer implements the generated qrdb.v1.TenantBarrierServiceServer.
type tenantBarrierServer struct {
	pb.UnimplementedTenantBarrierServiceServer

	svc    tenantpkg.TenantServicer
	authMW *authmw.AuthMiddleware
}

// authorizeTenantBarrierOp enforces the tenant-scoped authorization gate for a
// per-tenant barrier operation against the target tenant. When authMW is nil
// the gate is a no-op (unauthenticated deployment).
func (s *tenantBarrierServer) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenant(ctx, tenantID, perm)
}

// authorizeTenantBarrierInit authorizes a tenant barrier initialization via
// the shared trust-on-first-init rule (middleware.AuthorizeTenantBarrierInit
// — the single cross-transport home of the bootstrap-window semantics). When
// authMW is nil the gate is a no-op (unauthenticated deployment).
func (s *tenantBarrierServer) authorizeTenantBarrierInit(ctx context.Context, tenantID string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInit(ctx, tenantID, s.svc)
}

// authorizeTenantBarrierSealedOp authorizes the tenant barrier operations
// that must remain reachable while the tenant's IAM itself is unreachable
// (status/unseal) via the shared sealed-window rule
// (middleware.AuthorizeTenantBarrierSealedOp). When authMW is nil the gate
// is a no-op.
func (s *tenantBarrierServer) authorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierSealedOp(ctx, tenantID, perm)
}

// peerCertThumbprintTenant extracts the mTLS leaf certificate thumbprint from
// the gRPC peer context for the tenant barrier server.
func peerCertThumbprintTenant(ctx context.Context) (string, error) {
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

// RegisterTenantBarrierService registers the per-tenant barrier service on the
// given gRPC server. When svc is nil the service is not registered, so callers
// that have no tenant servicer wired in can safely invoke this helper. The
// authMW (which may be nil) supplies the tenant-scoped authorization gate.
func RegisterTenantBarrierService(srv *grpc.Server, svc tenantpkg.TenantServicer, authMW *authmw.AuthMiddleware) {
	if svc == nil {
		return
	}
	pb.RegisterTenantBarrierServiceServer(srv, &tenantBarrierServer{svc: svc, authMW: authMW})
}

// Init initializes a per-tenant barrier. The response is share-free: shares
// are distributed to officers via the officer claim flow.
func (s *tenantBarrierServer) Init(ctx context.Context, req *pb.TenantBarrierInitRequest) (*pb.TenantBarrierInitResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierInit(ctx, req.GetTenantId()); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	result, err := s.svc.TenantBarrierInitWithStrategy(ctx, req.GetTenantId(), req.GetPassphrase(),
		int(req.GetThreshold()), int(req.GetShares()), seal.StrategyID(req.GetStrategy()))
	if err != nil {
		return nil, mapTenantError(err)
	}

	out := &pb.TenantBarrierInitResponse{TenantId: req.GetTenantId(), Strategy: req.GetStrategy()}
	if result != nil {
		out = &pb.TenantBarrierInitResponse{
			TenantId:    result.TenantID,
			Strategy:    result.Strategy,
			Threshold:   int32(result.Threshold),   //nolint:gosec
			TotalShares: int32(result.TotalShares), //nolint:gosec
			ShareCount:  int32(result.ShareCount),  //nolint:gosec
		}
	}
	return out, nil
}

// Unseal unseals a per-tenant barrier using a single key (passphrase strategy).
func (s *tenantBarrierServer) Unseal(ctx context.Context, req *pb.TenantBarrierUnsealRequest) (*emptypb.Empty, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSealedOp(ctx, req.GetTenantId(), permission.TenantBarrierUnseal); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	if err := s.svc.TenantBarrierUnseal(ctx, req.GetTenantId(), req.GetKey()); err != nil {
		return nil, mapTenantError(err)
	}
	return &emptypb.Empty{}, nil
}

// UnsealShare submits a single Shamir share toward the tenant barrier's quorum
// threshold and returns the quorum progress.
func (s *tenantBarrierServer) UnsealShare(ctx context.Context, req *pb.TenantBarrierUnsealShareRequest) (*pb.TenantBarrierUnsealProgress, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSealedOp(ctx, req.GetTenantId(), permission.TenantBarrierUnseal); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	prog, err := s.svc.TenantBarrierUnsealShare(ctx, req.GetTenantId(), req.GetShare())
	if err != nil {
		return nil, mapTenantError(err)
	}
	if prog == nil {
		return nil, status.Error(codes.Internal, "tenant barrier unseal-share: nil response")
	}
	return &pb.TenantBarrierUnsealProgress{
		TenantId:  prog.TenantID,
		Required:  int32(prog.Required),  //nolint:gosec
		Submitted: int32(prog.Submitted), //nolint:gosec
		Complete:  prog.Complete,
		Sealed:    prog.Sealed,
	}, nil
}

// Seal seals a per-tenant barrier.
func (s *tenantBarrierServer) Seal(ctx context.Context, req *pb.TenantBarrierSealRequest) (*emptypb.Empty, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierSeal); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	if err := s.svc.TenantBarrierSeal(ctx, req.GetTenantId()); err != nil {
		return nil, mapTenantError(err)
	}
	return &emptypb.Empty{}, nil
}

// Status reports the per-tenant barrier's sealed/initialized state and strategy.
func (s *tenantBarrierServer) Status(ctx context.Context, req *pb.TenantBarrierStatusRequest) (*pb.TenantBarrierStatusResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSealedOp(ctx, req.GetTenantId(), permission.TenantBarrierStatus); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	st, err := s.svc.TenantBarrierStatus(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapTenantError(err)
	}
	if st == nil {
		return nil, status.Error(codes.Internal, "tenant barrier status: nil response")
	}
	return &pb.TenantBarrierStatusResponse{
		TenantId:        st.TenantID,
		Sealed:          st.Sealed,
		Initialized:     st.Initialized,
		Strategy:        st.Strategy,
		HardwareBacked:  st.HardwareBacked,
		ShamirThreshold: int32(st.ShamirThreshold), //nolint:gosec
		ShamirTotal:     int32(st.ShamirTotal),     //nolint:gosec
	}, nil
}

// ---------------------------------------------------------------------------
// Tenant barrier Security Officer handlers (new: share-distribution model)
// ---------------------------------------------------------------------------

// OfficerRegister registers a Security Officer for the named tenant's barrier.
func (s *tenantBarrierServer) OfficerRegister(ctx context.Context, req *pb.TenantBarrierOfficerRegisterRequest) (*pb.TenantBarrierOfficerRegisterResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerRegister); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	svcReq := &ceremony.RegisterOfficerRequest{
		Username:          req.GetUsername(),
		CSRPEM:            req.GetCsrPem(),
		AuthenticatorType: ceremony.AuthenticatorType(req.GetAuthenticatorType()),
	}
	result, err := s.svc.TenantBarrierOfficerRegister(ctx, req.GetTenantId(), svcReq)
	if err != nil {
		return nil, mapTenantBarrierCeremonyError(err)
	}

	return &pb.TenantBarrierOfficerRegisterResponse{
		CertPem:    result.CertPEM,
		ShareIndex: int32(result.ShareIndex), //nolint:gosec
	}, nil
}

// OfficerList lists all registered Security Officers for the named tenant's barrier.
func (s *tenantBarrierServer) OfficerList(ctx context.Context, req *pb.TenantBarrierOfficerListRequest) (*pb.TenantBarrierOfficerListResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerList); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	officers, err := s.svc.TenantBarrierOfficerList(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapTenantBarrierCeremonyError(err)
	}

	return &pb.TenantBarrierOfficerListResponse{
		Officers: tenantOfficerStatusesToProto(officers),
	}, nil
}

// ShareClaimBegin starts the share-claim handshake for the calling Security
// Officer of the named tenant's barrier.
func (s *tenantBarrierServer) ShareClaimBegin(ctx context.Context, req *pb.TenantBarrierShareClaimBeginRequest) (*pb.TenantBarrierShareClaimBeginResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	// Share-claim is identity-gated: the registered officer's mTLS
	// certificate is the credential; the service enforces the per-tenant
	// registry lookup, thumbprint match, and nonce signature.
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	thumbprint, err := peerCertThumbprintTenant(ctx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	challenge, err := s.svc.TenantBarrierShareClaimBegin(ctx, req.GetTenantId(), req.GetUsername(), thumbprint)
	if err != nil {
		return nil, mapTenantBarrierCeremonyError(err)
	}

	return &pb.TenantBarrierShareClaimBeginResponse{
		NonceHex: hex.EncodeToString(challenge.Nonce),
		Expires:  challenge.ExpiresAt.Unix(),
	}, nil
}

// ShareClaimComplete completes the share-claim handshake and returns the
// Shamir share exactly once over the SO's own mTLS session.
func (s *tenantBarrierServer) ShareClaimComplete(ctx context.Context, req *pb.TenantBarrierShareClaimCompleteRequest) (*pb.TenantBarrierShareClaimCompleteResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	// Identity-gated: see ShareClaimBegin.
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	thumbprint, err := peerCertThumbprintTenant(ctx)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	result, err := s.svc.TenantBarrierShareClaimComplete(ctx, req.GetTenantId(), req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapTenantBarrierCeremonyError(err)
	}

	return &pb.TenantBarrierShareClaimCompleteResponse{
		ShareBytes: result.ShareBytes,
	}, nil
}

// OfficerClaimStatus returns per-officer claim status for the named tenant's
// barrier. The response contains only public metadata — no share bytes.
func (s *tenantBarrierServer) OfficerClaimStatus(ctx context.Context, req *pb.TenantBarrierOfficerClaimStatusRequest) (*pb.TenantBarrierOfficerClaimStatusResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerList); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	officers, err := s.svc.TenantBarrierOfficerClaimStatus(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapTenantBarrierCeremonyError(err)
	}

	return &pb.TenantBarrierOfficerClaimStatusResponse{
		Officers: tenantOfficerStatusesToProto(officers),
	}, nil
}

// ShamirSealVote submits a Shamir share as a seal vote for the named tenant's
// barrier. When the threshold is reached the tenant barrier is sealed.
func (s *tenantBarrierServer) ShamirSealVote(ctx context.Context, req *pb.TenantBarrierShamirSealVoteRequest) (*pb.TenantBarrierShamirSealVoteResponse, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierSealVote); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	progress, err := s.svc.TenantBarrierShamirSealVote(ctx, req.GetTenantId(), req.GetShareValue())
	if err != nil {
		return nil, mapTenantBarrierCeremonyError(err)
	}

	return &pb.TenantBarrierShamirSealVoteResponse{
		Required:  int32(progress.Required),  //nolint:gosec
		Submitted: int32(progress.Submitted), //nolint:gosec
		Complete:  progress.Complete,
	}, nil
}

// ---------------------------------------------------------------------------
// Error mapping helpers for tenant barrier ceremony operations
// ---------------------------------------------------------------------------

// mapTenantBarrierCeremonyError maps ceremony and seal errors from the tenant
// barrier SO flow to gRPC status codes.
func mapTenantBarrierCeremonyError(err error) error {
	if err == nil {
		return nil
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

	return mapTenantError(err)
}

// tenantOfficerStatusesToProto converts ceremony OfficerStatus slices to
// proto OfficerStatus messages.
func tenantOfficerStatusesToProto(officers []*ceremony.OfficerStatus) []*pb.OfficerStatus {
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
