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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Per-tenant barrier gRPC service (JSON-over-gRPC, no proto regeneration)
//
// Service name: "qrdb.v1.TenantBarrierService"
// Methods: Init, Unseal, Seal, Status
//
// The four method handlers wrap the existing tenantpkg.TenantServicer
// implementation (pkg/tenant.TenantService) and translate domain errors via
// mapTenantError. Request and response payloads are JSON bytes carried in
// google.protobuf.BytesValue messages, mirroring the pattern used by the
// audit and auth factor services.
// ---------------------------------------------------------------------------

// tenantBarrierServer holds the tenant servicer used to satisfy the
// TenantBarrierService gRPC handlers, plus the auth middleware used to
// enforce the tenant-scoped authorization gate against the request's target
// tenant. When authMW is nil the gate is a no-op (unauthenticated
// deployment), mirroring the rest of the server's behavior in that mode.
type tenantBarrierServer struct {
	svc    tenantpkg.TenantServicer
	authMW *authmw.AuthMiddleware
}

// authorizeTenantBarrierOp enforces the tenant-scoped authorization gate for
// a per-tenant barrier operation against the TRUE target tenant carried in
// the request body (not the identity's own tenant). The ScopeTenantOnly
// rule on the supplied permission denies the system tenant and any
// cross-tenant access.
func (s *tenantBarrierServer) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenant(ctx, tenantID, perm)
}

// tenantBarrierServiceIface is the empty marker interface required by
// grpc.ServiceDesc.HandlerType. gRPC reflects on this via Type.Implements,
// which requires an interface kind.
type tenantBarrierServiceIface interface{}

// TenantBarrierServiceDesc is the gRPC service descriptor for the per-tenant
// barrier service. Request and response bodies are JSON bytes wrapped in
// wrapperspb.BytesValue, avoiding the need for protobuf code generation.
var TenantBarrierServiceDesc = grpc.ServiceDesc{
	ServiceName: "qrdb.v1.TenantBarrierService",
	HandlerType: (*tenantBarrierServiceIface)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Init", Handler: tenantBarrierInitHandler},
		{MethodName: "Unseal", Handler: tenantBarrierUnsealHandler},
		{MethodName: "Seal", Handler: tenantBarrierSealHandler},
		{MethodName: "Status", Handler: tenantBarrierStatusHandler},
	},
	Streams: []grpc.StreamDesc{},
}

// RegisterTenantBarrierService registers the per-tenant barrier service on
// the given gRPC server. When svc is nil the service is not registered, so
// callers that have no tenant servicer wired in can safely invoke this
// helper. The authMW (which may be nil) supplies the tenant-scoped
// authorization gate enforced against each request's target tenant.
func RegisterTenantBarrierService(srv *grpc.Server, svc tenantpkg.TenantServicer, authMW *authmw.AuthMiddleware) {
	if svc == nil {
		return
	}
	srv.RegisterService(&TenantBarrierServiceDesc, &tenantBarrierServer{svc: svc, authMW: authMW})
}

// ---------------------------------------------------------------------------
// Wire types (JSON, mirror sdk/go/transport tenant_types.go)
// ---------------------------------------------------------------------------

