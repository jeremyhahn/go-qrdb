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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// testTenantHeader names the metadata key from which the test interceptor
// takes the caller's tenant; it stands in for the auth interceptor a real
// transport installs ahead of the handlers.
const testTenantHeader = "x-test-tenant"

// identityInterceptor attaches the tenant-admin identity named by
// testTenantHeader to the request context, as the transport's auth
// interceptor does for an authenticated caller.
func identityInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(testTenantHeader); len(v) == 1 {
			ctx = ctxWithIdentity(v[0])
		}
	}
	return handler(ctx, req)
}

// dialRegisteredServer serves a *grpc.Server built only through
// RegisterService, the function sdk.RegisterGRPCService aliases, over an
// in-memory listener and returns a TenantBarrierService client to it.
func dialRegisteredServer(t *testing.T, opts ...RegisterOption) pb.TenantBarrierServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.UnaryInterceptor(identityInterceptor))
	RegisterService(srv, newMockKVService(), newMockAdminService(), opts...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewTenantBarrierServiceClient(conn)
}

// barrierGateMiddleware is the middleware newBarrierGateServer gates on: a
// tenant admin holds the tenant barrier permissions for its own tenant only.
func barrierGateMiddleware(t *testing.T) *authmw.AuthMiddleware {
	t.Helper()
	return newBarrierGateServer(t).authMW
}

func tenantCallerCtx(tenantID string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), testTenantHeader, tenantID)
}

func TestRegisterService_TenantBarrier_GrantedCallerIsServed(t *testing.T) {
	client := dialRegisteredServer(t,
		WithRegisterTenantBarrierService(newMockTenantService()),
		WithRegisterAuthMiddleware(barrierGateMiddleware(t)))

	if _, err := client.Seal(tenantCallerCtx("acme"), &pb.TenantBarrierSealRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("Seal by the tenant's own admin: %v", err)
	}
}

func TestRegisterService_TenantBarrier_CallerWithoutGrantIsDenied(t *testing.T) {
	client := dialRegisteredServer(t,
		WithRegisterTenantBarrierService(newMockTenantService()),
		WithRegisterAuthMiddleware(barrierGateMiddleware(t)))

	_, err := client.Seal(tenantCallerCtx("acme"), &pb.TenantBarrierSealRequest{TenantId: "bob-corp"})
	requireCode(t, err, codes.PermissionDenied)
}

func TestRegisterService_TenantBarrier_NilMiddlewareIsNoAuthPosture(t *testing.T) {
	client := dialRegisteredServer(t, WithRegisterTenantBarrierService(newMockTenantService()))

	if _, err := client.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("Seal under the no-auth posture: %v", err)
	}
}

func TestRegisterService_TenantBarrier_WithoutOptionIsUnimplemented(t *testing.T) {
	client := dialRegisteredServer(t, WithRegisterAuthMiddleware(barrierGateMiddleware(t)))

	_, err := client.Seal(tenantCallerCtx("acme"), &pb.TenantBarrierSealRequest{TenantId: "acme"})
	requireCode(t, err, codes.Unimplemented)
}

func TestRegisterService_TenantBarrier_NilServicerIsUnregistered(t *testing.T) {
	client := dialRegisteredServer(t, WithRegisterTenantBarrierService(nil))

	_, err := client.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "acme"})
	requireCode(t, err, codes.Unimplemented)
}
