// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package node_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestMintDelegation_NoPolicyStandsIsRefused(t *testing.T) {
	principal := uniquePrincipal("mint")
	permission.RegisterRoleGrant(principal, permission.RolesList)
	n, _ := newDelegatingNode(t, principal)

	var notFound *service.DelegationPolicyNotFoundError
	got, err := n.MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant, CSRPEM: []byte("unused")})
	if got != nil || !errors.As(err, &notFound) || notFound.TenantID != delegatingTenant || notFound.Principal != principal {
		t.Fatalf("MintDelegation = %+v, %v; want *service.DelegationPolicyNotFoundError for %s in %s", got, err, principal, delegatingTenant)
	}
}

// A real node mints the first certificate under the tenant's standing policy:
// the node accepts the certificate it returned, the delegated context acts in
// the delegating tenant with the policy's scope, and nothing outside the
// principal's grants, or outside the tenant, is admitted.
func TestMintDelegation_MintsAuthenticatesAndScopes(t *testing.T) {
	principal := uniquePrincipal("mint")
	permission.RegisterRoleGrant(principal, permission.RolesList)
	n, _ := newDelegatingNode(t, principal)

	policy, err := n.Services().CertEnrollment.PutDelegationPolicy(tenant.WithTenantID(context.Background(), delegatingTenant),
		&service.DelegationPolicyRequest{Principal: principal, Scope: []string{string(permission.RolesList)}, TTL: time.Hour})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if policy.Principal != principal {
		t.Fatalf("PutDelegationPolicy = %+v; want a policy for %s", policy, principal)
	}

	_, csrPEM, err := certtest.NewCSR(principal)
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	issued, err := n.MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant, CSRPEM: csrPEM})
	if err != nil {
		t.Fatalf("MintDelegation: %v", err)
	}
	if issued.TenantID != delegatingTenant || issued.Principal != principal ||
		len(issued.Scope) != 1 || issued.Scope[0] != string(permission.RolesList) {
		t.Fatalf("MintDelegation = %+v; want %s delegated by %s with %s", issued, principal, delegatingTenant, permission.RolesList)
	}
	block, _ := pem.Decode(issued.CertPEM)
	if block == nil {
		t.Fatal("MintDelegation returned no PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse the minted delegation: %v", err)
	}

	ctx, err := n.DelegatedContext(context.Background(), []*x509.Certificate{leaf})
	if err != nil {
		t.Fatalf("DelegatedContext refused the certificate MintDelegation returned: %v", err)
	}
	id := auth.IdentityFromContext(ctx)
	if g, ok := auth.DelegationOf(id); !ok || g.Principal != principal || id.TenantID != delegatingTenant {
		t.Fatalf("context binds %+v; want %s delegated by %s", id, principal, delegatingTenant)
	}

	roles := n.Client().Roles()
	if _, err := roles.ListRoles(ctx, &transport.ListRolesRequest{TenantID: delegatingTenant}); err != nil {
		t.Fatalf("ListRoles in the delegating tenant: %v", err)
	}
	_, err = roles.CreateRole(ctx, &transport.CreateRoleRequest{
		Name: "minted-writer", TenantID: delegatingTenant, Permissions: []string{string(permission.RolesList)},
	})
	var denied *rbac.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("CreateRole under a delegation scoped to %s = %v; want *rbac.PermissionDeniedError", permission.RolesList, err)
	}
	if _, err := roles.ListRoles(ctx, &transport.ListRolesRequest{TenantID: otherTenant}); err == nil {
		t.Fatalf("ListRoles in %s admitted a delegation from %s", otherTenant, delegatingTenant)
	}
}

func TestMintDelegation_NoServicePrincipal(t *testing.T) {
	if got, err := (&node.Node{}).MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant}); got != nil || !errors.Is(err, node.ErrNoServicePrincipal) {
		t.Fatalf("MintDelegation = %+v, %v; want ErrNoServicePrincipal", got, err)
	}
}
