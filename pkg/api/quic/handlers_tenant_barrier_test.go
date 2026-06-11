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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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
	var initResp quicTenantBarrierInitResponse
	if jsonErr := json.Unmarshal(resp, &initResp); jsonErr != nil {
		t.Fatalf("unmarshal init response: %v", jsonErr)
	}
	if initResp.TenantID != "acme" {
		t.Errorf("expected tenant_id=acme in init response, got %q", initResp.TenantID)
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

// ---------------------------------------------------------------------------
// ctxWithFakeTLSQuic — injects a self-signed cert as the mTLS peer cert in ctx
// ---------------------------------------------------------------------------

func ctxWithFakeTLSQuic() context.Context {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("ctxWithFakeTLSQuic: generate key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-officer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("ctxWithFakeTLSQuic: create cert: " + err.Error())
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic("ctxWithFakeTLSQuic: parse cert: " + err.Error())
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	return auth.WithTLSState(context.Background(), state)
}

// ---------------------------------------------------------------------------
// extMockQUICTenantService — wraps mockTenantService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockQUICTenantService struct {
	*mockTenantService
	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	officerClaimStatusFn func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn         func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn      func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
	shamirSealVoteFn     func(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error)
}

func newExtQUICTenantSvc() *extMockQUICTenantService {
	return &extMockQUICTenantService{mockTenantService: &mockTenantService{}}
}

var _ tenant.TenantServicer = (*extMockQUICTenantService)(nil)

func (e *extMockQUICTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, tenantID, req)
	}
	return e.mockTenantService.TenantBarrierOfficerRegister(ctx, tenantID, req)
}
func (e *extMockQUICTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerList(ctx, tenantID)
}
func (e *extMockQUICTenantService) TenantBarrierOfficerClaimStatus(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerClaimStatusFn != nil {
		return e.officerClaimStatusFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerClaimStatus(ctx, tenantID)
}
func (e *extMockQUICTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if e.claimBeginFn != nil {
		return e.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimBegin(ctx, tenantID, username, thumbprint)
}
func (e *extMockQUICTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if e.claimCompleteFn != nil {
		return e.claimCompleteFn(ctx, tenantID, username, nonceHex, sig, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimComplete(ctx, tenantID, username, nonceHex, sig, thumbprint)
}
func (e *extMockQUICTenantService) TenantBarrierShamirSealVote(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error) {
	if e.shamirSealVoteFn != nil {
		return e.shamirSealVoteFn(ctx, tenantID, share)
	}
	return e.mockTenantService.TenantBarrierShamirSealVote(ctx, tenantID, share)
}

// Delegation pass-through for all other TenantServicer methods.

func (e *extMockQUICTenantService) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error) {
	return e.mockTenantService.CreateTenant(ctx, id, name, iso)
}
func (e *extMockQUICTenantService) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	return e.mockTenantService.GetTenant(ctx, id)
}
func (e *extMockQUICTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	return e.mockTenantService.ListTenants(ctx)
}
func (e *extMockQUICTenantService) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateTenant(ctx, id, p)
}
func (e *extMockQUICTenantService) DeleteTenant(ctx context.Context, id string) error {
	return e.mockTenantService.DeleteTenant(ctx, id)
}
func (e *extMockQUICTenantService) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateQuotas(ctx, id, q)
}
func (e *extMockQUICTenantService) TenantBarrierInit(ctx context.Context, id, passphrase string, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}
func (e *extMockQUICTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id, passphrase string, threshold, total int, sid seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInitWithStrategy(ctx, id, passphrase, threshold, total, sid)
}
func (e *extMockQUICTenantService) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	return e.mockTenantService.TenantBarrierUnseal(ctx, id, key)
}
func (e *extMockQUICTenantService) TenantBarrierUnsealShare(ctx context.Context, id, share string) (*tenant.TenantBarrierUnsealProgress, error) {
	return e.mockTenantService.TenantBarrierUnsealShare(ctx, id, share)
}
func (e *extMockQUICTenantService) TenantBarrierSeal(ctx context.Context, id string) error {
	return e.mockTenantService.TenantBarrierSeal(ctx, id)
}
func (e *extMockQUICTenantService) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	return e.mockTenantService.TenantBarrierStatus(ctx, id)
}
func (e *extMockQUICTenantService) IncrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.IncrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockQUICTenantService) DecrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.DecrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockQUICTenantService) GetQuotaUsage(ctx context.Context, id string, r quota.Resource) (int64, error) {
	return e.mockTenantService.GetQuotaUsage(ctx, id, r)
}

// ---------------------------------------------------------------------------
// quicTenantCertThumbprint unit tests
// ---------------------------------------------------------------------------

