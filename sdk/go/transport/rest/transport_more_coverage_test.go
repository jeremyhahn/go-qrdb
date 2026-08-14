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

package rest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Barrier
// ===========================================================================

func TestRESTBarrier_Initialize_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/initialize"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{}); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
}

func TestRESTBarrier_Initialize_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_Unseal_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/unseal"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
}

func TestRESTBarrier_Unseal_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_Seal_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/seal"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.BarrierSeal(context.Background()); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
}

func TestRESTBarrier_Seal_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.BarrierSeal(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_Status_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/barrier/status"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierStatusResponse{Sealed: false})
	})
	resp, err := rt.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if resp.Sealed {
		t.Fatal("expected unsealed")
	}
}

func TestRESTBarrier_Status_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/barrier/status"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.BarrierStatus(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTBarrier_Status_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/barrier/status"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.BarrierStatus(context.Background())
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTBarrier_Status_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierStatus(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_InitializeShamir_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/shamir/initialize"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierInitializeShamirResponse{ShareCount: 2, Threshold: 2, TotalShares: 2})
	})
	resp, err := rt.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{})
	if err != nil {
		t.Fatalf("BarrierInitializeShamir: %v", err)
	}
	if resp.ShareCount != 2 {
		t.Fatalf("got ShareCount=%d, want 2", resp.ShareCount)
	}
}

func TestRESTBarrier_InitializeShamir_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_UnsealWithShare_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/shamir/unseal-share"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierUnsealShareResponse{Complete: false, Submitted: 2, Required: 2})
	})
	resp, err := rt.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{Share: "s1"})
	if err != nil {
		t.Fatalf("BarrierUnsealWithShare: %v", err)
	}
	if resp.Submitted != 2 {
		t.Fatalf("Submitted = %d", resp.Submitted)
	}
}

func TestRESTBarrier_UnsealWithShare_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierUnsealWithShare(context.Background(), &transport.BarrierUnsealShareRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_UnsealWithShares_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/shamir/unseal-shares"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{Shares: []string{"s1", "s2"}}); err != nil {
		t.Fatalf("BarrierUnsealWithShares: %v", err)
	}
}

func TestRESTBarrier_UnsealWithShares_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.BarrierUnsealWithShares(context.Background(), &transport.BarrierUnsealSharesRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_Rekey_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/rekey"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierRekeyResponse{ShareCount: 2, Threshold: 2, TotalShares: 2})
	})
	resp, err := rt.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{Threshold: 2, Total: 2})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	if resp.ShareCount != 2 {
		t.Fatalf("got ShareCount=%d, want 2", resp.ShareCount)
	}
}

func TestRESTBarrier_Rekey_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_GenerateRecoveryKeys_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/recovery/generate"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierRecoveryKeysResponse{ShareCount: 1, Threshold: 1, Total: 1})
	})
	resp, err := rt.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{})
	if err != nil {
		t.Fatalf("BarrierGenerateRecoveryKeys: %v", err)
	}
	if resp.ShareCount != 1 {
		t.Fatalf("got ShareCount=%d, want 1", resp.ShareCount)
	}
}

func TestRESTBarrier_GenerateRecoveryKeys_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierGenerateRecoveryKeys(context.Background(), &transport.BarrierGenerateRecoveryKeysRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_RecoverWithKeys_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/recovery/recover"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{Keys: []string{"r1"}}); err != nil {
		t.Fatalf("BarrierRecoverWithKeys: %v", err)
	}
}

func TestRESTBarrier_RecoverWithKeys_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.BarrierRecoverWithKeys(context.Background(), &transport.BarrierRecoverWithKeysRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_DeleteRecoveryKeys_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/barrier/recovery"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.BarrierDeleteRecoveryKeys(context.Background()); err != nil {
		t.Fatalf("BarrierDeleteRecoveryKeys: %v", err)
	}
}

func TestRESTBarrier_DeleteRecoveryKeys_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/barrier/recovery"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := rt.BarrierDeleteRecoveryKeys(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTBarrier_DeleteRecoveryKeys_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	err := rt.BarrierDeleteRecoveryKeys(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_HasRecoveryKeys_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/barrier/recovery/status"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierHasRecoveryKeysResponse{HasKeys: true})
	})
	resp, err := rt.BarrierHasRecoveryKeys(context.Background())
	if err != nil {
		t.Fatalf("BarrierHasRecoveryKeys: %v", err)
	}
	if !resp.HasKeys {
		t.Fatal("expected HasKeys = true")
	}
}

func TestRESTBarrier_HasRecoveryKeys_ServerError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/barrier/recovery/status"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := rt.BarrierHasRecoveryKeys(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTBarrier_HasRecoveryKeys_BadJSON(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/barrier/recovery/status"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	})
	_, err := rt.BarrierHasRecoveryKeys(context.Background())
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestRESTBarrier_HasRecoveryKeys_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierHasRecoveryKeys(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTBarrier_GenerateRootToken_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/barrier/root-token"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BarrierRootTokenResponse{Token: "tok"})
	})
	resp, err := rt.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{})
	if err != nil {
		t.Fatalf("BarrierGenerateRootToken: %v", err)
	}
	if resp.Token != "tok" {
		t.Fatalf("Token = %q", resp.Token)
	}
}

