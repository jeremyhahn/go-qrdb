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

func TestHandleCeremonyInitialize_Success(t *testing.T) {
	ceremonySvc := &mockCeremonyService{
		ceremonyInitializeFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return &service.CeremonyInitResult{
				SPKIPin:    "sha256/testpin",
				CACertPEM:  []byte("ca-cert"),
				TLSCertPEM: []byte("tls-cert"),
				SOCertPEM:  []byte("so-cert"),
				SOKeyPEM:   []byte("so-key"),
				State:      "active",
				ShareCount: 5,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

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
}

func TestHandleCeremonyInitialize_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "ceremony init error"}
	ceremonySvc := &mockCeremonyService{
		ceremonyInitializeFn: func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))
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
// handleCeremonyClaimCertBegin
// ---------------------------------------------------------------------------

func TestHandleCeremonyClaimCertBegin_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.CeremonyClaimCertBeginRequest{Username: "officer1"})
	_, err := srv.handleCeremonyClaimCertBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonyClaimCertBegin_Success(t *testing.T) {
	fixedTime := time.Date(2026, 3, 16, 13, 0, 0, 0, time.UTC)
	ceremonySvc := &mockCeremonyService{
		ceremonyBeginClaimCertFn: func(_ context.Context, username string) (*service.CeremonyChallenge, error) {
			if username != "officer1" {
				t.Errorf("expected username 'officer1', got %q", username)
			}
			return &service.CeremonyChallenge{NonceHex: "cafebabe", ExpiresAt: fixedTime}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

	payload, _ := proto.Marshal(&pb.CeremonyClaimCertBeginRequest{Username: "officer1"})
	resp, err := srv.handleCeremonyClaimCertBegin(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var beginResp pb.CeremonyClaimCertBeginResponse
	if err := proto.Unmarshal(resp, &beginResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if beginResp.GetNonceHex() != "cafebabe" {
		t.Errorf("expected nonce_hex 'cafebabe', got %q", beginResp.GetNonceHex())
	}
	if beginResp.GetExpiresAt() == "" {
		t.Error("expected non-empty expires_at")
	}
}

func TestHandleCeremonyClaimCertBegin_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "begin error"}
	ceremonySvc := &mockCeremonyService{
		ceremonyBeginClaimCertFn: func(_ context.Context, _ string) (*service.CeremonyChallenge, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))
	payload, _ := proto.Marshal(&pb.CeremonyClaimCertBeginRequest{Username: "u"})
	_, err := srv.handleCeremonyClaimCertBegin(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonyClaimCertBegin_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(&mockCeremonyService{}))
	_, err := srv.handleCeremonyClaimCertBegin(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyClaimCertComplete
// ---------------------------------------------------------------------------

func TestHandleCeremonyClaimCertComplete_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.CeremonyClaimCertCompleteRequest{Username: "u", NonceHex: "n", Signature: "sig"})
	_, err := srv.handleCeremonyClaimCertComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonyClaimCertComplete_Success(t *testing.T) {
	ceremonySvc := &mockCeremonyService{
		ceremonyCompleteClaimCertFn: func(_ context.Context, username, nonceHex string, signature []byte) (*service.CeremonyClaimCertResult, error) {
			return &service.CeremonyClaimCertResult{
				CertPEM:   []byte("officer-cert"),
				CACertPEM: []byte("ca-cert"),
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

	payload, _ := proto.Marshal(&pb.CeremonyClaimCertCompleteRequest{
		Username:  "officer1",
		NonceHex:  "cafebabe",
		Signature: "base64sig",
	})
	resp, err := srv.handleCeremonyClaimCertComplete(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var completeResp pb.CeremonyClaimCertCompleteResponse
	if err := proto.Unmarshal(resp, &completeResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if completeResp.GetCertPem() != "officer-cert" {
		t.Errorf("expected cert_pem 'officer-cert', got %q", completeResp.GetCertPem())
	}
	if completeResp.GetCaCertPem() != "ca-cert" {
		t.Errorf("expected ca_cert_pem 'ca-cert', got %q", completeResp.GetCaCertPem())
	}
}

func TestHandleCeremonyClaimCertComplete_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "complete error"}
	ceremonySvc := &mockCeremonyService{
		ceremonyCompleteClaimCertFn: func(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))
	payload, _ := proto.Marshal(&pb.CeremonyClaimCertCompleteRequest{Username: "u", NonceHex: "n", Signature: "s"})
	_, err := srv.handleCeremonyClaimCertComplete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonyClaimCertComplete_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(&mockCeremonyService{}))
	_, err := srv.handleCeremonyClaimCertComplete(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyClaimShare
// ---------------------------------------------------------------------------

func TestHandleCeremonyClaimShare_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.CeremonyClaimShareRequest{Username: "officer1"})
	_, err := srv.handleCeremonyClaimShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonyClaimShare_Success(t *testing.T) {
	ceremonySvc := &mockCeremonyService{
		ceremonyClaimShareFn: func(_ context.Context, username string) ([]byte, error) {
			if username != "officer1" {
				t.Errorf("expected username 'officer1', got %q", username)
			}
			return []byte("my-shamir-share"), nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))

	payload, _ := proto.Marshal(&pb.CeremonyClaimShareRequest{Username: "officer1"})
	resp, err := srv.handleCeremonyClaimShare(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var shareResp pb.CeremonyClaimShareResponse
	if err := proto.Unmarshal(resp, &shareResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if shareResp.GetShare() != "my-shamir-share" {
		t.Errorf("expected share 'my-shamir-share', got %q", shareResp.GetShare())
	}
}

func TestHandleCeremonyClaimShare_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "share error"}
	ceremonySvc := &mockCeremonyService{
		ceremonyClaimShareFn: func(_ context.Context, _ string) ([]byte, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(ceremonySvc))
	payload, _ := proto.Marshal(&pb.CeremonyClaimShareRequest{Username: "u"})
	_, err := srv.handleCeremonyClaimShare(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleCeremonyClaimShare_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCeremonyService(&mockCeremonyService{}))
	_, err := srv.handleCeremonyClaimShare(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
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
