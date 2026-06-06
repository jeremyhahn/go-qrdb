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
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// AuditQueryRequest is an alias for wrapperspb.BytesValue used to carry a
// JSON-encoded audit.Filter over gRPC without proto code generation.
// Authorization: requires "audit:query" permission.
// TODO: switch to permission.AuditQuery constant once Phase 2 lands.
type AuditQueryRequest = wrapperspb.BytesValue

// AuditQueryResponse is an alias for wrapperspb.BytesValue used to carry a
// JSON-encoded audit.Page over gRPC.
type AuditQueryResponse = wrapperspb.BytesValue

// auditServiceIface is an empty interface used as the HandlerType marker for
// grpc.ServiceDesc. gRPC reflects on this via Type.Implements which requires
// an interface kind; a concrete struct pointer would panic at registration.
type auditServiceIface interface{}

// auditServiceDesc is the gRPC service descriptor for the AuditService.
var auditServiceDesc = grpc.ServiceDesc{
	ServiceName: "qrdb.v1.AuditService",
	HandlerType: (*auditServiceIface)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Query",
			Handler:    auditQueryHandler,
		},
	},
	Streams: []grpc.StreamDesc{},
}

// auditServiceServer holds the audit logger and implements the AuditService.
type auditServiceServer struct {
	auditLogger audit.AuditLogger
}

// auditQueryHandler is the unary gRPC handler for AuditService.Query.
// The request carries a JSON-encoded audit.Filter in a BytesValue wrapper;
// the response carries a JSON-encoded audit.Page in a BytesValue wrapper.
// This avoids proto code generation for the audit package.
func auditQueryHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := new(AuditQueryRequest)
	if err := dec(req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode: %v", err)
	}
	if interceptor == nil {
		return srv.(*auditServiceServer).query(ctx, req)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/qrdb.v1.AuditService/Query",
	}
	return interceptor(ctx, req, info, func(ctx context.Context, req any) (any, error) {
		return srv.(*auditServiceServer).query(ctx, req.(*AuditQueryRequest))
	})
}

// query decodes the filter, calls the audit logger, and returns the page.
func (s *auditServiceServer) query(ctx context.Context, req *AuditQueryRequest) (*AuditQueryResponse, error) {
	if s.auditLogger == nil {
		return nil, status.Error(codes.Unimplemented, "audit service not configured")
	}

	var f audit.Filter
	if len(req.GetValue()) > 0 {
		if err := json.Unmarshal(req.GetValue(), &f); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid filter JSON: %v", err)
		}
	}

	page, err := s.auditLogger.Query(ctx, f)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "audit query: %v", err)
	}

	b, err := json.Marshal(page)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal page: %v", err)
	}
	return wrapperspb.Bytes(b), nil
}

// RegisterAuditService registers the AuditService on the given gRPC server.
// When auditLogger is nil, all calls return codes.Unimplemented.
// Call this after RegisterService when an audit logger is available.
func RegisterAuditService(srv *grpc.Server, auditLogger audit.AuditLogger) {
	srv.RegisterService(&auditServiceDesc, &auditServiceServer{auditLogger: auditLogger})
}

// AuditFilter is a wire-compatible alias for audit.Filter used by gRPC clients.
type AuditFilter = audit.Filter

// AuditPage is a wire-compatible alias for audit.Page used by gRPC clients.
type AuditPage = audit.Page

// QueryAuditUnary sends an audit query to the server using a raw gRPC
// unary call and returns the decoded page. It is provided for SDK transports
// that need a concrete call site without generated stubs.
func QueryAuditUnary(ctx context.Context, conn *grpc.ClientConn, f audit.Filter) (*audit.Page, error) {
	filterJSON, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req := wrapperspb.Bytes(filterJSON)
	resp := new(AuditQueryResponse)
	if err := conn.Invoke(ctx, "/qrdb.v1.AuditService/Query", req, resp); err != nil {
		return nil, err
	}

	var page audit.Page
	if err := json.Unmarshal(resp.GetValue(), &page); err != nil {
		return nil, err
	}
	return &page, nil
}
