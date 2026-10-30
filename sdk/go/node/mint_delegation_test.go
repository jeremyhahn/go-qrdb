// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package node_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
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

func TestMintDelegation_NoServicePrincipal(t *testing.T) {
	if got, err := (&node.Node{}).MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: delegatingTenant}); got != nil || !errors.Is(err, node.ErrNoServicePrincipal) {
		t.Fatalf("MintDelegation = %+v, %v; want ErrNoServicePrincipal", got, err)
	}
}
