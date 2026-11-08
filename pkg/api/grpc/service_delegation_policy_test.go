// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if st, _ := status.FromError(err); st.Code() != want {
		t.Fatalf("code = %s, want %s (err %v)", st.Code(), want, err)
	}
}

func TestGRPC_PutDelegationPolicy_Success(t *testing.T) {
	var got *service.DelegationPolicyRequest
	fake := &fakeCertEnrollHandler{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			got = req
			return &delegation.Policy{Tenant: "acme", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.PutDelegationPolicy(context.Background(), &pb.PutDelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, Ttl: "30m",
	})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	if resp.GetTenantId() != "acme" || resp.GetPrincipal() != "billing" || resp.GetTtl() != "30m0s" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestGRPC_PutDelegationPolicy_Refusals(t *testing.T) {
	tests := []struct {
		name string
		svc  *fakeCertEnrollHandler
		req  *pb.PutDelegationPolicyRequest
		want codes.Code
	}{
		{"invalid ttl", &fakeCertEnrollHandler{}, &pb.PutDelegationPolicyRequest{Principal: "billing", Ttl: "soon"}, codes.InvalidArgument},
		{"denied", &fakeCertEnrollHandler{
			policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
				return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
			},
		}, &pb.PutDelegationPolicyRequest{Principal: "billing"}, codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, cleanup := startTestServerFull(t, WithCertEnrollment(tt.svc))
			defer cleanup()
			_, err := client.PutDelegationPolicy(context.Background(), tt.req)
			wantCode(t, err, tt.want)
		})
	}
}

func TestGRPC_ListDelegationPolicies_Success(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	resp, err := client.ListDelegationPolicies(context.Background(), &pb.ListDelegationPoliciesRequest{})
	if err != nil {
		t.Fatalf("ListDelegationPolicies: %v", err)
	}
	if len(resp.GetPolicies()) != 1 || resp.GetPolicies()[0].GetPrincipal() != "billing" ||
		resp.GetPolicies()[0].GetTtl() != delegation.DefaultLifetime.String() {
		t.Fatalf("response = %+v", resp)
	}
}

func TestGRPC_ListDelegationPolicies_Refusal(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return nil, &service.DelegationDeniedError{Reason: "denied"}
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()
	_, err := client.ListDelegationPolicies(context.Background(), &pb.ListDelegationPoliciesRequest{})
	wantCode(t, err, codes.PermissionDenied)
}

func TestGRPC_DeleteDelegationPolicy_Success(t *testing.T) {
	var gotPrincipal string
	fake := &fakeCertEnrollHandler{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()

	if _, err := client.DeleteDelegationPolicy(context.Background(), &pb.DeleteDelegationPolicyRequest{Principal: "billing"}); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if gotPrincipal != "billing" {
		t.Fatalf("principal = %q, want billing", gotPrincipal)
	}
}

func TestGRPC_DeleteDelegationPolicy_Refusal(t *testing.T) {
	fake := &fakeCertEnrollHandler{
		policyDeleteFn: func(context.Context, string, string) error {
			return &service.DelegationRequestError{Field: "principal", Reason: "no policy"}
		},
	}
	client, cleanup := startTestServerFull(t, WithCertEnrollment(fake))
	defer cleanup()
	_, err := client.DeleteDelegationPolicy(context.Background(), &pb.DeleteDelegationPolicyRequest{Principal: "billing"})
	wantCode(t, err, codes.InvalidArgument)
}

func TestGRPC_DelegationPolicies_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.PutDelegationPolicy(context.Background(), &pb.PutDelegationPolicyRequest{Principal: "billing"})
	wantCode(t, err, codes.Unimplemented)
	_, err = client.ListDelegationPolicies(context.Background(), &pb.ListDelegationPoliciesRequest{})
	wantCode(t, err, codes.Unimplemented)
	_, err = client.DeleteDelegationPolicy(context.Background(), &pb.DeleteDelegationPolicyRequest{Principal: "billing"})
	wantCode(t, err, codes.Unimplemented)
}
