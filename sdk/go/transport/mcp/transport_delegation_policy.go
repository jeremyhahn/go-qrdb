// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// callToolJSON calls tool and decodes its text result into out, naming
// method in a decode error.
func (t *Transport) callToolJSON(ctx context.Context, method, tool string, args map[string]any, out any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	text, err := t.callTool(ctx, tool, args)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if jsonErr := json.Unmarshal([]byte(text), out); jsonErr != nil {
		return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: jsonErr}}
	}
	return nil
}

// PutDelegationPolicy implements transport.CertClient.
func (t *Transport) PutDelegationPolicy(ctx context.Context, req *transport.DelegationPolicyRequest) (*transport.DelegationPolicy, error) {
	if req == nil {
		return nil, &transport.RequestError{Method: "PutDelegationPolicy", Err: &transport.NilRequestError{}}
	}
	args := map[string]any{"principal": req.Principal, "scope": req.Scope}
	if req.TTL != "" {
		args["ttl"] = req.TTL
	}
	var out transport.DelegationPolicy
	if err := t.callToolJSON(ctx, "PutDelegationPolicy", "qrdb_cert_delegation_policy_put", args, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDelegationPolicies implements transport.CertClient.
func (t *Transport) ListDelegationPolicies(ctx context.Context) ([]transport.DelegationPolicy, error) {
	var out struct {
		Policies []transport.DelegationPolicy `json:"policies"`
	}
	if err := t.callToolJSON(ctx, "ListDelegationPolicies", "qrdb_cert_delegation_policy_list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Policies, nil
}

// DeleteDelegationPolicy implements transport.CertClient.
func (t *Transport) DeleteDelegationPolicy(ctx context.Context, principal string) error {
	return t.callToolJSON(ctx, "DeleteDelegationPolicy", "qrdb_cert_delegation_policy_delete", map[string]any{"principal": principal}, nil)
}
