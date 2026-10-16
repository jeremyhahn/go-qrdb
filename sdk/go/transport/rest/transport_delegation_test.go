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
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRESTIssueDelegation_RequestAndResponseFields(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.IssueDelegationRequest
	notAfter := time.Date(2027, 5, 6, 7, 8, 9, 0, time.UTC)
	rs.handle(exactPath(http.MethodPost, "/certs/delegate"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeJSON(w, &transport.IssuedDelegation{
			TenantID: "acme", Principal: "billing", Scope: []string{"kv:get"},
			CertPEM: "cert-pem", Serial: "ser-1", NotAfter: notAfter,
		})
	})
	got, err := rt.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "15m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	if captured.Principal != "billing" || !slices.Equal(captured.Scope, []string{"kv:get"}) ||
		captured.CSRPEM != "csr-bytes" || captured.TTL != "15m" {
		t.Fatalf("request captured = %+v", captured)
	}
	if got.TenantID != "acme" || got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) ||
		got.CertPEM != "cert-pem" || got.Serial != "ser-1" || !got.NotAfter.Equal(notAfter) {
		t.Fatalf("response = %+v", got)
	}
}

func TestRESTIssueDelegation_RefusalIsARequestError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/delegate"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "a delegated caller cannot delegate", http.StatusForbidden)
	})
	_, err := rt.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr",
	})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 403", err, err)
	}
}

func TestRESTIssueDelegation_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}
