// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// delegationPolicyAdmin is the gate every delegation-policy RPC passes: the
// enrollment service is installed and the caller holds certs:delegate in
// its own tenant, which it returns.
func (s *qrdbServiceServer) delegationPolicyAdmin(ctx context.Context) (string, error) {
	if s.certEnroll == nil {
		return "", mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsDelegate); err != nil {
		return "", authmw.GRPCError(err, codes.PermissionDenied)
	}
	return tenantID, nil
}

func delegationPolicyProto(p *delegation.Policy) *pb.DelegationPolicy {
	return &pb.DelegationPolicy{TenantId: p.Tenant, Principal: p.Principal, Scope: p.Scope, Ttl: p.Lifetime().String()}
}

// PutDelegationPolicy writes the caller's own tenant's standing delegation
// policy (#1186).
func (s *qrdbServiceServer) PutDelegationPolicy(ctx context.Context, req *pb.PutDelegationPolicyRequest) (*pb.DelegationPolicy, error) {
	tenantID, err := s.delegationPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var ttl time.Duration
	if req.GetTtl() != "" {
		if ttl, err = time.ParseDuration(req.GetTtl()); err != nil {
			return nil, mapServiceError(&TTLParseError{Value: req.GetTtl(), Err: err})
		}
	}
	p, err := s.certEnroll.PutDelegationPolicy(ctx, &service.DelegationPolicyRequest{
		TenantID:  tenantID,
		Principal: req.GetPrincipal(),
		Scope:     req.GetScope(),
		TTL:       ttl,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return delegationPolicyProto(p), nil
}

// ListDelegationPolicies lists the caller's own tenant's standing delegation
// policies.
func (s *qrdbServiceServer) ListDelegationPolicies(ctx context.Context, _ *pb.ListDelegationPoliciesRequest) (*pb.ListDelegationPoliciesResponse, error) {
	tenantID, err := s.delegationPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	policies, err := s.certEnroll.ListDelegationPolicies(ctx, tenantID)
	if err != nil {
		return nil, mapServiceError(err)
	}
	out := &pb.ListDelegationPoliciesResponse{Policies: make([]*pb.DelegationPolicy, len(policies))}
	for i := range policies {
		out.Policies[i] = delegationPolicyProto(&policies[i])
	}
	return out, nil
}

// DeleteDelegationPolicy revokes the caller's own tenant's standing
// delegation policy for a principal.
func (s *qrdbServiceServer) DeleteDelegationPolicy(ctx context.Context, req *pb.DeleteDelegationPolicyRequest) (*pb.DeleteDelegationPolicyResponse, error) {
	tenantID, err := s.delegationPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.certEnroll.DeleteDelegationPolicy(ctx, tenantID, req.GetPrincipal()); err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.DeleteDelegationPolicyResponse{}, nil
}
