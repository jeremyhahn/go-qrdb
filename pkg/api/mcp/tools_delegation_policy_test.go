// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package mcp

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

func TestDelegationPolicyTools_Definitions(t *testing.T) {
	for want, got := range map[string]string{
		toolNameDelegationPolicyPut:    toolDelegationPolicyPut().Name,
		toolNameDelegationPolicyList:   toolDelegationPolicyList().Name,
		toolNameDelegationPolicyDelete: toolDelegationPolicyDelete().Name,
	} {
		if got != want {
			t.Fatalf("tool name = %q, want %q", got, want)
		}
	}
}

func TestDelegationPolicyPutTool_Success(t *testing.T) {
	var got *service.DelegationPolicyRequest
	svc := &fakeCertEnrollTool{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			got = req
			return &delegation.Policy{Tenant: "acme", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	result, err := handleDelegationPolicyPut(svc, nil)(context.Background(), makeReq(map[string]any{
		"principal": "billing",
		"scope":     []any{"kv:get"},
		"ttl":       "30m",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	var out delegationPolicyResult
	if err := json.Unmarshal([]byte(resultText(t, result)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.TenantID != "acme" || out.Principal != "billing" || out.TTL != "30m0s" {
		t.Fatalf("result = %+v", out)
	}
}

func TestDelegationPolicyPutTool_InvalidTTL(t *testing.T) {
	result, err := handleDelegationPolicyPut(&fakeCertEnrollTool{}, nil)(context.Background(), makeReq(map[string]any{
		"principal": "billing",
		"scope":     []any{"kv:get"},
		"ttl":       "soon",
	}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !result.IsError {
		t.Fatal("an invalid ttl is not an error result")
	}
}

func TestDelegationPolicyPutTool_ServiceError(t *testing.T) {
	denied := &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
	svc := &fakeCertEnrollTool{
		policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return nil, denied
		},
	}
	_, err := handleDelegationPolicyPut(svc, nil)(context.Background(), makeReq(map[string]any{
		"principal": "billing", "scope": []any{"kv:get"},
	}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != toolNameDelegationPolicyPut || !errors.Is(err, denied) {
		t.Fatalf("err = %T: %v; want a %s *ToolError wrapping the refusal", err, err, toolNameDelegationPolicyPut)
	}
}

func TestDelegationPolicyListTool_Success(t *testing.T) {
	svc := &fakeCertEnrollTool{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	result, err := handleDelegationPolicyList(svc, nil)(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Policies []delegationPolicyResult `json:"policies"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Policies) != 1 || out.Policies[0].Principal != "billing" || out.Policies[0].TTL != delegation.DefaultLifetime.String() {
		t.Fatalf("result = %+v", out)
	}
}

func TestDelegationPolicyListTool_ServiceError(t *testing.T) {
	_, err := handleDelegationPolicyList(&fakeCertEnrollTool{}, nil)(context.Background(), makeReq(map[string]any{}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != toolNameDelegationPolicyList {
		t.Fatalf("err = %T: %v; want a %s *ToolError", err, err, toolNameDelegationPolicyList)
	}
}

func TestDelegationPolicyDeleteTool_Success(t *testing.T) {
	var gotPrincipal string
	svc := &fakeCertEnrollTool{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	result, err := handleDelegationPolicyDelete(svc, nil)(context.Background(), makeReq(map[string]any{"principal": "billing"}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError || gotPrincipal != "billing" {
		t.Fatalf("principal = %q, result error = %v", gotPrincipal, result.IsError)
	}
}

func TestDelegationPolicyDeleteTool_ServiceError(t *testing.T) {
	_, err := handleDelegationPolicyDelete(&fakeCertEnrollTool{}, nil)(context.Background(), makeReq(map[string]any{"principal": "billing"}))
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != toolNameDelegationPolicyDelete {
		t.Fatalf("err = %T: %v; want a %s *ToolError", err, err, toolNameDelegationPolicyDelete)
	}
}
