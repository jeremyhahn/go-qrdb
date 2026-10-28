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
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Server-side zeroization of decoded barrier credentials
//
// The proto field being `bytes` only means the credential CAN be wiped. These
// tests are about whether it IS: the handler owns the slice protobuf decoded
// and zeroes it before returning, on the success path and the error paths.
//
// capturingBarrierSvc RETAINS the slice it is handed rather than copying it.
// That is the mechanism under test: the service layer and pkg/seal borrow the
// credential for the call and retain nothing, so the handler is free to wipe
// the backing array afterwards, and a retained alias reads back as zeros.
// Remove the handler's `defer secure.Wipe(...)` and these fail.
//
// The EMBEDDED INTERFACE, rather than this package's mockBarrierService, is
// deliberate. That mock lives behind
// `codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd`,
// and a helper's constraint must be a superset of its caller's; embedding it
// would force this file behind the same tags and delete these tests from
// every other build. Embedding the interface crosses nothing: the three
// methods under test are defined here, and any other method is nil and panics
// if a handler unexpectedly reaches for it, which is the assertion we want.
// ---------------------------------------------------------------------------

type capturingBarrierSvc struct {
	service.BarrierServicer
	seen     []byte
	seenNext []byte
	err      error
}

var _ service.BarrierServicer = (*capturingBarrierSvc)(nil)

func newCapturingBarrierSvc() *capturingBarrierSvc { return &capturingBarrierSvc{} }

// BarrierInitialized reports the double as never initialized, so the
// initialize handler reaches BarrierInitialize and hands it the credential.
func (c *capturingBarrierSvc) BarrierInitialized(context.Context) (bool, error) { return false, nil }

func (c *capturingBarrierSvc) BarrierInitialize(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingBarrierSvc) BarrierUnseal(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingBarrierSvc) BarrierChangePassphrase(_ context.Context, current, next []byte) error {
	c.seen = current
	c.seenNext = next
	return c.err
}

// assertGRPCWiped fails unless every byte of b is zero. An empty slice is a
// failure: it would mean the handler never received the credential and the
// test is proving nothing.
func assertGRPCWiped(t *testing.T, what string, b []byte) {
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

func TestGRPCBarrierInitialize_WipesDecodedSecret(t *testing.T) {
	svc := newCapturingBarrierSvc()
	s := &qrdbServiceServer{barrierSvc: svc}

	if _, err := s.BarrierInitialize(context.Background(),
		&pb.BarrierInitializeRequest{Secret: []byte("hunter2")}); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
	}
	assertGRPCWiped(t, "BarrierInitialize", svc.seen)
}

func TestGRPCBarrierInitialize_WipesOnServiceError(t *testing.T) {
	svc := newCapturingBarrierSvc()
	svc.err = errors.New("barrier already initialized")
	s := &qrdbServiceServer{barrierSvc: svc}

	if _, err := s.BarrierInitialize(context.Background(),
		&pb.BarrierInitializeRequest{Secret: []byte("hunter2")}); err == nil {
		t.Fatal("expected the service error to surface")
	}
	assertGRPCWiped(t, "BarrierInitialize (error path)", svc.seen)
}

func TestGRPCBarrierUnseal_WipesDecodedSecret(t *testing.T) {
	svc := newCapturingBarrierSvc()
	s := &qrdbServiceServer{barrierSvc: svc}

	if _, err := s.BarrierUnseal(context.Background(),
		&pb.BarrierUnsealRequest{Secret: []byte("hunter2")}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
	assertGRPCWiped(t, "BarrierUnseal", svc.seen)
}

// TestGRPCBarrierUnseal_WipesOnWrongSecret covers the path an attacker drives
// most often: a rejected unseal must not leave the attempted credential in
// the daemon's heap.
func TestGRPCBarrierUnseal_WipesOnWrongSecret(t *testing.T) {
	svc := newCapturingBarrierSvc()
	svc.err = errors.New("invalid credentials")
	s := &qrdbServiceServer{barrierSvc: svc}

	if _, err := s.BarrierUnseal(context.Background(),
		&pb.BarrierUnsealRequest{Secret: []byte("wrong-guess")}); err == nil {
		t.Fatal("expected the unseal failure to surface")
	}
	assertGRPCWiped(t, "BarrierUnseal (rejected)", svc.seen)
}

func TestGRPCBarrierChangePassphrase_WipesBothPassphrases(t *testing.T) {
	svc := newCapturingBarrierSvc()
	s := &qrdbServiceServer{barrierSvc: svc, pinBackend: pintest.NewSOPINBackend(t)}

	if _, err := s.BarrierChangePassphrase(context.Background(),
		&pb.BarrierChangePassphraseRequest{Current: []byte("old-pass"), Next: []byte("new-pass"), SoPin: pintest.ThrottledSOPIN}); err != nil {
		t.Fatalf("BarrierChangePassphrase: %v", err)
	}
	assertGRPCWiped(t, "BarrierChangePassphrase current", svc.seen)
	assertGRPCWiped(t, "BarrierChangePassphrase next", svc.seenNext)
}

func TestGRPCBarrierChangePassphrase_WrongCurrentIsPermissionDenied(t *testing.T) {
	svc := newCapturingBarrierSvc()
	svc.err = seal.ErrCredentialMismatch
	s := &qrdbServiceServer{barrierSvc: svc, pinBackend: pintest.NewSOPINBackend(t)}

	_, err := s.BarrierChangePassphrase(context.Background(),
		&pb.BarrierChangePassphraseRequest{Current: []byte("wrong"), Next: []byte("new-pass"), SoPin: pintest.ThrottledSOPIN})
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied (err %v)", got, err)
	}
	assertGRPCWiped(t, "BarrierChangePassphrase current (rejected)", svc.seen)
	assertGRPCWiped(t, "BarrierChangePassphrase next (rejected)", svc.seenNext)
}

