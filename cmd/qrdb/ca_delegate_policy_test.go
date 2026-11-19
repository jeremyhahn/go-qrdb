// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func TestCADelegatePolicyPutCmd_SendsTheRequest(t *testing.T) {
	var got *service.DelegationPolicyRequest
	fe := &fakeCertEnrollment{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			got = req
			return &delegation.Policy{Tenant: "acme", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "ca", "delegate", "policy", "put", "--principal", "billing",
		"--scope", "kv:get,kv:put", "--scope", "kv:list", "--ttl", "30m")
	if err != nil {
		t.Fatalf("ca delegate policy put: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get", "kv:put", "kv:list"}) || got.TTL != 30*time.Minute {
		t.Fatalf("request = %+v", got)
	}
	for _, want := range []string{"Principal: billing (tenant acme)", "Scope: kv:get,kv:put,kv:list", "TTL: 30m0s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}

func TestCADelegatePolicyPutCmd_JSON(t *testing.T) {
	fe := &fakeCertEnrollment{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return &delegation.Policy{Tenant: "acme", Principal: req.Principal, Scope: req.Scope}, nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "--output", "json", "ca", "delegate", "policy", "put",
		"--principal", "billing", "--scope", "kv:get")
	if err != nil {
		t.Fatalf("ca delegate policy put: %v", err)
	}
	var got struct {
		Principal string   `json:"principal"`
		Scope     []string `json:"scope"`
		TTL       string   `json:"ttl"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != delegation.DefaultLifetime.String() {
		t.Fatalf("JSON = %+v", got)
	}
}

func TestCADelegatePolicyPutCmd_RefusesBadInvocations(t *testing.T) {
	refused := errors.New("refused")
	refusing := &fakeCertEnrollment{
		policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return nil, refused
		},
	}
	tests := []struct {
		name  string
		fake  *fakeCertEnrollment
		args  []string
		check func(error) bool
	}{
		{"no principal", &fakeCertEnrollment{}, []string{"--scope", "kv:get"}, nil},
		{"no scope", &fakeCertEnrollment{}, []string{"--principal", "billing"}, nil},
		{"bad ttl", &fakeCertEnrollment{}, []string{"--principal", "billing", "--scope", "kv:get", "--ttl", "soon"}, nil},
		{"service refusal", refusing, []string{"--principal", "billing", "--scope", "kv:get"},
			func(err error) bool { return errors.Is(err, refused) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executeCertEnrollCmd(t, tt.fake, append([]string{"ca", "delegate", "policy", "put"}, tt.args...)...)
			if err == nil || (tt.check != nil && !tt.check(err)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCADelegatePolicyListCmd_PrintsATable(t *testing.T) {
	fe := &fakeCertEnrollment{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{
				{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get", "kv:put"}, TTL: 15 * time.Minute},
				{Tenant: "acme", Principal: "reports", Scope: []string{"kv:list"}},
			}, nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "ca", "delegate", "policy", "list")
	if err != nil {
		t.Fatalf("ca delegate policy list: %v", err)
	}
	for _, want := range []string{"PRINCIPAL", "billing", "kv:get,kv:put", "15m0s", "reports", "1h0m0s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}

func TestCADelegatePolicyListCmd_JSON(t *testing.T) {
	fe := &fakeCertEnrollment{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "--output", "json", "ca", "delegate", "policy", "list")
	if err != nil {
		t.Fatalf("ca delegate policy list: %v", err)
	}
	var got []struct {
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	if len(got) != 1 || got[0].Principal != "billing" {
		t.Fatalf("JSON = %+v", got)
	}
}

func TestCADelegatePolicyListCmd_ServiceRefusal(t *testing.T) {
	if _, err := executeCertEnrollCmd(t, &fakeCertEnrollment{}, "ca", "delegate", "policy", "list"); err == nil {
		t.Fatal("list succeeded on a service that refuses")
	}
}

func TestCADelegatePolicyDeleteCmd_DeletesThePrincipal(t *testing.T) {
	var gotPrincipal string
	fe := &fakeCertEnrollment{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	out, err := executeCertEnrollCmd(t, fe, "ca", "delegate", "policy", "delete", "billing")
	if err != nil {
		t.Fatalf("ca delegate policy delete: %v", err)
	}
	if gotPrincipal != "billing" || !strings.Contains(out, "Delegation policy for billing deleted") {
		t.Fatalf("principal = %q, output %q", gotPrincipal, out)
	}
}

func TestCADelegatePolicyDeleteCmd_RefusesBadInvocations(t *testing.T) {
	absent := &service.DelegationRequestError{Field: "principal", Reason: "no delegation policy"}
	refusing := &fakeCertEnrollment{
		policyDeleteFn: func(context.Context, string, string) error { return absent },
	}
	tests := []struct {
		name  string
		fake  *fakeCertEnrollment
		args  []string
		check func(error) bool
	}{
		{"no principal", &fakeCertEnrollment{}, nil, nil},
		{"two principals", &fakeCertEnrollment{}, []string{"billing", "reports"}, nil},
		{"service refusal", refusing, []string{"billing"}, func(err error) bool { return errors.Is(err, absent) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executeCertEnrollCmd(t, tt.fake, append([]string{"ca", "delegate", "policy", "delete"}, tt.args...)...)
			if err == nil || (tt.check != nil && !tt.check(err)) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
