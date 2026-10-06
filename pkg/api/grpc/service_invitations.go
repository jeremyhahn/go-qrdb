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

	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// InviteUser creates a pending account in the caller's own tenant -- or
// re-invites one that is still pending -- and returns its one-time enrolment
// token. It is gated and authorized identically to IssueCert/EnrollCert: the
// request names no tenant of its own, so authorizeCertTenant checks the
// caller's own tenant against users:invite.
func (s *qrdbServiceServer) InviteUser(ctx context.Context, req *pb.InviteUserRequest) (*pb.InvitationResponse, error) {
	if s.invitationSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "invitations"})
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.UsersInvite); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	if err := s.authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, s.roleSvc, tenantID, req.GetRoleIds()); err != nil {
		return nil, mapServiceError(err)
	}
	inv, err := s.invitationSvc.InviteUser(ctx, &service.InviteUserRequest{
		TenantID:          tenantID,
		Username:          req.GetUsername(),
		Email:             req.GetEmail(),
		Name:              req.GetName(),
		RoleIDs:           req.GetRoleIds(),
		KeyPinFingerprint: req.GetKeyPinFingerprint(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return invitationToProto(inv), nil
}

// RedeemInvitation redeems a one-time token with the redeemer's own key. It
// is PUBLIC: Start registers QRDBService_RedeemInvitation_FullMethodName with
// AuthMiddleware.RegisterPublicGRPCMethods, so the unary interceptor skips
// authentication and authorization for it entirely, and this handler carries
// no identity or tenant check of its own -- the token is the authority. The
// caller's remote address, needed for the service's per-source redemption
// throttle, is already in ctx: RemoteAddrUnaryInterceptor runs unconditionally,
// before the public-method bypass is even checked.
func (s *qrdbServiceServer) RedeemInvitation(ctx context.Context, req *pb.RedeemInvitationRequest) (*pb.EnrolledCertResponse, error) {
	if s.invitationSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "invitations"})
	}
	enrolled, err := s.invitationSvc.RedeemInvitation(ctx, &service.RedeemInvitationRequest{
		TenantID: req.GetTenantId(),
		Token:    req.GetToken(),
		CSRPEM:   []byte(req.GetCsrPem()),
		CertPEM:  []byte(req.GetCertPem()),
		ChainPEM: []byte(req.GetChainPem()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return enrolledToProto(enrolled), nil
}

// invitationToProto converts a service.Invitation to its wire type.
func invitationToProto(inv *service.Invitation) *pb.InvitationResponse {
	return &pb.InvitationResponse{
		Id:                inv.ID,
		Token:             inv.Token,
		ExpiresAt:         inv.ExpiresAt.UTC().Format(time.RFC3339),
		UserId:            inv.UserID,
		Username:          inv.Username,
		TenantId:          inv.TenantID,
		Created:           inv.Created,
		KeyPinFingerprint: inv.KeyPinFingerprint,
	}
}