func TestQuicTenantCertThumbprint_MissingTLS(t *testing.T) {
	_, err := quicTenantCertThumbprint(context.Background())
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestQuicTenantCertThumbprint_Success(t *testing.T) {
	ctx := ctxWithFakeTLSQuic()
	thumb, err := quicTenantCertThumbprint(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == "" {
		t.Fatal("expected non-empty thumbprint")
	}
}

// ---------------------------------------------------------------------------
// quicTenantOfficerStatusesToResponse — zero / non-zero ClaimedAt paths
// ---------------------------------------------------------------------------

func TestQuicTenantOfficerStatusesToResponse_Fields(t *testing.T) {
	now := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	officers := []*ceremony.OfficerStatus{
		{
			Username:       "alice",
			CertThumbprint: "abc",
			ShareIndex:     1,
			AuthFactor:     "software",
			SharePending:   true,
			EnrolledAt:     now,
			ClaimedAt:      time.Time{}, // zero — ClaimedAt must be empty string
		},
		{
			Username:   "bob",
			ShareIndex: 2,
			EnrolledAt: now,
			ClaimedAt:  now, // non-zero — ClaimedAt must be non-empty
		},
	}

	resp := quicTenantOfficerStatusesToResponse(officers)
	if len(resp) != 2 {
		t.Fatalf("len = %d, want 2", len(resp))
	}

	// First: unclaimed → ClaimedAt should be empty.
	if resp[0].ClaimedAt != "" {
		t.Fatalf("claimed_at present for unclaimed officer: %q", resp[0].ClaimedAt)
	}
	if resp[0].Username != "alice" {
		t.Fatalf("username = %q, want alice", resp[0].Username)
	}

	// Second: claimed → ClaimedAt should be non-empty.
	if resp[1].ClaimedAt == "" {
		t.Fatal("claimed_at empty for claimed officer")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierOfficerRegister tests
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierOfficerRegister_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, _ := json.Marshal(quicTenantOfficerRegisterRequest{
		TenantID: "acme",
		Username: "officer1",
		CSRPEM:   "csr-data",
	})
	_, err := srv.handleTenantBarrierOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var snce *ServiceNotConfiguredError
	if !errors.As(err, &snce) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierOfficerRegister_QUIC_MissingTenantID(t *testing.T) {
	svc := newExtQUICTenantSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerRegisterRequest{Username: "officer1", CSRPEM: "csr-data"})
	_, err := srv.handleTenantBarrierOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected InvalidArgumentError")
	}
	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierOfficerRegister_QUIC_Success(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.officerRegisterFn = func(_ context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
		if tenantID != "acme" {
			t.Fatalf("tenantID = %q, want acme", tenantID)
		}
		return &ceremony.RegisterOfficerResult{CertPEM: []byte("tenant-cert"), ShareIndex: 1}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerRegisterRequest{
		TenantID: "acme",
		Username: "officer1",
		CSRPEM:   "csr-data",
	})
	resp, err := srv.handleTenantBarrierOfficerRegister(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out quicTenantOfficerRegisterResponse
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out.CertPEM != "tenant-cert" {
		t.Fatalf("cert_pem = %q, want tenant-cert", out.CertPEM)
	}
}

func TestHandleTenantBarrierOfficerRegister_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.officerRegisterFn = func(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
		return nil, ceremony.ErrInsufficientOfficers
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerRegisterRequest{
		TenantID: "acme",
		Username: "officer1",
		CSRPEM:   "csr-data",
	})
	_, err := srv.handleTenantBarrierOfficerRegister(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrInsufficientOfficers) {
		t.Fatalf("expected ErrInsufficientOfficers, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierOfficerList tests
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierOfficerList_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, _ := json.Marshal(quicTenantOfficerListRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierOfficerList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleTenantBarrierOfficerList_QUIC_MissingTenantID(t *testing.T) {
	svc := newExtQUICTenantSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerListRequest{})
	_, err := srv.handleTenantBarrierOfficerList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected InvalidArgumentError")
	}
	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierOfficerList_QUIC_Success(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	svc := newExtQUICTenantSvc()
	svc.officerListFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return []*ceremony.OfficerStatus{
			{Username: "alice", ShareIndex: 1, EnrolledAt: now},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerListRequest{TenantID: "acme"})
	resp, err := srv.handleTenantBarrierOfficerList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	officers, ok := out["officers"].([]interface{})
	if !ok || len(officers) != 1 {
		t.Fatalf("expected 1 officer, got %v", out["officers"])
	}
}

func TestHandleTenantBarrierOfficerList_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.officerListFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerListRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierOfficerList(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierOfficerClaimStatus tests
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierOfficerClaimStatus_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, _ := json.Marshal(quicTenantOfficerListRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierOfficerClaimStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleTenantBarrierOfficerClaimStatus_QUIC_Success(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 0, 0, 0, time.UTC)
	svc := newExtQUICTenantSvc()
	svc.officerClaimStatusFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return []*ceremony.OfficerStatus{
			{Username: "bob", ShareIndex: 2, ClaimedAt: now},
		}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerListRequest{TenantID: "acme"})
	resp, err := srv.handleTenantBarrierOfficerClaimStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	officers, ok := out["officers"].([]interface{})
	if !ok || len(officers) != 1 {
		t.Fatalf("expected 1 officer, got %v", out["officers"])
	}
}

func TestHandleTenantBarrierOfficerClaimStatus_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.officerClaimStatusFn = func(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantOfficerListRequest{TenantID: "acme"})
	_, err := srv.handleTenantBarrierOfficerClaimStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierShareClaimBegin tests
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierShareClaimBegin_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, _ := json.Marshal(quicTenantShareClaimBeginRequest{TenantID: "acme", Username: "officer1"})
	_, err := srv.handleTenantBarrierShareClaimBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleTenantBarrierShareClaimBegin_QUIC_MissingCert(t *testing.T) {
	svc := newExtQUICTenantSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantShareClaimBeginRequest{TenantID: "acme", Username: "officer1"})
	_, err := srv.handleTenantBarrierShareClaimBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestHandleTenantBarrierShareClaimBegin_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.claimBeginFn = func(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantShareClaimBeginRequest{TenantID: "acme", Username: "officer1"})
	_, err := srv.handleTenantBarrierShareClaimBegin(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrOfficerNotFound) {
		t.Fatalf("expected ErrOfficerNotFound, got %v", err)
	}
}

func TestHandleTenantBarrierShareClaimBegin_QUIC_Success(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := newExtQUICTenantSvc()
	svc.claimBeginFn = func(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
		return &ceremony.ChallengeResponse{Nonce: []byte{0xde, 0xad, 0xbe, 0xef}, ExpiresAt: expires}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantShareClaimBeginRequest{TenantID: "acme", Username: "officer1"})
	resp, err := srv.handleTenantBarrierShareClaimBegin(ctxWithFakeTLSQuic(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out quicTenantShareClaimBeginResponse
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out.NonceHex == "" {
		t.Fatal("nonce_hex missing from claim-begin response")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierShareClaimComplete tests
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierShareClaimComplete_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	payload, _ := json.Marshal(quicTenantShareClaimCompleteRequest{TenantID: "acme", Username: "officer1", NonceHex: "aabb", Signature: sig})
	_, err := srv.handleTenantBarrierShareClaimComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleTenantBarrierShareClaimComplete_QUIC_MissingCert(t *testing.T) {
	svc := newExtQUICTenantSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	payload, _ := json.Marshal(quicTenantShareClaimCompleteRequest{TenantID: "acme", Username: "officer1", NonceHex: "aabb", Signature: sig})
	_, err := srv.handleTenantBarrierShareClaimComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ClientCertRequiredError")
	}
	var ce *ClientCertRequiredError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClientCertRequiredError, got %T", err)
	}
}

func TestHandleTenantBarrierShareClaimComplete_QUIC_BadBase64(t *testing.T) {
	svc := newExtQUICTenantSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantShareClaimCompleteRequest{TenantID: "acme", Username: "officer1", NonceHex: "aabb", Signature: "!!!bad!!!"})
	_, err := srv.handleTenantBarrierShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected InvalidArgumentError for bad base64 signature")
	}
	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierShareClaimComplete_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.claimCompleteFn = func(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
		return nil, ceremony.ErrCertThumbprintMismatch
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	payload, _ := json.Marshal(quicTenantShareClaimCompleteRequest{TenantID: "acme", Username: "officer1", NonceHex: "aabb", Signature: sig})
	_, err := srv.handleTenantBarrierShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, ceremony.ErrCertThumbprintMismatch) {
		t.Fatalf("expected ErrCertThumbprintMismatch, got %v", err)
	}
}

func TestHandleTenantBarrierShareClaimComplete_QUIC_Success(t *testing.T) {
	shareBytes := []byte("tenant-share-bytes")
	svc := newExtQUICTenantSvc()
	svc.claimCompleteFn = func(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
		return &ceremony.ShareClaimResult{ShareBytes: shareBytes}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	payload, _ := json.Marshal(quicTenantShareClaimCompleteRequest{TenantID: "acme", Username: "officer1", NonceHex: "aabb", Signature: sig})
	resp, err := srv.handleTenantBarrierShareClaimComplete(ctxWithFakeTLSQuic(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out quicTenantShareClaimCompleteResponse
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	// INV-11: share_bytes must appear exactly here.
	if out.ShareBytes == "" {
		t.Fatal("share_bytes missing from claim-complete response")
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierShamirSealVote tests
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierShamirSealVote_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})

	payload, _ := json.Marshal(quicTenantSealVoteRequest{TenantID: "acme", Share: "abc"})
	_, err := srv.handleTenantBarrierShamirSealVote(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
}

func TestHandleTenantBarrierShamirSealVote_QUIC_MissingTenantID(t *testing.T) {
	svc := newExtQUICTenantSvc()
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantSealVoteRequest{Share: "abc"})
	_, err := srv.handleTenantBarrierShamirSealVote(context.Background(), payload)
	if err == nil {
		t.Fatal("expected InvalidArgumentError")
	}
	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierShamirSealVote_QUIC_Success(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
		return &tenant.TenantBarrierUnsealProgress{Required: 3, Submitted: 1, Complete: false}, nil
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantSealVoteRequest{TenantID: "acme", Share: "abc123"})
	resp, err := srv.handleTenantBarrierShamirSealVote(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out["required"] == nil {
		t.Fatal("required missing from seal-vote response")
	}
}

func TestHandleTenantBarrierShamirSealVote_QUIC_ServiceError(t *testing.T) {
	svc := newExtQUICTenantSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
		return nil, seal.ErrSealVoteVerificationFailed
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	payload, _ := json.Marshal(quicTenantSealVoteRequest{TenantID: "acme", Share: "abc"})
	_, err := srv.handleTenantBarrierShamirSealVote(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, seal.ErrSealVoteVerificationFailed) {
		t.Fatalf("expected ErrSealVoteVerificationFailed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnsealShare — covers the previously-zero function
// ---------------------------------------------------------------------------

// tenantSvcWithUnsealShare embeds extMockQUICTenantService and overrides
// TenantBarrierUnsealShare to return a controllable result.
type tenantSvcWithUnsealShare struct {
	*extMockQUICTenantService
	unsealShareFn func(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error)
}

func (t *tenantSvcWithUnsealShare) TenantBarrierUnsealShare(ctx context.Context, tenantID, share string) (*tenant.TenantBarrierUnsealProgress, error) {
	if t.unsealShareFn != nil {
		return t.unsealShareFn(ctx, tenantID, share)
	}
	return t.extMockQUICTenantService.TenantBarrierUnsealShare(ctx, tenantID, share)
}

func TestHandleTenantBarrierUnsealShare_QUIC_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(map[string]string{"tenant_id": "acme", "share": "abc"})
	_, err := srv.handleTenantBarrierUnsealShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError")
	}
	var snce *ServiceNotConfiguredError
	if !errors.As(err, &snce) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierUnsealShare_QUIC_UnmarshalError(t *testing.T) {
	svc := &tenantSvcWithUnsealShare{extMockQUICTenantService: newExtQUICTenantSvc()}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	_, err := srv.handleTenantBarrierUnsealShare(context.Background(), []byte(`oops`))
	if err == nil {
		t.Fatal("expected UnmarshalError")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierUnsealShare_QUIC_MissingTenantID(t *testing.T) {
	svc := &tenantSvcWithUnsealShare{extMockQUICTenantService: newExtQUICTenantSvc()}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(map[string]string{"share": "abc"})
	_, err := srv.handleTenantBarrierUnsealShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected InvalidArgumentError")
	}
	var iae *InvalidArgumentError
	if !errors.As(err, &iae) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierUnsealShare_QUIC_ServiceError(t *testing.T) {
	svc := &tenantSvcWithUnsealShare{
		extMockQUICTenantService: newExtQUICTenantSvc(),
		unsealShareFn: func(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
			return nil, seal.ErrShamirBarrierRequiresQuorum
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(map[string]string{"tenant_id": "acme", "share": "abc"})
	_, err := srv.handleTenantBarrierUnsealShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, seal.ErrShamirBarrierRequiresQuorum) {
		t.Fatalf("expected ErrShamirBarrierRequiresQuorum, got %v", err)
	}
}

func TestHandleTenantBarrierUnsealShare_QUIC_Success(t *testing.T) {
	svc := &tenantSvcWithUnsealShare{
		extMockQUICTenantService: newExtQUICTenantSvc(),
		unsealShareFn: func(_ context.Context, tenantID, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
			return &tenant.TenantBarrierUnsealProgress{
				TenantID:  tenantID,
				Required:  2,
				Submitted: 1,
				Complete:  false,
				Sealed:    true,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(map[string]string{"tenant_id": "acme", "share": "share-x"})
	resp, err := srv.handleTenantBarrierUnsealShare(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]interface{}
	if jsonErr := json.Unmarshal(resp, &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out["tenant_id"] != "acme" {
		t.Fatalf("tenant_id = %v, want acme", out["tenant_id"])
	}
	if out["required"] == nil {
		t.Fatal("required missing from response")
	}
}
