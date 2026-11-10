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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

func TestGRPC_CeremonyInitialize_Success(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ca := &stubCeremonyCA{}
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc), WithCeremonyCA(ca))
	defer cleanup()

	resp, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize failed: %v", err)
	}
	if resp.GetSpkiPin() != "pin123" {
		t.Fatalf("expected spki_pin 'pin123', got %q", resp.GetSpkiPin())
	}
	if resp.GetShareCount() != 3 {
		t.Fatalf("expected share_count=3, got %d", resp.GetShareCount())
	}
	if ceremonySvc.capturedCA != ca {
		t.Errorf("expected the configured CeremonyCA to reach the service layer, got %#v", ceremonySvc.capturedCA)
	}
}

// TestGRPC_CeremonyInitialize_WipesAdminKeyPEM proves the generate path's
// private key is wiped from the service-layer result (CeremonyInitResult.
// WipeAdminKeyPEM) once this handler has built its own, independent copy
// for the wire response -- go-qrdb#560 review item 7: "WipeAdminKeyPEM
// runs on every transport, not only REST." The client must still receive
// the correct, un-zeroed key: proving the wipe happens AFTER
// holderpb.CeremonyInitToPB's bytes.Clone, never before it.
func TestGRPC_CeremonyInitialize_WipesAdminKeyPEM(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc), WithCeremonyCA(&stubCeremonyCA{}))
	defer cleanup()

	resp, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize failed: %v", err)
	}
	if string(resp.GetAdminKeyPem()) != "admin-key" {
		t.Fatalf("client must still receive the un-zeroed key, got %q", resp.GetAdminKeyPem())
	}
	if ceremonySvc.initResult.AdminKeyPEM != nil {
		t.Fatalf("expected the service result's AdminKeyPEM to be wiped (nil) after the call, got %q", ceremonySvc.initResult.AdminKeyPEM)
	}
}

// TestGRPC_CeremonyInitialize_NilCeremonyCAReachesTheService proves a
// server with a ceremony service but no CeremonyCA (WithCeremonyCA never
// applied, as a BYO-TLS deployment configures it) reaches the ceremony
// service layer with ca == nil rather than being refused at the transport
// -- shared by gRPC and Unix, which registers this same qrdbServiceServer
// over a different socket.
// ceremony.CeremonyService.Initialize is nil-safe by construction -- a
// BYO-TLS node pins its own configured leaf instead of self-issuing one --
// so refusing here (the pre-fix behavior, go-qrdb#560 review item 5) only
// blocked `qrdb init` on a BYO-TLS deployment using either transport, for
// no safety reason.
func TestGRPC_CeremonyInitialize_NilCeremonyCAReachesTheService(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc))
	defer cleanup()

	if _, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ceremonySvc.capturedCA != nil {
		t.Fatalf("expected the service to be called with a nil CA (BYO-TLS), got %#v", ceremonySvc.capturedCA)
	}
}

// TestGRPC_CeremonyInitialize_ForwardsAllFields is the regression guard for
// the bug where the gRPC CeremonyInitialize handler only forwarded
// SoPin/UserPin into service.CeremonyInitializeOptions, silently dropping
// CredentialSealStrategy, Threshold, Officers, Hostname, and Sans from the
// wire request. Also covers the admin-credential fields (go-qrdb#548
// review, B2): admin_csr_pem/admin_cert_pem/admin_cert_pool_pem/
// admin_generate/secret on the request, and admin_cert_pem/admin_key_pem/
// admin_credential_source on the response -- holderpb.CeremonyInitFromPB/
// CeremonyInitToPB, shared by gRPC, Unix (the same qrdbServiceServer over a
// Unix socket) and QUIC (pkg/api/quic/handlers_ceremony.go uses the
// identical holderpb conversion).
func TestGRPC_CeremonyInitialize_ForwardsAllFields(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	client, cleanup := startTestServerFull(t, WithCeremonyService(ceremonySvc), WithCeremonyCA(&stubCeremonyCA{}))
	defer cleanup()

	resp, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{
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
		Secret:           []byte("barrier-secret"),
	})
	if err != nil {
		t.Fatalf("CeremonyInitialize failed: %v", err)
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
	if string(ceremonySvc.capturedSecret) != "barrier-secret" {
		t.Errorf("Secret = %q, want %q", ceremonySvc.capturedSecret, "barrier-secret")
	}
	if resp.GetAdminCertPem() != "admin-cert" || string(resp.GetAdminKeyPem()) != "admin-key" || resp.GetAdminCredentialSource() != "generated" {
		t.Errorf("admin-credential response fields not forwarded: cert=%q key=%q source=%q",
			resp.GetAdminCertPem(), resp.GetAdminKeyPem(), resp.GetAdminCredentialSource())
	}
}

func TestGRPC_CeremonyStatus_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCeremonyService(newMockCeremonyService()))
	defer cleanup()

	resp, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("CeremonyStatus failed: %v", err)
	}
	if resp.GetState() != "initialized" {
		t.Fatalf("expected state 'initialized', got %q", resp.GetState())
	}
}

func TestGRPC_CeremonySignCSR_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithCeremonyService(newMockCeremonyService()))
	defer cleanup()

	resp, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{
		Username: "officer1",
		SoPin:    "pin",
		CsrPem:   "-----BEGIN CERTIFICATE REQUEST-----\n...",
		Role:     "officer",
	})
	if err != nil {
		t.Fatalf("CeremonySignCSR failed: %v", err)
	}
	if string(resp.GetCertPem()) != "signed-cert" {
		t.Fatalf("expected 'signed-cert', got %q", resp.GetCertPem())
	}
}

// TestGRPC_Ceremony_NilService checks that every ceremony RPC answers
// Unimplemented when the server has no ceremony service.
func TestGRPC_Ceremony_NilService(t *testing.T) {
	calls := map[string]func(pb.QRDBServiceClient) error{
		"CeremonyInitialize": func(c pb.QRDBServiceClient) error {
			_, err := c.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
			return err
		},
		"CeremonyStatus": func(c pb.QRDBServiceClient) error {
			_, err := c.CeremonyStatus(context.Background(), &emptypb.Empty{})
			return err
		},
		"CeremonySignCSR": func(c pb.QRDBServiceClient) error {
			_, err := c.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{Username: "u"})
			return err
		},
	}
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireUnimplemented(t, call(client))
		})
	}
}
