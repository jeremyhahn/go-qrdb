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
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// User auth factor gRPC service (M14)
//
// JSON-over-gRPC pattern, matching the existing AuthFactorService server in
// authfactor_server.go. Service name: qrdb.v1.UserAuthFactorService.
// ---------------------------------------------------------------------------

// userAuthFactorServer is the gRPC handler bundle for the M14 user-side
// auth factor service.
type userAuthFactorServer struct {
	svc service.UserAuthFactorServicer
}

// userAuthFactorServiceIface is the marker type required by grpc.ServiceDesc.
type userAuthFactorServiceIface interface{}

// UserAuthFactorServiceDesc is the gRPC service descriptor.
var UserAuthFactorServiceDesc = grpc.ServiceDesc{
	ServiceName: "qrdb.v1.UserAuthFactorService",
	HandlerType: (*userAuthFactorServiceIface)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Add", Handler: userFactorAddHandler},
		{MethodName: "List", Handler: userFactorListHandler},
		{MethodName: "Get", Handler: userFactorGetHandler},
		{MethodName: "Remove", Handler: userFactorRemoveHandler},
		{MethodName: "Rotate", Handler: userFactorRotateHandler},
		{MethodName: "Relabel", Handler: userFactorRelabelHandler},
		{MethodName: "Login", Handler: userFactorLoginHandler},
	},
	Streams: []grpc.StreamDesc{},
}

// RegisterUserAuthFactorService registers the user-side auth factor service
// with a gRPC server. When svc is nil, the service is not registered.
func RegisterUserAuthFactorService(srv *grpc.Server, svc service.UserAuthFactorServicer) {
	if svc == nil {
		return
	}
	srv.RegisterService(&UserAuthFactorServiceDesc, &userAuthFactorServer{svc: svc})
}

// ---------------------------------------------------------------------------
// Wire types — JSON shapes matching transport.AuthFactor* on the SDK side.
// ---------------------------------------------------------------------------

// GRPCUserAuthFactorRecord is the JSON wire shape for an auth.AuthFactor.
type GRPCUserAuthFactorRecord struct {
	ID           string            `json:"id"`
	UserID       uint64            `json:"user_id"`
	Type         string            `json:"type"`
	Label        string            `json:"label,omitempty"`
	Credential   []byte            `json:"credential"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	RegisteredAt string            `json:"registered_at"`
	LastUsedAt   string            `json:"last_used_at,omitempty"`
	RevokedAt    string            `json:"revoked_at,omitempty"`
	CreatedBy    string            `json:"created_by,omitempty"`
}

// GRPCUserAuthFactorAddRequest is the wire shape for Add.
type GRPCUserAuthFactorAddRequest struct {
	UserID     uint64            `json:"user_id"`
	Type       string            `json:"type"`
	Label      string            `json:"label,omitempty"`
	Credential []byte            `json:"credential"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedBy  string            `json:"created_by,omitempty"`
}

// GRPCUserAuthFactorAddResponse is the wire shape for Add response.
type GRPCUserAuthFactorAddResponse struct {
	Factor GRPCUserAuthFactorRecord `json:"factor"`
}

// GRPCUserAuthFactorListRequest is the wire shape for List.
type GRPCUserAuthFactorListRequest struct {
	UserID uint64 `json:"user_id"`
}

// GRPCUserAuthFactorListResponse is the wire shape for List response.
type GRPCUserAuthFactorListResponse struct {
	Factors []GRPCUserAuthFactorRecord `json:"factors"`
	Total   int                        `json:"total"`
}

// GRPCUserAuthFactorGetRequest is the wire shape for Get.
type GRPCUserAuthFactorGetRequest struct {
	FactorID string `json:"factor_id"`
}

// GRPCUserAuthFactorGetResponse is the wire shape for Get response.
type GRPCUserAuthFactorGetResponse struct {
	Factor GRPCUserAuthFactorRecord `json:"factor"`
}

