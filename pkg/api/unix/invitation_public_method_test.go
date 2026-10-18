//go:build codec_json && raft

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

// RedeemInvitation is the one PUBLIC built-in RPC this transport registers
// (not a module declaration): see Start's identical registration to
// pkg/api/grpc/server.go's. This file proves it end-to-end over a real Unix
// socket connection, the way grpc_public_methods_test.go proves a module's
// declared public method reaches the shared AuthMiddleware.

package unix

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fakeInvitationHandler is a configurable, faithful
// service.InvitationServicer double.
type fakeInvitationHandler struct {
	inviteFn func(ctx context.Context, req *service.InviteUserRequest) (*service.Invitation, error)
	redeemFn func(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error)
}

var _ service.InvitationServicer = (*fakeInvitationHandler)(nil)

func (f *fakeInvitationHandler) InviteUser(ctx context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
	return f.inviteFn(ctx, req)
}

func (f *fakeInvitationHandler) RedeemInvitation(ctx context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
	return f.redeemFn(ctx, req)
}

// TestUnixServer_RedeemInvitationIsServedWithoutAuthentication is the
// behavioural proof that Start's AuthMiddleware.RegisterPublicGRPCMethods
// registration for RedeemInvitation actually reaches the interceptor on
// THIS transport, independently of the TCP gRPC server: an AuthMiddleware
// configured with an authenticator that refuses every caller still serves
// RedeemInvitation over a real Unix socket connection, while InviteUser --
// the control -- is refused as Unauthenticated on the SAME connection.
func TestUnixServer_RedeemInvitationIsServedWithoutAuthentication(t *testing.T) {
	mw := authmw.New(allowAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(alwaysFailAuthenticator{}))
	var redeemed bool
	fake := &fakeInvitationHandler{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			redeemed = true
			return &service.EnrolledCertificate{Username: "alice"}, nil
		},
	}

	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithInvitations(fake),
		WithAuthMiddleware(mw),
		WithTLS(tlsCfg),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer conn.Close()
	client := pb.NewQRDBServiceClient(conn)

	// Control: InviteUser is authenticated, and this middleware's only
	// authenticator always fails, so it must be refused.
	if _, err := client.InviteUser(context.Background(), &pb.InviteUserRequest{Username: "alice"}); err == nil {
		t.Fatal("expected InviteUser to require authentication, got nil error")
	} else if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Fatalf("InviteUser: expected Unauthenticated, got %s", st.Code())
	}

	// RedeemInvitation must still reach the service over the SAME
	// credential-less connection.
	if _, err := client.RedeemInvitation(context.Background(), &pb.RedeemInvitationRequest{
		TenantId: "acme", Token: "tok-123", CsrPem: "csr-pem",
	}); err != nil {
		t.Fatalf("RedeemInvitation must be served without authentication: %v", err)
	}
	if !redeemed {
		t.Fatal("RedeemInvitation never reached the service")
	}
}

// TestUnixServer_InvitationsNilServiceIsUnimplemented covers the companion
// case: without WithInvitations, InviteUser and RedeemInvitation are both
// registered RPCs (the proto service always carries them) but return
// Unimplemented rather than silently succeeding or panicking.
func TestUnixServer_InvitationsNilServiceIsUnimplemented(t *testing.T) {
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath, WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn := dialUnixTLS(t, socketPath, tlsCfg)
	defer conn.Close()
	client := pb.NewQRDBServiceClient(conn)

	_, err := client.RedeemInvitation(context.Background(), &pb.RedeemInvitationRequest{
		TenantId: "acme", Token: "tok-123", CsrPem: "csr-pem",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}
