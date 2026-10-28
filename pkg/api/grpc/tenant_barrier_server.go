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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/holderpb"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Per-tenant barrier gRPC service (native protobuf: qrdb.v1.TenantBarrierService)
//
// Handlers wrap the tenantpkg.TenantServicer implementation and translate
// domain errors via mapServiceError. The fine-grained, tenant-scoped
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

// authzError converts a failed tenant-barrier authorization gate into the gRPC
// code the error ACTUALLY MEANS, rather than assuming every refusal is a
// permission problem.
//
// Eleven RPCs on this surface each wrote codes.PermissionDenied directly. That
// was survivable only while nothing but a denial came back out of the gate;
// go-qrdb#335 ended it, because the middleware now refuses a decision whose
// audit record it could not write and *middleware.AuditError escapes these
// gates RELIABLY -- the bootstrap-window fallback's own audit write fails for
// the same reason the first one did. PermissionDenied is terminal in every
// gRPC client library, so an operator recovering a node with a full audit disk
// would be told, without recourse, that they may not unseal it.
//
// The fallback stays codes.PermissionDenied rather than the interceptors'
// codes.Unauthenticated: these gates run AFTER authentication, and this keeps
// the answer identical to what these RPCs returned before for anything the
// middleware does not classify.
//
// It mirrors writeAuthzErrorJSON on the REST server and the wire-status
// mapping on the QUIC server, all three over the same
// middleware.ClassifyAuthError, so the four transports cannot drift again.
func authzError(err error) error {
	return authmw.GRPCError(err, codes.PermissionDenied)
}

// authorizeTenantBarrierOp enforces the tenant-scoped authorization gate for a
// per-tenant barrier operation against the target tenant.
func (s *tenantBarrierServer) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	return authorizeTenantBarrierOp(ctx, s.authMW, tenantID, perm)
}

// authorizeTenantBarrierOp authorizes perm against the tenant a barrier
// operation names (not the caller's ambient tenant), the one gate every
// tenant-barrier handler shares, including BarrierEnableEncryption on a named
// tenant (go-qrdb#791). When mw is nil the gate is a no-op (unauthenticated
// deployment).
func authorizeTenantBarrierOp(ctx context.Context, mw *authmw.AuthMiddleware, tenantID string, perm permission.Permission) error {
	if mw == nil {
		return nil
	}
	return mw.AuthorizeTenant(ctx, tenantID, perm)
}

// beginTenantBarrierInit issues the init nonce through the shared
// trust-on-first-init rule (middleware.BeginTenantBarrierInit -- the single
// cross-transport home of the bootstrap-window semantics and the init
// proof). When authMW is nil the deployment is unauthenticated: there is no
// identity to prove, and the empty challenge says so.
func (s *tenantBarrierServer) beginTenantBarrierInit(ctx context.Context, tenantID string) (*initproof.Challenge, error) {
	if s.authMW == nil {
		return &initproof.Challenge{}, nil
	}
	return s.authMW.BeginTenantBarrierInit(ctx, tenantID, s.svc)
}

