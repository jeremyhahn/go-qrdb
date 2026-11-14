// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"
	"net/url"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// delegationPoliciesPath is the collection of the caller's own tenant's
// standing delegation policies (#1186).
const delegationPoliciesPath = "/certs/delegate/policies"

// PutDelegationPolicy implements transport.CertClient.
func (t *Transport) PutDelegationPolicy(ctx context.Context, req *transport.DelegationPolicyRequest) (*transport.DelegationPolicy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "PutDelegationPolicy", Err: &transport.NilRequestError{}}
	}
	var out transport.DelegationPolicy
	if err := t.postJSONDecode(ctx, "PutDelegationPolicy", delegationPoliciesPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDelegationPolicies implements transport.CertClient.
func (t *Transport) ListDelegationPolicies(ctx context.Context) ([]transport.DelegationPolicy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out struct {
		Policies []transport.DelegationPolicy `json:"policies"`
	}
	if err := t.getJSONDecode(ctx, "ListDelegationPolicies", delegationPoliciesPath, &out); err != nil {
		return nil, err
	}
	return out.Policies, nil
}

// DeleteDelegationPolicy implements transport.CertClient.
func (t *Transport) DeleteDelegationPolicy(ctx context.Context, principal string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return t.deleteNoContent(ctx, "DeleteDelegationPolicy", delegationPoliciesPath+"/"+url.PathEscape(principal))
}
