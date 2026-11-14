// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func delegationPolicyFromProto(p *pb.DelegationPolicy) transport.DelegationPolicy {
	return transport.DelegationPolicy{TenantID: p.GetTenantId(), Principal: p.GetPrincipal(), Scope: p.GetScope(), TTL: p.GetTtl()}
}

// PutDelegationPolicy implements transport.CertClient.
func (t *Transport) PutDelegationPolicy(ctx context.Context, req *transport.DelegationPolicyRequest) (*transport.DelegationPolicy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "PutDelegationPolicy", Err: &transport.NilRequestError{}}
	}
	resp, err := t.client.PutDelegationPolicy(ctx, &pb.PutDelegationPolicyRequest{Principal: req.Principal, Scope: req.Scope, Ttl: req.TTL})
	if err != nil {
		return nil, mapGRPCError("PutDelegationPolicy", err)
	}
	p := delegationPolicyFromProto(resp)
	return &p, nil
}

// ListDelegationPolicies implements transport.CertClient.
func (t *Transport) ListDelegationPolicies(ctx context.Context) ([]transport.DelegationPolicy, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.ListDelegationPolicies(ctx, &pb.ListDelegationPoliciesRequest{})
	if err != nil {
		return nil, mapGRPCError("ListDelegationPolicies", err)
	}
	out := make([]transport.DelegationPolicy, len(resp.GetPolicies()))
	for i, p := range resp.GetPolicies() {
		out[i] = delegationPolicyFromProto(p)
	}
	return out, nil
}

// DeleteDelegationPolicy implements transport.CertClient.
func (t *Transport) DeleteDelegationPolicy(ctx context.Context, principal string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.DeleteDelegationPolicy(ctx, &pb.DeleteDelegationPolicyRequest{Principal: principal}); err != nil {
		return mapGRPCError("DeleteDelegationPolicy", err)
	}
	return nil
}