// authorizeTenantBarrierInit authorizes a tenant barrier initialization and
// redeems its proof of possession (middleware.AuthorizeTenantBarrierInitProof).
// When authMW is nil the gate is a no-op (unauthenticated deployment).
func (s *tenantBarrierServer) authorizeTenantBarrierInit(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInitProof(ctx, tenantID, s.svc, proof)
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

// InitBegin issues the nonce InitComplete must sign, bound to this
// connection's TLS exporter.
func (s *tenantBarrierServer) InitBegin(ctx context.Context, req *pb.TenantBarrierInitBeginRequest) (*pb.TenantBarrierInitBeginResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	challenge, err := s.beginTenantBarrierInit(ctx, req.GetTenantId())
	if err != nil {
		return nil, authzError(err)
	}
	out := &pb.TenantBarrierInitBeginResponse{Nonce: challenge.Nonce}
	if !challenge.ExpiresAt.IsZero() {
		out.Expires = challenge.ExpiresAt.Unix()
	}
	return out, nil
}

// InitComplete initializes a per-tenant barrier, given proof of possession
// of the first admin's key over this connection. The response is share-free:
// shares are distributed to officers via the officer claim flow.
func (s *tenantBarrierServer) InitComplete(ctx context.Context, req *pb.TenantBarrierInitRequest) (*pb.TenantBarrierInitResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	// The decoded credential is this handler's to own and to destroy. The
	// tenant service borrows it for the call and retains nothing, so a
	// deferred wipe covers every exit path including the error ones.
	defer secure.Wipe(req.GetPassphrase())
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	proof := &authmw.TenantBarrierInitProof{
		Nonce:     req.GetNonce(),
		PublicKey: req.GetPublicKey(),
		Signature: req.GetSignature(),
	}
	if err := s.authorizeTenantBarrierInit(ctx, req.GetTenantId(), proof); err != nil {
		return nil, authzError(err)
	}

	var roster *tenantpkg.TenantBarrierShamirRoster
	if len(req.GetOfficers()) > 0 {
		officers, err := holderpb.TenantBarrierRosterFromPB(req.GetOfficers())
		if err != nil {
			return nil, mapServiceError(err)
		}
		roster = &tenantpkg.TenantBarrierShamirRoster{
			Officers: officers,
			Policy:   seal.FactorPolicy(req.GetPolicy()),
		}
	}

	result, err := s.svc.TenantBarrierInitWithStrategy(ctx, req.GetTenantId(), req.GetPassphrase(),
		int(req.GetThreshold()), int(req.GetShares()), seal.StrategyID(req.GetStrategy()), roster,
		tenantpkg.WithInitStatement(req.GetInitStatement()),
		tenantpkg.WithPreparedShamir(req.GetRoster(), req.GetUnsealKeyCheck()))
	if err != nil {
		return nil, mapServiceError(err)
	}

	out := tenantBarrierInitResponse(req.GetTenantId(), result)
	if result == nil {
		out.Strategy = req.GetStrategy()
	}
	return out, nil
}

// SignedInitBegin prepares the tenant barrier's init statement for a first
// admin who signs it away from this connection. Its authorization is
// AuthorizeTenantBarrierInit's: RBAC, or the tenant's trust-on-first-init
// window. No channel-bound nonce is asked for: SignedInitComplete admits only
// a statement the tenant's anchored first admin signed, which is the proof of
// possession InitComplete's nonce stands in for.
func (s *tenantBarrierServer) SignedInitBegin(ctx context.Context, req *pb.TenantBarrierSignedInitBeginRequest) (*pb.TenantBarrierSignedInitBeginResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSignedInit(ctx, req.GetTenantId()); err != nil {
		return nil, authzError(err)
	}
	var roster *tenantpkg.TenantBarrierShamirRoster
	if len(req.GetOfficers()) > 0 {
		officers, err := holderpb.TenantBarrierRosterFromPB(req.GetOfficers())
		if err != nil {
			return nil, mapServiceError(err)
		}
		roster = &tenantpkg.TenantBarrierShamirRoster{Officers: officers, Policy: seal.FactorPolicy(req.GetPolicy())}
	}
	begun, err := s.svc.TenantBarrierInitBegin(ctx, &tenantpkg.TenantBarrierInitBeginRequest{
		TenantID:        req.GetTenantId(),
		Strategy:        seal.StrategyID(req.GetStrategy()),
		KeyRef:          req.GetKeyRef(),
		Threshold:       int(req.GetThreshold()),
		Shares:          int(req.GetShares()),
		Roster:          roster,
		SignerPublicKey: req.GetPublicKey(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.TenantBarrierSignedInitBeginResponse{
		TenantId:    begun.TenantID,
		Strategy:    begun.Strategy,
		Reference:   begun.Reference,
		Statement:   begun.Statement,
		Expires:     begun.ExpiresAt.Unix(),
		Threshold:   int32(begun.Threshold),   //nolint:gosec
		TotalShares: int32(begun.TotalShares), //nolint:gosec
		ShareCount:  int32(begun.ShareCount),  //nolint:gosec
		Holders:     holderpb.TenantBarrierHoldersToPB(begun.Holders),
	}, nil
}

// SignedInitComplete redeems SignedInitBegin's reference with the first
// admin's detached signature and initializes the barrier under the
// statement. Authorized as SignedInitBegin.
func (s *tenantBarrierServer) SignedInitComplete(ctx context.Context, req *pb.TenantBarrierSignedInitCompleteRequest) (*pb.TenantBarrierInitResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	// The decoded credential is this handler's to own and to destroy, as in
	// InitComplete: it moves into protected memory at once.
	passphrase, err := tenantpkg.NewSignedInitPassphrase(req.GetPassphrase())
	if err != nil {
		return nil, mapServiceError(err)
	}
	defer passphrase.Destroy()
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSignedInit(ctx, req.GetTenantId()); err != nil {
		return nil, authzError(err)
	}
	result, err := s.svc.TenantBarrierInitComplete(ctx, &tenantpkg.TenantBarrierInitCompleteRequest{
		TenantID:   req.GetTenantId(),
		Reference:  req.GetReference(),
		Signature:  req.GetSignature(),
		Passphrase: passphrase,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return tenantBarrierInitResponse(req.GetTenantId(), result), nil
}

// authorizeTenantBarrierSignedInit authorizes either half of a signed init.
func (s *tenantBarrierServer) authorizeTenantBarrierSignedInit(ctx context.Context, tenantID string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInit(ctx, tenantID, s.svc)
}

// tenantBarrierInitResponse is result on the wire; a nil result reports
// tenantID alone.
func tenantBarrierInitResponse(tenantID string, result *tenantpkg.TenantBarrierInitResult) *pb.TenantBarrierInitResponse {
	if result == nil {
		return &pb.TenantBarrierInitResponse{TenantId: tenantID}
	}
	return &pb.TenantBarrierInitResponse{
		TenantId:       result.TenantID,
		Strategy:       result.Strategy,
		Threshold:      int32(result.Threshold),   //nolint:gosec
		TotalShares:    int32(result.TotalShares), //nolint:gosec
		ShareCount:     int32(result.ShareCount),  //nolint:gosec
		Holders:        holderpb.TenantBarrierHoldersToPB(result.Holders),
		Roster:         result.Roster,
		UnsealKeyCheck: result.UnsealKeyCheck,
		AdminPending:   result.AdminPending,
	}
}

// Unseal unseals a per-tenant barrier using a single key (passphrase strategy).
func (s *tenantBarrierServer) Unseal(ctx context.Context, req *pb.TenantBarrierUnsealRequest) (*emptypb.Empty, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSealedOp(ctx, req.GetTenantId(), permission.TenantBarrierUnseal); err != nil {
		return nil, authzError(err)
	}
	if err := s.svc.TenantBarrierUnseal(ctx, req.GetTenantId(), req.GetKey()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// Seal seals a per-tenant barrier.
func (s *tenantBarrierServer) Seal(ctx context.Context, req *pb.TenantBarrierSealRequest) (*emptypb.Empty, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierSeal); err != nil {
		return nil, authzError(err)
	}
	if err := s.svc.TenantBarrierSeal(ctx, req.GetTenantId()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// Status reports the per-tenant barrier's sealed/initialized state and strategy.
func (s *tenantBarrierServer) Status(ctx context.Context, req *pb.TenantBarrierStatusRequest) (*pb.TenantBarrierStatusResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierSealedOp(ctx, req.GetTenantId(), permission.TenantBarrierStatus); err != nil {
		return nil, authzError(err)
	}
	st, err := s.svc.TenantBarrierStatus(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
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
		ClusterSeal:     ClusterSealToProto(st.ClusterSeal),
		KeyFingerprint:  st.KeyFingerprint,
		Nodes:           tenantBarrierNodesToProto(st.Nodes),
	}, nil
}

// tenantBarrierNodesToProto encodes the nodes serving a tenant barrier, each
// with its API endpoints: the nodes a client fans a barrier init out to.
func tenantBarrierNodesToProto(nodes []tenantpkg.BarrierNode) []*pb.TenantBarrierNode {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]*pb.TenantBarrierNode, len(nodes))
	for i, n := range nodes {
		out[i] = &pb.TenantBarrierNode{Node: n.Node, Endpoints: n.Endpoints}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tenant barrier Security Officer handlers (new: share-distribution model)
// ---------------------------------------------------------------------------

// OfficerRegister registers a Security Officer for the named tenant's barrier.
func (s *tenantBarrierServer) OfficerRegister(ctx context.Context, req *pb.TenantBarrierOfficerRegisterRequest) (*pb.TenantBarrierOfficerRegisterResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerRegister); err != nil {
		return nil, authzError(err)
	}

	regReq, err := holderpb.TenantRegisterFromPB(req)
	if err != nil {
		return nil, mapServiceError(err)
	}
	svcReq := service.HolderRegistration(ctx, regReq)
	result, err := s.svc.TenantBarrierOfficerRegister(ctx, req.GetTenantId(), &svcReq)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return holderpb.TenantRegisterToPB(result), nil
}

// OfficerList lists all registered Security Officers for the named tenant's barrier.
func (s *tenantBarrierServer) OfficerList(ctx context.Context, req *pb.TenantBarrierOfficerListRequest) (*pb.TenantBarrierOfficerListResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerList); err != nil {
		return nil, authzError(err)
	}

	officers, err := s.svc.TenantBarrierOfficerList(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.TenantBarrierOfficerListResponse{
		Officers: holderpb.StatusesToPB(officers),
	}, nil
}

// ShareClaimBegin starts the share-claim handshake for the calling Security
// Officer of the named tenant's barrier.
func (s *tenantBarrierServer) ShareClaimBegin(ctx context.Context, req *pb.TenantBarrierShareClaimBeginRequest) (*pb.TenantBarrierShareClaimBeginResponse, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	// Share-claim is identity-gated: the registered officer's mTLS
	// certificate is the credential; the service enforces the per-tenant
	// registry lookup, thumbprint match, and nonce signature.
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, authzError(err)
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	challenge, err := s.svc.TenantBarrierShareClaimBegin(ctx, req.GetTenantId(), req.GetUsername(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	// Identity-gated: see ShareClaimBegin.
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, authzError(err)
	}

	thumbprint, err := peerCertThumbprint(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}

	result, err := s.svc.TenantBarrierShareClaimComplete(ctx, req.GetTenantId(), req.GetUsername(), req.GetNonceHex(), req.GetSignature(), thumbprint)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.TenantBarrierShareClaimCompleteResponse{
		ShareIndex: int32(result.ShareIndex), //nolint:gosec
		Envelope:   result.Envelope,
	}, nil
}

// ---------------------------------------------------------------------------
// Error mapping helpers for tenant barrier ceremony operations
// ---------------------------------------------------------------------------

// QuorumBegin issues a quorum challenge to a tenant share holder. It is
// identity-gated: the factor proof at submission is the authorization, and
// the tenant service confines the caller to its own tenant.
func (s *tenantBarrierServer) QuorumBegin(ctx context.Context, req *pb.TenantBarrierQuorumBeginRequest) (*pb.BarrierQuorumChallenge, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, authzError(err)
	}
	ch, err := s.svc.TenantBarrierQuorumBegin(ctx, req.GetTenantId(), seal.QuorumOp(req.GetOp()), req.GetUsername(), req.GetFactor())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.ChallengeToPB(ch), nil
}

// QuorumSubmit submits one authenticated tenant holder's share.
func (s *tenantBarrierServer) QuorumSubmit(ctx context.Context, req *pb.TenantBarrierQuorumSubmitRequest) (*pb.BarrierQuorumOutcome, error) {
	// The decoded share is this handler's to destroy on every path.
	defer secure.Wipe(req.GetShare())
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.IdentityGated); err != nil {
		return nil, authzError(err)
	}
	out, err := s.svc.TenantBarrierQuorumSubmit(ctx, req.GetTenantId(), holderpb.TenantSubmitFromPB(req, optionalPeerThumbprint(ctx)))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.OutcomeToPB(service.QuorumOutcomeFrom(out)), nil
}

// RekeyStart opens a rekey round on the tenant barrier.
func (s *tenantBarrierServer) RekeyStart(ctx context.Context, req *pb.TenantBarrierRekeyStartRequest) (*pb.BarrierQuorumRound, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierRekey); err != nil {
		return nil, authzError(err)
	}
	round, err := s.svc.TenantBarrierRekeyStart(ctx, req.GetTenantId(), int(req.GetThreshold()), seal.FactorPolicy(req.GetPolicy()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RoundToPB(round), nil
}

// OfficerRevoke revokes a tenant holder through a quorum-gated rekey.
func (s *tenantBarrierServer) OfficerRevoke(ctx context.Context, req *pb.TenantBarrierOfficerRevokeRequest) (*pb.BarrierQuorumRound, error) {
	if s.svc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "tenant barrier"})
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if err := s.authorizeTenantBarrierOp(ctx, req.GetTenantId(), permission.TenantBarrierOfficerRevoke); err != nil {
		return nil, authzError(err)
	}
	round, err := s.svc.TenantBarrierOfficerRevoke(ctx, req.GetTenantId(), req.GetUsername(), int(req.GetThreshold()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return holderpb.RoundToPB(round), nil
}
