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

//go:build !integration

package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fakeInvitationHandler is a configurable, faithful
// service.InvitationServicer double for the InviteUser/RedeemInvitation
// handler tests below.
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

// ---------------------------------------------------------------------------
// InviteUser
// ---------------------------------------------------------------------------

func TestGRPC_InviteUser_Success(t *testing.T) {
	expiresAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeInvitationHandler{
		inviteFn: func(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
			if req.Username != "alice" {
				t.Fatalf("expected username 'alice', got %q", req.Username)
			}
			return &service.Invitation{
				ID:        1,
				Token:     "tok-123",
				ExpiresAt: expiresAt,
				UserID:    7,
				Username:  "alice",
				TenantID:  req.TenantID,
				Created:   true,
			}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithInvitations(fake))
	defer cleanup()

	resp, err := client.InviteUser(context.Background(), &pb.InviteUserRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("InviteUser failed: %v", err)
	}
	if resp.GetToken() != "tok-123" {
		t.Errorf("expected token 'tok-123', got %q", resp.GetToken())
	}
	if resp.GetUserId() != 7 {
		t.Errorf("expected userID 7, got %d", resp.GetUserId())
	}
	if !resp.GetCreated() {
		t.Error("expected created = true")
	}
	if resp.GetExpiresAt() == "" {
		t.Error("expected non-empty expiresAt")
	}
}

func TestGRPC_InviteUser_ServiceError(t *testing.T) {
	fake := &fakeInvitationHandler{
		inviteFn: func(context.Context, *service.InviteUserRequest) (*service.Invitation, error) {
			return nil, &service.InvitationFieldError{Field: "username", Reason: "is required"}
		},
	}
	client, cleanup := startTestServerFull(t, WithInvitations(fake))
	defer cleanup()

	_, err := client.InviteUser(context.Background(), &pb.InviteUserRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_InviteUser_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.InviteUser(context.Background(), &pb.InviteUserRequest{Username: "alice"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// RedeemInvitation
// ---------------------------------------------------------------------------

func TestGRPC_RedeemInvitation_Success(t *testing.T) {
	notAfter := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeInvitationHandler{
		redeemFn: func(_ context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			if req.Token != "tok-123" {
				t.Fatalf("expected token 'tok-123', got %q", req.Token)
			}
			return &service.EnrolledCertificate{
				UserID: 7, Username: "alice", TenantID: req.TenantID,
				CertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----"),
				Serial:  "ABCDEF01", Thumbprint: "thumb-1", NotAfter: notAfter,
			}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithInvitations(fake))
	defer cleanup()

	resp, err := client.RedeemInvitation(context.Background(), &pb.RedeemInvitationRequest{
		TenantId: "acme", Token: "tok-123", CsrPem: "csr-pem",
	})
	if err != nil {
		t.Fatalf("RedeemInvitation failed: %v", err)
	}
	if resp.GetSerial() != "ABCDEF01" {
		t.Errorf("expected serial 'ABCDEF01', got %q", resp.GetSerial())
	}
	if resp.GetThumbprint() != "thumb-1" {
		t.Errorf("expected thumbprint 'thumb-1', got %q", resp.GetThumbprint())
	}
}

func TestGRPC_RedeemInvitation_ServiceError(t *testing.T) {
	fake := &fakeInvitationHandler{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			return nil, &service.InvitationInvalidError{}
		},
	}
	client, cleanup := startTestServerFull(t, WithInvitations(fake))
	defer cleanup()

	_, err := client.RedeemInvitation(context.Background(), &pb.RedeemInvitationRequest{
		TenantId: "acme", Token: "bad-token", CsrPem: "csr-pem",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}
}

func TestGRPC_RedeemInvitation_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.RedeemInvitation(context.Background(), &pb.RedeemInvitationRequest{
		TenantId: "acme", Token: "tok-123", CsrPem: "csr-pem",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// TestGRPC_RedeemInvitation_IsServedWithoutAuthentication is the behavioural
// proof that Start's AuthMiddleware.RegisterPublicGRPCMethods registration
// actually reaches the interceptor: an AuthMiddleware configured with ZERO
// authenticators refuses every other RPC as Unauthenticated (InviteUser,
// the control, proves the middleware is really wired in), yet
// RedeemInvitation -- registered as the one PUBLIC built-in method in
// Start -- still reaches the fake service with no credentials presented at
// all. This is the gRPC twin of REST's
// TestRedeemInvitation_IsServedWithoutAuthentication and QUIC's
// handlePublicIf-backed registration.
func TestGRPC_RedeemInvitation_IsServedWithoutAuthentication(t *testing.T) {
	mw := authmw.New(rbac.New(&mockRoleService{}))
	var redeemed bool
	fake := &fakeInvitationHandler{
		redeemFn: func(context.Context, *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
			redeemed = true
			return &service.EnrolledCertificate{Username: "alice"}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithInvitations(fake), WithAuthMiddleware(mw))
	defer cleanup()

	// Control: InviteUser is authenticated, and this middleware has no
	// authenticator that could ever admit a caller, so it must be refused.
	if _, err := client.InviteUser(context.Background(), &pb.InviteUserRequest{Username: "alice"}); err == nil {
		t.Fatal("expected InviteUser to require authentication, got nil error")
	} else if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Fatalf("InviteUser: expected Unauthenticated, got %s", st.Code())
	}

	// RedeemInvitation must still reach the service with the SAME
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
