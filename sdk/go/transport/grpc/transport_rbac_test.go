// Copyright (c) 2026 Jeremy Hahn
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
	"net"
	"slices"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// rbacStubServer answers the two RBAC RPCs and records the Authorize request.
type rbacStubServer struct {
	pb.UnimplementedQRDBServiceServer
	own      []*pb.OwnPermission
	decision *pb.AuthorizeResponse
	err      error
	asked    *pb.AuthorizeRequest
}

func (s *rbacStubServer) ListOwnPermissions(context.Context, *emptypb.Empty) (*pb.ListOwnPermissionsResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &pb.ListOwnPermissionsResponse{Permissions: s.own}, nil
}

func (s *rbacStubServer) Authorize(_ context.Context, req *pb.AuthorizeRequest) (*pb.AuthorizeResponse, error) {
	s.asked = req
	if s.err != nil {
		return nil, s.err
	}
	return s.decision, nil
}

// startRBACStub serves s and returns a Transport connected to it.
func startRBACStub(t *testing.T, s *rbacStubServer) *Transport {
	t.Helper()
	gs := googlegrpc.NewServer()
	pb.RegisterQRDBServiceServer(gs, s)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	conn, err := googlegrpc.NewClient(lis.Addr().String(), googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
	})
	return &Transport{config: &transport.Config{Address: lis.Addr().String()}, conn: conn, client: pb.NewQRDBServiceClient(conn)}
}

var rbacBob = &transport.Identity{Subject: "bob", TenantID: "t1", Roles: []string{"reader"}}

func TestGRPCRBAC_ListOwnPermissions(t *testing.T) {
	tr := startRBACStub(t, &rbacStubServer{own: []*pb.OwnPermission{{Name: "kv:get", Resource: "kv", Action: "get", Scope: "self"}}})
	got, err := tr.RBACClient().ListOwnPermissions(context.Background())
	want := []transport.PermissionInfo{{Name: "kv:get", Resource: "kv", Action: "get", Scope: transport.PermissionScopeSelf}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("ListOwnPermissions = %+v, %v; want %+v", got, err, want)
	}
}

func TestGRPCRBAC_ListOwnPermissionsServerError(t *testing.T) {
	tr := startRBACStub(t, &rbacStubServer{err: status.Error(codes.Unauthenticated, "no identity")})
	if _, err := tr.RBACClient().ListOwnPermissions(context.Background()); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestGRPCRBAC_AuthorizeSendsTheIdentity(t *testing.T) {
	s := &rbacStubServer{decision: &pb.AuthorizeResponse{Allowed: true}}
	if err := startRBACStub(t, s).RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); err != nil {
		t.Fatalf("Authorize = %v, want nil", err)
	}
	id := s.asked.GetIdentity()
	if s.asked.GetResource() != "kv" || s.asked.GetAction() != "get" ||
		id.GetSubject() != "bob" || id.GetTenantId() != "t1" || !slices.Equal(id.GetRoles(), rbacBob.Roles) {
		t.Fatalf("server was asked %v", s.asked)
	}
}

func TestGRPCRBAC_AuthorizeDeniedIsUnauthorized(t *testing.T) {
	tr := startRBACStub(t, &rbacStubServer{decision: &pb.AuthorizeResponse{Reason: "no grant"}})
	err := tr.RBACClient().Authorize(context.Background(), rbacBob, "kv", "put")
	var denied *transport.AuthorizationDeniedError
	if !errors.Is(err, auth.ErrUnauthorized) || !errors.As(err, &denied) || denied.Reason != "no grant" {
		t.Fatalf("Authorize = %v, want an AuthorizationDeniedError carrying the reason", err)
	}
}

func TestGRPCRBAC_AuthorizeServerError(t *testing.T) {
	tr := startRBACStub(t, &rbacStubServer{err: status.Error(codes.PermissionDenied, "permissions:resolve")})
	if err := tr.RBACClient().Authorize(context.Background(), rbacBob, "kv", "get"); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestGRPCRBAC_Closed(t *testing.T) {
	tr := startRBACStub(t, &rbacStubServer{})
	tr.closed.Store(true)
	c := tr.RBACClient()
	var ce *transport.ClosedError
	if _, err := c.ListOwnPermissions(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("ListOwnPermissions = %T, want ClosedError", err)
	}
	if err := c.Authorize(context.Background(), rbacBob, "kv", "get"); !errors.As(err, &ce) {
		t.Fatalf("Authorize = %T, want ClosedError", err)
	}
	if _, err := c.ListPermissions(context.Background(), rbacBob); !errors.As(err, &ce) {
		t.Fatalf("ListPermissions = %T, want ClosedError", err)
	}
}
