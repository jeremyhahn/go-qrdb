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

package embedded

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestIssueDelegation_Success(t *testing.T) {
	var got *service.IssueDelegationRequest
	mock := &mockCertEnrollmentServicer{
		issueDelegationFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			got = req
			return &service.IssuedDelegation{
				TenantID: req.TenantID, Principal: req.Principal, Scope: req.Scope,
				CertPEM: []byte("cert"), Serial: "d1", NotAfter: testTime(),
			}, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	issued, err := tr.IssueDelegation(tenant.WithTenantID(context.Background(), "tenant-a"), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "15m",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The tenant is always the caller's own; the request cannot name one.
	if got.TenantID != "tenant-a" || got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) ||
		string(got.CSRPEM) != "csr-bytes" || got.TTL != 15*time.Minute {
		t.Errorf("service received %+v", got)
	}
	if issued.TenantID != "tenant-a" || issued.CertPEM != "cert" || issued.Serial != "d1" || !issued.NotAfter.Equal(testTime()) {
		t.Errorf("issued = %+v", issued)
	}
}

func TestIssueDelegation_NoTTL(t *testing.T) {
	mock := &mockCertEnrollmentServicer{
		issueDelegationFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			if req.TTL != 0 {
				t.Errorf("expected zero TTL, got %v", req.TTL)
			}
			return &service.IssuedDelegation{TenantID: req.TenantID}, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	if _, err := tr.IssueDelegation(tenant.WithTenantID(context.Background(), "tenant-a"), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIssueDelegation_InvalidTTL(t *testing.T) {
	mock := &mockCertEnrollmentServicer{
		issueDelegationFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			t.Fatal("the service must not be called with an invalid TTL")
			return nil, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	if _, err := tr.IssueDelegation(tenant.WithTenantID(context.Background(), "tenant-a"), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "soon",
	}); err == nil {
		t.Fatal("expected error for invalid TTL, got nil")
	}
}

func TestIssueDelegation_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{Principal: "billing"})
	assertNotImplementedError(t, err, "IssueDelegation")
}

func TestIssueDelegation_ServiceError(t *testing.T) {
	denied := &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
	mock := &mockCertEnrollmentServicer{
		issueDelegationFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			return nil, denied
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	_, err := tr.IssueDelegation(tenant.WithTenantID(context.Background(), "tenant-a"), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes",
	})
	if !errors.Is(err, denied) {
		t.Fatalf("err = %v (%T); want the service's refusal", err, err)
	}
}
