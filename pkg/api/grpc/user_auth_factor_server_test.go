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

//go:build qrdb_ts

// The qrdb_ts constraint keeps these tests apart from their same-named
// !qrdb_ts twins in grpc_coverage2_test.go, which is audited separately.
package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ---------------------------------------------------------------------------
// user_auth_factor_server.go — native pb round-trip
// ---------------------------------------------------------------------------

func TestUserAuthFactor_Add_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId:     42,
		Type:       "totp",
		Label:      "my-totp",
		Credential: []byte("secret"),
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if resp.GetFactor().GetLabel() != "my-totp" {
		t.Fatalf("factor label = %q, want my-totp", resp.GetFactor().GetLabel())
	}
}

func TestUserAuthFactor_Add_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.addErr = auth.ErrFactorAlreadyExists
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Add(context.Background(), &pb.UserAuthFactorAddRequest{
		UserId: 1,
		Type:   "totp",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %s", st.Code())
	}
}

func TestUserAuthFactor_List_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).List(context.Background(), &pb.UserAuthFactorListRequest{
		UserId: 42,
	})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected total=1, got %d", resp.Total)
	}
}

func TestUserAuthFactor_List_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.listErr = auth.ErrUserHasNoFactors
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).List(context.Background(), &pb.UserAuthFactorListRequest{
		UserId: 42,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestUserAuthFactor_Get_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Get(context.Background(), &pb.UserAuthFactorGetRequest{
		FactorId: "factor-1",
	})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.Factor.Id != "factor-1" {
		t.Fatalf("expected factor-1, got %q", resp.Factor.Id)
	}
}

func TestUserAuthFactor_Get_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.getErr = auth.ErrFactorNotFound
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Get(context.Background(), &pb.UserAuthFactorGetRequest{
		FactorId: "x",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestUserAuthFactor_Remove_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Remove(context.Background(), &pb.UserAuthFactorRemoveRequest{
		FactorId: "factor-1",
	})
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if resp.GetStatus() != "revoked" {
		t.Fatalf("status = %q, want revoked", resp.GetStatus())
	}
}

func TestUserAuthFactor_Remove_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.removeErr = auth.ErrLastFactor
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Remove(context.Background(), &pb.UserAuthFactorRemoveRequest{
		FactorId: "x",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestUserAuthFactor_Rotate_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Rotate(context.Background(), &pb.UserAuthFactorRotateRequest{
		OldFactorId: "factor-1",
		NewFactor:   &pb.UserAuthFactorNewFactor{UserId: 42, Type: "totp"},
	})
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	if resp.GetNewFactor().GetType() != "totp" {
		t.Fatalf("new factor type = %q, want totp", resp.GetNewFactor().GetType())
	}
}

func TestUserAuthFactor_Rotate_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.rotateErr = auth.ErrFactorRevoked
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Rotate(context.Background(), &pb.UserAuthFactorRotateRequest{
		OldFactorId: "x",
		NewFactor:   &pb.UserAuthFactorNewFactor{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %s", st.Code())
	}
}

func TestUserAuthFactor_Relabel_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Relabel(context.Background(), &pb.UserAuthFactorRelabelRequest{
		FactorId: "factor-1",
		Label:    "new-label",
	})
	if err != nil {
		t.Fatalf("Relabel failed: %v", err)
	}
	if resp.GetFactor().GetId() != "factor-1" {
		t.Fatalf("factor id = %q, want factor-1 (re-read after relabel)", resp.GetFactor().GetId())
	}
}

func TestUserAuthFactor_Relabel_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.relabelErr = auth.ErrFactorTypeUnsupported
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Relabel(context.Background(), &pb.UserAuthFactorRelabelRequest{
		FactorId: "x",
		Label:    "l",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestUserAuthFactor_Login_Success(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	resp, err := pb.NewUserAuthFactorServiceClient(conn).Login(context.Background(), &pb.UserAuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("key"),
	})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if resp.Factor.Id != "factor-1" {
		t.Fatalf("expected factor-1, got %q", resp.Factor.Id)
	}
}

func TestUserAuthFactor_Login_Error(t *testing.T) {
	svc := newFullMockUserAuthFactorSvc()
	svc.lookupErr = auth.ErrFactorNotFound
	conn, cleanup := startServerWithUserAuthFactorFull(t, svc)
	defer cleanup()

	_, err := pb.NewUserAuthFactorServiceClient(conn).Login(context.Background(), &pb.UserAuthFactorLoginRequest{
		Type:      "totp",
		LookupKey: []byte("bad"),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// userFactorToProto — conversion helpers
// ---------------------------------------------------------------------------

func TestUserFactorToProtoFull_Nil(t *testing.T) {
	got := userFactorToProto(nil)
	if got.Id != "" {
		t.Errorf("expected empty Id for nil factor, got %q", got.Id)
	}
}

func TestUserFactorToProtoFull_WithLastUsed(t *testing.T) {
	now := time.Now()
	f := &auth.AuthFactor{
		ID:           "f1",
		UserID:       1,
		Type:         "totp",
		RegisteredAt: now,
		LastUsedAt:   &now,
	}
	got := userFactorToProto(f)
	if got.LastUsedAt == "" {
		t.Error("expected non-empty LastUsedAt")
	}
}

func TestUserFactorToProtoFull_WithRevokedAt(t *testing.T) {
	now := time.Now()
	f := &auth.AuthFactor{
		ID:           "f1",
		UserID:       1,
		Type:         "totp",
		RegisteredAt: now,
		RevokedAt:    &now,
	}
	got := userFactorToProto(f)
	if got.RevokedAt == "" {
		t.Error("expected non-empty RevokedAt")
	}
}

func TestUserFactorFromAddProtoFull(t *testing.T) {
	req := &pb.UserAuthFactorAddRequest{
		UserId:     42,
		Type:       "totp",
		Label:      "my-totp",
		Credential: []byte("secret"),
		Metadata:   map[string]string{"k": "v"},
		CreatedBy:  "admin",
	}
	got := userFactorFromAddProto(req)
	if got.UserID != 42 {
		t.Fatalf("expected UserID 42, got %d", got.UserID)
	}
	if got.Label != "my-totp" {
		t.Fatalf("expected Label 'my-totp', got %q", got.Label)
	}
}
