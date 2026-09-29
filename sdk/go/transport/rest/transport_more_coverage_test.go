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
