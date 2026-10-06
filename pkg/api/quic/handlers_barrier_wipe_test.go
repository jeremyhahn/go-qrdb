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
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Server-side zeroization of decoded barrier credentials
//
// The proto field being `bytes` (and the tenant frame's JSON field being a
// []byte) only means the credential CAN be wiped. These tests are about
// whether it IS: the handler owns the slice the decoder produced and zeroes
// it before returning, on the success path and the error paths.
//
// The capturing doubles below RETAIN the slice they are handed rather than
// copying it, so a retained alias reads back as zeros once the handler has
// returned. Remove the handler's `defer secure.Wipe(...)` and these fail.
//
// Both doubles EMBED THE INTERFACE rather than this package's mock structs.
// Those mocks live behind
// `codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd`,
// and a helper's constraint must be a superset of its caller's; embedding
// them would force this file behind the same tags and delete these tests from
// every other build. Embedding the interface crosses nothing.
// ---------------------------------------------------------------------------

type capturingQUICBarrierSvc struct {
	service.BarrierServicer
	seen []byte
	err  error
}

var _ service.BarrierServicer = (*capturingQUICBarrierSvc)(nil)

// BarrierInitialized reports the double as never initialized, so the
// initialize handler reaches BarrierInitialize and hands it the credential.
func (c *capturingQUICBarrierSvc) BarrierInitialized(context.Context) (bool, error) {
	return false, nil
}

func (c *capturingQUICBarrierSvc) BarrierInitialize(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingQUICBarrierSvc) BarrierUnseal(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingQUICBarrierSvc) BarrierQuorumSubmit(_ context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	c.seen = req.Share
	return nil, c.err
}

type capturingQUICTenantSvc struct {
	tenant.TenantServicer
	seen []byte
}

var _ tenant.TenantServicer = (*capturingQUICTenantSvc)(nil)

func (c *capturingQUICTenantSvc) TenantBarrierQuorumSubmit(_ context.Context, _ string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	c.seen = sub.Share
	return nil, errors.New("factor verification failed")
}

func (c *capturingQUICTenantSvc) TenantBarrierInitWithStrategy(_ context.Context, tenantID string, passphrase []byte, threshold, shares int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster) (*tenant.TenantBarrierInitResult, error) {
	c.seen = passphrase
	return &tenant.TenantBarrierInitResult{
		TenantID: tenantID, Strategy: "passphrase", Threshold: threshold, TotalShares: shares,
	}, nil
}

// assertQUICWiped fails unless every byte of b is zero. An empty slice is a
// failure: it would mean the handler never received the credential and the
// test is proving nothing.
func assertQUICWiped(t *testing.T, what string, b []byte) {
	t.Helper()
	if len(b) == 0 {
		t.Fatalf("%s: the handler was never handed a credential; this test proves nothing", what)
	}
	for i, c := range b {
		if c != 0 {
			t.Fatalf("%s: byte %d survived the handler at %q — the decoded credential was not zeroized", what, i, b)
		}
	}
}

func TestQUICBarrierInitialize_WipesDecodedSecret(t *testing.T) {
	svc := &capturingQUICBarrierSvc{}
	srv := NewServer(nil, nil, "localhost:0", WithBarrierService(svc))

	payload, err := proto.Marshal(&pb.BarrierInitializeRequest{Secret: []byte("hunter2")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := srv.handleBarrierInitialize(context.Background(), payload); err != nil {
		t.Fatalf("handleBarrierInitialize: %v", err)
	}
	assertQUICWiped(t, "BarrierInitialize", svc.seen)
}

func TestQUICBarrierInitialize_WipesOnServiceError(t *testing.T) {
	svc := &capturingQUICBarrierSvc{err: errors.New("barrier already initialized")}
	srv := NewServer(nil, nil, "localhost:0", WithBarrierService(svc))

	payload, err := proto.Marshal(&pb.BarrierInitializeRequest{Secret: []byte("hunter2")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := srv.handleBarrierInitialize(context.Background(), payload); err == nil {
		t.Fatal("expected the service error to surface")
	}
	assertQUICWiped(t, "BarrierInitialize (error path)", svc.seen)
}

// TestQUICBarrierUnseal_WipesOnWrongSecret covers the path an attacker drives
// most often: a rejected unseal must not leave the attempted credential in
// the daemon's heap.
func TestQUICBarrierUnseal_WipesOnWrongSecret(t *testing.T) {
	svc := &capturingQUICBarrierSvc{err: errors.New("invalid credentials")}
	srv := NewServer(nil, nil, "localhost:0", WithBarrierService(svc))

	payload, err := proto.Marshal(&pb.BarrierUnsealRequest{Secret: []byte("wrong-guess")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := srv.handleBarrierUnseal(context.Background(), payload); err == nil {
		t.Fatal("expected the unseal failure to surface")
	}
	assertQUICWiped(t, "BarrierUnseal (rejected)", svc.seen)
}

// TestQUICBarrierQuorumSubmit_WipesShare proves the decoded share is
// zeroized when the service refuses the submission without consuming it.
func TestQUICBarrierQuorumSubmit_WipesShare(t *testing.T) {
	svc := &capturingQUICBarrierSvc{err: errors.New("factor verification failed")}
	srv := NewServer(nil, nil, "localhost:0", WithBarrierService(svc))

	payload, err := proto.Marshal(&pb.BarrierQuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "alice", Share: []byte("share-bytes")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := srv.handleBarrierQuorumSubmit(context.Background(), payload); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	assertQUICWiped(t, "BarrierQuorumSubmit (refused)", svc.seen)
}

// TestQUICTenantBarrierQuorumSubmit_WipesShare is the tenant form.
func TestQUICTenantBarrierQuorumSubmit_WipesShare(t *testing.T) {
	svc := &capturingQUICTenantSvc{}
	srv := NewServer(nil, nil, "localhost:0", WithTenantService(svc))

	payload, err := proto.Marshal(&pb.TenantBarrierQuorumSubmitRequest{TenantId: "acme", Op: "unseal", Username: "alice", Share: []byte("share-bytes")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := srv.handleTenantBarrierQuorumSubmit(context.Background(), payload); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	assertQUICWiped(t, "TenantBarrierQuorumSubmit (refused)", svc.seen)
}

func TestQUICTenantBarrierInit_WipesDecodedPassphrase(t *testing.T) {
	svc := &capturingQUICTenantSvc{}
	srv := NewServer(nil, nil, "localhost:0", WithTenantService(svc))

	payload, err := json.Marshal(quicTenantBarrierInitRequest{
		TenantID: "acme", Passphrase: []byte("tenant-pw"), Strategy: "passphrase",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := srv.handleTenantBarrierInit(context.Background(), payload); err != nil {
		t.Fatalf("handleTenantBarrierInit: %v", err)
	}
	assertQUICWiped(t, "TenantBarrierInit", svc.seen)
}

// TestQUICTenantBarrierInitFrameIsBase64 pins the tenant frame's wire shape.
// The passphrase field is []byte, which encoding/json renders as base64 —
// the same convention the unseal frame's key already uses.
func TestQUICTenantBarrierInitFrameIsBase64(t *testing.T) {
	data, err := json.Marshal(quicTenantBarrierInitRequest{
		TenantID: "acme", Passphrase: []byte("hunter2"),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `{"tenant_id":"acme","passphrase":"aHVudGVyMg=="}`
	if string(data) != want {
		t.Fatalf("frame = %s, want %s", data, want)
	}
}
