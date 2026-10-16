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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandleIssueDelegation_Success(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var got *service.IssueDelegationRequest
	fake := &fakeCertEnroll{
		delegFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			got = req
			return &service.IssuedDelegation{
				TenantID:  "acme",
				Principal: req.Principal,
				Scope:     req.Scope,
				CertPEM:   []byte("-----BEGIN CERTIFICATE-----\nd\n-----END CERTIFICATE-----\n"),
				Serial:    "0A0B",
				NotAfter:  notAfter,
			}, nil
		},
	}
	payload, _ := json.Marshal(issueDelegationWireRequest{Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-pem", TTL: "30m"})

	resp, err := newCertEnrollServer(fake).handleIssueDelegation(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleIssueDelegation: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) ||
		string(got.CSRPEM) != "csr-pem" || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	var out issuedDelegationWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.TenantID != "acme" || out.Principal != "billing" || out.Serial != "0A0B" ||
		!slices.Equal(out.Scope, []string{"kv:get"}) || out.CertPEM == "" || !out.NotAfter.Equal(notAfter) {
		t.Fatalf("response = %+v", out)
	}
}

func TestHandleIssueDelegation_Refusals(t *testing.T) {
	payload, _ := json.Marshal(issueDelegationWireRequest{Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-pem"})
	badTTL, _ := json.Marshal(issueDelegationWireRequest{Principal: "billing", TTL: "soon"})
	refusing := &fakeCertEnroll{
		delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			return nil, errMockCert
		},
	}

	tests := []struct {
		name    string
		srv     *Server
		payload []byte
		check   func(error) bool
	}{
		{"no service", NewServer(nil, nil, "localhost:0"), payload,
			func(err error) bool { var e *ServiceNotConfiguredError; return errors.As(err, &e) }},
		{"invalid json", newCertEnrollServer(&fakeCertEnroll{}), []byte("{bad"),
			func(err error) bool { var e *UnmarshalError; return errors.As(err, &e) }},
		{"invalid ttl", newCertEnrollServer(&fakeCertEnroll{}), badTTL,
			func(err error) bool { var e *InvalidTTLError; return errors.As(err, &e) }},
		{"service refusal", newCertEnrollServer(refusing), payload,
			func(err error) bool { return errors.Is(err, errMockCert) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.srv.handleIssueDelegation(context.Background(), tt.payload); !tt.check(err) {
				t.Fatalf("err = %T: %v", err, err)
			}
		})
	}
}

// TestQUICIssueDelegation_ConsultsTheTenancyGate: issuance is refused by the
// tenancy gate before the service is reached, asking about certs:delegate.
func TestQUICIssueDelegation_ConsultsTheTenancyGate(t *testing.T) {
	srv, authz := newCertEnrollScopeGateServer(t)
	payload, _ := json.Marshal(issueDelegationWireRequest{Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-pem"})

	_, err := srv.handleIssueDelegation(certScopeGateCtx(), payload)
	if !errors.Is(err, errCertScopeGateRefused) {
		t.Fatalf("err = %v, want the gate's refusal", err)
	}
	assertGateAsked(t, authz, permission.CertsDelegate)
}
