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

package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitSvc records the requests both halves of a signed init reach the
// tenant service with.
type signedInitSvc struct {
	*mockTenantSvc
	begin    *tenant.TenantBarrierInitBeginRequest
	complete *tenant.TenantBarrierInitCompleteRequest
}

func (s *signedInitSvc) TenantBarrierInitBegin(_ context.Context, req *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	s.begin = req
	return &tenant.TenantBarrierInitBeginResult{
		TenantID: req.TenantID, Strategy: string(req.Strategy), Reference: []byte("ref"), Statement: []byte("stmt"),
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(),
	}, nil
}

func (s *signedInitSvc) TenantBarrierInitComplete(_ context.Context, req *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	s.complete = req
	if string(req.Signature) != "sig" {
		return nil, &tenant.SignedInitError{TenantID: req.TenantID, Reason: tenant.SignedInitUnknown}
	}
	return &tenant.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "none"}, nil
}

func connectSignedInit(t *testing.T) (*Transport, *signedInitSvc) {
	t.Helper()
	svc := &signedInitSvc{mockTenantSvc: newMockTenantSvc()}
	tr, cleanup := connectTenantInProcess(t, svc)
	t.Cleanup(cleanup)
	return tr, svc
}

func TestTenantBarrierSignedInitBegin_RoundTrip(t *testing.T) {
	tr, svc := connectSignedInit(t)
	resp, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{
		TenantID: "acme", Strategy: "none", KeyRef: "kr", PublicKey: []byte("spki"),
		Officers: []transport.OfficerRegisterRequest{{Username: "alice", Role: "so"}},
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if svc.begin.TenantID != "acme" || svc.begin.KeyRef != "kr" || string(svc.begin.SignerPublicKey) != "spki" || svc.begin.Roster == nil {
		t.Fatalf("service saw %+v", svc.begin)
	}
	if string(resp.Reference) != "ref" || string(resp.Statement) != "stmt" || !resp.ExpiresAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTenantBarrierSignedInitBegin_NilAndClosed(t *testing.T) {
	tr, _ := connectSignedInit(t)
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), nil); err == nil {
		t.Fatal("a nil request returned no error")
	}
	_ = tr.Close()
	if _, err := tr.TenantBarrierSignedInitBegin(context.Background(), &transport.TenantBarrierSignedInitBeginRequest{TenantID: "acme"}); !errors.As(err, new(*transport.ClosedError)) {
		t.Fatalf("closed: err = %v, want *transport.ClosedError", err)
	}
}

func TestTenantBarrierSignedInitComplete_RoundTrip(t *testing.T) {
	tr, svc := connectSignedInit(t)
	resp, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("sig"),
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if string(svc.complete.Reference) != "ref" || string(svc.complete.Signature) != "sig" || resp.TenantID != "acme" || resp.Strategy != "none" {
		t.Fatalf("service saw %+v, response %+v", svc.complete, resp)
	}
}

func TestTenantBarrierSignedInitComplete_Refusals(t *testing.T) {
	tr, _ := connectSignedInit(t)
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("forged"),
	}); err == nil {
		t.Fatal("a refused signature returned no error")
	}
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), nil); err == nil {
		t.Fatal("a nil request returned no error")
	}
	_ = tr.Close()
	if _, err := tr.TenantBarrierSignedInitComplete(context.Background(), &transport.TenantBarrierSignedInitCompleteRequest{TenantID: "acme"}); !errors.As(err, new(*transport.ClosedError)) {
		t.Fatalf("closed: err = %v, want *transport.ClosedError", err)
	}
}