// grpcTenantBarrierInitRequest is the JSON wire shape for Init calls.
// Passphrase carries the tenant admin's caller-supplied secret (required
// for the passphrase strategy; no derived fallback). Strategy selects the
// seal strategy; when empty the passphrase (software) strategy is used.
type grpcTenantBarrierInitRequest struct {
	TenantID   string `json:"tenant_id"`
	Passphrase string `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
}

// grpcTenantBarrierUnsealRequest is the JSON wire shape for Unseal calls.
// Either Share or Key may be set; servers prefer Key when both are non-empty.
type grpcTenantBarrierUnsealRequest struct {
	TenantID string `json:"tenant_id"`
	Share    []byte `json:"share,omitempty"`
	Key      []byte `json:"key,omitempty"`
}

// grpcTenantBarrierSealRequest is the JSON wire shape for Seal calls.
type grpcTenantBarrierSealRequest struct {
	TenantID string `json:"tenant_id"`
}

// grpcTenantBarrierStatusRequest is the JSON wire shape for Status calls.
type grpcTenantBarrierStatusRequest struct {
	TenantID string `json:"tenant_id"`
}

// grpcTenantBarrierStatusResponse is the JSON wire shape for Status responses.
// Mirrors transport.TenantBarrierStatusResponse on the SDK side.
type grpcTenantBarrierStatusResponse struct {
	TenantID       string `json:"tenant_id"`
	Sealed         bool   `json:"sealed"`
	Initialized    bool   `json:"initialized"`
	Strategy       string `json:"strategy"`
	HardwareBacked bool   `json:"hardware_backed"`
}

// ---------------------------------------------------------------------------
// Exported wire-type aliases for SDK transport reuse.
// ---------------------------------------------------------------------------

// GRPCTenantBarrierInitRequest is the exported wire type for Init calls.
type GRPCTenantBarrierInitRequest = grpcTenantBarrierInitRequest

// GRPCTenantBarrierUnsealRequest is the exported wire type for Unseal calls.
type GRPCTenantBarrierUnsealRequest = grpcTenantBarrierUnsealRequest

// GRPCTenantBarrierSealRequest is the exported wire type for Seal calls.
type GRPCTenantBarrierSealRequest = grpcTenantBarrierSealRequest

// GRPCTenantBarrierStatusRequest is the exported wire type for Status calls.
type GRPCTenantBarrierStatusRequest = grpcTenantBarrierStatusRequest

// GRPCTenantBarrierStatusResponse is the exported wire type for Status responses.
type GRPCTenantBarrierStatusResponse = grpcTenantBarrierStatusResponse

// ---------------------------------------------------------------------------
// Handlers (grpc.UnaryHandler signature)
//
// Each handler decodes a *wrapperspb.BytesValue request, JSON-unmarshals the
// payload into the typed wire request, calls the service, and JSON-marshals
// the response back into a *wrapperspb.BytesValue. Domain errors flow through
// mapTenantError to gRPC status codes.
// ---------------------------------------------------------------------------

func tenantBarrierInitHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.BytesValue)
	if err := dec(in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if interceptor == nil {
		return srv.(*tenantBarrierServer).init(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/qrdb.v1.TenantBarrierService/Init"}
	return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
		return srv.(*tenantBarrierServer).init(ctx, req.(*wrapperspb.BytesValue))
	})
}

func tenantBarrierUnsealHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.BytesValue)
	if err := dec(in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if interceptor == nil {
		return srv.(*tenantBarrierServer).unseal(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/qrdb.v1.TenantBarrierService/Unseal"}
	return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
		return srv.(*tenantBarrierServer).unseal(ctx, req.(*wrapperspb.BytesValue))
	})
}

func tenantBarrierSealHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.BytesValue)
	if err := dec(in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if interceptor == nil {
		return srv.(*tenantBarrierServer).seal(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/qrdb.v1.TenantBarrierService/Seal"}
	return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
		return srv.(*tenantBarrierServer).seal(ctx, req.(*wrapperspb.BytesValue))
	})
}

func tenantBarrierStatusHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(wrapperspb.BytesValue)
	if err := dec(in); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if interceptor == nil {
		return srv.(*tenantBarrierServer).getStatus(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/qrdb.v1.TenantBarrierService/Status"}
	return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
		return srv.(*tenantBarrierServer).getStatus(ctx, req.(*wrapperspb.BytesValue))
	})
}

// ---------------------------------------------------------------------------
// Service-method implementations.
// ---------------------------------------------------------------------------

// init unmarshals the request, calls TenantBarrierInit, and returns an empty
// JSON object on success.
func (s *tenantBarrierServer) init(ctx context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}

	var r grpcTenantBarrierInitRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.TenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	if err := s.authorizeTenantBarrierOp(ctx, r.TenantID, permission.TenantBarrierInit); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	if err := s.svc.TenantBarrierInitWithStrategy(ctx, r.TenantID, r.Passphrase, r.Threshold, r.Shares, seal.StrategyID(r.Strategy)); err != nil {
		return nil, mapTenantError(err)
	}

	return wrapperspb.Bytes(emptyJSONObject()), nil
}

// unseal unmarshals the request, calls TenantBarrierUnseal, and returns an
// empty JSON object on success. When both Key and Share are set, Key wins.
func (s *tenantBarrierServer) unseal(ctx context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}

	var r grpcTenantBarrierUnsealRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.TenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	if err := s.authorizeTenantBarrierOp(ctx, r.TenantID, permission.TenantBarrierUnseal); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	key := r.Key
	if len(key) == 0 {
		key = r.Share
	}

	if err := s.svc.TenantBarrierUnseal(ctx, r.TenantID, key); err != nil {
		return nil, mapTenantError(err)
	}

	return wrapperspb.Bytes(emptyJSONObject()), nil
}

// seal unmarshals the request and calls TenantBarrierSeal.
func (s *tenantBarrierServer) seal(ctx context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}

	var r grpcTenantBarrierSealRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.TenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	if err := s.authorizeTenantBarrierOp(ctx, r.TenantID, permission.TenantBarrierSeal); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	if err := s.svc.TenantBarrierSeal(ctx, r.TenantID); err != nil {
		return nil, mapTenantError(err)
	}

	return wrapperspb.Bytes(emptyJSONObject()), nil
}

// getStatus unmarshals the request, calls TenantBarrierStatus, and marshals
// the response into a JSON wire payload.
func (s *tenantBarrierServer) getStatus(ctx context.Context, req *wrapperspb.BytesValue) (*wrapperspb.BytesValue, error) {
	if s.svc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "tenant barrier"}).Error())
	}

	var r grpcTenantBarrierStatusRequest
	if err := json.Unmarshal(req.GetValue(), &r); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unmarshal: %v", err)
	}
	if r.TenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	// Reading barrier state leaks isolation metadata; gate it with the same
	// tenant-scoped authorizer as init/unseal/seal.
	if err := s.authorizeTenantBarrierOp(ctx, r.TenantID, permission.TenantBarrierStatus); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}

	st, err := s.svc.TenantBarrierStatus(ctx, r.TenantID)
	if err != nil {
		return nil, mapTenantError(err)
	}
	if st == nil {
		return nil, status.Error(codes.Internal, "tenant barrier status: nil response")
	}

	out := grpcTenantBarrierStatusResponse{
		TenantID:       st.TenantID,
		Sealed:         st.Sealed,
		Initialized:    st.Initialized,
		Strategy:       st.Strategy,
		HardwareBacked: st.HardwareBacked,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return wrapperspb.Bytes(b), nil
}

// emptyJSONObject returns the canonical empty JSON object {} used as the
// success payload for void-returning RPCs.
func emptyJSONObject() []byte {
	return []byte(`{}`)
}

// ---------------------------------------------------------------------------
// Client-callable unary helpers for SDK transports.
// ---------------------------------------------------------------------------

// TenantBarrierInitUnary calls the Init method over a raw gRPC connection.
func TenantBarrierInitUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcTenantBarrierInitRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp := new(wrapperspb.BytesValue)
	return conn.Invoke(ctx, "/qrdb.v1.TenantBarrierService/Init", wrapperspb.Bytes(b), resp)
}

// TenantBarrierUnsealUnary calls the Unseal method over a raw gRPC connection.
func TenantBarrierUnsealUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcTenantBarrierUnsealRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp := new(wrapperspb.BytesValue)
	return conn.Invoke(ctx, "/qrdb.v1.TenantBarrierService/Unseal", wrapperspb.Bytes(b), resp)
}

// TenantBarrierSealUnary calls the Seal method over a raw gRPC connection.
func TenantBarrierSealUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcTenantBarrierSealRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp := new(wrapperspb.BytesValue)
	return conn.Invoke(ctx, "/qrdb.v1.TenantBarrierService/Seal", wrapperspb.Bytes(b), resp)
}

// TenantBarrierStatusUnary calls the Status method over a raw gRPC connection.
func TenantBarrierStatusUnary(ctx context.Context, conn *grpc.ClientConn, req *grpcTenantBarrierStatusRequest) (*grpcTenantBarrierStatusResponse, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp := new(wrapperspb.BytesValue)
	if err := conn.Invoke(ctx, "/qrdb.v1.TenantBarrierService/Status", wrapperspb.Bytes(b), resp); err != nil {
		return nil, err
	}
	var out grpcTenantBarrierStatusResponse
	if err := json.Unmarshal(resp.GetValue(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
