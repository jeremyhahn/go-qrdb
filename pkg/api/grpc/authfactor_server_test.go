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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

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
// Helpers: encode/invoke handlers directly
// ---------------------------------------------------------------------------

// callHandler is a helper that builds a wrapperspb.BytesValue from a struct,
// calls the given handler function, and returns the response.
func marshalBytesValue(t interface{ Fatalf(string, ...any) }, v any) *wrapperspb.BytesValue {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalBytesValue: %v", err)
	}
	return wrapperspb.Bytes(b)
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
// enrollFactorHandler
// ---------------------------------------------------------------------------

func TestEnrollFactorHandler_MissingSubject(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcEnrollFactorRequest{FactorName: "piv"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := enrollFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error for missing subject")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestEnrollFactorHandler_MissingFactorName(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcEnrollFactorRequest{Subject: "alice"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := enrollFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error for missing factor_name")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestEnrollFactorHandler_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcEnrollFactorRequest{FactorName: "pkcs11", Subject: "alice"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	resp, err := enrollFactorHandler(srv, context.Background(), dec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bv, ok := resp.(*wrapperspb.BytesValue)
	if !ok {
		t.Fatalf("expected *wrapperspb.BytesValue, got %T", resp)
	}
	var out grpcEnrollFactorResponse
	if err := json.Unmarshal(bv.GetValue(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.EnrollmentID != "enroll-1" {
		t.Errorf("expected enrollment_id 'enroll-1', got %q", out.EnrollmentID)
	}
	if out.FactorName != "pkcs11" {
		t.Errorf("expected factor_name 'pkcs11', got %q", out.FactorName)
	}
}

func TestEnrollFactorHandler_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, _ string, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return nil, &authfactors.FactorNotFoundError{Name: "bad-factor"}
	}
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcEnrollFactorRequest{FactorName: "bad-factor", Subject: "alice"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := enrollFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// challengeFactorHandler
// ---------------------------------------------------------------------------

func TestChallengeFactorHandler_MissingEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcChallengeFactorRequest{})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := challengeFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error for missing enrollment_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestChallengeFactorHandler_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcChallengeFactorRequest{EnrollmentID: "enroll-1"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	resp, err := challengeFactorHandler(srv, context.Background(), dec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bv, ok := resp.(*wrapperspb.BytesValue)
	if !ok {
		t.Fatalf("expected *wrapperspb.BytesValue, got %T", resp)
	}
	var out grpcChallengeFactorResponse
	if err := json.Unmarshal(bv.GetValue(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ChallengeID != "challenge-1" {
		t.Errorf("expected challenge_id 'challenge-1', got %q", out.ChallengeID)
	}
}

func TestChallengeFactorHandler_NotFound(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.challengeFn = func(_ context.Context, _ string) (*authfactors.Challenge, error) {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: "missing"}
	}
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcChallengeFactorRequest{EnrollmentID: "missing"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := challengeFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// verifyFactorHandler
// ---------------------------------------------------------------------------

func TestVerifyFactorHandler_MissingChallengeID(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcVerifyFactorRequest{Value: []byte("sig")})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := verifyFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error for missing challenge_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestVerifyFactorHandler_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcVerifyFactorRequest{ChallengeID: "challenge-1", Value: []byte("sig")})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	resp, err := verifyFactorHandler(srv, context.Background(), dec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bv, ok := resp.(*wrapperspb.BytesValue)
	if !ok {
		t.Fatalf("expected *wrapperspb.BytesValue, got %T", resp)
	}
	var out map[string]string
	if err := json.Unmarshal(bv.GetValue(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "verified" {
		t.Errorf("expected status 'verified', got %q", out["status"])
	}
}

func TestVerifyFactorHandler_VerificationFailed(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.verifyFn = func(_ context.Context, _, _ string, _ *authfactors.Response) error {
		return &authfactors.VerificationFailedError{FactorName: "piv", Reason: "bad sig"}
	}
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcVerifyFactorRequest{ChallengeID: "c1", Value: []byte("bad")})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := verifyFactorHandler(srv, context.Background(), dec, nil)
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

	payload := marshalBytesValue(t, grpcVerifyFactorRequest{ChallengeID: "c1", Value: []byte("v")})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := verifyFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// listFactorsHandler
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

	payload := marshalBytesValue(t, grpcListFactorsRequest{Subject: "alice"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	resp, err := listFactorsHandler(srv, context.Background(), dec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bv, ok := resp.(*wrapperspb.BytesValue)
	if !ok {
		t.Fatalf("expected *wrapperspb.BytesValue, got %T", resp)
	}
	var out grpcListFactorsResponse
	if err := json.Unmarshal(bv.GetValue(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 2 {
		t.Errorf("expected Total=2, got %d", out.Total)
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

	payload := marshalBytesValue(t, grpcListFactorsRequest{})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	resp, err := listFactorsHandler(srv, context.Background(), dec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bv := resp.(*wrapperspb.BytesValue)
	var out grpcListFactorsResponse
	if err := json.Unmarshal(bv.GetValue(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 1 {
		t.Fatalf("expected 1 enrollment, got %d", out.Total)
	}
	if out.Enrollments[0].RevokedAt == "" {
		t.Error("expected RevokedAt to be set")
	}
}

func TestListFactorsHandler_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return nil, errors.New("db error")
	}
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcListFactorsRequest{})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := listFactorsHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// revokeFactorHandler
// ---------------------------------------------------------------------------

func TestRevokeFactorHandler_MissingEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorService()
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcRevokeFactorRequest{})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := revokeFactorHandler(srv, context.Background(), dec, nil)
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

	payload := marshalBytesValue(t, grpcRevokeFactorRequest{EnrollmentID: "enroll-1"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	resp, err := revokeFactorHandler(srv, context.Background(), dec, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected RevokeFactor to be called")
	}
	bv, ok := resp.(*wrapperspb.BytesValue)
	if !ok {
		t.Fatalf("expected *wrapperspb.BytesValue, got %T", resp)
	}
	var out map[string]string
	if err := json.Unmarshal(bv.GetValue(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["status"] != "revoked" {
		t.Errorf("expected status 'revoked', got %q", out["status"])
	}
}

func TestRevokeFactorHandler_NotFound(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, _ string) error {
		return &authfactors.EnrollmentNotFoundError{EnrollmentID: "missing"}
	}
	srv := &authFactorServer{svc: svc}

	payload := marshalBytesValue(t, grpcRevokeFactorRequest{EnrollmentID: "missing"})
	dec := func(v any) error {
		v.(*wrapperspb.BytesValue).Value = payload.Value
		return nil
	}
	_, err := revokeFactorHandler(srv, context.Background(), dec, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}