// GRPCUserAuthFactorRemoveRequest is the wire shape for Remove.
type GRPCUserAuthFactorRemoveRequest struct {
	FactorID string `json:"factor_id"`
	Force    bool   `json:"force,omitempty"`
}

// GRPCUserAuthFactorRemoveResponse is the wire shape for Remove response.
type GRPCUserAuthFactorRemoveResponse struct {
	Status string `json:"status"`
}

// GRPCUserAuthFactorRotateRequest is the wire shape for Rotate.
type GRPCUserAuthFactorRotateRequest struct {
	OldFactorID string                       `json:"old_factor_id"`
	NewFactor   GRPCUserAuthFactorAddRequest `json:"new_factor"`
}

// GRPCUserAuthFactorRotateResponse is the wire shape for Rotate response.
type GRPCUserAuthFactorRotateResponse struct {
	NewFactor GRPCUserAuthFactorRecord `json:"new_factor"`
}

// GRPCUserAuthFactorRelabelRequest is the wire shape for Relabel.
type GRPCUserAuthFactorRelabelRequest struct {
	FactorID string `json:"factor_id"`
	Label    string `json:"label"`
}

// GRPCUserAuthFactorRelabelResponse is the wire shape for Relabel response.
type GRPCUserAuthFactorRelabelResponse struct {
	Factor GRPCUserAuthFactorRecord `json:"factor"`
}

// GRPCUserAuthFactorLoginRequest is the wire shape for Login.
type GRPCUserAuthFactorLoginRequest struct {
	Type      string `json:"type"`
	LookupKey []byte `json:"lookup_key"`
}

// GRPCUserAuthFactorLoginResponse is the wire shape for Login response.
type GRPCUserAuthFactorLoginResponse struct {
	UserID   uint64                   `json:"user_id"`
	Username string                   `json:"username"`
	Email    string                   `json:"email,omitempty"`
	TenantID string                   `json:"tenant_id,omitempty"`
	Roles    []string                 `json:"roles,omitempty"`
	Active   bool                     `json:"active"`
	Factor   GRPCUserAuthFactorRecord `json:"factor"`
}

// ---------------------------------------------------------------------------
// Helper conversions
// ---------------------------------------------------------------------------

func userFactorToWire(f *auth.AuthFactor) GRPCUserAuthFactorRecord {
	if f == nil {
		return GRPCUserAuthFactorRecord{}
	}
	r := GRPCUserAuthFactorRecord{
		ID:           f.ID,
		UserID:       f.UserID,
		Type:         string(f.Type),
		Label:        f.Label,
		Credential:   f.Credential,
		Metadata:     f.Metadata,
		RegisteredAt: f.RegisteredAt.Format(time.RFC3339),
		CreatedBy:    f.CreatedBy,
	}
	if f.LastUsedAt != nil {
		r.LastUsedAt = f.LastUsedAt.Format(time.RFC3339)
	}
	if f.RevokedAt != nil {
		r.RevokedAt = f.RevokedAt.Format(time.RFC3339)
	}
	return r
}

func userFactorFromAddWire(req *GRPCUserAuthFactorAddRequest) *auth.AuthFactor {
	return &auth.AuthFactor{
		UserID:     req.UserID,
		Type:       auth.AuthFactorType(req.Type),
		Label:      req.Label,
		Credential: req.Credential,
		Metadata:   req.Metadata,
		CreatedBy:  req.CreatedBy,
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func userFactorAddHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorAddRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	factor := userFactorFromAddWire(&r)
	if err := s.svc.Add(ctx, r.UserID, factor); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	out, _ := json.Marshal(GRPCUserAuthFactorAddResponse{Factor: userFactorToWire(factor)})
	return wrapperspb.Bytes(out), nil
}

func userFactorListHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorListRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	factors, err := s.svc.List(ctx, r.UserID)
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	resp := GRPCUserAuthFactorListResponse{
		Factors: make([]GRPCUserAuthFactorRecord, len(factors)),
		Total:   len(factors),
	}
	for i, f := range factors {
		resp.Factors[i] = userFactorToWire(f)
	}
	out, _ := json.Marshal(resp)
	return wrapperspb.Bytes(out), nil
}

func userFactorGetHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorGetRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	factor, err := s.svc.Get(ctx, r.FactorID)
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	out, _ := json.Marshal(GRPCUserAuthFactorGetResponse{Factor: userFactorToWire(factor)})
	return wrapperspb.Bytes(out), nil
}

func userFactorRemoveHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorRemoveRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if err := s.svc.Remove(ctx, r.FactorID, r.Force); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	out, _ := json.Marshal(GRPCUserAuthFactorRemoveResponse{Status: "revoked"})
	return wrapperspb.Bytes(out), nil
}

func userFactorRotateHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorRotateRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	newFactor := userFactorFromAddWire(&r.NewFactor)
	if err := s.svc.Rotate(ctx, r.OldFactorID, newFactor); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	out, _ := json.Marshal(GRPCUserAuthFactorRotateResponse{NewFactor: userFactorToWire(newFactor)})
	return wrapperspb.Bytes(out), nil
}

func userFactorRelabelHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorRelabelRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if err := s.svc.Relabel(ctx, r.FactorID, r.Label); err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	factor, err := s.svc.Get(ctx, r.FactorID)
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	out, _ := json.Marshal(GRPCUserAuthFactorRelabelResponse{Factor: userFactorToWire(factor)})
	return wrapperspb.Bytes(out), nil
}

func userFactorLoginHandler(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	s := srv.(*userAuthFactorServer)
	req := new(wrapperspb.BytesValue)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	var r GRPCUserAuthFactorLoginRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	factor, err := s.svc.LookupByCredentialKey(ctx, auth.AuthFactorType(r.Type), r.LookupKey)
	if err != nil {
		return nil, mapUserAuthFactorGRPCError(err)
	}
	resp := GRPCUserAuthFactorLoginResponse{
		UserID: factor.UserID,
		Active: true,
		Factor: userFactorToWire(factor),
	}
	out, _ := json.Marshal(resp)
	return wrapperspb.Bytes(out), nil
}

// ---------------------------------------------------------------------------
// Client helpers (used by SDK transport)
// ---------------------------------------------------------------------------

// UserAuthFactorAddUnary invokes Add over a gRPC connection.
func UserAuthFactorAddUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorAddRequest) (*GRPCUserAuthFactorAddResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/Add", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorAddResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserAuthFactorListUnary invokes List over a gRPC connection.
func UserAuthFactorListUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorListRequest) (*GRPCUserAuthFactorListResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/List", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorListResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserAuthFactorGetUnary invokes Get over a gRPC connection.
func UserAuthFactorGetUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorGetRequest) (*GRPCUserAuthFactorGetResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/Get", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorGetResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserAuthFactorRemoveUnary invokes Remove over a gRPC connection.
func UserAuthFactorRemoveUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorRemoveRequest) (*GRPCUserAuthFactorRemoveResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/Remove", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorRemoveResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserAuthFactorRotateUnary invokes Rotate over a gRPC connection.
func UserAuthFactorRotateUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorRotateRequest) (*GRPCUserAuthFactorRotateResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/Rotate", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorRotateResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserAuthFactorRelabelUnary invokes Relabel over a gRPC connection.
func UserAuthFactorRelabelUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorRelabelRequest) (*GRPCUserAuthFactorRelabelResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/Relabel", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorRelabelResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserAuthFactorLoginUnary invokes Login over a gRPC connection.
func UserAuthFactorLoginUnary(ctx context.Context, conn *grpc.ClientConn, req *GRPCUserAuthFactorLoginRequest) (*GRPCUserAuthFactorLoginResponse, error) {
	b, _ := json.Marshal(req)
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.UserAuthFactorService/Login", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	out := new(GRPCUserAuthFactorLoginResponse)
	if err := json.Unmarshal(resp.GetValue(), out); err != nil {
		return nil, err
	}
	return out, nil
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
	case errors.Is(err, auth.ErrFactorTypeUnsupported), errors.Is(err, auth.ErrInvalidFactor):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
