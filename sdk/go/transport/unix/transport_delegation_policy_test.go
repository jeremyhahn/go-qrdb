// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package unix

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestUnixRT_PutDelegationPolicy(t *testing.T) {
	var gotReq *service.DelegationPolicyRequest
	enroll := &mockCertEnrollmentSvc{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			gotReq = req
			return &delegation.Policy{Tenant: "t1", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m",
	})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if got.Principal != "billing" || got.TTL != "15m0s" || !slices.Equal(got.Scope, []string{"kv:get"}) {
		t.Errorf("got %+v", got)
	}
	if gotReq == nil || gotReq.Principal != "billing" || gotReq.TTL != 15*time.Minute {
		t.Errorf("server received %+v; want Principal=billing TTL=15m", gotReq)
	}
}

func TestUnixRT_PutDelegationPolicy_Refused(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{
		policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	_, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{Principal: "billing", Scope: []string{"kv:get"}})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.PermissionDenied) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying PermissionDenied", err, err)
	}
}

func TestUnixRT_ListDelegationPolicies(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "t1", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	got, err := tr.ListDelegationPolicies(context.Background())
	if err != nil {
		t.Fatalf("ListDelegationPolicies: %v", err)
	}
	if len(got) != 1 || got[0].Principal != "billing" || got[0].TTL != delegation.DefaultLifetime.String() {
		t.Errorf("got %+v", got)
	}
}

func TestUnixRT_ListDelegationPolicies_Refused(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(&mockCertEnrollmentSvc{}))
	defer cleanup()

	if _, err := tr.ListDelegationPolicies(context.Background()); err == nil {
		t.Fatal("ListDelegationPolicies succeeded on a service that refuses")
	}
}

func TestUnixRT_DeleteDelegationPolicy(t *testing.T) {
	var gotPrincipal string
	enroll := &mockCertEnrollmentSvc{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if gotPrincipal != "billing" {
		t.Errorf("principal = %q, want billing", gotPrincipal)
	}
}

func TestUnixRT_DeleteDelegationPolicy_Refused(t *testing.T) {
	enroll := &mockCertEnrollmentSvc{
		policyDeleteFn: func(context.Context, string, string) error {
			return &service.DelegationRequestError{Field: "principal", Reason: "no delegation policy"}
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithCertEnrollment(enroll))
	defer cleanup()

	err := tr.DeleteDelegationPolicy(context.Background(), "billing")
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.InvalidArgument) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying InvalidArgument", err, err)
	}
}