func TestGRPCBarrierChangePassphrase_NilService(t *testing.T) {
	s := &qrdbServiceServer{}
	if _, err := s.BarrierChangePassphrase(context.Background(),
		&pb.BarrierChangePassphraseRequest{Current: []byte("a"), Next: []byte("b")}); err == nil {
		t.Fatal("expected an error with no barrier service")
	}
}

// TestGRPCBarrierChangePassphrase_RequiresSOPIN verifies the passphrase
// change is SO-gated like every other custody operation: no PIN backend is
// FailedPrecondition, a missing or wrong SO PIN Unauthenticated, and in none
// of those is the barrier service reached; the right SO PIN reaches it. The
// passphrases are wiped on every path.
func TestGRPCBarrierChangePassphrase_RequiresSOPIN(t *testing.T) {
	cases := map[string]struct {
		noBackend bool
		soPIN     string
		want      codes.Code
	}{
		"no pin backend": {noBackend: true, soPIN: pintest.ThrottledSOPIN, want: codes.FailedPrecondition},
		"missing pin":    {soPIN: "", want: codes.Unauthenticated},
		"wrong pin":      {soPIN: "wrong-pin-0000", want: codes.Unauthenticated},
		"right pin":      {soPIN: pintest.ThrottledSOPIN, want: codes.OK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newCapturingBarrierSvc()
			s := &qrdbServiceServer{barrierSvc: svc}
			if !tc.noBackend {
				s.pinBackend = pintest.NewSOPINBackend(t)
			}
			current, next := []byte("old-pass"), []byte("new-pass")
			_, err := s.BarrierChangePassphrase(context.Background(),
				&pb.BarrierChangePassphraseRequest{Current: current, Next: next, SoPin: tc.soPIN})
			if got := status.Code(err); got != tc.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tc.want, err)
			}
			if reached := svc.seen != nil; reached != (tc.want == codes.OK) {
				t.Fatalf("service reached = %v, want %v", reached, tc.want == codes.OK)
			}
			assertGRPCWiped(t, "BarrierChangePassphrase current", current)
			assertGRPCWiped(t, "BarrierChangePassphrase next", next)
		})
	}
}

func (c *capturingBarrierSvc) BarrierQuorumSubmit(_ context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	c.seen = req.Share
	return nil, c.err
}

// capturingTenantSvc retains the share a tenant quorum submission carried.
type capturingTenantSvc struct {
	tenant.TenantServicer
	seen []byte
}

var _ tenant.TenantServicer = (*capturingTenantSvc)(nil)

func (c *capturingTenantSvc) TenantBarrierQuorumSubmit(_ context.Context, _ string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	c.seen = sub.Share
	return nil, errors.New("factor verification failed")
}

// TestGRPCBarrierQuorumSubmit_WipesShare proves the decoded share is
// zeroized when the service refuses the submission without consuming it.
func TestGRPCBarrierQuorumSubmit_WipesShare(t *testing.T) {
	svc := newCapturingBarrierSvc()
	svc.err = errors.New("factor verification failed")
	s := &qrdbServiceServer{barrierSvc: svc}

	if _, err := s.BarrierQuorumSubmit(context.Background(),
		&pb.BarrierQuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "alice", Share: []byte("share-bytes")}); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	assertGRPCWiped(t, "BarrierQuorumSubmit (refused)", svc.seen)
}

// TestGRPCTenantBarrierQuorumSubmit_WipesShare is the tenant form.
func TestGRPCTenantBarrierQuorumSubmit_WipesShare(t *testing.T) {
	svc := &capturingTenantSvc{}
	s := &tenantBarrierServer{svc: svc}

	if _, err := s.QuorumSubmit(context.Background(),
		&pb.TenantBarrierQuorumSubmitRequest{TenantId: "acme", Op: "unseal", Username: "alice", Share: []byte("share-bytes")}); err == nil {
		t.Fatal("expected the refusal to surface")
	}
	assertGRPCWiped(t, "TenantBarrierQuorumSubmit (refused)", svc.seen)
}
