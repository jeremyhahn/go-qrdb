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
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// userAuthFactorServer implements the generated pb.UserAuthFactorServiceServer.
type userAuthFactorServer struct {
	pb.UnimplementedUserAuthFactorServiceServer

	svc service.UserAuthFactorServicer
	// userSvc resolves the user record after a factor login so the response
	// can be enriched with username, tenant, and role set. Optional: when nil
	// the login response carries only the resolved user ID and factor.
	userSvc user.UserServicer
}

// RegisterUserAuthFactorService registers the user-side auth factor service
// with a gRPC server. When svc is nil, the service is not registered. The
// userSvc is optional and enriches the login response with the user's
// username, tenant, and roles; a nil userSvc leaves those fields empty.
func RegisterUserAuthFactorService(srv *grpc.Server, svc service.UserAuthFactorServicer, userSvc user.UserServicer) {
	if svc == nil {
		return
	}
	pb.RegisterUserAuthFactorServiceServer(srv, &userAuthFactorServer{svc: svc, userSvc: userSvc})
}

// Add handles the UserAuthFactorService.Add RPC.
func (s *userAuthFactorServer) Add(ctx context.Context, req *pb.UserAuthFactorAddRequest) (*pb.UserAuthFactorAddResponse, error) {
	factor := userFactorFromAddProto(req)
	if err := s.svc.Add(ctx, req.GetUserId(), factor); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	return &pb.UserAuthFactorAddResponse{Factor: userFactorToProto(factor)}, nil
}

// List handles the UserAuthFactorService.List RPC.
func (s *userAuthFactorServer) List(ctx context.Context, req *pb.UserAuthFactorListRequest) (*pb.UserAuthFactorListResponse, error) {
	factors, err := s.svc.List(ctx, req.GetUserId())
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	resp := &pb.UserAuthFactorListResponse{
		Factors: make([]*pb.UserAuthFactorRecord, len(factors)),
		Total:   int32(len(factors)), //nolint:gosec // factor count is bounded
	}
	for i, f := range factors {
		resp.Factors[i] = userFactorToProto(f)
	}
	return resp, nil
}

// Get handles the UserAuthFactorService.Get RPC.
func (s *userAuthFactorServer) Get(ctx context.Context, req *pb.UserAuthFactorGetRequest) (*pb.UserAuthFactorGetResponse, error) {
	factor, err := s.svc.Get(ctx, req.GetFactorId())
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	return &pb.UserAuthFactorGetResponse{Factor: userFactorToProto(factor)}, nil
}

// Remove handles the UserAuthFactorService.Remove RPC.
func (s *userAuthFactorServer) Remove(ctx context.Context, req *pb.UserAuthFactorRemoveRequest) (*pb.UserAuthFactorRemoveResponse, error) {
	if err := s.svc.Remove(ctx, req.GetFactorId(), req.GetForce()); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	return &pb.UserAuthFactorRemoveResponse{Status: "revoked"}, nil
}

// Rotate handles the UserAuthFactorService.Rotate RPC.
func (s *userAuthFactorServer) Rotate(ctx context.Context, req *pb.UserAuthFactorRotateRequest) (*pb.UserAuthFactorRotateResponse, error) {
	newFactor := userFactorFromNewFactorProto(req.GetNewFactor())
	if err := s.svc.Rotate(ctx, req.GetOldFactorId(), newFactor); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	return &pb.UserAuthFactorRotateResponse{NewFactor: userFactorToProto(newFactor)}, nil
}

// Relabel handles the UserAuthFactorService.Relabel RPC.
func (s *userAuthFactorServer) Relabel(ctx context.Context, req *pb.UserAuthFactorRelabelRequest) (*pb.UserAuthFactorRelabelResponse, error) {
	if err := s.svc.Relabel(ctx, req.GetFactorId(), req.GetLabel()); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	factor, err := s.svc.Get(ctx, req.GetFactorId())
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	return &pb.UserAuthFactorRelabelResponse{Factor: userFactorToProto(factor)}, nil
}

