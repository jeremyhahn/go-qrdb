// Copyright (c) 2026 Jeremy Hahn
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
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// postDelegate sends body to POST /certs/delegate on srv.
func postDelegate(srv *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/certs/delegate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

func TestHandleIssueDelegation_Success(t *testing.T) {
	var got *service.IssueDelegationRequest
	ce := &fakeCertEnroll{
		delegFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			got = req
			return &service.IssuedDelegation{
				TenantID:  "acme",
				Principal: req.Principal,
				Scope:     req.Scope,
				CertPEM:   []byte("-----BEGIN CERTIFICATE-----\nd\n-----END CERTIFICATE-----"),
				Serial:    "0A0B",
				NotAfter:  testTime,
			}, nil
		},
	}

	rec := postDelegate(newEnrollAuthServer(ce), `{"principal":"billing","scope":["kv:get","kv:put"],"csrPEM":"csr-pem","ttl":"30m"}`)

	assertStatus(t, rec, http.StatusCreated)
	assertContentType(t, rec, "application/json")
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get", "kv:put"}) ||
		string(got.CSRPEM) != "csr-pem" || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	var resp issuedDelegationResponse
	decodeJSON(t, rec, &resp)
	if resp.TenantID != "acme" || resp.Principal != "billing" || resp.Serial != "0A0B" ||
		!resp.NotAfter.Equal(testTime) || !strings.Contains(resp.CertPEM, "BEGIN CERTIFICATE") {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleIssueDelegation_NoTTLIsTheDefault(t *testing.T) {
	ce := &fakeCertEnroll{
		delegFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			if req.TTL != 0 {
				t.Fatalf("ttl = %v, want 0 (the default)", req.TTL)
			}
			return &service.IssuedDelegation{Serial: "01"}, nil
		},
	}
	assertStatus(t, postDelegate(newEnrollAuthServer(ce), `{"principal":"billing","scope":["kv:get"],"csrPEM":"csr-pem"}`), http.StatusCreated)
}

func TestHandleIssueDelegation_RefusesBadRequests(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})
	for name, body := range map[string]string{
		"invalid json": "{bad",
		"invalid ttl":  `{"principal":"billing","scope":["kv:get"],"csrPEM":"csr-pem","ttl":"soon"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertStatus(t, postDelegate(srv, body), http.StatusBadRequest)
		})
	}
}

func TestHandleIssueDelegation_MapsServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"bad request", &service.DelegationRequestError{Field: "scope", Reason: "unknown permission"}, http.StatusBadRequest},
		{"denied", &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}, http.StatusForbidden},
		{"cross tenant", &tenant.CrossTenantDeniedError{IdentityTenantID: "acme", RequestedTenantID: "globex"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := &fakeCertEnroll{
				delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
					return nil, tt.err
				},
			}
			assertStatus(t, postDelegate(newEnrollAuthServer(ce), `{"principal":"billing","scope":["kv:get"],"csrPEM":"csr-pem"}`), tt.want)
		})
	}
}

// Without cert enrollment the route is not registered at all.
func TestHandleIssueDelegation_NotRoutedWithoutEnrollment(t *testing.T) {
	assertStatus(t, postDelegate(newAuthServer(nil, nil, nil), `{"principal":"billing"}`), http.StatusNotFound)
}

func TestHandleIssueDelegation_NilServiceDirect(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)
	rec := httptest.NewRecorder()
	srv.handleIssueDelegation(rec, httptest.NewRequest(http.MethodPost, "/certs/delegate", strings.NewReader("{}")))
	assertStatus(t, rec, http.StatusNotImplemented)
}
