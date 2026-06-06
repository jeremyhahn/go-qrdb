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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// handleTenantBarrierInit
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
	if sncErr.Service != "tenant" {
		t.Errorf("expected service 'tenant', got %q", sncErr.Service)
	}
}

func TestHandleTenantBarrierInit_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierInit(context.Background(), []byte(`{not-json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierInit_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{})

	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierInit_Success(t *testing.T) {
	called := false
	var gotID, gotPassphrase string
	var gotThreshold, gotShares int
	svc := &mockTenantService{
		barrierInitFn: func(_ context.Context, id, passphrase string, threshold, shares int) error {
			called = true
			gotID = id
			gotPassphrase = passphrase
			gotThreshold = threshold
			gotShares = shares
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Passphrase: "supplied-secret", Threshold: 3, Shares: 5})

	resp, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response payload, got %d bytes", len(resp))
	}
	if !called {
		t.Fatal("expected TenantBarrierInit to be called")
	}
	if gotID != "acme" {
		t.Errorf("expected id 'acme', got %q", gotID)
	}
	if gotThreshold != 3 {
		t.Errorf("expected threshold 3, got %d", gotThreshold)
	}
	if gotShares != 5 {
		t.Errorf("expected shares 5, got %d", gotShares)
	}
	if gotPassphrase != "supplied-secret" {
		t.Errorf("expected passphrase 'supplied-secret', got %q", gotPassphrase)
	}
}

func TestHandleTenantBarrierInit_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierInitFn: func(_ context.Context, _, _ string, _, _ int) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "missing"})

	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierUnseal_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierUnseal(context.Background(), []byte(`oops`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{Key: []byte("k")})

	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_KeyPreferredOverShare(t *testing.T) {
	var gotMaterial []byte
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, share []byte) error {
			gotMaterial = share
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{
		TenantID: "acme",
		Share:    []byte("share-data"),
		Key:      []byte("root-key"),
	})

	if _, err := srv.handleTenantBarrierUnseal(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(gotMaterial, []byte("root-key")) {
		t.Errorf("expected key to be preferred, got %q", gotMaterial)
	}
}

func TestHandleTenantBarrierUnseal_ShareWhenKeyEmpty(t *testing.T) {
	var gotMaterial []byte
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, share []byte) error {
			gotMaterial = share
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{
		TenantID: "acme",
		Share:    []byte("share-data"),
	})

	if _, err := srv.handleTenantBarrierUnseal(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(gotMaterial, []byte("share-data")) {
		t.Errorf("expected share data, got %q", gotMaterial)
	}
}

func TestHandleTenantBarrierUnseal_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, _ []byte) error {
			return &testError{msg: "unseal failed"}
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierSeal_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierSeal(context.Background(), []byte(`)(*&`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{})

	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_Success(t *testing.T) {
	called := false
	var gotID string
	svc := &mockTenantService{
		barrierSealFn: func(_ context.Context, id string) error {
			called = true
			gotID = id
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "acme"})

	resp, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response, got %d bytes", len(resp))
	}
	if !called || gotID != "acme" {
		t.Errorf("expected Seal('acme'), called=%v id=%q", called, gotID)
	}
}

func TestHandleTenantBarrierSeal_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierSealFn: func(_ context.Context, _ string) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "missing"})

	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierStatus_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierStatus(context.Background(), []byte(`bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{})

	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_Success(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:       id,
				Sealed:         false,
				Initialized:    true,
				Strategy:       "shamir",
				HardwareBacked: false,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})

	resp, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got quicTenantBarrierStatusResponse
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TenantID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", got.TenantID)
	}
	if got.Sealed {
		t.Error("expected Sealed=false")
	}
	if !got.Initialized {
		t.Error("expected Initialized=true")
	}
	if got.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", got.Strategy)
	}
	if got.HardwareBacked {
		t.Error("expected HardwareBacked=false")
	}
}

func TestHandleTenantBarrierStatus_HardwareBacked(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:       id,
				Sealed:         true,
				Initialized:    true,
				Strategy:       "pkcs11",
				HardwareBacked: true,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})

	resp, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got quicTenantBarrierStatusResponse
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.HardwareBacked {
		t.Error("expected HardwareBacked=true")
	}
	if got.Strategy != "pkcs11" {
		t.Errorf("expected strategy 'pkcs11', got %q", got.Strategy)
	}
}

func TestHandleTenantBarrierStatus_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "missing"})

	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// missingTenantIDError
// ---------------------------------------------------------------------------

func TestMissingTenantIDError_Message(t *testing.T) {
	e := &missingTenantIDError{}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// Dispatch registration: confirm the four barrier handlers wire up when a
// tenant service is configured.
// ---------------------------------------------------------------------------

func TestTenantBarrier_DispatchRegistered(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	srv.buildHandlers()
	for _, method := range []string{
		"tenant.BarrierInit",
		"tenant.BarrierUnseal",
		"tenant.BarrierSeal",
		"tenant.BarrierStatus",
	} {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("expected handler %q to be registered", method)
		}
	}
}

func TestTenantBarrier_DispatchSkippedWhenServiceAbsent(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	srv.buildHandlers()
	for _, method := range []string{
		"tenant.BarrierInit",
		"tenant.BarrierUnseal",
		"tenant.BarrierSeal",
		"tenant.BarrierStatus",
	} {
		if _, ok := srv.handlers[method]; ok {
			t.Errorf("expected handler %q to be absent when tenantSvc is nil", method)
		}
	}
}
