// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

func TestE2E_IssueDelegation_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	// Handed off over a channel; see TestE2E_IssueCert_HappyPath.
	gotReq := make(chan map[string]any, 1)
	srv.SetResponder("cert.Delegate", func(payload []byte) (uint16, []byte) {
		var req map[string]any
		_ = json.Unmarshal(payload, &req)
		gotReq <- req
		b, _ := json.Marshal(transport.IssuedDelegation{
			TenantID: "t1", Principal: "billing", Scope: []string{"kv:get"}, CertPEM: "cert", Serial: "d1",
		})
		return wire.StatusOK, b
	})
	resp, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "15m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	if resp.TenantID != "t1" || resp.Principal != "billing" || resp.Serial != "d1" || !slices.Equal(resp.Scope, []string{"kv:get"}) {
		t.Fatalf("resp = %+v", resp)
	}
	req := <-gotReq
	if req["principal"] != "billing" || req["csrPEM"] != "csr-bytes" || req["ttl"] != "15m" {
		t.Fatalf("server received %+v; want principal=billing csrPEM=csr-bytes ttl=15m", req)
	}
	if scope, _ := req["scope"].([]any); len(scope) != 1 || scope[0] != "kv:get" {
		t.Fatalf("server received scope %v; want [kv:get]", req["scope"])
	}
}

func TestE2E_IssueDelegation_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Delegate", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("a delegated caller cannot delegate")
	})
	_, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr"})
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError", err, err)
	}
}

func TestE2E_IssueDelegation_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Delegate", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte("{not json")
	})
	_, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr"})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T); want an *UnmarshalError", err, err)
	}
}

func TestTransport_IssueDelegation_Closed(t *testing.T) {
	tr, err := New(transport.WithAddress("localhost:63002"))
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	_ = tr.Close()
	_, err = tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{Principal: "billing"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
}
