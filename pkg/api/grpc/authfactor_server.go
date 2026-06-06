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
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Auth factor gRPC service (JSON-over-gRPC, no proto regeneration required)
// ---------------------------------------------------------------------------

// authFactorServer provides gRPC handlers for the AuthFactor service.
// It exposes the same five operations as the REST layer using JSON-serialised
// request and response messages encoded as wrapperspb.BytesValue proto messages.
//
// Service name: "qrdb.v1.AuthFactorService"
// Methods: EnrollFactor, ChallengeFactor, VerifyFactor, ListFactors, RevokeFactor
type authFactorServer struct {
	svc service.AuthFactorServicer
}

// ---------------------------------------------------------------------------
// Request / response types (JSON wire format, mirrors REST layer)
// ---------------------------------------------------------------------------

// grpcEnrollFactorRequest mirrors the REST enroll payload.
type grpcEnrollFactorRequest struct {
	FactorName   string         `json:"factor_name"`
	Subject      string         `json:"subject"`
	TenantID     string         `json:"tenant_id,omitempty"`
	MethodParams map[string]any `json:"method_params,omitempty"`
}

// grpcEnrollFactorResponse mirrors the REST enroll response.
type grpcEnrollFactorResponse struct {
	EnrollmentID string            `json:"enrollment_id"`
	FactorName   string            `json:"factor_name"`
	UserID       string            `json:"user_id"`
	TenantID     string            `json:"tenant_id,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	EnrolledAt   string            `json:"enrolled_at"`
}

// grpcChallengeFactorRequest carries the enrollment ID.
type grpcChallengeFactorRequest struct {
	EnrollmentID string `json:"enrollment_id"`
}

// grpcChallengeFactorResponse carries the challenge payload.
type grpcChallengeFactorResponse struct {
	ChallengeID  string         `json:"challenge_id"`
	EnrollmentID string         `json:"enrollment_id"`
	Nonce        []byte         `json:"nonce"`
	ExpiresAt    string         `json:"expires_at"`
	MethodData   map[string]any `json:"method_data,omitempty"`
}

// grpcVerifyFactorRequest carries the challenge response.
type grpcVerifyFactorRequest struct {
	FactorName  string            `json:"factor_name,omitempty"`
	ChallengeID string            `json:"challenge_id"`
	Value       []byte            `json:"value"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// grpcListFactorsRequest optionally filters by subject.
type grpcListFactorsRequest struct {
	Subject string `json:"subject,omitempty"`
}

// grpcEnrollmentEntry is the list-entry wire shape.
type grpcEnrollmentEntry struct {
	EnrollmentID string            `json:"enrollment_id"`
	FactorName   string            `json:"factor_name"`
	UserID       string            `json:"user_id"`
	TenantID     string            `json:"tenant_id,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	EnrolledAt   string            `json:"enrolled_at"`
	RevokedAt    string            `json:"revoked_at,omitempty"`
}

// grpcListFactorsResponse carries the enrollments list.
type grpcListFactorsResponse struct {
	Enrollments []*grpcEnrollmentEntry `json:"enrollments"`
	Total       int                    `json:"total"`
}

// grpcRevokeFactorRequest carries the enrollment ID to revoke.
type grpcRevokeFactorRequest struct {
	EnrollmentID string `json:"enrollment_id"`
}

// ---------------------------------------------------------------------------
// gRPC method descriptors for self-registration
// ---------------------------------------------------------------------------

// authFactorServiceIface is an empty interface used only as the HandlerType
// marker for grpc.ServiceDesc. gRPC's RegisterService reflects on this type
// via Type.Implements, which requires an interface kind. The concrete
// authFactorServer satisfies this trivially.
type authFactorServiceIface interface{}

// AuthFactorServiceDesc is the gRPC service descriptor for registration.
// Request and response bodies are JSON bytes wrapped in wrapperspb.BytesValue
// proto messages, avoiding proto code generation for the authfactor package.
var AuthFactorServiceDesc = grpc.ServiceDesc{
	ServiceName: "qrdb.v1.AuthFactorService",
	HandlerType: (*authFactorServiceIface)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "EnrollFactor", Handler: enrollFactorHandler},
		{MethodName: "ChallengeFactor", Handler: challengeFactorHandler},
		{MethodName: "VerifyFactor", Handler: verifyFactorHandler},
		{MethodName: "ListFactors", Handler: listFactorsHandler},
		{MethodName: "RevokeFactor", Handler: revokeFactorHandler},
	},
	Streams: []grpc.StreamDesc{},
}

// RegisterAuthFactorService registers the auth factor service with a gRPC server.
// When svc is nil the service is not registered.
func RegisterAuthFactorService(srv *grpc.Server, svc service.AuthFactorServicer) {
	if svc == nil {
		return
	}
	srv.RegisterService(&AuthFactorServiceDesc, &authFactorServer{svc: svc})
}

// ---------------------------------------------------------------------------
// Handler functions (grpc.UnaryHandler signature)
// Each handler decodes a *wrapperspb.BytesValue request, extracts the JSON
// payload, calls the service, and returns a *wrapperspb.BytesValue response.
// ---------------------------------------------------------------------------

func enrollFactorHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*authFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r grpcEnrollFactorRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.Subject == "" {
		return nil, status.Error(codes.InvalidArgument, "subject is required")
	}
	if r.FactorName == "" {
		return nil, status.Error(codes.InvalidArgument, "factor_name is required")
	}

	params := authfactors.EnrollParams{
		UserID:       r.Subject,
		TenantID:     r.TenantID,
		MethodParams: r.MethodParams,
	}
	if params.MethodParams == nil {
		params.MethodParams = make(map[string]any)
	}

	enrollment, err := s.svc.EnrollFactor(ctx, r.FactorName, r.Subject, params)
	if err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	b, err := json.Marshal(grpcEnrollFactorResponse{
		EnrollmentID: enrollment.EnrollmentID,
		FactorName:   enrollment.FactorName,
		UserID:       enrollment.UserID,
		TenantID:     enrollment.TenantID,
		Metadata:     enrollment.Metadata,
		EnrolledAt:   enrollment.EnrolledAt.Format(time.RFC3339),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return wrapperspb.Bytes(b), nil
}

func challengeFactorHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*authFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r grpcChallengeFactorRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.EnrollmentID == "" {
		return nil, status.Error(codes.InvalidArgument, "enrollment_id is required")
	}

	challenge, err := s.svc.ChallengeFactor(ctx, r.EnrollmentID)
	if err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	b, err := json.Marshal(grpcChallengeFactorResponse{
		ChallengeID:  challenge.ChallengeID,
		EnrollmentID: challenge.EnrollmentID,
		Nonce:        challenge.Nonce,
		ExpiresAt:    challenge.ExpiresAt.Format(time.RFC3339),
		MethodData:   challenge.MethodData,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return wrapperspb.Bytes(b), nil
}

func verifyFactorHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*authFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r grpcVerifyFactorRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.ChallengeID == "" {
		return nil, status.Error(codes.InvalidArgument, "challenge_id is required")
	}

	response := &authfactors.Response{
		Value:    r.Value,
		Metadata: r.Metadata,
	}
	if response.Metadata == nil {
		response.Metadata = make(map[string]string)
	}

	if err := s.svc.VerifyFactor(ctx, r.FactorName, r.ChallengeID, response); err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	b, _ := json.Marshal(map[string]string{"status": "verified"})
	return wrapperspb.Bytes(b), nil
}

func listFactorsHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*authFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r grpcListFactorsRequest
	_ = json.Unmarshal(req.GetValue(), &r) // optional body; ignore unmarshal error

	enrollments, err := s.svc.ListFactors(ctx, r.Subject)
	if err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	entries := make([]*grpcEnrollmentEntry, len(enrollments))
	for i, e := range enrollments {
		entry := &grpcEnrollmentEntry{
			EnrollmentID: e.EnrollmentID,
			FactorName:   e.FactorName,
			UserID:       e.UserID,
			TenantID:     e.TenantID,
			Metadata:     e.Metadata,
			EnrolledAt:   e.EnrolledAt.Format(time.RFC3339),
		}
		if !e.RevokedAt.IsZero() {
			entry.RevokedAt = e.RevokedAt.Format(time.RFC3339)
		}
		entries[i] = entry
	}

	b, err := json.Marshal(grpcListFactorsResponse{Enrollments: entries, Total: len(entries)})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return wrapperspb.Bytes(b), nil
}

func revokeFactorHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*authFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r grpcRevokeFactorRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.EnrollmentID == "" {
		return nil, status.Error(codes.InvalidArgument, "enrollment_id is required")
	}

	if err := s.svc.RevokeFactor(ctx, r.EnrollmentID); err != nil {
		return nil, mapAuthFactorGRPCError(err)
	}

	b, _ := json.Marshal(map[string]string{"status": "revoked"})
	return wrapperspb.Bytes(b), nil
}

// ---------------------------------------------------------------------------
// Exported type aliases for SDK transport use
// ---------------------------------------------------------------------------

// GRPCEnrollFactorRequest is the exported wire type for EnrollFactor calls.
type GRPCEnrollFactorRequest = grpcEnrollFactorRequest

// GRPCEnrollFactorResponse is the exported wire type for EnrollFactor responses.
type GRPCEnrollFactorResponse = grpcEnrollFactorResponse

// GRPCChallengeFactorRequest is the exported wire type for ChallengeFactor calls.
type GRPCChallengeFactorRequest = grpcChallengeFactorRequest

// GRPCChallengeFactorResponse is the exported wire type for ChallengeFactor responses.
type GRPCChallengeFactorResponse = grpcChallengeFactorResponse

// GRPCVerifyFactorRequest is the exported wire type for VerifyFactor calls.
type GRPCVerifyFactorRequest = grpcVerifyFactorRequest

// GRPCListFactorsRequest is the exported wire type for ListFactors calls.
type GRPCListFactorsRequest = grpcListFactorsRequest

// GRPCListFactorsResponse is the exported wire type for ListFactors responses.
type GRPCListFactorsResponse = grpcListFactorsResponse

// GRPCRevokeFactorRequest is the exported wire type for RevokeFactor calls.
type GRPCRevokeFactorRequest = grpcRevokeFactorRequest

// ---------------------------------------------------------------------------
// Client-callable unary helper functions
// ---------------------------------------------------------------------------

// EnrollFactorUnary calls the EnrollFactor method over a raw gRPC connection.
// It is used by SDK transports that communicate with the AuthFactorService
// without generated proto stubs.
func EnrollFactorUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcEnrollFactorRequest) (*grpcEnrollFactorResponse, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.AuthFactorService/EnrollFactor", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	var out grpcEnrollFactorResponse
	if err := json.Unmarshal(resp.GetValue(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChallengeFactorUnary calls the ChallengeFactor method over a raw gRPC connection.
func ChallengeFactorUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcChallengeFactorRequest) (*grpcChallengeFactorResponse, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.AuthFactorService/ChallengeFactor", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	var out grpcChallengeFactorResponse
	if err := json.Unmarshal(resp.GetValue(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VerifyFactorUnary calls the VerifyFactor method over a raw gRPC connection.
func VerifyFactorUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcVerifyFactorRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp := new(wrapperspb.BytesValue)
	return conn.Invoke(ctx, "/qrdb.v1.AuthFactorService/VerifyFactor", wrapperspb.Bytes(b), resp)
}

// ListFactorsUnary calls the ListFactors method over a raw gRPC connection.
func ListFactorsUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcListFactorsRequest) (*grpcListFactorsResponse, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.AuthFactorService/ListFactors", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	var out grpcListFactorsResponse
	if err := json.Unmarshal(resp.GetValue(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeFactorUnary calls the RevokeFactor method over a raw gRPC connection.
func RevokeFactorUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcRevokeFactorRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp := new(wrapperspb.BytesValue)
	return conn.Invoke(ctx, "/qrdb.v1.AuthFactorService/RevokeFactor", wrapperspb.Bytes(b), resp)
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// mapAuthFactorGRPCError converts an auth factor error to a gRPC status error.
func mapAuthFactorGRPCError(err error) error {
	if err == nil {
		return nil
	}
	switch err.(type) {
	case *authfactors.EnrollmentNotFoundError, *authfactors.FactorNotFoundError:
		return status.Error(codes.NotFound, err.Error())
	case *authfactors.EnrollmentInvalidError:
		return status.Error(codes.InvalidArgument, err.Error())
	case *authfactors.EnrollmentRevokedError, *authfactors.ChallengeExpiredError:
		return status.Error(codes.FailedPrecondition, err.Error())
	case *authfactors.VerificationFailedError:
		return status.Error(codes.Unauthenticated, err.Error())
	case *authfactors.HardwareUnavailableError:
		return status.Error(codes.Unavailable, err.Error())
	default:
		if err == authfactors.ErrFactorDisabled {
			return status.Error(codes.Unimplemented, err.Error())
		}
		return status.Error(codes.Internal, err.Error())
	}
}
