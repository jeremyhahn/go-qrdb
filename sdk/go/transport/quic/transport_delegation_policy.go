// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// callJSON marshals req, calls op and unmarshals its answer into out (nil
// discards it), naming method in every error.
func (t *Transport) callJSON(ctx context.Context, method, op string, req, out any) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &MarshalError{Err: err}}
	}
	data, err := t.call(ctx, op, payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &transport.RequestError{Method: method, Err: &UnmarshalError{Err: err}}
	}
	return nil
}

// PutDelegationPolicy implements transport.CertClient.
func (t *Transport) PutDelegationPolicy(ctx context.Context, req *transport.DelegationPolicyRequest) (*transport.DelegationPolicy, error) {
	var out transport.DelegationPolicy
	if err := t.callJSON(ctx, "PutDelegationPolicy", "cert.DelegationPolicyPut", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDelegationPolicies implements transport.CertClient.
func (t *Transport) ListDelegationPolicies(ctx context.Context) ([]transport.DelegationPolicy, error) {
	var out struct {
		Policies []transport.DelegationPolicy `json:"policies"`
	}
	if err := t.callJSON(ctx, "ListDelegationPolicies", "cert.DelegationPolicyList", struct{}{}, &out); err != nil {
		return nil, err
	}
	return out.Policies, nil
}

// DeleteDelegationPolicy implements transport.CertClient.
func (t *Transport) DeleteDelegationPolicy(ctx context.Context, principal string) error {
	return t.callJSON(ctx, "DeleteDelegationPolicy", "cert.DelegationPolicyDelete", struct {
		Principal string `json:"principal"`
	}{principal}, nil)
}