// Login handles the UserAuthFactorService.Login RPC.
func (s *userAuthFactorServer) Login(ctx context.Context, req *pb.UserAuthFactorLoginRequest) (*pb.UserAuthFactorLoginResponse, error) {
	factor, err := s.svc.LookupByCredentialKey(ctx, auth.AuthFactorType(req.GetType()), req.GetLookupKey())
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	resp := &pb.UserAuthFactorLoginResponse{
		UserId: factor.UserID,
		Active: true,
		Factor: userFactorToProto(factor),
	}
	// Enrich the response from the user service, when wired. The factor login
	// yields only the user ID; resolve the full record so username, tenant,
	// and roles reach the client. Best-effort: a lookup failure leaves the
	// base identifiers intact rather than failing an authenticated login.
	if s.userSvc != nil && factor.UserID != 0 {
		if u, uErr := s.userSvc.GetUser(ctx, factor.UserID); uErr == nil && u != nil {
			resp.Username = u.Username
			resp.TenantId = u.TenantID
			resp.Roles = u.RoleIDs
			resp.Active = u.CanAuthenticate()
		}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// userFactorToProto converts an auth.AuthFactor to its protobuf wire shape.
func userFactorToProto(f *auth.AuthFactor) *pb.UserAuthFactorRecord {
	if f == nil {
		return &pb.UserAuthFactorRecord{}
	}
	r := &pb.UserAuthFactorRecord{
		Id:             f.ID,
		UserId:         f.UserID,
		Type:           string(f.Type),
		Label:          f.Label,
		Credential:     f.Credential,
		SpkiCredential: f.SPKICredential,
		Metadata:       f.Metadata,
		RegisteredAt:   f.RegisteredAt.UTC().Format(time.RFC3339),
		CreatedBy:      f.CreatedBy,
	}
	if f.LastUsedAt != nil {
		r.LastUsedAt = f.LastUsedAt.UTC().Format(time.RFC3339)
	}
	if f.RevokedAt != nil {
		r.RevokedAt = f.RevokedAt.UTC().Format(time.RFC3339)
	}
	return r
}

// userFactorFromAddProto constructs an auth.AuthFactor from an Add request.
func userFactorFromAddProto(req *pb.UserAuthFactorAddRequest) *auth.AuthFactor {
	return &auth.AuthFactor{
		UserID:         req.GetUserId(),
		Type:           auth.AuthFactorType(req.GetType()),
		Label:          req.GetLabel(),
		Credential:     req.GetCredential(),
		SPKICredential: req.GetSpkiCredential(),
		Metadata:       req.GetMetadata(),
		CreatedBy:      req.GetCreatedBy(),
	}
}

// userFactorFromNewFactorProto constructs an auth.AuthFactor from a Rotate
// new-factor message.
func userFactorFromNewFactorProto(req *pb.UserAuthFactorNewFactor) *auth.AuthFactor {
	if req == nil {
		return &auth.AuthFactor{}
	}
	return &auth.AuthFactor{
		UserID:         req.GetUserId(),
		Type:           auth.AuthFactorType(req.GetType()),
		Label:          req.GetLabel(),
		Credential:     req.GetCredential(),
		SPKICredential: req.GetSpkiCredential(),
		Metadata:       req.GetMetadata(),
		CreatedBy:      req.GetCreatedBy(),
	}
}

// mapUserAuthFactorGRPCError maps an auth-package error into a gRPC status.
func mapUserAuthFactorGRPCError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, auth.ErrFactorNotFound), errors.Is(err, auth.ErrUserHasNoFactors):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, auth.ErrFactorRevoked):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, auth.ErrLastFactor), errors.Is(err, auth.ErrFactorAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, auth.ErrFactorTypeUnsupported), errors.Is(err, auth.ErrInvalidFactor),
		errors.Is(err, auth.ErrReservedMetadata):
		// InvalidArgument, not PermissionDenied: no caller may write the
		// server-owned metadata namespace over any transport, so this is a
		// malformed request rather than an authorization decision about this
		// principal. err.Error() names the offending key and the prefix.
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
