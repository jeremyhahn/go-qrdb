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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
	seen []byte
	err  error
}

var _ service.BarrierServicer = (*capturingBarrierSvc)(nil)

func newCapturingBarrierSvc() *capturingBarrierSvc { return &capturingBarrierSvc{} }

func (c *capturingBarrierSvc) BarrierInitialize(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingBarrierSvc) BarrierUnseal(_ context.Context, secret []byte) error {
	c.seen = secret
	return c.err
}

func (c *capturingBarrierSvc) BarrierInitializeShamir(_ context.Context, secret []byte, threshold, total int) (*service.ShamirInitInfo, error) {
	c.seen = secret
	if c.err != nil {
		return nil, c.err
	}
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total, ShareCount: total}, nil
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

func TestGRPCBarrierShamirInitialize_WipesDecodedSecret(t *testing.T) {
	svc := newCapturingBarrierSvc()
	s := &qrdbServiceServer{barrierSvc: svc}

	if _, err := s.BarrierShamirInitialize(context.Background(),
		&pb.BarrierShamirInitializeRequest{Secret: []byte("shamir-seed"), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatalf("BarrierShamirInitialize: %v", err)
	}
	assertGRPCWiped(t, "BarrierInitializeShamir", svc.seen)
}

// TestGRPCBarrierSecretsAreBytes guards the proto field type itself. proto3
// encodes `string` and `bytes` identically, so a revert to `string` is
// invisible on the wire and would only surface as an unwipeable Go string in
// the daemon — exactly the defect this change removes. Assigning a []byte to
// the generated field is what does not compile if the type reverts; the
// runtime assertion below is the belt to that brace.
func TestGRPCBarrierSecretsAreBytes(t *testing.T) {
	init := &pb.BarrierInitializeRequest{Secret: []byte("x")}
	unseal := &pb.BarrierUnsealRequest{Secret: []byte("x")}
	shamir := &pb.BarrierShamirInitializeRequest{Secret: []byte("x")}
	enable := &pb.BarrierEnableEncryptionRequest{Passphrase: []byte("x")}
	tenantInit := &pb.TenantBarrierInitRequest{Passphrase: []byte("x")}

	for name, got := range map[string][]byte{
		"BarrierInitializeRequest.Secret":           init.GetSecret(),
		"BarrierUnsealRequest.Secret":               unseal.GetSecret(),
		"BarrierShamirInitializeRequest.Secret":     shamir.GetSecret(),
		"BarrierEnableEncryptionRequest.Passphrase": enable.GetPassphrase(),
		"TenantBarrierInitRequest.Passphrase":       tenantInit.GetPassphrase(),
	} {
		if string(got) != "x" {
			t.Fatalf("%s round-trip = %q, want %q", name, got, "x")
		}
		// Mutating through the getter proves the field is a slice the caller
		// owns, not a copy of an immutable string.
		got[0] = 0
	}
	if init.GetSecret()[0] != 0 {
		t.Fatal("BarrierInitializeRequest.Secret is not wipeable through its getter")
	}
}
