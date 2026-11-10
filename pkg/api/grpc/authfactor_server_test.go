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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
// mapServiceError
// ---------------------------------------------------------------------------

func TestMapAuthFactorGRPCError_Nil(t *testing.T) {
	if got := mapServiceError(nil); got != nil {
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
			name: "AuthFactorTenantMismatchError",
			err:  &service.AuthFactorTenantMismatchError{Requested: "globex", Context: "acme"},
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
			name: "ErrTenantSealed",
			err:  tenant.ErrTenantSealed,
			want: codes.Unavailable,
		},
		{
			name: "AuthFactorListReadError wrapping ErrTenantSealed",
			err:  &service.AuthFactorListReadError{Key: "__sys:authfactor/e1", Err: tenant.ErrTenantSealed},
			want: codes.Unavailable,
		},
		{
			name: "seal.ErrSealed (barrier sealed mid-request, distinct from tenant.ErrTenantSealed)",
			err:  seal.ErrSealed,
			want: codes.Unavailable,
		},
		{
			name: "AuthFactorListReadError wrapping seal.ErrSealed",
			err:  &service.AuthFactorListReadError{Key: "__sys:authfactor/e1", Err: seal.ErrSealed},
			want: codes.Unavailable,
		},
		{
			name: "generic error",
			err:  errors.New("something went wrong"),
			want: codes.Internal,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServiceError(tc.err)
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

// TestAuthFactorServer_SealedAfterResolve drives every gRPC (and therefore
// Unix, which registers this same service) auth-factor RPC with the error a
// tenant sealing between Resolve and the store operation actually produces —
// seal.ErrSealed, asserted end to end against a real barrier in pkg/service —
// and asserts codes.Unavailable rather than codes.Internal.
func TestAuthFactorServer_SealedAfterResolve(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(context.Context, string, string, authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return nil, seal.ErrSealed
	}
	svc.challengeFn = func(context.Context, string) (*authfactors.Challenge, error) { return nil, seal.ErrSealed }
	svc.verifyFn = func(context.Context, string, string, *authfactors.Response) error { return seal.ErrSealed }
	svc.listFn = func(context.Context, string) ([]*authfactors.Enrollment, error) {
		return nil, &service.AuthFactorListReadError{Key: "__sys:authfactor/e1", Err: seal.ErrSealed}
	}
	svc.revokeFn = func(context.Context, string) error { return seal.ErrSealed }
	srv := &authFactorServer{svc: svc}
	ctx := context.Background()

	calls := map[string]func() error{
		"EnrollFactor": func() error {
			_, err := srv.EnrollFactor(ctx, &pb.EnrollFactorRequest{FactorName: "piv", Subject: "u1"})
			return err
		},
		"ChallengeFactor": func() error {
			_, err := srv.ChallengeFactor(ctx, &pb.ChallengeFactorRequest{EnrollmentId: "e1"})
			return err
		},
		"VerifyFactor": func() error {
			_, err := srv.VerifyFactor(ctx, &pb.VerifyFactorRequest{ChallengeId: "000000000001.c"})
			return err
		},
		"ListFactors": func() error {
			_, err := srv.ListFactors(ctx, &pb.ListFactorsRequest{})
			return err
		},
		"RevokeFactor": func() error {
			_, err := srv.RevokeFactor(ctx, &pb.RevokeFactorRequest{EnrollmentId: "e1"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if got := status.Code(call()); got != codes.Unavailable {
				t.Errorf("%s with a tenant sealed after Resolve = %s, want %s", name, got, codes.Unavailable)
			}
		})
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

// TestEnrollFactorHandler_PassesParams asserts the handler hands the
// service the factor, subject, tenant and method params from the request,
// and that a request without method params still yields a non-nil map.
func TestEnrollFactorHandler_PassesParams(t *testing.T) {
	var gotName, gotSubject string
	var got authfactors.EnrollParams
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		gotName, gotSubject, got = factorName, subject, params
		return &authfactors.Enrollment{EnrollmentID: "e1", FactorName: factorName, UserID: subject, TenantID: params.TenantID}, nil
	}
	srv := &authFactorServer{svc: svc}

	resp, err := srv.EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		Subject: "alice", FactorName: "totp", TenantId: "t1", MethodParams: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if gotName != "totp" || gotSubject != "alice" || got.UserID != "alice" || got.TenantID != "t1" {
		t.Fatalf("service got name=%q subject=%q params=%+v", gotName, gotSubject, got)
	}
	if got.MethodParams["k"] != "v" {
		t.Fatalf("MethodParams = %v, want k=v", got.MethodParams)
	}
	if resp.GetUserId() != "alice" || resp.GetTenantId() != "t1" {
		t.Fatalf("response UserId=%q TenantId=%q", resp.GetUserId(), resp.GetTenantId())
	}

	if _, err := srv.EnrollFactor(context.Background(), &pb.EnrollFactorRequest{Subject: "alice", FactorName: "totp"}); err != nil {
		t.Fatalf("EnrollFactor without params: %v", err)
	}
	if got.MethodParams == nil {
		t.Fatal("MethodParams is nil for a request without params")
	}
}

// TestChallengeFactorHandler_MapsChallenge asserts the response carries the
// enrollment and nonce of the challenge the service issued.
func TestChallengeFactorHandler_MapsChallenge(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.challengeFn = func(_ context.Context, enrollmentID string) (*authfactors.Challenge, error) {
		return &authfactors.Challenge{ChallengeID: "c1", EnrollmentID: enrollmentID, Nonce: []byte("n")}, nil
	}
	srv := &authFactorServer{svc: svc}

	resp, err := srv.ChallengeFactor(context.Background(), &pb.ChallengeFactorRequest{EnrollmentId: "enroll-9"})
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if resp.GetChallengeId() != "c1" || resp.GetEnrollmentId() != "enroll-9" || string(resp.GetNonce()) != "n" {
		t.Fatalf("response = %+v", resp)
	}
}

// TestVerifyFactorHandler_PassesResponse asserts the handler hands the
// service the factor name, challenge and response value, and replaces a nil
// metadata map with an empty one.
func TestVerifyFactorHandler_PassesResponse(t *testing.T) {
	var gotName, gotChallenge string
	var got *authfactors.Response
	svc := newMockAuthFactorService()
	svc.verifyFn = func(_ context.Context, factorName, challengeID string, response *authfactors.Response) error {
		gotName, gotChallenge, got = factorName, challengeID, response
		return nil
	}
	srv := &authFactorServer{svc: svc}

	if _, err := srv.VerifyFactor(context.Background(), &pb.VerifyFactorRequest{
		ChallengeId: "c1", FactorName: "totp", Value: []byte("123456"),
	}); err != nil {
		t.Fatalf("VerifyFactor: %v", err)
	}
	if gotName != "totp" || gotChallenge != "c1" || got == nil || string(got.Value) != "123456" {
		t.Fatalf("service got name=%q challenge=%q response=%+v", gotName, gotChallenge, got)
	}
	if got.Metadata == nil {
		t.Fatal("Metadata is nil for a request without metadata")
	}
}

// TestRegisterAuthFactorService_RegistersOnlyWithService asserts the
// AuthFactorService is registered for a non-nil service and skipped for nil.
func TestRegisterAuthFactorService_RegistersOnlyWithService(t *testing.T) {
	name := pb.AuthFactorService_ServiceDesc.ServiceName

	without := grpc.NewServer()
	RegisterAuthFactorService(without, nil)
	if _, ok := without.GetServiceInfo()[name]; ok {
		t.Fatalf("%s registered for a nil service", name)
	}

	with := grpc.NewServer()
	RegisterAuthFactorService(with, newMockAuthFactorService())
	if _, ok := with.GetServiceInfo()[name]; !ok {
		t.Fatalf("%s not registered for a non-nil service", name)
	}
}

// TestServer_WithAuthFactorService_ServesEnroll asserts a Server built with
// WithAuthFactorService answers AuthFactorService RPCs from that service.
func TestServer_WithAuthFactorService_ServesEnroll(t *testing.T) {
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithAuthFactorService(newMockAuthFactorService()))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := pb.NewAuthFactorServiceClient(conn).EnrollFactor(context.Background(), &pb.EnrollFactorRequest{
		FactorName: "pkcs11", Subject: "alice", TenantId: "t1",
	})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	if resp.GetEnrollmentId() != "enroll-1" || resp.GetUserId() != "alice" || resp.GetTenantId() != "t1" {
		t.Fatalf("response = %+v", resp)
	}
}
