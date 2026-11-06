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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

	cases := []struct {
		name      string
		verb      string
		path      string
		wantBody  any // nil: no body
		status    int // 0: 200 with answer as JSON
		answer    any
		callCheck func(ctx context.Context, rt *Transport) error
	}{
		{"BarrierInitialize", http.MethodPost, "/v1/barrier/initialize", initialize, 0, nil,
			func(ctx context.Context, rt *Transport) error {
				return rt.BarrierInitialize(ctx, initialize)
			}},
		{"BarrierUnseal", http.MethodPost, "/v1/barrier/unseal", &transport.BarrierUnsealRequest{}, 0, nil,
			func(ctx context.Context, rt *Transport) error {
				return rt.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{})
			}},
		{"BarrierSeal", http.MethodPost, "/v1/barrier/seal", json.RawMessage("null"), 0, nil, // no SO PIN bound
			func(ctx context.Context, rt *Transport) error {
				return rt.BarrierSeal(ctx)
			}},
		{"BarrierStatus", http.MethodGet, "/v1/barrier/status", nil, 0,
			transport.BarrierStatusResponse{Initialized: true, Sealed: true},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierStatus(ctx)
				if err == nil && (!resp.Initialized || !resp.Sealed) {
					return errors.New("status not decoded, want initialized and sealed")
				}
				return err
			}},
		{"BarrierInitializeShamir", http.MethodPost, "/v1/barrier/shamir/initialize", shamir, 0,
			transport.BarrierInitializeShamirResponse{ShareCount: 3, Threshold: 2, TotalShares: 3},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierInitializeShamir(ctx, shamir)
				if err == nil && (resp.ShareCount != 3 || resp.Threshold != 2 || resp.TotalShares != 3) {
					return errors.New("shamir answer not decoded, want 2 of 3")
				}
				return err
			}},
		{"BarrierGenerateRecoveryKeys", http.MethodPost, "/v1/barrier/recovery/generate", recovery, 0,
			transport.BarrierRecoveryKeysResponse{ShareCount: 3, Threshold: 2, Total: 3},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierGenerateRecoveryKeys(ctx, recovery)
				if err == nil && (resp.ShareCount != 3 || resp.Threshold != 2 || resp.Total != 3) {
					return errors.New("recovery answer not decoded, want 2 of 3")
				}
				return err
			}},
		{"BarrierDeleteRecoveryKeys", http.MethodDelete, "/v1/barrier/recovery", nil, http.StatusNoContent, nil,
			func(ctx context.Context, rt *Transport) error {
				return rt.BarrierDeleteRecoveryKeys(ctx)
			}},
		{"BarrierHasRecoveryKeys", http.MethodGet, "/v1/barrier/recovery/status", nil, 0,
			transport.BarrierHasRecoveryKeysResponse{HasKeys: true},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.BarrierHasRecoveryKeys(ctx)
				if err == nil && !resp.HasKeys {
					return errors.New("HasKeys not decoded, want true")
				}
				return err
			}},
		{"CeremonyInitialize", http.MethodPost, "/v1/ceremony/initialize", ceremony, 0,
			transport.CeremonyInitializeResponse{SPKIPin: "pin"},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.CeremonyInitialize(ctx, ceremony)
				if err == nil && resp.SPKIPin != "pin" {
					return errors.New("decoded SPKIPin " + resp.SPKIPin + ", want pin")
				}
				return err
			}},
		{"CeremonyStatus", http.MethodGet, "/v1/ceremony/status", nil, 0,
			transport.CeremonyStatusResponse{State: "initialized"},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.CeremonyStatus(ctx)
				if err == nil && resp.State != "initialized" {
					return errors.New("decoded state " + resp.State + ", want initialized")
				}
				return err
			}},
		{"CeremonySignCSR", http.MethodPost, "/v1/ceremony/sign-csr", csr, 0,
			transport.SignCSRInitResponse{CertPEM: "CERT"},
			func(ctx context.Context, rt *Transport) error {
				resp, err := rt.CeremonySignCSR(ctx, csr)
				if err == nil && resp.CertPEM != "CERT" {
					return errors.New("decoded CertPEM " + resp.CertPEM + ", want CERT")
				}
				return err
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want []byte
			if c.wantBody != nil {
				var err error
				if want, err = json.Marshal(c.wantBody); err != nil {
					t.Fatalf("marshal want: %v", err)
				}
			}
			rt, rs, _ := newExtTransport(t)
			var gotBody []byte
			rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				if c.status != 0 {
					w.WriteHeader(c.status)
					return
				}
				writeJSON(w, c.answer)
			})

			if err := c.callCheck(context.Background(), rt); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !bytes.Equal(bytes.TrimSpace(gotBody), want) {
				t.Fatalf("%s %s body = %s, want %s", c.verb, c.path, gotBody, want)
			}
		})
	}
}

// TestRESTBarrier_SurfacesAFailedAnswer checks a barrier read that the server
// refuses, or answers with a body that is not JSON, returns a RequestError
// naming the RPC rather than a zero response.
func TestRESTBarrier_SurfacesAFailedAnswer(t *testing.T) {
	calls := []struct {
		method string
		path   string
		verb   string
		call   func(ctx context.Context, rt *Transport) error
	}{
		{"BarrierStatus", "/v1/barrier/status", http.MethodGet, func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierStatus(ctx)
			return err
		}},
		{"BarrierHasRecoveryKeys", "/v1/barrier/recovery/status", http.MethodGet, func(ctx context.Context, rt *Transport) error {
			_, err := rt.BarrierHasRecoveryKeys(ctx)
			return err
		}},
		{"BarrierDeleteRecoveryKeys", "/v1/barrier/recovery", http.MethodDelete, func(ctx context.Context, rt *Transport) error {
			return rt.BarrierDeleteRecoveryKeys(ctx)
		}},
	}
	answers := []struct {
		name       string
		reply      func(w http.ResponseWriter)
		wantStatus int
		wantDecode bool
	}{
		{"ServerError", func(w http.ResponseWriter) { http.Error(w, "boom", http.StatusInternalServerError) },
			http.StatusInternalServerError, false},
		{"BadJSON", func(w http.ResponseWriter) { _, _ = w.Write([]byte("not-json")) }, 0, true},
	}
	for _, c := range calls {
		for _, a := range answers {
			if a.wantDecode && c.verb == http.MethodDelete {
				continue // DELETE decodes no answer body.
			}
			t.Run(c.method+"/"+a.name, func(t *testing.T) {
				rt, rs, _ := newExtTransport(t)
				rs.handle(exactPath(c.verb, c.path), func(w http.ResponseWriter, _ *http.Request) { a.reply(w) })

				err := c.call(context.Background(), rt)
				var re *transport.RequestError
				if !errors.As(err, &re) || re.Method != c.method || re.StatusCode != a.wantStatus {
					t.Fatalf("err = %v, want RequestError{Method: %s, StatusCode: %d}", err, c.method, a.wantStatus)
				}
				var de *jsonDecodeError
				if got := errors.As(err, &de); got != a.wantDecode {
					t.Fatalf("err = %v, wraps a JSON decode error = %v, want %v", err, got, a.wantDecode)
				}
			})
		}
	}
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
