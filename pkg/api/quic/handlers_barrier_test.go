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
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleBarrierInitialize
// ---------------------------------------------------------------------------

func TestHandleBarrierInitialize_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierInitializeRequest{Secret: "mysecret"})

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
		barrierInitializeFn: func(_ context.Context, secret string) error {
			called = true
			if secret != "mysecret" {
				t.Errorf("expected secret 'mysecret', got %q", secret)
			}
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, err := proto.Marshal(&pb.BarrierInitializeRequest{Secret: "mysecret"})
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
		barrierInitializeFn: func(_ context.Context, _ string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, _ := proto.Marshal(&pb.BarrierInitializeRequest{Secret: "s"})
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
	payload, _ := proto.Marshal(&pb.BarrierUnsealRequest{Secret: "s"})
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
		barrierUnsealFn: func(_ context.Context, secret string) error {
			if secret != "unseal-secret" {
				t.Errorf("expected secret 'unseal-secret', got %q", secret)
			}
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, _ := proto.Marshal(&pb.BarrierUnsealRequest{Secret: "unseal-secret"})
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
		barrierUnsealFn: func(_ context.Context, _ string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierUnsealRequest{Secret: "s"})
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
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	resp, err := srv.handleBarrierSeal(context.Background(), nil)
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

func TestHandleBarrierShamirInitialize_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShamirInitializeRequest{Secret: "s", Threshold: 2, TotalShares: 3})
	_, err := srv.handleBarrierShamirInitialize(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirInitialize_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierInitializeShamirFn: func(_ context.Context, secret string, threshold, total int) (*service.ShamirInitInfo, error) {
			return &service.ShamirInitInfo{
				Threshold:   threshold,
				TotalShares: total,
				ShareCount:  total,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, _ := proto.Marshal(&pb.BarrierShamirInitializeRequest{Secret: "s", Threshold: 2, TotalShares: 3})
	resp, err := srv.handleBarrierShamirInitialize(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// BarrierShamirOperationResponse: shares omitted (INV-1/INV-11).
	var initResp pb.BarrierShamirOperationResponse
	if err := proto.Unmarshal(resp, &initResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if initResp.GetShareCount() != 3 {
		t.Errorf("expected share_count=3, got %d", initResp.GetShareCount())
	}
	if initResp.GetThreshold() != 2 {
		t.Errorf("expected threshold 2, got %d", initResp.GetThreshold())
	}
	if initResp.GetTotalShares() != 3 {
		t.Errorf("expected total_shares 3, got %d", initResp.GetTotalShares())
	}
}

func TestHandleBarrierShamirInitialize_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "shamir init error"}
	barrierSvc := &mockBarrierService{
		barrierInitializeShamirFn: func(_ context.Context, _ string, _, _ int) (*service.ShamirInitInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirInitializeRequest{Secret: "s", Threshold: 2, TotalShares: 3})
	_, err := srv.handleBarrierShamirInitialize(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

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
// handleBarrierShamirUnsealShare
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirUnsealShare_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShamirUnsealShareRequest{Share: "s1"})
	_, err := srv.handleBarrierShamirUnsealShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirUnsealShare_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierUnsealWithShareFn: func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
			return &service.UnsealShareProgress{Required: 3, Submitted: 1, Complete: false}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirUnsealShareRequest{Share: "share-1"})
	resp, err := srv.handleBarrierShamirUnsealShare(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var unsealResp pb.BarrierShamirUnsealShareResponse
	if err := proto.Unmarshal(resp, &unsealResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if unsealResp.GetRequired() != 3 {
		t.Errorf("expected required=3, got %d", unsealResp.GetRequired())
	}
	if unsealResp.GetSubmitted() != 1 {
		t.Errorf("expected submitted=1, got %d", unsealResp.GetSubmitted())
	}
	if unsealResp.GetComplete() {
		t.Error("expected complete=false")
	}
}

func TestHandleBarrierShamirUnsealShare_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "share error"}
	barrierSvc := &mockBarrierService{
		barrierUnsealWithShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirUnsealShareRequest{Share: "s"})
	_, err := srv.handleBarrierShamirUnsealShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestHandleBarrierShamirUnsealShare_UnmarshalError verifies that a corrupt
// payload returns an UnmarshalError.
func TestHandleBarrierShamirUnsealShare_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierShamirUnsealShare(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirUnsealShares
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirUnsealShares_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShamirUnsealSharesRequest{Shares: []string{"s1", "s2"}})
	_, err := srv.handleBarrierShamirUnsealShares(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirUnsealShares_Success(t *testing.T) {
	var capturedShares []string
	barrierSvc := &mockBarrierService{
		barrierUnsealWithSharesFn: func(_ context.Context, shares []string) error {
			capturedShares = shares
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirUnsealSharesRequest{Shares: []string{"s1", "s2", "s3"}})
	resp, err := srv.handleBarrierShamirUnsealShares(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if len(capturedShares) != 3 {
		t.Errorf("expected 3 shares, got %d", len(capturedShares))
	}
}

// TestHandleBarrierShamirUnsealShares_ServiceError verifies that a service
// error is propagated correctly.
func TestHandleBarrierShamirUnsealShares_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "unseal shares error"}
	barrierSvc := &mockBarrierService{
		barrierUnsealWithSharesFn: func(_ context.Context, _ []string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirUnsealSharesRequest{Shares: []string{"s1", "s2"}})
	_, err := srv.handleBarrierShamirUnsealShares(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

// TestHandleBarrierShamirUnsealShares_UnmarshalError verifies that a corrupt
// payload returns an UnmarshalError.
func TestHandleBarrierShamirUnsealShares_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierShamirUnsealShares(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRekey
// ---------------------------------------------------------------------------

func TestHandleBarrierRekey_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierRekeyRequest{Threshold: 2, Total: 3})
	_, err := srv.handleBarrierRekey(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRekey_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierRekeyFn: func(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
			return &service.ShamirInitInfo{
				Threshold:   threshold,
				TotalShares: total,
				ShareCount:  total,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRekeyRequest{Threshold: 2, Total: 3})
	resp, err := srv.handleBarrierRekey(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// BarrierShamirOperationResponse: shares omitted (INV-1/INV-11).
	var rekeyResp pb.BarrierShamirOperationResponse
	if err := proto.Unmarshal(resp, &rekeyResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rekeyResp.GetShareCount() != 3 {
		t.Errorf("expected share_count=3, got %d", rekeyResp.GetShareCount())
	}
}

func TestHandleBarrierRekey_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "rekey error"}
	barrierSvc := &mockBarrierService{
		barrierRekeyFn: func(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRekeyRequest{Threshold: 2, Total: 3})
	_, err := srv.handleBarrierRekey(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRekey_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierRekey(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryGenerate
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryGenerate_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierRecoveryGenerateRequest{Threshold: 2, Total: 3})
	_, err := srv.handleBarrierRecoveryGenerate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRecoveryGenerate_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierGenRecoveryKeysFn: func(_ context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
			return &service.RecoveryKeysInfo{Threshold: threshold, Total: total, ShareCount: total}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRecoveryGenerateRequest{Threshold: 2, Total: 2})
	resp, err := srv.handleBarrierRecoveryGenerate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var genResp pb.BarrierRecoveryKeysResponse
	if err := proto.Unmarshal(resp, &genResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if genResp.GetShareCount() != 2 {
		t.Errorf("expected share_count=2, got %d", genResp.GetShareCount())
	}
}

func TestHandleBarrierRecoveryGenerate_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "gen error"}
	barrierSvc := &mockBarrierService{
		barrierGenRecoveryKeysFn: func(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRecoveryGenerateRequest{Threshold: 2, Total: 3})
	_, err := srv.handleBarrierRecoveryGenerate(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

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
// handleBarrierRecoveryRecover
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryRecover_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierRecoveryRecoverRequest{Keys: []string{"k1"}})
	_, err := srv.handleBarrierRecoveryRecover(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRecoveryRecover_Success(t *testing.T) {
	var capturedKeys []string
	barrierSvc := &mockBarrierService{
		barrierRecoverWithKeysFn: func(_ context.Context, keys []string) error {
			capturedKeys = keys
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRecoveryRecoverRequest{Keys: []string{"key1", "key2"}})
	resp, err := srv.handleBarrierRecoveryRecover(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if len(capturedKeys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(capturedKeys))
	}
}

func TestHandleBarrierRecoveryRecover_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "recover error"}
	barrierSvc := &mockBarrierService{
		barrierRecoverWithKeysFn: func(_ context.Context, _ []string) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRecoveryRecoverRequest{Keys: []string{"k"}})
	_, err := srv.handleBarrierRecoveryRecover(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestHandleBarrierRecoveryRecover_UnmarshalError verifies that a corrupt
// payload returns an UnmarshalError.
func TestHandleBarrierRecoveryRecover_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierRecoveryRecover(context.Background(), []byte{0xff, 0xfe})
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
// handleBarrierRootToken
// ---------------------------------------------------------------------------

func TestHandleBarrierRootToken_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierRootTokenRequest{Shares: []string{"s1", "s2"}})
	_, err := srv.handleBarrierRootToken(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRootToken_Success(t *testing.T) {
	fixedTime := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	barrierSvc := &mockBarrierService{
		barrierGenerateRootTokenFn: func(_ context.Context, shares []string) (*service.RootTokenInfo, error) {
			return &service.RootTokenInfo{Token: "deadbeef0123", CreatedAt: fixedTime}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRootTokenRequest{Shares: []string{"s1", "s2", "s3"}})
	resp, err := srv.handleBarrierRootToken(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tokenResp pb.BarrierRootTokenResponse
	if err := proto.Unmarshal(resp, &tokenResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tokenResp.GetToken() != "deadbeef0123" {
		t.Errorf("expected token 'deadbeef0123', got %q", tokenResp.GetToken())
	}
	if tokenResp.GetCreatedAt() == "" {
		t.Error("expected non-empty created_at")
	}
}

func TestHandleBarrierRootToken_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "root token error"}
	barrierSvc := &mockBarrierService{
		barrierGenerateRootTokenFn: func(_ context.Context, _ []string) (*service.RootTokenInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRootTokenRequest{Shares: []string{"s1"}})
	_, err := srv.handleBarrierRootToken(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierRootToken_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierRootToken(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// extMockQUICBarrierService — extends mockBarrierService with SO fn overrides
// ---------------------------------------------------------------------------

type extMockQUICBarrierService struct {
	*mockBarrierService
	officerRegisterFn            func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn                func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	officerClaimStatusFn         func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn            func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn         func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	shamirSealVoteFn             func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	recoveryOfficerRegisterFn    func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	recoveryShareClaimBeginFn    func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	recoveryShareClaimCompleteFn func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
}

func newExtQUICBarrierSvc() *extMockQUICBarrierService {
	return &extMockQUICBarrierService{mockBarrierService: &mockBarrierService{}}
}

var _ service.BarrierServicer = (*extMockQUICBarrierService)(nil)

func (e *extMockQUICBarrierService) BarrierOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, req)
	}
	return e.mockBarrierService.BarrierOfficerRegister(ctx, req)
}

func (e *extMockQUICBarrierService) BarrierOfficerList(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx)
	}
	return e.mockBarrierService.BarrierOfficerList(ctx)
}

func (e *extMockQUICBarrierService) BarrierOfficerClaimStatus(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if e.officerClaimStatusFn != nil {
		return e.officerClaimStatusFn(ctx)
	}
	return e.mockBarrierService.BarrierOfficerClaimStatus(ctx)
}

func (e *extMockQUICBarrierService) BarrierShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if e.shareClaimBeginFn != nil {
		return e.shareClaimBeginFn(ctx, username, thumbprint)
	}
	return e.mockBarrierService.BarrierShareClaimBegin(ctx, username, thumbprint)
}

func (e *extMockQUICBarrierService) BarrierShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if e.shareClaimCompleteFn != nil {
		return e.shareClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return e.mockBarrierService.BarrierShareClaimComplete(ctx, username, nonceHex, sig, thumbprint)
}

func (e *extMockQUICBarrierService) BarrierShamirSealVote(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if e.shamirSealVoteFn != nil {
		return e.shamirSealVoteFn(ctx, share)
	}
	return e.mockBarrierService.BarrierShamirSealVote(ctx, share)
}

func (e *extMockQUICBarrierService) BarrierRecoveryOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if e.recoveryOfficerRegisterFn != nil {
		return e.recoveryOfficerRegisterFn(ctx, req)
	}
	return e.mockBarrierService.BarrierRecoveryOfficerRegister(ctx, req)
}

func (e *extMockQUICBarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if e.recoveryShareClaimBeginFn != nil {
		return e.recoveryShareClaimBeginFn(ctx, username, thumbprint)
	}
	return e.mockBarrierService.BarrierRecoveryShareClaimBegin(ctx, username, thumbprint)
}

func (e *extMockQUICBarrierService) BarrierRecoveryShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if e.recoveryShareClaimCompleteFn != nil {
		return e.recoveryShareClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return e.mockBarrierService.BarrierRecoveryShareClaimComplete(ctx, username, nonceHex, sig, thumbprint)
}

// ---------------------------------------------------------------------------
// quicCertThumbprint (system barrier variant) tests
// ---------------------------------------------------------------------------

func TestQuicCertThumbprint_MissingTLS(t *testing.T) {
	_, err := quicCertThumbprint(context.Background())
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
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
// quicBarrierOfficerStatusesToProto tests
// ---------------------------------------------------------------------------

func TestQuicBarrierOfficerStatusesToProto_Fields(t *testing.T) {
	now := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	officers := []*service.BarrierOfficerStatus{
		{
			Username:       "alice",
			CertThumbprint: "abc123",
			ShareIndex:     1,
			AuthFactor:     "software",
			SharePending:   true,
			EnrolledAt:     now,
			ClaimedAt:      time.Time{}, // zero — ClaimedAt should be empty
		},
		{
			Username:   "bob",
			ShareIndex: 2,
			EnrolledAt: now,
			ClaimedAt:  now, // non-zero — ClaimedAt should be set
		},
	}

	out := quicBarrierOfficerStatusesToProto(officers)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}

	if out[0].GetUsername() != "alice" {
		t.Fatalf("username = %q, want alice", out[0].GetUsername())
	}
	if out[0].GetClaimedAt() != "" {
		t.Fatalf("claimed_at present for unclaimed officer: %q", out[0].GetClaimedAt())
	}
	if out[0].GetSharePending() != true {
		t.Fatal("share_pending should be true")
	}

	if out[1].GetClaimedAt() == "" {
		t.Fatal("claimed_at empty for claimed officer")
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

func TestHandleBarrierOfficerRegister_QUIC_Success(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.officerRegisterFn = func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		if req.Username != "officer1" {
			t.Fatalf("username = %q, want officer1", req.Username)
		}
		return &service.BarrierOfficerRegisterResult{
			CertPEM:    []byte("officer-cert-pem"),
			ShareIndex: 2,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierOfficerRegisterRequest{
		Username: "officer1",
		CsrPem:   []byte("csr-pem"),
	})
	resp, err := srv.handleBarrierOfficerRegister(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierOfficerRegisterResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if string(out.GetCertPem()) != "officer-cert-pem" {
		t.Fatalf("cert_pem = %q, want officer-cert-pem", out.GetCertPem())
	}
	if out.GetShareIndex() != 2 {
		t.Fatalf("share_index = %d, want 2", out.GetShareIndex())
	}
}

func TestHandleBarrierOfficerRegister_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.officerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, ceremony.ErrInsufficientOfficers
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierOfficerRegisterRequest{Username: "officer1"})
	_, err := srv.handleBarrierOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrInsufficientOfficers) {
		t.Fatalf("expected ErrInsufficientOfficers, got %v", err)
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

func TestHandleBarrierOfficerList_QUIC_Success(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	svc := newExtQUICBarrierSvc()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "alice", ShareIndex: 1, EnrolledAt: now},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	resp, err := srv.handleBarrierOfficerList(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierOfficerListResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if len(out.GetOfficers()) != 1 {
		t.Fatalf("officers len = %d, want 1", len(out.GetOfficers()))
	}
	if out.GetOfficers()[0].GetUsername() != "alice" {
		t.Fatalf("username = %q, want alice", out.GetOfficers()[0].GetUsername())
	}
}

func TestHandleBarrierOfficerList_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	_, err := srv.handleBarrierOfficerList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrOfficerNotFound) {
		t.Fatalf("expected ErrOfficerNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierOfficerClaimStatus tests
// ---------------------------------------------------------------------------

func TestHandleBarrierOfficerClaimStatus_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierOfficerClaimStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBarrierOfficerClaimStatus_QUIC_Success(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 0, 0, 0, time.UTC)
	svc := newExtQUICBarrierSvc()
	svc.officerClaimStatusFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "bob", ShareIndex: 2, EnrolledAt: now, ClaimedAt: now},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	resp, err := srv.handleBarrierOfficerClaimStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierOfficerClaimStatusResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if len(out.GetOfficers()) != 1 {
		t.Fatalf("officers len = %d, want 1", len(out.GetOfficers()))
	}
	if out.GetOfficers()[0].GetClaimedAt() == "" {
		t.Fatal("claimed_at empty for claimed officer")
	}
}

func TestHandleBarrierOfficerClaimStatus_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.officerClaimStatusFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	_, err := srv.handleBarrierOfficerClaimStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error from service")
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

func TestHandleBarrierShareClaimBegin_QUIC_MissingCert(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "officer1"})
	_, err := srv.handleBarrierShareClaimBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestHandleBarrierShareClaimBegin_QUIC_Success(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := newExtQUICBarrierSvc()
	svc.shareClaimBeginFn = func(_ context.Context, username, _ string) (*service.BarrierShareClaimChallenge, error) {
		if username != "officer1" {
			t.Fatalf("username = %q, want officer1", username)
		}
		return &service.BarrierShareClaimChallenge{
			NonceHex:  "deadbeef",
			ExpiresAt: expires,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "officer1"})
	resp, err := srv.handleBarrierShareClaimBegin(ctxWithFakeTLSQuic(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierShareClaimBeginResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if out.GetNonceHex() != "deadbeef" {
		t.Fatalf("nonce_hex = %q, want deadbeef", out.GetNonceHex())
	}
	if out.GetExpiresAt() == "" {
		t.Fatal("expires_at should be set")
	}
}

func TestHandleBarrierShareClaimBegin_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.shareClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "officer1"})
	_, err := srv.handleBarrierShareClaimBegin(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrOfficerNotFound) {
		t.Fatalf("expected ErrOfficerNotFound, got %v", err)
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

func TestHandleBarrierShareClaimComplete_QUIC_MissingCert(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "officer1",
		NonceHex:  "aabb",
		Signature: []byte("sig"),
	})
	_, err := srv.handleBarrierShareClaimComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestHandleBarrierShareClaimComplete_QUIC_Success(t *testing.T) {
	shareBytes := []byte("system-share-bytes")
	svc := newExtQUICBarrierSvc()
	svc.shareClaimCompleteFn = func(_ context.Context, username, nonceHex string, sig []byte, _ string) (*service.BarrierShareClaimResult, error) {
		if username != "officer1" {
			t.Fatalf("username = %q, want officer1", username)
		}
		return &service.BarrierShareClaimResult{
			ShareIndex: 1,
			ShareBytes: shareBytes,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "officer1",
		NonceHex:  "deadbeef",
		Signature: []byte("sig"),
	})
	resp, err := srv.handleBarrierShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierShareClaimCompleteResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	// INV-11: share_bytes must appear exactly in the claim-complete response.
	if len(out.GetShareBytes()) == 0 {
		t.Fatal("share_bytes missing from claim-complete response")
	}
	if out.GetShareIndex() != 1 {
		t.Fatalf("share_index = %d, want 1", out.GetShareIndex())
	}
}

func TestHandleBarrierShareClaimComplete_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.shareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return nil, ceremony.ErrCertThumbprintMismatch
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "officer1",
		NonceHex:  "aabb",
		Signature: []byte("sig"),
	})
	_, err := srv.handleBarrierShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrCertThumbprintMismatch) {
		t.Fatalf("expected ErrCertThumbprintMismatch, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirSealVote tests
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirSealVote_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShamirSealVoteRequest{Share: "share-abc"})
	_, err := srv.handleBarrierShamirSealVote(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleBarrierShamirSealVote_QUIC_Success(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.shamirSealVoteFn = func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
		if share != "share-abc" {
			t.Fatalf("share = %q, want share-abc", share)
		}
		return &service.UnsealShareProgress{Required: 3, Submitted: 1, Complete: false}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShamirSealVoteRequest{Share: "share-abc"})
	resp, err := srv.handleBarrierShamirSealVote(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierShamirSealVoteResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if out.GetRequired() != 3 {
		t.Fatalf("required = %d, want 3", out.GetRequired())
	}
	if out.GetSubmitted() != 1 {
		t.Fatalf("submitted = %d, want 1", out.GetSubmitted())
	}
	if out.GetComplete() {
		t.Fatal("complete should be false")
	}
}

func TestHandleBarrierShamirSealVote_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, seal.ErrSealVoteVerificationFailed
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShamirSealVoteRequest{Share: "bad-share"})
	_, err := srv.handleBarrierShamirSealVote(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, seal.ErrSealVoteVerificationFailed) {
		t.Fatalf("expected ErrSealVoteVerificationFailed, got %v", err)
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

func TestHandleBarrierRecoveryOfficerRegister_QUIC_Success(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.recoveryOfficerRegisterFn = func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return &service.BarrierOfficerRegisterResult{
			CertPEM:    []byte("recovery-cert-pem"),
			ShareIndex: 3,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierOfficerRegisterRequest{
		Username: "recovery-officer",
		CsrPem:   []byte("recovery-csr"),
	})
	resp, err := srv.handleBarrierRecoveryOfficerRegister(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierOfficerRegisterResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if string(out.GetCertPem()) != "recovery-cert-pem" {
		t.Fatalf("cert_pem = %q, want recovery-cert-pem", out.GetCertPem())
	}
}

func TestHandleBarrierRecoveryOfficerRegister_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.recoveryOfficerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, errors.New("recovery register failed")
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierOfficerRegisterRequest{Username: "recovery-officer"})
	_, err := srv.handleBarrierRecoveryOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
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

func TestHandleBarrierRecoveryShareClaimBegin_QUIC_MissingCert(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "rec-officer"})
	_, err := srv.handleBarrierRecoveryShareClaimBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestHandleBarrierRecoveryShareClaimBegin_QUIC_Success(t *testing.T) {
	expires := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	svc := newExtQUICBarrierSvc()
	svc.recoveryShareClaimBeginFn = func(_ context.Context, username, _ string) (*service.BarrierShareClaimChallenge, error) {
		return &service.BarrierShareClaimChallenge{
			NonceHex:  "cafebabe",
			ExpiresAt: expires,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "rec-officer"})
	resp, err := srv.handleBarrierRecoveryShareClaimBegin(ctxWithFakeTLSQuic(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierShareClaimBeginResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	if out.GetNonceHex() != "cafebabe" {
		t.Fatalf("nonce_hex = %q, want cafebabe", out.GetNonceHex())
	}
}

func TestHandleBarrierRecoveryShareClaimBegin_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.recoveryShareClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return nil, ceremony.ErrNoPendingShare
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimBeginRequest{Username: "rec-officer"})
	_, err := srv.handleBarrierRecoveryShareClaimBegin(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrNoPendingShare) {
		t.Fatalf("expected ErrNoPendingShare, got %v", err)
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

func TestHandleBarrierRecoveryShareClaimComplete_QUIC_MissingCert(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "rec-officer",
		NonceHex:  "aabb",
		Signature: []byte("sig"),
	})
	_, err := srv.handleBarrierRecoveryShareClaimComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestHandleBarrierRecoveryShareClaimComplete_QUIC_Success(t *testing.T) {
	recoveryShareBytes := []byte("recovery-share-bytes")
	svc := newExtQUICBarrierSvc()
	svc.recoveryShareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return &service.BarrierShareClaimResult{
			ShareIndex: 3,
			ShareBytes: recoveryShareBytes,
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "rec-officer",
		NonceHex:  "cafebabe",
		Signature: []byte("valid-sig"),
	})
	resp, err := srv.handleBarrierRecoveryShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out pb.BarrierShareClaimCompleteResponse
	if protoErr := proto.Unmarshal(resp, &out); protoErr != nil {
		t.Fatalf("unmarshal: %v", protoErr)
	}
	// INV-11: share_bytes appears only in claim-complete.
	if len(out.GetShareBytes()) == 0 {
		t.Fatal("share_bytes missing from recovery claim-complete response")
	}
	if out.GetShareIndex() != 3 {
		t.Fatalf("share_index = %d, want 3", out.GetShareIndex())
	}
}

func TestHandleBarrierRecoveryShareClaimComplete_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	svc.recoveryShareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return nil, ceremony.ErrShareAlreadyClaimed
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))

	payload, _ := proto.Marshal(&pb.BarrierShareClaimCompleteRequest{
		Username:  "rec-officer",
		NonceHex:  "aabb",
		Signature: []byte("sig"),
	})
	_, err := srv.handleBarrierRecoveryShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrShareAlreadyClaimed) {
		t.Fatalf("expected ErrShareAlreadyClaimed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// UnmarshalError paths — corrupt protobuf payloads hit the unmarshal branch
// ---------------------------------------------------------------------------

func TestHandleBarrierOfficerRegister_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierOfficerRegister(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBarrierShareClaimBegin_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierShareClaimBegin(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBarrierShareClaimComplete_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierShareClaimComplete(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBarrierShamirSealVote_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierShamirSealVote(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBarrierRecoveryOfficerRegister_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierRecoveryOfficerRegister(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBarrierRecoveryShareClaimBegin_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierRecoveryShareClaimBegin(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleBarrierRecoveryShareClaimComplete_QUIC_UnmarshalError(t *testing.T) {
	svc := newExtQUICBarrierSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(svc))
	_, err := srv.handleBarrierRecoveryShareClaimComplete(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}
