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
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// authFactorServer implements the generated pb.AuthFactorServiceServer.
type authFactorServer struct {
	pb.UnimplementedAuthFactorServiceServer

	svc service.AuthFactorServicer
}

// RegisterAuthFactorService registers the auth factor service with a gRPC server.
// When svc is nil the service is not registered.
func RegisterAuthFactorService(srv *grpc.Server, svc service.AuthFactorServicer) {
	if svc == nil {
		return
	}
	pb.RegisterAuthFactorServiceServer(srv, &authFactorServer{svc: svc})
}

// EnrollFactor handles the AuthFactorService.EnrollFactor RPC.
func (s *authFactorServer) EnrollFactor(ctx context.Context, req *pb.EnrollFactorRequest) (*pb.EnrollFactorResponse, error) {
	if req.GetSubject() == "" {
		return nil, status.Error(codes.InvalidArgument, "subject is required")
	}
	if req.GetFactorName() == "" {
		return nil, status.Error(codes.InvalidArgument, "factor_name is required")
	}

	methodParams := make(map[string]any, len(req.GetMethodParams()))
	for k, v := range req.GetMethodParams() {
		methodParams[k] = v
	}

	params := authfactors.EnrollParams{
		UserID:       req.GetSubject(),
		TenantID:     req.GetTenantId(),
		MethodParams: methodParams,
	}

	enrollment, err := s.svc.EnrollFactor(ctx, req.GetFactorName(), req.GetSubject(), params)
	if err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	return &pb.EnrollFactorResponse{
		EnrollmentId: enrollment.EnrollmentID,
		FactorName:   enrollment.FactorName,
		UserId:       enrollment.UserID,
		TenantId:     enrollment.TenantID,
		Metadata:     enrollment.Metadata,
		EnrolledAt:   enrollment.EnrolledAt.UTC().Format(time.RFC3339),
	}, nil
}

// ChallengeFactor handles the AuthFactorService.ChallengeFactor RPC.
func (s *authFactorServer) ChallengeFactor(ctx context.Context, req *pb.ChallengeFactorRequest) (*pb.ChallengeFactorResponse, error) {
	if req.GetEnrollmentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "enrollment_id is required")
	}

	challenge, err := s.svc.ChallengeFactor(ctx, req.GetEnrollmentId())
	if err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	resp := &pb.ChallengeFactorResponse{
		ChallengeId:  challenge.ChallengeID,
		EnrollmentId: challenge.EnrollmentID,
		Nonce:        challenge.Nonce,
		ExpiresAt:    challenge.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if len(challenge.MethodData) > 0 {
		resp.MethodData = make(map[string]string, len(challenge.MethodData))
		for k, v := range challenge.MethodData {
			resp.MethodData[k] = auditAttrString(v)
		}
	}
	return resp, nil
}

// VerifyFactor handles the AuthFactorService.VerifyFactor RPC.
func (s *authFactorServer) VerifyFactor(ctx context.Context, req *pb.VerifyFactorRequest) (*emptypb.Empty, error) {
	if req.GetChallengeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "challenge_id is required")
	}

	response := &authfactors.Response{
		Value:    req.GetValue(),
		Metadata: req.GetMetadata(),
	}
	if response.Metadata == nil {
		response.Metadata = make(map[string]string)
	}

	if err := s.svc.VerifyFactor(ctx, req.GetFactorName(), req.GetChallengeId(), response); err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// ListFactors handles the AuthFactorService.ListFactors RPC.
func (s *authFactorServer) ListFactors(ctx context.Context, req *pb.ListFactorsRequest) (*pb.ListFactorsResponse, error) {
	enrollments, err := s.svc.ListFactors(ctx, req.GetSubject())
	if err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	entries := make([]*pb.EnrollmentEntry, len(enrollments))
	for i, e := range enrollments {
		entry := &pb.EnrollmentEntry{
			EnrollmentId: e.EnrollmentID,
			FactorName:   e.FactorName,
			UserId:       e.UserID,
			TenantId:     e.TenantID,
			Metadata:     e.Metadata,
			EnrolledAt:   e.EnrolledAt.UTC().Format(time.RFC3339),
		}
		if !e.RevokedAt.IsZero() {
			entry.RevokedAt = e.RevokedAt.UTC().Format(time.RFC3339)
		}
		entries[i] = entry
	}

	return &pb.ListFactorsResponse{
		Enrollments: entries,
		Total:       int32(len(entries)), //nolint:gosec // enrollment count is bounded
	}, nil
}

// RevokeFactor handles the AuthFactorService.RevokeFactor RPC.
func (s *authFactorServer) RevokeFactor(ctx context.Context, req *pb.RevokeFactorRequest) (*emptypb.Empty, error) {
	if req.GetEnrollmentId() == "" {
		return nil, status.Error(codes.InvalidArgument, "enrollment_id is required")
	}

	if err := s.svc.RevokeFactor(ctx, req.GetEnrollmentId()); err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

// mapAuthFactorGRPCError converts an auth factor error to a gRPC status error.
func mapAuthFactorGRPCError(err error) error {
	if err == nil {
		return nil
	}
	var (
		enrollmentNotFound  *authfactors.EnrollmentNotFoundError
		factorNotFound      *authfactors.FactorNotFoundError
		enrollmentInvalid   *authfactors.EnrollmentInvalidError
		enrollmentRevoked   *authfactors.EnrollmentRevokedError
		challengeExpired    *authfactors.ChallengeExpiredError
		verificationFailed  *authfactors.VerificationFailedError
		hardwareUnavailable *authfactors.HardwareUnavailableError
	)
	switch {
	case errors.As(err, &enrollmentNotFound), errors.As(err, &factorNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.As(err, &enrollmentInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.As(err, &enrollmentRevoked), errors.As(err, &challengeExpired):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.As(err, &verificationFailed):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.As(err, &hardwareUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, authfactors.ErrFactorDisabled):
		return status.Error(codes.Unimplemented, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
