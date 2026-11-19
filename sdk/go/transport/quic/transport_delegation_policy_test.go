// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestE2E_PutDelegationPolicy_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	gotReq := make(chan map[string]any, 1)
	srv.SetResponder("cert.DelegationPolicyPut", func(payload []byte) (uint16, []byte) {
		var req map[string]any
		_ = json.Unmarshal(payload, &req)
		gotReq <- req
		b, _ := json.Marshal(transport.DelegationPolicy{TenantID: "t1", Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m0s"})
		return wire.StatusOK, b
	})
	resp, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m",
	})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if resp.TenantID != "t1" || resp.Principal != "billing" || resp.TTL != "15m0s" || !slices.Equal(resp.Scope, []string{"kv:get"}) {
		t.Fatalf("resp = %+v", resp)
	}
	if req := <-gotReq; req["principal"] != "billing" || req["ttl"] != "15m" {
		t.Fatalf("server received %+v; want principal=billing ttl=15m", req)
	}
}

// A nil request is refused on the client: it is not sent as a JSON null.
func TestE2E_PutDelegationPolicy_NilRequest(t *testing.T) {
	srv, tr := startE2EServer(t)
	reached := make(chan struct{}, 2)
	srv.SetResponder("cert.DelegationPolicyPut", func([]byte) (uint16, []byte) {
		reached <- struct{}{}
		b, _ := json.Marshal(transport.DelegationPolicy{TenantID: "t1", Principal: "billing"})
		return wire.StatusOK, b
	})

	for _, name := range []string{"first call", "second call"} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PutDelegationPolicy(nil) panicked: %v", r)
				}
			}()
			got, err := tr.PutDelegationPolicy(context.Background(), nil)
			var nilReq *transport.NilRequestError
			var reqErr *transport.RequestError
			if got != nil || !errors.As(err, &nilReq) || !errors.As(err, &reqErr) || reqErr.Method != "PutDelegationPolicy" {
				t.Fatalf("PutDelegationPolicy(nil) = %+v, %v; want a PutDelegationPolicy *transport.RequestError wrapping *transport.NilRequestError", got, err)
			}
			if len(reached) != 0 {
				t.Fatal("the nil request reached the server")
			}
		})
	}
}

func TestE2E_PutDelegationPolicy_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.DelegationPolicyPut", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("permission denied")
	})
	_, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{Principal: "billing"})
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError", err, err)
	}
}

func TestE2E_ListDelegationPolicies_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.DelegationPolicyList", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte(`{"policies":[{"tenantID":"t1","principal":"billing","scope":["kv:get"],"ttl":"1h0m0s"}]}`)
	})
	got, err := tr.ListDelegationPolicies(context.Background())
	if err != nil {
		t.Fatalf("ListDelegationPolicies: %v", err)
	}
	if len(got) != 1 || got[0].Principal != "billing" || got[0].TTL != "1h0m0s" {
		t.Fatalf("policies = %+v", got)
	}
}

func TestE2E_ListDelegationPolicies_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.DelegationPolicyList", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("{not json")
	})
	_, err := tr.ListDelegationPolicies(context.Background())
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T); want an *UnmarshalError", err, err)
	}
}

func TestE2E_DeleteDelegationPolicy_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	gotReq := make(chan map[string]any, 1)
	srv.SetResponder("cert.DelegationPolicyDelete", func(payload []byte) (uint16, []byte) {
		var req map[string]any
		_ = json.Unmarshal(payload, &req)
		gotReq <- req
		return wire.StatusOK, []byte("{}")
	})
	if err := tr.DeleteDelegationPolicy(context.Background(), "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if req := <-gotReq; req["principal"] != "billing" {
		t.Fatalf("server received %+v; want principal=billing", req)
	}
}

func TestE2E_DeleteDelegationPolicy_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.DelegationPolicyDelete", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("no delegation policy")
	})
	err := tr.DeleteDelegationPolicy(context.Background(), "billing")
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError", err, err)
	}
}

func TestTransport_DelegationPolicies_Closed(t *testing.T) {
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
