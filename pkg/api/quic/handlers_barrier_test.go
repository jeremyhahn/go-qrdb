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
				Shares:      []string{"share1", "share2", "share3"},
				Threshold:   threshold,
				TotalShares: total,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))

	payload, _ := proto.Marshal(&pb.BarrierShamirInitializeRequest{Secret: "s", Threshold: 2, TotalShares: 3})
	resp, err := srv.handleBarrierShamirInitialize(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var initResp pb.BarrierShamirInitializeResponse
	if err := proto.Unmarshal(resp, &initResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(initResp.GetShares()) != 3 {
		t.Errorf("expected 3 shares, got %d", len(initResp.GetShares()))
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
// handleBarrierShamirListShares
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirListShares_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierShamirListShares(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirListShares_Success(t *testing.T) {
	barrierSvc := &mockBarrierService{
		barrierShamirListSharesFn: func(_ context.Context) (*service.ShamirSharesInfo, error) {
			return &service.ShamirSharesInfo{Count: 2, Threshold: 2, Total: 3}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	resp, err := srv.handleBarrierShamirListShares(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sharesResp pb.BarrierShamirSharesResponse
	if err := proto.Unmarshal(resp, &sharesResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sharesResp.GetCount() != 2 {
		t.Errorf("expected count=2, got %d", sharesResp.GetCount())
	}
	if sharesResp.GetThreshold() != 2 {
		t.Errorf("expected threshold=2, got %d", sharesResp.GetThreshold())
	}
	if sharesResp.GetTotal() != 3 {
		t.Errorf("expected total=3, got %d", sharesResp.GetTotal())
	}
}

func TestHandleBarrierShamirListShares_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "list shares error"}
	barrierSvc := &mockBarrierService{
		barrierShamirListSharesFn: func(_ context.Context) (*service.ShamirSharesInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierShamirListShares(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirDeleteShare
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirDeleteShare_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierShamirDeleteShareRequest{Index: 1})
	_, err := srv.handleBarrierShamirDeleteShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirDeleteShare_Success(t *testing.T) {
	var capturedIndex int
	barrierSvc := &mockBarrierService{
		barrierShamirDeleteShareFn: func(_ context.Context, index int) error {
			capturedIndex = index
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirDeleteShareRequest{Index: 2})
	resp, err := srv.handleBarrierShamirDeleteShare(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if capturedIndex != 2 {
		t.Errorf("expected index 2, got %d", capturedIndex)
	}
}

func TestHandleBarrierShamirDeleteShare_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "delete error"}
	barrierSvc := &mockBarrierService{
		barrierShamirDeleteShareFn: func(_ context.Context, _ int) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierShamirDeleteShareRequest{Index: 1})
	_, err := srv.handleBarrierShamirDeleteShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestHandleBarrierShamirDeleteShare_UnmarshalError verifies that a corrupt
// payload returns an UnmarshalError.
func TestHandleBarrierShamirDeleteShare_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(&mockBarrierService{}))
	_, err := srv.handleBarrierShamirDeleteShare(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirDeleteAllShares
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirDeleteAllShares_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierShamirDeleteAllShares(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirDeleteAllShares_Success(t *testing.T) {
	called := false
	barrierSvc := &mockBarrierService{
		barrierShamirDeleteAllFn: func(_ context.Context) error { called = true; return nil },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	resp, err := srv.handleBarrierShamirDeleteAllShares(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected delete all to be called")
	}
}

func TestHandleBarrierShamirDeleteAllShares_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "delete all error"}
	barrierSvc := &mockBarrierService{
		barrierShamirDeleteAllFn: func(_ context.Context) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierShamirDeleteAllShares(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirVerify
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirVerify_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleBarrierShamirVerify(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleBarrierShamirVerify_Success(t *testing.T) {
	called := false
	barrierSvc := &mockBarrierService{
		barrierShamirVerifyFn: func(_ context.Context) error { called = true; return nil },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	resp, err := srv.handleBarrierShamirVerify(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if !called {
		t.Fatal("expected verify to be called")
	}
}

func TestHandleBarrierShamirVerify_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "verify error"}
	barrierSvc := &mockBarrierService{
		barrierShamirVerifyFn: func(_ context.Context) error { return svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	_, err := srv.handleBarrierShamirVerify(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
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
				Shares:      []string{"new1", "new2", "new3"},
				Threshold:   threshold,
				TotalShares: total,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithBarrierService(barrierSvc))
	payload, _ := proto.Marshal(&pb.BarrierRekeyRequest{Threshold: 2, Total: 3})
	resp, err := srv.handleBarrierRekey(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rekeyResp pb.BarrierShamirInitializeResponse
	if err := proto.Unmarshal(resp, &rekeyResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rekeyResp.GetShares()) != 3 {
		t.Errorf("expected 3 shares, got %d", len(rekeyResp.GetShares()))
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
			return &service.RecoveryKeysInfo{Keys: []string{"k1", "k2"}, Threshold: threshold, Total: total}, nil
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
	if len(genResp.GetKeys()) != 2 {
		t.Errorf("expected 2 keys, got %d", len(genResp.GetKeys()))
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