func TestRESTBarrier_GenerateRootToken_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.BarrierGenerateRootToken(context.Background(), &transport.BarrierGenerateRootTokenRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Ceremony
// ===========================================================================

func TestRESTCeremony_Initialize_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/ceremony/initialize"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.CeremonyInitializeResponse{SPKIPin: "pin"})
	})
	resp, err := rt.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize: %v", err)
	}
	if resp.SPKIPin != "pin" {
		t.Fatalf("SPKIPin = %q", resp.SPKIPin)
	}
}

func TestRESTCeremony_Initialize_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCeremony_Status_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/v1/ceremony/status"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.CeremonyStatusResponse{State: "initialized"})
	})
	resp, err := rt.CeremonyStatus(context.Background())
	if err != nil {
		t.Fatalf("CeremonyStatus: %v", err)
	}
	if resp.State != "initialized" {
		t.Fatalf("State = %q", resp.State)
	}
}

func TestRESTCeremony_Status_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonyStatus(context.Background())
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCeremony_ClaimCertBegin_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/ceremony/claim-cert/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.ClaimCertBeginResponse{NonceHex: "dead"})
	})
	resp, err := rt.CeremonyClaimCertBegin(context.Background(), &transport.ClaimCertBeginRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("CeremonyClaimCertBegin: %v", err)
	}
	if resp.NonceHex != "dead" {
		t.Fatalf("NonceHex = %q", resp.NonceHex)
	}
}

func TestRESTCeremony_ClaimCertBegin_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonyClaimCertBegin(context.Background(), &transport.ClaimCertBeginRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCeremony_ClaimCertComplete_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/ceremony/claim-cert/"), func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/complete") {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, &transport.ClaimCertCompleteResponse{CertPEM: "CERT"})
	})
	resp, err := rt.CeremonyClaimCertComplete(context.Background(), &transport.ClaimCertCompleteRequest{Username: "alice"})
	if err != nil {
		t.Fatalf("CeremonyClaimCertComplete: %v", err)
	}
	if resp.CertPEM != "CERT" {
		t.Fatalf("CertPEM = %q", resp.CertPEM)
	}
}

func TestRESTCeremony_ClaimCertComplete_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonyClaimCertComplete(context.Background(), &transport.ClaimCertCompleteRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCeremony_ClaimShare_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/ceremony/officer-share/begin"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.BeginClaimShareResponse{NonceHex: "abcd"})
	})
	rs.handle(pathPrefix(http.MethodPost, "/v1/ceremony/officer-share/complete"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.CompleteClaimShareResponse{ShareIndex: 1, ShareJSON: "{}"})
	})

	begun, err := rt.CeremonyBeginClaimShare(context.Background(), &transport.BeginClaimShareRequest{})
	if err != nil {
		t.Fatalf("CeremonyBeginClaimShare: %v", err)
	}
	if begun.NonceHex != "abcd" {
		t.Fatalf("NonceHex = %q", begun.NonceHex)
	}

	resp, err := rt.CeremonyCompleteClaimShare(context.Background(), &transport.CompleteClaimShareRequest{NonceHex: begun.NonceHex})
	if err != nil {
		t.Fatalf("CeremonyCompleteClaimShare: %v", err)
	}
	if resp.ShareJSON != "{}" {
		t.Fatalf("ShareJSON = %q", resp.ShareJSON)
	}
}

func TestRESTCeremony_ClaimShare_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonyBeginClaimShare(context.Background(), &transport.BeginClaimShareRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCeremony_SignCSR_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/ceremony/sign-csr"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.SignCSRInitResponse{CertPEM: "CERT"})
	})
	resp, err := rt.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{})
	if err != nil {
		t.Fatalf("CeremonySignCSR: %v", err)
	}
	if resp.CertPEM != "CERT" {
		t.Fatalf("CertPEM = %q", resp.CertPEM)
	}
}

func TestRESTCeremony_SignCSR_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonySignCSR(context.Background(), &transport.SignCSRInitRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestRESTCeremony_EnrollExternalOfficer_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPost, "/v1/init/enroll-external-officer"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, &transport.EnrollExternalOfficerResponse{SealedShare: "sealed"})
	})
	resp, err := rt.CeremonyEnrollExternalOfficer(context.Background(), &transport.EnrollExternalOfficerRequest{})
	if err != nil {
		t.Fatalf("CeremonyEnrollExternalOfficer: %v", err)
	}
	if resp.SealedShare != "sealed" {
		t.Fatalf("SealedShare = %q", resp.SealedShare)
	}
}

func TestRESTCeremony_EnrollExternalOfficer_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.CeremonyEnrollExternalOfficer(context.Background(), &transport.EnrollExternalOfficerRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}
