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

package grpc

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestGRPCIssueDelegation_RoundTrip: the request crosses the wire and the
// server's answer, bound to the caller's tenant (t1), decodes back out.
func TestGRPCIssueDelegation_RoundTrip(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	got, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get", "kv:put"}, CSRPEM: "csr-bytes", TTL: "15m",
	})
	if err != nil {
		t.Fatalf("IssueDelegation: %v", err)
	}
	// The fixture server runs no auth chain, so the call is the system
	// tenant's: the handler names the caller's tenant, never the request's.
	if got.TenantID != "system" || got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get", "kv:put"}) {
		t.Fatalf("response = %+v", got)
	}
}

// TestGRPCIssueDelegation_InvalidTTLIsARequestError: the server refuses an
// unparseable lifetime before the service sees it.
func TestGRPCIssueDelegation_InvalidTTLIsARequestError(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	_, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{
		Principal: "billing", Scope: []string{"kv:get"}, CSRPEM: "csr-bytes", TTL: "soon",
	})
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T); want a *transport.RequestError", err, err)
	}
}

func TestGRPCIssueDelegation_Closed(t *testing.T) {
	tr := &Transport{}
	tr.closed.Store(true)
	_, err := tr.IssueDelegation(context.Background(), &transport.IssueDelegationRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}
