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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleCeremonyInitialize
// ---------------------------------------------------------------------------

func TestHandleCeremonyInitialize_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleCeremonyInitialize(context.Background(), nil)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

// TestHandleCeremonyInitialize_NilCeremonyCAReachesTheService proves a
// server with a ceremony service but no CeremonyCA (WithCeremonyCA never
// applied, as a BYO-TLS deployment configures it) reaches the ceremony
// service layer with ca == nil rather than being refused at the transport.
// ceremony.CeremonyService.Initialize is nil-safe by construction -- a
// BYO-TLS node pins its own configured leaf instead of self-issuing one --
// so refusing here (the pre-fix behavior, go-qrdb#560 review item 5) only
// blocked `qrdb init` on a BYO-TLS deployment using this transport, for no
// safety reason: the nil CA never panics, it never even reaches an
// unchecked dereference.
func TestHandleCeremonyInitialize_NilCeremonyCAReachesTheService(t *testing.T) {
	ceremonySvc := &mockCeremonyService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

	if _, err := srv.handleCeremonyInitialize(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ceremonySvc.capturedCA != nil {
		t.Fatalf("expected the service to be called with a nil CA (BYO-TLS), got %#v", ceremonySvc.capturedCA)
	}
}

func TestHandleCeremonyInitialize_Success(t *testing.T) {
	ceremonySvc := &mockCeremonyService{
		ceremonyInitializeFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return &service.CeremonyInitResult{
				SPKIPin:    "sha256/testpin",
				CACertPEM:  []byte("ca-cert"),
				TLSCertPEM: []byte("tls-cert"),
				State:      "active",
				ShareCount: 5,
			}, nil
		},
	}
	ca := stubCeremonyCA{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc), WithCeremonyCA(ca))

	resp, err := srv.handleCeremonyInitialize(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var initResp pb.CeremonyInitializeResponse
	if err := proto.Unmarshal(resp, &initResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if initResp.GetSpkiPin() != "sha256/testpin" {
		t.Errorf("expected spki_pin 'sha256/testpin', got %q", initResp.GetSpkiPin())
	}
	if initResp.GetCaCertPem() != "ca-cert" {
		t.Errorf("expected ca_cert_pem 'ca-cert', got %q", initResp.GetCaCertPem())
	}
	if initResp.GetState() != "active" {
		t.Errorf("expected state 'active', got %q", initResp.GetState())
	}
	if initResp.GetShareCount() != 5 {
		t.Errorf("expected share_count 5, got %d", initResp.GetShareCount())
	}
	if ceremonySvc.capturedCA != ca {
		t.Errorf("expected the configured CeremonyCA to reach the service layer, got %#v", ceremonySvc.capturedCA)
	}
}

// TestHandleCeremonyInitialize_ForwardsAllFields is the regression guard for
// the bug where the QUIC handleCeremonyInitialize handler only forwarded
// SoPin/UserPin into service.CeremonyInitializeOptions, silently dropping
// CredentialSealStrategy, Threshold, Officers, Hostname, and Sans from the
// wire request. Also covers the admin-credential fields (go-qrdb#548
// review, B2): admin_csr_pem/admin_cert_pem/admin_cert_pool_pem/
// admin_generate/secret on the request, and admin_cert_pem/admin_key_pem/
// admin_credential_source on the response -- holderpb.CeremonyInitFromPB/
// CeremonyInitToPB, the SAME conversion gRPC and Unix use (Unix shares
// gRPC's qrdbServiceServer over a different socket).
func TestHandleCeremonyInitialize_ForwardsAllFields(t *testing.T) {
	ceremonySvc := &mockCeremonyService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc), WithCeremonyCA(stubCeremonyCA{}))

	payload, err := proto.Marshal(&pb.CeremonyInitializeRequest{
		SoPin:                  "pin",
		UserPin:                "upin",
		CredentialSealStrategy: "shamir",
		Threshold:              2,
		Officers: []*pb.CeremonyOfficer{
			{Username: "officer-1", CsrPem: "csr1"},
			{Username: "officer-2", CsrPem: "csr2"},
		},
		Hostname:         "override.example",
		Sans:             []string{"alt1.example", "alt2.example"},
		AdminCsrPem:      "admin-csr",
		AdminCertPem:     "admin-cert-in",
		AdminCertPoolPem: "admin-pool",
		AdminGenerate:    true,
		Secret:           "barrier-secret",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	respBytes, err := srv.handleCeremonyInitialize(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var initResp pb.CeremonyInitializeResponse
	if err := proto.Unmarshal(respBytes, &initResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	got := ceremonySvc.capturedInitOpts
	if got == nil {
		t.Fatal("expected CeremonyInitialize to be called with non-nil opts")
	}
	if got.SOPin != "pin" || got.UserPin != "upin" {
		t.Errorf("SOPin/UserPin not forwarded: %+v", got)
	}
	if got.CredentialSealStrategy != "shamir" {
		t.Errorf("CredentialSealStrategy = %q, want %q", got.CredentialSealStrategy, "shamir")
	}
	if got.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", got.Threshold)
	}
	if got.Hostname != "override.example" {
		t.Errorf("Hostname = %q, want %q", got.Hostname, "override.example")
	}
	if len(got.SANs) != 2 || got.SANs[0] != "alt1.example" {
		t.Errorf("SANs = %v", got.SANs)
	}
	if len(got.Officers) != 2 || got.Officers[0].Username != "officer-1" || got.Officers[0].CSRPEM != "csr1" {
		t.Errorf("Officers = %+v", got.Officers)
	}
	if got.AdminCSRPEM != "admin-csr" || got.AdminCertPEM != "admin-cert-in" || got.AdminCertPoolPEM != "admin-pool" {
		t.Errorf("admin-credential request fields not forwarded: %+v", got)
	}
	if !got.AdminGenerate {
		t.Error("AdminGenerate not forwarded")
	}
	if got.Secret != "barrier-secret" {
		t.Errorf("Secret = %q, want %q", got.Secret, "barrier-secret")
	}
	if initResp.GetAdminCertPem() != "admin-cert" || initResp.GetAdminKeyPem() != "admin-key" || initResp.GetAdminCredentialSource() != "generated" {
		t.Errorf("admin-credential response fields not forwarded: cert=%q key=%q source=%q",
			initResp.GetAdminCertPem(), initResp.GetAdminKeyPem(), initResp.GetAdminCredentialSource())
	}
}

func TestHandleCeremonyInitialize_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "ceremony init error"}
	ceremonySvc := &mockCeremonyService{
		ceremonyInitializeFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc), WithCeremonyCA(stubCeremonyCA{}))
	_, err := srv.handleCeremonyInitialize(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyStatus
// ---------------------------------------------------------------------------

func TestHandleCeremonyStatus_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	_, err := srv.handleCeremonyStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCeremonyStatus_Success(t *testing.T) {
	ceremonySvc := &mockCeremonyService{
		ceremonyStatusFn: func(_ context.Context) (*service.CeremonyStatusInfo, error) {
			return &service.CeremonyStatusInfo{State: "pending", SPKIPin: "sha256/pin"}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

	resp, err := srv.handleCeremonyStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var statusResp pb.CeremonyStatusResponse
	if err := proto.Unmarshal(resp, &statusResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if statusResp.GetState() != "pending" {
		t.Errorf("expected state 'pending', got %q", statusResp.GetState())
	}
	if statusResp.GetSpkiPin() != "sha256/pin" {
		t.Errorf("expected spki_pin 'sha256/pin', got %q", statusResp.GetSpkiPin())
	}
}

func TestHandleCeremonyStatus_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "status error"}
	ceremonySvc := &mockCeremonyService{
		ceremonyStatusFn: func(_ context.Context) (*service.CeremonyStatusInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))
	_, err := srv.handleCeremonyStatus(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// handleCeremonySignCSR
// ---------------------------------------------------------------------------

func TestHandleCeremonySignCSR_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.CeremonySignCSRRequest{Username: "u", SoPin: "pin", CsrPem: "csr", Role: "admin"})
	_, err := srv.handleCeremonySignCSR(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonySignCSR_Success(t *testing.T) {
	ceremonySvc := &mockCeremonyService{
		ceremonySignCSRFn: func(_ context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
			return &service.CeremonySignCSRResult{CertPEM: []byte("issued-cert")}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

	payload, _ := proto.Marshal(&pb.CeremonySignCSRRequest{
		Username: "officer1",
		SoPin:    "secret-pin",
		CsrPem:   "-----BEGIN CERTIFICATE REQUEST-----",
		Role:     "admin",
	})
	resp, err := srv.handleCeremonySignCSR(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var csrResp pb.CeremonySignCSRResponse
	if err := proto.Unmarshal(resp, &csrResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if csrResp.GetCertPem() != "issued-cert" {
		t.Errorf("expected cert_pem 'issued-cert', got %q", csrResp.GetCertPem())
	}
}

func TestHandleCeremonySignCSR_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "sign error"}
	ceremonySvc := &mockCeremonyService{
		ceremonySignCSRFn: func(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))
	payload, _ := proto.Marshal(&pb.CeremonySignCSRRequest{Username: "u", SoPin: "p", CsrPem: "c", Role: "r"})
	_, err := srv.handleCeremonySignCSR(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonySignCSR_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(&mockCeremonyService{}))
	_, err := srv.handleCeremonySignCSR(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}
