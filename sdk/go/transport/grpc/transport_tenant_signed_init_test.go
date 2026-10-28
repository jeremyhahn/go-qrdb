// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"
	"time"

	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitServicer records the requests both halves of a signed init reach
// the tenant service with.
type signedInitServicer struct {
	*fakeTenantServicer
	begin    *tenantpkg.TenantBarrierInitBeginRequest
	complete *tenantpkg.TenantBarrierInitCompleteRequest
}

func (s *signedInitServicer) TenantBarrierInitBegin(_ context.Context, req *tenantpkg.TenantBarrierInitBeginRequest) (*tenantpkg.TenantBarrierInitBeginResult, error) {
	s.begin = req
	return &tenantpkg.TenantBarrierInitBeginResult{
		TenantID: req.TenantID, Strategy: string(req.Strategy), Reference: []byte("ref"), Statement: []byte("stmt"),
		ExpiresAt: time.Unix(1_800_000_000, 0),
	}, nil
}

func (s *signedInitServicer) TenantBarrierInitComplete(_ context.Context, req *tenantpkg.TenantBarrierInitCompleteRequest) (*tenantpkg.TenantBarrierInitResult, error) {
	s.complete = req
	if string(req.Signature) != "sig" {
		return nil, &tenantpkg.SignedInitError{TenantID: req.TenantID, Reason: tenantpkg.SignedInitUnknown}
	}
	return &tenantpkg.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "none"}, nil
}

func connectSignedInit(t *testing.T) (*Transport, *signedInitServicer) {
	t.Helper()
	svc := &signedInitServicer{fakeTenantServicer: &fakeTenantServicer{}}
	addr, stop := startTenantBarrierServer(t, svc)
	t.Cleanup(stop)
	tr, cleanup := connectBareTransport(t, addr)
	t.Cleanup(cleanup)
	return tr, svc
}

func TestTransport_TenantBarrierSignedInitBegin_RoundTrip(t *testing.T) {
	tr, svc := connectSignedInit(t)
	resp, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{
		TenantID: "acme", Strategy: "none", KeyRef: "kr", PublicKey: []byte("spki"),
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if svc.begin.TenantID != "acme" || svc.begin.KeyRef != "kr" || string(svc.begin.SignerPublicKey) != "spki" {
		t.Fatalf("service saw %+v", svc.begin)
	}
	if string(resp.Reference) != "ref" || string(resp.Statement) != "stmt" || !resp.ExpiresAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTransport_TenantBarrierSignedInitBegin_NilAndClosed(t *testing.T) {
	tr, _ := connectSignedInit(t)
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), nil); !errors.As(err, new(*transport.RequestError)) {
		t.Fatalf("nil request: err = %v, want *transport.RequestError", err)
	}
	_ = tr.Close()
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); !errors.As(err, new(*transport.ClosedError)) {
		t.Fatalf("closed: err = %v, want *transport.ClosedError", err)
	}
}

func TestTransport_TenantBarrierSignedInitComplete_RoundTrip(t *testing.T) {
	tr, svc := connectSignedInit(t)
	resp, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("sig"),
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if string(svc.complete.Reference) != "ref" || resp.TenantID != "acme" || resp.Strategy != "none" {
		t.Fatalf("service saw %+v, response %+v", svc.complete, resp)
	}
}

func TestTransport_TenantBarrierSignedInitComplete_Refusals(t *testing.T) {
	tr, svc := connectSignedInit(t)
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("forged"),
	}); err == nil {
		t.Fatal("a refused signature returned no error")
	}
	svc.complete = nil
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("sig"), Passphrase: []byte("secret"),
	}); err == nil || svc.complete != nil {
		t.Fatalf("passphrase over plaintext: err = %v, reached server = %v", err, svc.complete != nil)
	}
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), nil); err == nil {
		t.Fatal("a nil request returned no error")
	}
	_ = tr.Close()
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); !errors.As(err, new(*transport.ClosedError)) {
		t.Fatalf("closed: err = %v, want *transport.ClosedError", err)
	}
}
