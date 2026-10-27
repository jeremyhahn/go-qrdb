// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSignedInitTestTransport serves both halves of a signed init over a
// plaintext test server, recording the decoded bodies.
func newSignedInitTestTransport(t *testing.T) (*Transport, *transport.TenantBarrierSignedInitBeginRequest, *transport.TenantBarrierSignedInitCompleteRequest) {
	t.Helper()
	begin := &transport.TenantBarrierSignedInitBeginRequest{}
	complete := &transport.TenantBarrierSignedInitCompleteRequest{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tenants/{id}/barrier/signed-init/begin", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(begin); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(transport.TenantBarrierSignedInitBeginResponse{
			TenantID: r.PathValue("id"), Strategy: begin.Strategy, Reference: []byte("ref"), Statement: []byte("stmt"),
			ExpiresAt: time.Unix(1_800_000_000, 0).UTC(),
		})
	})
	mux.HandleFunc("POST /v1/tenants/{id}/barrier/signed-init/complete", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(complete); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if string(complete.Signature) != "sig" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"signed init refused"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tenant_id":"` + r.PathValue("id") + `","strategy":"none"}`))
	})
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return newWiredTransport(httpSrv), begin, complete
}

func TestTenantBarrierSignedInitBegin_RoundTrip(t *testing.T) {
	rt, got, _ := newSignedInitTestTransport(t)

	resp, err := rt.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{
		TenantID: "acme", Strategy: "none", KeyRef: "kr", PublicKey: []byte("spki"),
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if got.Strategy != "none" || got.KeyRef != "kr" || string(got.PublicKey) != "spki" {
		t.Fatalf("server saw %+v", got)
	}
	if resp.TenantID != "acme" || string(resp.Reference) != "ref" || string(resp.Statement) != "stmt" || !resp.ExpiresAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTenantBarrierSignedInitBegin_NilRequest(t *testing.T) {
	rt, _, _ := newSignedInitTestTransport(t)
	if _, err := rt.TenantBarrierSignedInitBegin(context.Background(), nil); !errors.As(err, new(*transport.RequestError)) {
		t.Fatalf("err = %v, want *transport.RequestError", err)
	}
}

func TestTenantBarrierSignedInitComplete_RoundTrip(t *testing.T) {
	rt, _, got := newSignedInitTestTransport(t)

	resp, err := rt.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("sig"),
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if string(got.Reference) != "ref" || string(got.Signature) != "sig" {
		t.Fatalf("server saw %+v", got)
	}
	if resp.TenantID != "acme" || resp.Strategy != "none" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTenantBarrierSignedInitComplete_RefusedSignatureIsAnError(t *testing.T) {
	rt, _, _ := newSignedInitTestTransport(t)
	_, err := rt.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("forged"),
	})
	if err == nil {
		t.Fatal("a refused signature returned no error")
	}
}

// TestTenantBarrierSignedInitComplete_PassphraseRefusedInPlaintext pins that
// complete's passphrase never leaves over a plaintext connection.
func TestTenantBarrierSignedInitComplete_PassphraseRefusedInPlaintext(t *testing.T) {
	rt, _, got := newSignedInitTestTransport(t)
	_, err := rt.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("sig"), Passphrase: []byte("secret"),
	})
	if err == nil {
		t.Fatal("a passphrase was sent over plaintext")
	}
	if got.Reference != nil {
		t.Fatal("the request reached the server")
	}
}

func TestTenantBarrierSignedInitComplete_NilRequest(t *testing.T) {
	rt, _, _ := newSignedInitTestTransport(t)
	if _, err := rt.TenantBarrierSignedInitComplete(context.Background(), nil); err == nil {
		t.Fatal("a nil request returned no error")
	}
}
