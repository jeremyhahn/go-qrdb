// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestPutDelegationPolicy_Success(t *testing.T) {
	var gotReq *service.DelegationPolicyRequest
	enroll := newMockCertEnrollmentServicer()
	enroll.policyPutFn = func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
		gotReq = req
		return &delegation.Policy{Tenant: "t1", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
	}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m",
	})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if got.TenantID != "t1" || got.Principal != "billing" || got.TTL != "15m0s" || !slices.Equal(got.Scope, []string{"kv:get"}) {
		t.Errorf("got %+v", got)
	}
	if gotReq == nil || gotReq.Principal != "billing" || gotReq.TTL != 15*time.Minute {
		t.Errorf("server received %+v; want Principal=billing TTL=15m", gotReq)
	}
}

func TestPutDelegationPolicy_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.policyPutFn = func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
		return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
	}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if _, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"},
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestListDelegationPolicies_Success(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.policyListFn = func(context.Context, string) ([]delegation.Policy, error) {
		return []delegation.Policy{{Tenant: "t1", Principal: "billing", Scope: []string{"kv:get"}}}, nil
	}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.ListDelegationPolicies(context.Background())
	if err != nil {
		t.Fatalf("ListDelegationPolicies: %v", err)
	}
	if len(got) != 1 || got[0].Principal != "billing" || got[0].TTL != delegation.DefaultLifetime.String() {
		t.Errorf("got %+v", got)
	}
}

func TestListDelegationPolicies_ServiceError(t *testing.T) {
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(newMockCertEnrollmentServicer()))
	defer cleanup()

	if _, err := tr.ListDelegationPolicies(context.Background()); err == nil {
		t.Fatal("ListDelegationPolicies succeeded on a service that refuses")
	}
}

func TestDeleteDelegationPolicy_Success(t *testing.T) {
	var gotPrincipal string
	enroll := newMockCertEnrollmentServicer()
	enroll.policyDeleteFn = func(_ context.Context, _, principal string) error {
		gotPrincipal = principal
		return nil
	}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if gotPrincipal != "billing" {
		t.Errorf("principal = %q, want billing", gotPrincipal)
	}
}

func TestDeleteDelegationPolicy_ServiceError(t *testing.T) {
	enroll := newMockCertEnrollmentServicer()
	enroll.policyDeleteFn = func(context.Context, string, string) error {
		return &service.DelegationRequestError{Field: "principal", Reason: "no delegation policy"}
	}
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil, mcpapi.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDelegationPolicies_Closed(t *testing.T) {
	tr, cleanup := connectAuthInProcess(t, nil, nil, nil)
	cleanup()
	_ = tr.Close()
	var ce *transport.ClosedError
	if _, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{}); !errors.As(err, &ce) {
		t.Fatalf("put: expected ClosedError, got %T", err)
	}
	if _, err := tr.ListDelegationPolicies(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("list: expected ClosedError, got %T", err)
	}
	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); !errors.As(err, &ce) {
		t.Fatalf("delete: expected ClosedError, got %T", err)
	}
}
