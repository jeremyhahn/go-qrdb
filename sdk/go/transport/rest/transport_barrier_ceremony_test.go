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
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTBarrierCeremony_SendsTheDocumentedRequest pins each barrier and
// ceremony RPC to its verb, path and body on the wire, and checks the answer
// is decoded into the response.
func TestRESTBarrierCeremony_SendsTheDocumentedRequest(t *testing.T) {
	initialize := &transport.BarrierInitializeRequest{BarrierStrategy: "passphrase"}
	shamir := &transport.BarrierInitializeShamirRequest{Threshold: 2, TotalShares: 3, Policy: "any"}
	recovery := &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, Policy: "any"}
	ceremony := &transport.CeremonyInitializeRequest{Threshold: 2, Policy: "any"}
	csr := &transport.SignCSRInitRequest{Username: "alice", CSRPEM: "CSR", Role: "admin"}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "BarrierInitialize", verb: http.MethodPost, path: "/v1/barrier/initialize", wantBody: initialize,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.BarrierInitialize(ctx, initialize)
			}},
		{name: "BarrierUnseal", verb: http.MethodPost, path: "/v1/barrier/unseal", wantBody: &transport.BarrierUnsealRequest{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{})
			}},
		{name: "BarrierSeal", verb: http.MethodPost, path: "/v1/barrier/seal",
			wantBody: json.RawMessage("null"), // no SO PIN bound
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.BarrierSeal(ctx)
			}},
		{name: "BarrierStatus", verb: http.MethodGet, path: "/v1/barrier/status",
			answer: transport.BarrierStatusResponse{Initialized: true, Sealed: true},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierStatus(ctx)
				if err == nil && (!resp.Initialized || !resp.Sealed) {
					return errors.New("status not decoded, want initialized and sealed")
				}
				return err
			}},
		{name: "BarrierInitializeShamir", verb: http.MethodPost, path: "/v1/barrier/shamir/initialize", wantBody: shamir,
			answer: transport.BarrierInitializeShamirResponse{ShareCount: 3, Threshold: 2, TotalShares: 3},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierInitializeShamir(ctx, shamir)
				if err == nil && (resp.ShareCount != 3 || resp.Threshold != 2 || resp.TotalShares != 3) {
					return errors.New("shamir answer not decoded, want 2 of 3")
				}
				return err
			}},
		{name: "BarrierGenerateRecoveryKeys", verb: http.MethodPost, path: "/v1/barrier/recovery/generate", wantBody: recovery,
			answer: transport.BarrierRecoveryKeysResponse{ShareCount: 3, Threshold: 2, Total: 3},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierGenerateRecoveryKeys(ctx, recovery)
				if err == nil && (resp.ShareCount != 3 || resp.Threshold != 2 || resp.Total != 3) {
					return errors.New("recovery answer not decoded, want 2 of 3")
				}
				return err
			}},
		{name: "BarrierDeleteRecoveryKeys", verb: http.MethodDelete, path: "/v1/barrier/recovery", status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.BarrierDeleteRecoveryKeys(ctx)
			}},
		{name: "BarrierHasRecoveryKeys", verb: http.MethodGet, path: "/v1/barrier/recovery/status",
			answer: transport.BarrierHasRecoveryKeysResponse{HasKeys: true},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierHasRecoveryKeys(ctx)
				if err == nil && !resp.HasKeys {
					return errors.New("HasKeys not decoded, want true")
				}
				return err
			}},
		{name: "CeremonyInitialize", verb: http.MethodPost, path: "/v1/ceremony/initialize", wantBody: ceremony,
			answer: transport.CeremonyInitializeResponse{SPKIPin: "pin"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.CeremonyInitialize(ctx, ceremony)
				if err == nil && resp.SPKIPin != "pin" {
					return errors.New("decoded SPKIPin " + resp.SPKIPin + ", want pin")
				}
				return err
			}},
		{name: "CeremonyStatus", verb: http.MethodGet, path: "/v1/ceremony/status",
			answer: transport.CeremonyStatusResponse{State: "initialized"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.CeremonyStatus(ctx)
				if err == nil && resp.State != "initialized" {
					return errors.New("decoded state " + resp.State + ", want initialized")
				}
				return err
			}},
		{name: "CeremonySignCSR", verb: http.MethodPost, path: "/v1/ceremony/sign-csr", wantBody: csr,
			answer: transport.SignCSRInitResponse{CertPEM: "CERT"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.CeremonySignCSR(ctx, csr)
				if err == nil && resp.CertPEM != "CERT" {
					return errors.New("decoded CertPEM " + resp.CertPEM + ", want CERT")
				}
				return err
			}},
	})
}

// TestRESTBarrier_SurfacesAFailedAnswer checks a barrier read that the server
// refuses, or answers with a body that is not JSON, returns a RequestError
// naming the RPC rather than a zero response.
func TestRESTBarrier_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"BarrierStatus", http.MethodGet, "/v1/barrier/status", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierStatus(ctx)
			return err
		}},
		{"BarrierHasRecoveryKeys", http.MethodGet, "/v1/barrier/recovery/status", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierHasRecoveryKeys(ctx)
			return err
		}},
		{"BarrierDeleteRecoveryKeys", http.MethodDelete, "/v1/barrier/recovery", false, func(ctx context.Context, rt *Transport) error {
			return rt.BarrierDeleteRecoveryKeys(ctx)
		}},
	})
}

// TestRESTBarrierCeremony_Closed checks every barrier and ceremony RPC
// refuses on a closed transport before anything reaches the wire.
func TestRESTBarrierCeremony_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"BarrierInitialize": func(ctx context.Context, rt *Transport) error {
			return rt.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{})
		},
		"BarrierUnseal": func(ctx context.Context, rt *Transport) error {
			return rt.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{})
		},
		"BarrierSeal": func(ctx context.Context, rt *Transport) error {
			return rt.BarrierSeal(ctx)
		},
		"BarrierStatus": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierStatus(ctx)
			return err
		},
		"BarrierInitializeShamir": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{})
			return err
		},
		"BarrierGenerateRecoveryKeys": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{})
			return err
		},
		"BarrierDeleteRecoveryKeys": func(ctx context.Context, rt *Transport) error {
			return rt.BarrierDeleteRecoveryKeys(ctx)
		},
		"BarrierHasRecoveryKeys": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierHasRecoveryKeys(ctx)
			return err
		},
		"CeremonyInitialize": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{})
			return err
		},
		"CeremonyStatus": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CeremonyStatus(ctx)
			return err
		},
		"CeremonySignCSR": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{})
			return err
		},
	})
}
