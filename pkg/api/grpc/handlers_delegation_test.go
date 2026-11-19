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

//go:build !integration

package grpc

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestGRPC_IssueDelegation_Success(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	var got *service.IssueDelegationRequest
	fake := &fakeCertEnrollHandler{
		delegFn: func(_ context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
			got = req
			return &service.IssuedDelegation{
				TenantID:  "acme",
				Principal: req.Principal,
				Scope:     req.Scope,
				CertPEM:   []byte("-----BEGIN CERTIFICATE-----\nd\n-----END CERTIFICATE-----"),
				Serial:    "0A0B",
				NotAfter:  notAfter,
			}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.IssueDelegation(context.Background(), &pb.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get", "kv:put"}, CsrPem: "csr-pem", Ttl: "30m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get", "kv:put"}) ||
		string(got.CSRPEM) != "csr-pem" || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	if resp.GetTenantId() != "acme" || resp.GetPrincipal() != "billing" || resp.GetSerial() != "0A0B" ||
		!slices.Equal(resp.GetScope(), []string{"kv:get", "kv:put"}) || resp.GetCertPem() == "" ||
		resp.GetNotAfter() != "2027-01-01T00:00:00Z" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestGRPC_IssueDelegation_Refusals(t *testing.T) {
	tests := []struct {
		name string
		svc  *fakeCertEnrollHandler
		req  *pb.IssueDelegationRequest
		want codes.Code
	}{
		{"invalid ttl", &fakeCertEnrollHandler{}, &pb.IssueDelegationRequest{Principal: "billing", Ttl: "soon"}, codes.InvalidArgument},
		{"bad request", &fakeCertEnrollHandler{
			delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
				return nil, &service.DelegationRequestError{Field: "scope", Reason: "unknown permission"}
			},
		}, &pb.IssueDelegationRequest{Principal: "billing"}, codes.InvalidArgument},
		{"denied", &fakeCertEnrollHandler{
			delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
				return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
			},
		}, &pb.IssueDelegationRequest{Principal: "billing"}, codes.PermissionDenied},
		{"no policy", &fakeCertEnrollHandler{
			delegFn: func(context.Context, *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
				return nil, &service.DelegationPolicyNotFoundError{TenantID: "acme", Principal: "billing"}
			},
		}, &pb.IssueDelegationRequest{Principal: "billing"}, codes.NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, cleanup := startTestServerFull(t, WithCertEnrollment(tt.svc))
			defer cleanup()
			_, err := client.IssueDelegation(context.Background(), tt.req)
			if st, _ := status.FromError(err); st.Code() != tt.want {
				t.Fatalf("code = %s, want %s (err %v)", st.Code(), tt.want, err)
			}
		})
	}
}

func TestGRPC_IssueDelegation_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IssueDelegation(context.Background(), &pb.IssueDelegationRequest{Principal: "billing"})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented (err %v)", st.Code(), err)
	}
}
