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
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockAuthFactorService implements service.AuthFactorServicer.
// ---------------------------------------------------------------------------

type mockAuthFactorService struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

var _ service.AuthFactorServicer = (*mockAuthFactorService)(nil)

func newMockAuthFactorService() *mockAuthFactorService {
	return &mockAuthFactorService{}
}

func (m *mockAuthFactorService) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		EnrolledAt:   time.Now(),
	}, nil
}

func (m *mockAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "challenge-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(5 * time.Minute),
	}, nil
}

func (m *mockAuthFactorService) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}

func (m *mockAuthFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "enroll-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
	}, nil
}

func (m *mockAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// mapAuthFactorGRPCError
// ---------------------------------------------------------------------------

func TestMapAuthFactorGRPCError_Nil(t *testing.T) {
	if got := mapAuthFactorGRPCError(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestMapAuthFactorGRPCError_AllTypes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{
			name: "EnrollmentNotFoundError",
			err:  &authfactors.EnrollmentNotFoundError{EnrollmentID: "e1"},
			want: codes.NotFound,
		},
		{
			name: "FactorNotFoundError",
			err:  &authfactors.FactorNotFoundError{Name: "fido2"},
			want: codes.NotFound,
		},
		{
			name: "EnrollmentInvalidError",
			err:  &authfactors.EnrollmentInvalidError{EnrollmentID: "e1", Reason: "bad key"},
			want: codes.InvalidArgument,
		},
		{
			name: "EnrollmentRevokedError",
			err:  &authfactors.EnrollmentRevokedError{EnrollmentID: "e1"},
			want: codes.FailedPrecondition,
		},
		{
			name: "ChallengeExpiredError",
			err:  &authfactors.ChallengeExpiredError{ChallengeID: "c1"},
			want: codes.FailedPrecondition,
		},
		{
			name: "VerificationFailedError",
			err:  &authfactors.VerificationFailedError{FactorName: "piv", Reason: "sig mismatch"},
			want: codes.Unauthenticated,
		},
		{
			name: "HardwareUnavailableError",
			err:  &authfactors.HardwareUnavailableError{Factor: "pkcs11", Reason: "no device"},
			want: codes.Unavailable,
		},
		{
			name: "ErrFactorDisabled",
			err:  authfactors.ErrFactorDisabled,
			want: codes.Unimplemented,
		},
		{
			name: "generic error",
			err:  errors.New("something went wrong"),
			want: codes.Internal,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapAuthFactorGRPCError(tc.err)
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("expected gRPC status error, got %T", got)
			}
			if st.Code() != tc.want {
				t.Errorf("expected %s, got %s", tc.want, st.Code())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// authFactorServer.EnrollFactor
// ---------------------------------------------------------------------------

func TestEnrollFactorHandler_MissingSubject(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	_, err := srv.EnrollFactor(context.Background(), &pb.EnrollFactorRequest{FactorName: "piv"})
	if err == nil {
		t.Fatal("expected error for missing subject")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestEnrollFactorHandler_MissingFactorName(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	_, err := srv.EnrollFactor(context.Background(), &pb.EnrollFactorRequest{Subject: "alice"})
	if err == nil {
		t.Fatal("expected error for missing factor_name")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestEnrollFactorHandler_Success(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	resp, err := srv.EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		FactorName: "pkcs11",
		Subject:    "alice",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EnrollmentId != "enroll-1" {
		t.Errorf("expected enrollment_id 'enroll-1', got %q", resp.EnrollmentId)
	}
	if resp.FactorName != "pkcs11" {
		t.Errorf("expected factor_name 'pkcs11', got %q", resp.FactorName)
	}
}

func TestEnrollFactorHandler_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, _ string, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return nil, &authfactors.FactorNotFoundError{Name: "bad-factor"}
	}
	srv := &authFactorServer{svc: svc}
	_, err := srv.EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		FactorName: "bad-factor",
		Subject:    "alice",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// authFactorServer.ChallengeFactor
// ---------------------------------------------------------------------------

func TestChallengeFactorHandler_MissingEnrollmentID(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	_, err := srv.ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{})
	if err == nil {
		t.Fatal("expected error for missing enrollment_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestChallengeFactorHandler_Success(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	resp, err := srv.ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{
		EnrollmentId: "enroll-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ChallengeId != "challenge-1" {
		t.Errorf("expected challenge_id 'challenge-1', got %q", resp.ChallengeId)
	}
}

func TestChallengeFactorHandler_NotFound(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.challengeFn = func(_ context.Context, _ string) (*authfactors.Challenge, error) {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: "missing"}
	}
	srv := &authFactorServer{svc: svc}
	_, err := srv.ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{
		EnrollmentId: "missing",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// authFactorServer.VerifyFactor
// ---------------------------------------------------------------------------

func TestVerifyFactorHandler_MissingChallengeID(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	_, err := srv.VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		Value: []byte("sig"),
	})
	if err == nil {
		t.Fatal("expected error for missing challenge_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestVerifyFactorHandler_Success(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	resp, err := srv.VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "challenge-1",
		Value:       []byte("sig"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil empty response")
	}
}

func TestVerifyFactorHandler_VerificationFailed(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.verifyFn = func(_ context.Context, _, _ string, _ *authfactors.Response) error {
		return &authfactors.VerificationFailedError{FactorName: "piv", Reason: "bad sig"}
	}
	srv := &authFactorServer{svc: svc}
	_, err := srv.VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "c1",
		Value:       []byte("bad"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %s", st.Code())
	}
}

func TestVerifyFactorHandler_FactorDisabled(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.verifyFn = func(_ context.Context, _, _ string, _ *authfactors.Response) error {
		return authfactors.ErrFactorDisabled
	}
	srv := &authFactorServer{svc: svc}
	_, err := srv.VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "c1",
		Value:       []byte("v"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// authFactorServer.ListFactors
// ---------------------------------------------------------------------------

func TestListFactorsHandler_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, subject string) ([]*authfactors.Enrollment, error) {
		return []*authfactors.Enrollment{
			{EnrollmentID: "e1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
			{EnrollmentID: "e2", FactorName: "piv", UserID: subject, EnrolledAt: time.Now()},
		}, nil
	}
	srv := &authFactorServer{svc: svc}
	resp, err := srv.ListFactors(context.Background(), &pb.ListFactorsRequest{Subject: "alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("expected Total=2, got %d", resp.Total)
	}
}

func TestListFactorsHandler_WithRevokedAt(t *testing.T) {
	revokedAt := time.Now().Add(-time.Hour)
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return []*authfactors.Enrollment{
			{EnrollmentID: "e1", FactorName: "piv", UserID: "bob", EnrolledAt: time.Now(), RevokedAt: revokedAt},
		}, nil
	}
	srv := &authFactorServer{svc: svc}
	resp, err := srv.ListFactors(context.Background(), &pb.ListFactorsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected 1 enrollment, got %d", resp.Total)
	}
	if resp.Enrollments[0].RevokedAt == "" {
		t.Error("expected RevokedAt to be set")
	}
}

func TestListFactorsHandler_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return nil, errors.New("db error")
	}
	srv := &authFactorServer{svc: svc}
	_, err := srv.ListFactors(context.Background(), &pb.ListFactorsRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// authFactorServer.RevokeFactor
// ---------------------------------------------------------------------------

func TestRevokeFactorHandler_MissingEnrollmentID(t *testing.T) {
	srv := &authFactorServer{svc: newMockAuthFactorService()}
	_, err := srv.RevokeFactor(context.Background(), &pb.RevokeFactorRequest{})
	if err == nil {
		t.Fatal("expected error for missing enrollment_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestRevokeFactorHandler_Success(t *testing.T) {
	called := false
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, id string) error {
		called = true
		if id != "enroll-1" {
			return errors.New("unexpected id: " + id)
		}
		return nil
	}
	srv := &authFactorServer{svc: svc}
	resp, err := srv.RevokeFactor(context.Background(), &pb.RevokeFactorRequest{
		EnrollmentId: "enroll-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected RevokeFactor to be called")
	}
	if resp == nil {
		t.Fatal("expected non-nil empty response")
	}
}

func TestRevokeFactorHandler_NotFound(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, _ string) error {
		return &authfactors.EnrollmentNotFoundError{EnrollmentID: "missing"}
	}
	srv := &authFactorServer{svc: svc}
	_, err := srv.RevokeFactor(context.Background(), &pb.RevokeFactorRequest{
		EnrollmentId: "missing",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}
