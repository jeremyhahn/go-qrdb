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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleBarrierInitialize
// ---------------------------------------------------------------------------

func TestHandleBarrierInitialize_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierInitializeRequest{Secret: []byte("mysecret")})

	_, err := srv.handleBarrierInitialize(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleBarrierInitialize_Success(t *testing.T) {
	called := false
	barrierSvc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, secret []byte) error {
			called = true
			if string(secret) != "mysecret" {
				t.Errorf("expected secret 'mysecret', got %q", secret)
			}
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, err := proto.Marshal(&pb.BarrierInitializeRequest{Secret: []byte("mysecret")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	resp, err := srv.handleBarrierInitialize(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response for barrier initialize")
	}
	if !called {
		t.Fatal("expected barrier initialize to be called")
	}
}

func TestHandleBarrierInitialize_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "init failed"}
	barrierSvc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, _ []byte) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, _ := proto.Marshal(&pb.BarrierInitializeRequest{Secret: []byte("s")})
	_, err := srv.handleBarrierInitialize(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleBarrierInitialize_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierInitialize(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleBarrierUnseal_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierUnsealRequest{Secret: []byte("s")})
	_, err := srv.handleBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleBarrierUnseal_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierUnsealFn: func(_ context.Context, secret []byte) error {
			if string(secret) != "unseal-secret" {
				t.Errorf("expected secret 'unseal-secret', got %q", secret)
			}
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, _ := proto.Marshal(&pb.BarrierUnsealRequest{Secret: []byte("unseal-secret")})
	resp, err := srv.handleBarrierUnseal(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
}

func TestHandleBarrierUnseal_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "unseal failed"}
	barrierSvc := &mockBarrierService{
		barrierUnsealFn: func(_ context.Context, _ []byte) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierUnsealRequest{Secret: []byte("s")})
	_, err := srv.handleBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestHandleBarrierUnseal_UnmarshalError verifies that a corrupt payload
// returns an UnmarshalError.
func TestHandleBarrierUnseal_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierUnseal(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleBarrierSeal_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierSeal(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleBarrierSeal_Success(t *testing.T) {
	called := false
	barrierSvc := &mockBarrierService{
		barrierSealFn: func(_ context.Context) error {
			called = true
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc),
		WithPINBackend(authtest.BarrierPINBackend(t, "seal-so-pin")))
	payload, err := proto.Marshal(&pb.BarrierSealRequest{SoPin: "seal-so-pin"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := srv.handleBarrierSeal(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected seal to be called")
	}
}

func TestHandleBarrierSeal_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "seal failed"}
	barrierSvc := &mockBarrierService{
		barrierSealFn: func(_ context.Context) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierSeal(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleBarrierStatus_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleBarrierStatus_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:         false,
				Strategy:       "shamir",
				HardwareBacked: true,
				InitializedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	resp, err := srv.handleBarrierStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var statusResp pb.BarrierStatusResponse
	if err := proto.Unmarshal(resp, &statusResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if statusResp.GetSealed() {
		t.Error("expected sealed=false")
	}
	if statusResp.GetStrategy() != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", statusResp.GetStrategy())
	}
	if !statusResp.GetHardwareBacked() {
		t.Error("expected hardware_backed=true")
	}
}

// TestHandleBarrierStatus_Uninitialized is the pre-init answer: a barrier
// never initialized carries an empty InitializedAt, as on every other
// transport, so a client can tell a node waiting for its first boot.
func TestHandleBarrierStatus_Uninitialized(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	resp, err := srv.handleBarrierStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var statusResp pb.BarrierStatusResponse
	if err := proto.Unmarshal(resp, &statusResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !statusResp.GetSealed() || statusResp.GetInitializedAt() != "" {
		t.Fatalf("status = %+v, want sealed with no InitializedAt", &statusResp)
	}
}

func TestHandleBarrierStatus_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "status error"}
	barrierSvc := &mockBarrierService{
		barrierStatusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirInitialize
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirInitialize_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierShamirInitialize(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryGenerate
// ---------------------------------------------------------------------------

// TestHandleBarrierRecoveryGenerate_UnmarshalError verifies that a corrupt
// payload returns an UnmarshalError.
func TestHandleBarrierRecoveryGenerate_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierRecoveryGenerate(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryDelete
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryDelete_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierRecoveryDelete(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRecoveryDelete_Success(t *testing.T) {
	called := false
	barrierSvc := &mockBarrierService{
		barrierDeleteRecoveryKeysFn: func(_ context.Context) error { called = true; return nil },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	resp, err := srv.handleBarrierRecoveryDelete(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected delete to be called")
	}
}

func TestHandleBarrierRecoveryDelete_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "delete error"}
	barrierSvc := &mockBarrierService{
		barrierDeleteRecoveryKeysFn: func(_ context.Context) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierRecoveryDelete(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryStatus
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryStatus_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierRecoveryStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRecoveryStatus_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierHasRecoveryKeysFn: func(_ context.Context) (bool, error) { return true, nil },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	resp, err := srv.handleBarrierRecoveryStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var statusResp pb.BarrierRecoveryStatusResponse
	if err := proto.Unmarshal(resp, &statusResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !statusResp.GetHasRecoveryKeys() {
		t.Error("expected has_recovery_keys=true")
	}
}

func TestHandleBarrierRecoveryStatus_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "status error"}
	barrierSvc := &mockBarrierService{
		barrierHasRecoveryKeysFn: func(_ context.Context) (bool, error) { return false, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierRecoveryStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// quicCertThumbprint (system barrier variant) tests
// ---------------------------------------------------------------------------

func TestQuicCertThumbprint_MissingTLS(t *testing.T) {
	_, err := quicCertThumbprint(context.Background())
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *service.ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *service.ClientCertRequiredError, got %T", err)
	}
}

func TestQuicCertThumbprint_Success(t *testing.T) {
	ctx := ctxWithFakeTLSQuic()
	thumb, err := quicCertThumbprint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == "" {
		t.Fatal("expected non-empty thumbprint")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierOfficerRegister tests
// ---------------------------------------------------------------------------

func TestHandleBarrierOfficerRegister_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierOfficerRegisterRequest{
		Username: "officer1",
		CsrPem:   []byte("csr-pem"),
	})
	_, err := srv.handleBarrierOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var snce *ServiceNotConfiguredError
	if !errors.As(err, &snce) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierOfficerList tests
// ---------------------------------------------------------------------------

func TestHandleBarrierOfficerList_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierOfficerList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var snce *ServiceNotConfiguredError
	if !errors.As(err, &snce) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShareClaimBegin tests
// ---------------------------------------------------------------------------

func TestHandleBarrierShareClaimBegin_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "officer1"})
	_, err := srv.handleBarrierShareClaimBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShareClaimComplete tests
// ---------------------------------------------------------------------------

func TestHandleBarrierShareClaimComplete_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "officer1",
		NonceHex:  "aabb",
		Signature: []byte("sig"),
	})
	_, err := srv.handleBarrierShareClaimComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryOfficerRegister tests
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryOfficerRegister_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierOfficerRegisterRequest{Username: "recovery-officer"})
	_, err := srv.handleBarrierRecoveryOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryShareClaimBegin tests
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryShareClaimBegin_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "rec-officer"})
	_, err := srv.handleBarrierRecoveryShareClaimBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryShareClaimComplete tests
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryShareClaimComplete_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "rec-officer",
		NonceHex:  "aabb",
		Signature: []byte("sig"),
	})
	_, err := srv.handleBarrierRecoveryShareClaimComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}
