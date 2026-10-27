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

package quic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitRecorder records the requests both halves of a signed init reach
// the tenant service with.
type signedInitRecorder struct {
	*mockTenantService
	begin       *tenant.TenantBarrierInitBeginRequest
	complete    *tenant.TenantBarrierInitCompleteRequest
	passphrase  []byte
	completeErr error
}

func (r *signedInitRecorder) TenantBarrierInitBegin(_ context.Context, req *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	r.begin = req
	return &tenant.TenantBarrierInitBeginResult{
		TenantID: req.TenantID, Strategy: string(req.Strategy), Reference: []byte("ref"), Statement: []byte("stmt"),
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(), Threshold: req.Threshold, TotalShares: req.Shares,
	}, nil
}

func (r *signedInitRecorder) TenantBarrierInitComplete(_ context.Context, req *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	r.complete = req
	r.passphrase = req.Passphrase
	if r.completeErr != nil {
		return nil, r.completeErr
	}
	return &tenant.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "passphrase"}, nil
}

func newSignedInitServer() (*Server, *signedInitRecorder) {
	svc := &signedInitRecorder{mockTenantService: &mockTenantService{}}
	return newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc)), svc
}

func TestHandleTenantBarrierSignedInitBegin_MapsRequestAndResponse(t *testing.T) {
	srv, svc := newSignedInitServer()
	payload, _ := json.Marshal(transport.TenantBarrierSignedInitBeginRequest{
		TenantID: "acme", Strategy: "shamir", KeyRef: "kr", Threshold: 2, Shares: 3, PublicKey: []byte("spki"),
		Officers: []transport.OfficerRegisterRequest{{Username: "alice", Role: "so"}},
	})

	out, err := srv.handleTenantBarrierSignedInitBegin(context.Background(), payload)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	got := svc.begin
	if got.TenantID != "acme" || got.Strategy != seal.StrategyID("shamir") || got.KeyRef != "kr" || got.Threshold != 2 ||
		got.Shares != 3 || string(got.SignerPublicKey) != "spki" || got.Roster == nil || len(got.Roster.Officers) != 1 {
		t.Fatalf("service request = %+v", got)
	}
	var resp transport.TenantBarrierSignedInitBeginResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(resp.Reference) != "ref" || string(resp.Statement) != "stmt" || !resp.ExpiresAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleTenantBarrierSignedInitBegin_Refusals(t *testing.T) {
	srv, svc := newSignedInitServer()
	if _, err := srv.handleTenantBarrierSignedInitBegin(context.Background(), []byte(`{not-json`)); !errors.As(err, new(*UnmarshalError)) {
		t.Fatalf("bad payload: err = %v, want *UnmarshalError", err)
	}
	if _, err := srv.handleTenantBarrierSignedInitBegin(context.Background(), []byte(`{}`)); !errors.As(err, new(*InvalidArgumentError)) {
		t.Fatalf("no tenant: err = %v, want *InvalidArgumentError", err)
	}
	if svc.begin != nil {
		t.Fatal("service reached by a refused request")
	}
	bare := newTestServer(&mockKVService{}, &mockAdminService{})
	if _, err := bare.handleTenantBarrierSignedInitBegin(context.Background(), []byte(`{"tenant_id":"acme"}`)); !errors.As(err, new(*ServiceNotConfiguredError)) {
		t.Fatalf("no service: err = %v, want *ServiceNotConfiguredError", err)
	}
}

func TestHandleTenantBarrierSignedInitComplete_MapsRequestAndWipesPassphrase(t *testing.T) {
	srv, svc := newSignedInitServer()
	payload, _ := json.Marshal(transport.TenantBarrierSignedInitCompleteRequest{
		TenantID: "acme", Reference: []byte("ref"), Signature: []byte("sig"), Passphrase: []byte("secret-passphrase"),
	})

	out, err := srv.handleTenantBarrierSignedInitComplete(context.Background(), payload)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if svc.complete.TenantID != "acme" || string(svc.complete.Reference) != "ref" || string(svc.complete.Signature) != "sig" {
		t.Fatalf("service request = %+v", svc.complete)
	}
	if len(svc.passphrase) == 0 || !bytes.Equal(svc.passphrase, make([]byte, len(svc.passphrase))) {
		t.Fatalf("passphrase %q not wiped", svc.passphrase)
	}
	var resp quicTenantBarrierInitResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.TenantID != "acme" || resp.Strategy != "passphrase" {
		t.Fatalf("response %s, err %v", out, err)
	}
}

func TestHandleTenantBarrierSignedInitComplete_Refusals(t *testing.T) {
	srv, svc := newSignedInitServer()
	if _, err := srv.handleTenantBarrierSignedInitComplete(context.Background(), []byte(`[`)); !errors.As(err, new(*UnmarshalError)) {
		t.Fatalf("bad payload: err = %v, want *UnmarshalError", err)
	}
	if _, err := srv.handleTenantBarrierSignedInitComplete(context.Background(), []byte(`{}`)); !errors.As(err, new(*InvalidArgumentError)) {
		t.Fatalf("no tenant: err = %v, want *InvalidArgumentError", err)
	}
	svc.completeErr = &tenant.SignedInitError{TenantID: "acme", Reason: tenant.SignedInitUnknown}
	if _, err := srv.handleTenantBarrierSignedInitComplete(context.Background(), []byte(`{"tenant_id":"acme"}`)); !errors.As(err, new(*tenant.SignedInitError)) {
		t.Fatalf("replay: err = %v, want *tenant.SignedInitError", err)
	}
	bare := newTestServer(&mockKVService{}, &mockAdminService{})
	if _, err := bare.handleTenantBarrierSignedInitComplete(context.Background(), []byte(`{"tenant_id":"acme"}`)); !errors.As(err, new(*ServiceNotConfiguredError)) {
		t.Fatalf("no service: err = %v, want *ServiceNotConfiguredError", err)
	}
}
