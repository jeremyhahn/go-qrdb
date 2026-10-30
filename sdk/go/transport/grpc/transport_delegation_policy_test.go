// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestGRPCPutDelegationPolicy_RoundTrip(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	got, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m",
	})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if got.TenantID != "system" || got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != "15m0s" {
		t.Fatalf("response = %+v", got)
	}
}

func TestGRPCPutDelegationPolicy_RefusalsAreRequestErrors(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	for name, ttl := range map[string]string{"invalid ttl": "soon", "past MaxLifetime": "48h"} {
		t.Run(name, func(t *testing.T) {
			_, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
				Principal: "billing", Scope: []string{"kv:get"}, TTL: ttl,
			})
			var re *transport.RequestError
			if !errors.As(err, &re) {
				t.Fatalf("err = %v (%T); want a *transport.RequestError", err, err)
			}
		})
	}
}

func TestGRPCListDelegationPolicies_RoundTrip(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	got, err := tr.ListDelegationPolicies(context.Background())
	if err != nil {
		t.Fatalf("ListDelegationPolicies: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "system" || got[0].Principal != "billing" || got[0].TTL != "1h0m0s" {
		t.Fatalf("policies = %+v", got)
	}
}

func TestGRPCDeleteDelegationPolicy_RoundTrip(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	var re *transport.RequestError
	if err := tr.DeleteDelegationPolicy(context.Background(), "nobody"); !errors.As(err, &re) {
		t.Fatalf("absent policy: err = %v (%T); want a *transport.RequestError", err, err)
	}
}

func TestGRPCDelegationPolicies_Closed(t *testing.T) {
	tr := &Transport{}
	tr.closed.Store(true)
	var ce *transport.ClosedError
	if _, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{}); !errors.As(err, &ce) {
		t.Fatalf("put: expected ClosedError, got %T", err)
	}
	if _, err := tr.ListDelegationPolicies(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("list: expected ClosedError, got %T", err)
	}
	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); !errors.As(err, &ce) {
		t.Fatalf("delete: expected ClosedError, got %T", err)
	}
}
