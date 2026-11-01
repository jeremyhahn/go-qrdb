// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestHandlePutDelegationPolicy_Success(t *testing.T) {
	var got *service.DelegationPolicyRequest
	fake := &fakeCertEnroll{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			got = req
			return &delegation.Policy{Tenant: "acme", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	payload, _ := json.Marshal(delegationPolicyWireRequest{Principal: "billing", Scope: []string{"kv:get"}, TTL: "30m"})

	resp, err := newCertEnrollServer(fake).handlePutDelegationPolicy(context.Background(), payload)
	if err != nil {
		t.Fatalf("handlePutDelegationPolicy: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	var out delegationPolicyWire
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.TenantID != "acme" || out.Principal != "billing" || out.TTL != "30m0s" {
		t.Fatalf("response = %+v", out)
	}
}

func TestHandlePutDelegationPolicy_Refusals(t *testing.T) {
	denied := &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
	fake := &fakeCertEnroll{
		policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return nil, denied
		},
	}
	srv := newCertEnrollServer(fake)

	var unmarshalErr *UnmarshalError
	if _, err := srv.handlePutDelegationPolicy(context.Background(), []byte("{bad")); !errors.As(err, &unmarshalErr) {
		t.Fatalf("invalid json: err = %v, want *UnmarshalError", err)
	}
	var ttlErr *InvalidTTLError
	if _, err := srv.handlePutDelegationPolicy(context.Background(), []byte(`{"principal":"billing","ttl":"soon"}`)); !errors.As(err, &ttlErr) {
		t.Fatalf("invalid ttl: err = %v, want *InvalidTTLError", err)
	}
	if _, err := srv.handlePutDelegationPolicy(context.Background(), []byte(`{"principal":"billing"}`)); !errors.Is(err, denied) {
		t.Fatalf("service refusal: err = %v, want %v", err, denied)
	}
}

func TestHandleListDelegationPolicies_Success(t *testing.T) {
	fake := &fakeCertEnroll{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	resp, err := newCertEnrollServer(fake).handleListDelegationPolicies(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleListDelegationPolicies: %v", err)
	}
	var out struct {
		Policies []delegationPolicyWire `json:"policies"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out.Policies) != 1 || out.Policies[0].Principal != "billing" || out.Policies[0].TTL != delegation.DefaultLifetime.String() {
		t.Fatalf("response = %+v", out)
	}
}

func TestHandleListDelegationPolicies_Refusal(t *testing.T) {
	var notConfigured *service.CertEnrollmentNotConfiguredError
	if _, err := newCertEnrollServer(&fakeCertEnroll{}).handleListDelegationPolicies(context.Background(), nil); !errors.As(err, &notConfigured) {
		t.Fatalf("err = %v, want *service.CertEnrollmentNotConfiguredError", err)
	}
}

func TestHandleDeleteDelegationPolicy_Success(t *testing.T) {
	var gotPrincipal string
	fake := &fakeCertEnroll{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	resp, err := newCertEnrollServer(fake).handleDeleteDelegationPolicy(context.Background(), []byte(`{"principal":"billing"}`))
	if err != nil {
		t.Fatalf("handleDeleteDelegationPolicy: %v", err)
	}
	if gotPrincipal != "billing" || string(resp) != "{}" {
		t.Fatalf("principal = %q, response = %s", gotPrincipal, resp)
	}
}

func TestHandleDeleteDelegationPolicy_Refusals(t *testing.T) {
	srv := newCertEnrollServer(&fakeCertEnroll{})
	var unmarshalErr *UnmarshalError
	if _, err := srv.handleDeleteDelegationPolicy(context.Background(), []byte("{bad")); !errors.As(err, &unmarshalErr) {
		t.Fatalf("invalid json: err = %v, want *UnmarshalError", err)
	}
	var notConfigured *service.CertEnrollmentNotConfiguredError
	if _, err := srv.handleDeleteDelegationPolicy(context.Background(), []byte(`{"principal":"billing"}`)); !errors.As(err, &notConfigured) {
		t.Fatalf("err = %v, want *service.CertEnrollmentNotConfiguredError", err)
	}
}

func TestDelegationPolicyHandlers_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	var notConfigured *ServiceNotConfiguredError
	for name, h := range map[string]func(context.Context, []byte) ([]byte, error){
		"put":    srv.handlePutDelegationPolicy,
		"list":   srv.handleListDelegationPolicies,
		"delete": srv.handleDeleteDelegationPolicy,
	} {
		if _, err := h(context.Background(), []byte("{}")); !errors.As(err, &notConfigured) {
			t.Fatalf("%s: err = %v, want *ServiceNotConfiguredError", name, err)
		}
	}
}
