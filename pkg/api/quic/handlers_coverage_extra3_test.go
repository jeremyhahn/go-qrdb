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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleAuditQuery — NilService/EmptyPayload/WithFilter/UnmarshalError are
// unique here; ServiceError is in audit_frame_test.go so omitted.
// ---------------------------------------------------------------------------

func TestHandleAuditQuery_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleAuditQuery(context.Background(), nil)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleAuditQuery_EmptyPayload(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(&mockAuditLogger{}))
	resp, err := srv.handleAuditQuery(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleAuditQuery_WithFilter(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(&mockAuditLogger{}))
	filter := audit.Filter{}
	payload, _ := json.Marshal(filter)
	resp, err := srv.handleAuditQuery(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestHandleAuditQuery_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuditLogger(&mockAuditLogger{}))
	_, err := srv.handleAuditQuery(context.Background(), []byte("{bad json"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// fullAuthFactorSvc — returns real values so success/list/verify tests work
// ---------------------------------------------------------------------------

// fullAuthFactorSvc returns real Enrollment values so success/list/verify tests work.
type fullAuthFactorSvc struct {
	enrollFn    func(ctx context.Context, name, subject string, p authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, name, challengeID string, resp *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

func (f *fullAuthFactorSvc) EnrollFactor(ctx context.Context, name, subject string, p authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if f.enrollFn != nil {
		return f.enrollFn(ctx, name, subject, p)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "enroll-1",
		FactorName:   name,
		UserID:       subject,
		TenantID:     p.TenantID,
		EnrolledAt:   time.Now(),
	}, nil
}

func (f *fullAuthFactorSvc) ChallengeFactor(ctx context.Context, id string) (*authfactors.Challenge, error) {
	if f.challengeFn != nil {
		return f.challengeFn(ctx, id)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: id,
		Nonce:        []byte("nonce-bytes"),
		ExpiresAt:    time.Now().Add(5 * time.Minute),
	}, nil
}

func (f *fullAuthFactorSvc) VerifyFactor(ctx context.Context, name, challengeID string, resp *authfactors.Response) error {
	if f.verifyFn != nil {
		return f.verifyFn(ctx, name, challengeID, resp)
	}
	return nil
}

func (f *fullAuthFactorSvc) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if f.listFn != nil {
		return f.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "e1", FactorName: "fido2", UserID: subject, EnrolledAt: time.Now()},
	}, nil
}

func (f *fullAuthFactorSvc) RevokeFactor(ctx context.Context, id string) error {
	if f.revokeFn != nil {
		return f.revokeFn(ctx, id)
	}
	return nil
}

func newAuthFactorServer() *Server {
	return newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(&fullAuthFactorSvc{}))
}

// ---------------------------------------------------------------------------
// handleAuthFactorVerify — NilMetadata is unique (not in authfactor_frame_test.go)
// ---------------------------------------------------------------------------

func TestHandleAuthFactorVerify_NilMetadata(t *testing.T) {
	srv := newAuthFactorServer()
	payload, _ := json.Marshal(quicVerifyFactorRequest{ChallengeID: "ch1", Value: []byte("v")})
	_, err := srv.handleAuthFactorVerify(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleAuthFactorList — WithRevokedAt is unique (not in authfactor_frame_test.go)
// ---------------------------------------------------------------------------

func TestHandleAuthFactorList_WithRevokedAt(t *testing.T) {
	now := time.Now()
	svc := &fullAuthFactorSvc{listFn: func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return []*authfactors.Enrollment{
			{EnrollmentID: "e1", FactorName: "fido2", UserID: "u1", EnrolledAt: now, RevokedAt: now},
		}, nil
	}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithAuthFactorService(svc))
	payload, _ := json.Marshal(quicListFactorsRequest{})
	resp, err := srv.handleAuthFactorList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries, _ := out["enrollments"].([]any)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// errMissingFieldType.Error() and errMissingField
// ---------------------------------------------------------------------------

func TestErrMissingFieldType_Error(t *testing.T) {
	e := &errMissingFieldType{msg: "field is required"}
	if e.Error() != "field is required" {
		t.Errorf("expected 'field is required', got %q", e.Error())
	}
}

func TestErrMissingField_Returns(t *testing.T) {
	err := errMissingField("test message")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Error() != "test message" {
		t.Errorf("expected 'test message', got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// handlers_barrier_pin.go — applyBarrierInitializePINs and verifyBarrierSOPIN
// ---------------------------------------------------------------------------

func TestApplyBarrierInitializePINs_NilBackend(t *testing.T) {
	err := applyBarrierInitializePINs(nil, "sopin", "userpin")
	if err != nil {
		t.Fatalf("expected nil with nil backend, got %v", err)
	}
}

func TestApplyBarrierInitializePINs_SetSOPIN(t *testing.T) {
	called := false
	backend := &trackingPINBackend{
		setSOPINFn: func(_, newPIN string) error {
			called = true
			if newPIN != "sopin123" {
				return errors.New("wrong sopin")
			}
			return nil
		},
	}
	err := applyBarrierInitializePINs(backend, "sopin123", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected SetSOPIN to be called")
	}
}

func TestApplyBarrierInitializePINs_SetSOPINError(t *testing.T) {
	svcErr := errors.New("sopin error")
	backend := &trackingPINBackend{
		setSOPINFn: func(_, _ string) error { return svcErr },
	}
	err := applyBarrierInitializePINs(backend, "sopin", "")
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

func TestApplyBarrierInitializePINs_SetUserPIN(t *testing.T) {
	called := false
	backend := &trackingPINBackend{
		setUserPINFn: func(_, newPIN string) error {
			called = true
			if newPIN != "userpin123" {
				return errors.New("wrong userpin")
			}
			return nil
		},
	}
	err := applyBarrierInitializePINs(backend, "", "userpin123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected SetUserPIN to be called")
	}
}

func TestApplyBarrierInitializePINs_SetUserPINError(t *testing.T) {
	svcErr := errors.New("userpin error")
	backend := &trackingPINBackend{
		setUserPINFn: func(_, _ string) error { return svcErr },
	}
	err := applyBarrierInitializePINs(backend, "", "userpin")
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

func TestVerifyBarrierSOPIN_NilBackend(t *testing.T) {
	err := verifyBarrierSOPIN(nil, "sopin")
	if err != nil {
		t.Fatalf("expected nil with nil backend, got %v", err)
	}
}

func TestVerifyBarrierSOPIN_SOPINNotSet(t *testing.T) {
	backend := &trackingPINBackend{sopinSetFn: func() bool { return false }}
	err := verifyBarrierSOPIN(backend, "sopin")
	if err != nil {
		t.Fatalf("expected nil when sopin not set, got %v", err)
	}
}

func TestVerifyBarrierSOPIN_VerifySOPIN(t *testing.T) {
	called := false
	backend := &trackingPINBackend{
		sopinSetFn: func() bool { return true },
		verifySOPINFn: func(p string) error {
			called = true
			if p != "correct" {
				return errors.New("wrong pin")
			}
			return nil
		},
	}
	err := verifyBarrierSOPIN(backend, "correct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected VerifySOPIN to be called")
	}
}

func TestVerifyBarrierSOPIN_VerifySOPINError(t *testing.T) {
	svcErr := errors.New("wrong pin")
	backend := &trackingPINBackend{
		sopinSetFn:    func() bool { return true },
		verifySOPINFn: func(_ string) error { return svcErr },
	}
	err := verifyBarrierSOPIN(backend, "wrong")
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

// trackingPINBackend is a test-only PINBackend that allows per-method overrides.
type trackingPINBackend struct {
	mockPINBackend
	setSOPINFn    func(current, newPIN string) error
	setUserPINFn  func(soPIN, newPIN string) error
	sopinSetFn    func() bool
	verifySOPINFn func(pin string) error
}

func (b *trackingPINBackend) SetSOPIN(current, newPIN string) error {
	if b.setSOPINFn != nil {
		return b.setSOPINFn(current, newPIN)
	}
	return nil
}

func (b *trackingPINBackend) SetUserPIN(soPIN, newPIN string) error {
	if b.setUserPINFn != nil {
		return b.setUserPINFn(soPIN, newPIN)
	}
	return nil
}

func (b *trackingPINBackend) SOPINSet() bool {
	if b.sopinSetFn != nil {
		return b.sopinSetFn()
	}
	return false
}

func (b *trackingPINBackend) VerifySOPIN(p string) error {
	if b.verifySOPINFn != nil {
		return b.verifySOPINFn(p)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Database handler — MissingName (unique: not in handlers_collection_database_test.go)
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateEtcd_MissingName(t *testing.T) {
	srv := newDatabaseServer()
	payload, _ := json.Marshal(databaseUpdateEtcdRequest{Name: ""})
	_, err := srv.handleDatabaseUpdateEtcd(context.Background(), payload)
	var ia *InvalidArgumentError
	if !errors.As(err, &ia) {
		t.Fatalf("expected InvalidArgumentError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// User auth factor handler error paths (unique: not in any full-tag test file)
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorList_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorList(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorList_ServiceError(t *testing.T) {
	svcErr := errors.New("list error")
	svc := &listErrUserAuthFactorSvc{err: svcErr}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	payload, _ := json.Marshal(quicUserFactorListRequest{UserID: 1})
	_, err := srv.handleUserAuthFactorList(context.Background(), payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

type listErrUserAuthFactorSvc struct {
	mockUserAuthFactorSvc
	err error
}

func (s *listErrUserAuthFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	return nil, s.err
}

func TestHandleUserAuthFactorGet_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorGet(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorRemove_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorRemove(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorRotate_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorRotate(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorRelabel_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorRelabel(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorRelabel_ServiceError(t *testing.T) {
	svcErr := errors.New("relabel error")
	svc := &relabelErrUserAuthFactorSvc{err: svcErr}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	payload, _ := json.Marshal(quicUserFactorRelabelRequest{FactorID: "f1", Label: "new"})
	_, err := srv.handleUserAuthFactorRelabel(context.Background(), payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

type relabelErrUserAuthFactorSvc struct {
	mockUserAuthFactorSvc
	err error
}

func (s *relabelErrUserAuthFactorSvc) Relabel(_ context.Context, _ string, _ string) error {
	return s.err
}

func TestHandleUserAuthFactorLogin_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorLogin(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

// Ensure proto and pb are referenced to avoid unused import errors.
var _ = proto.Marshal
var _ = (*pb.BarrierShamirInitializeRequest)(nil)

// Ensure service and database are referenced.
var _ = (*service.ShamirInitInfo)(nil)
var _ = (*database.DatabaseInfo)(nil)
