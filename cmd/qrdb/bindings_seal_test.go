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

//go:build qrdb_embedded && !integration

package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newSealTestBinding wires a SealBinding to an embedded transport backed by
// the given mock barrier service, mirroring how the desktop GUI connects.
func newSealTestBinding(t *testing.T, svc service.BarrierServicer, opts ...embedded.Option) *SealBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		append([]embedded.Option{embedded.WithBarrierService(svc)}, opts...)...)
	return NewSealBinding(testLogger(t), nil, conn)
}

// ---------------------------------------------------------------------------
// NewSealBinding constructor
// ---------------------------------------------------------------------------

func TestNewSealBinding_Constructor(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	b := NewSealBinding(log, esb, conn)
	if b == nil {
		t.Fatal("expected non-nil SealBinding")
	}
	if b.logger != log {
		t.Error("expected logger to be set")
	}
	if b.conn != conn {
		t.Error("expected connection binding to be set")
	}
	if b.server != esb {
		t.Error("expected server binding to be set")
	}
}

func TestNewSealBinding_NilServer(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	b := NewSealBinding(log, nil, conn)
	if b == nil {
		t.Fatal("expected non-nil SealBinding with nil server")
	}
}

// ---------------------------------------------------------------------------
// SealBinding.Initialize
// ---------------------------------------------------------------------------

func TestSealBinding_Initialize_Success(t *testing.T) {
	var gotSecret string
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, secret []byte) error {
			gotSecret = string(secret)
			return nil
		},
	}
	b := newSealTestBinding(t, svc)

	if err := b.Initialize("my-passphrase"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if gotSecret != "my-passphrase" {
		t.Errorf("expected secret %q forwarded to barrier service, got %q", "my-passphrase", gotSecret)
	}
}

func TestSealBinding_Initialize_MarksConfigInitialized(t *testing.T) {
	log := testLogger(t)
	b := newSealTestBinding(t, &mockBarrierService{})
	cb := NewConfigBinding(log, nil, b.conn)
	b.SetConfigBinding(cb)

	if err := b.Initialize(""); err != nil {
		t.Fatalf("Initialize with empty secret: %v", err)
	}
	if !cb.sessionInitialized.Load() {
		t.Error("expected ConfigBinding to be marked initialized after Initialize")
	}
}

func TestSealBinding_Initialize_ServiceError(t *testing.T) {
	log := testLogger(t)
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, _ []byte) error { return errBarrierTest },
	}
	b := newSealTestBinding(t, svc)
	cb := NewConfigBinding(log, nil, b.conn)
	b.SetConfigBinding(cb)

	err := b.Initialize("secret")
	if !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected barrier service error, got %v", err)
	}
	if cb.sessionInitialized.Load() {
		t.Error("expected ConfigBinding NOT marked initialized on failure")
	}
}

func TestSealBinding_Initialize_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	err := b.Initialize("secret")
	if err == nil {
		t.Fatal("expected error from Initialize when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SealBinding.InitializeShamir
// ---------------------------------------------------------------------------

func TestSealBinding_InitializeShamir_ShareFree(t *testing.T) {
	var gotPolicy seal.FactorPolicy
	var gotThreshold, gotTotal int
	svc := &mockBarrierService{
		initializeShamirFn: func(_ context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
			gotPolicy = policy
			gotThreshold = threshold
			gotTotal = total
			return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total, ShareCount: total}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	result, err := b.InitializeShamir(2, 3, "any-enrolled")
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if gotPolicy != seal.FactorPolicyAnyEnrolled || gotThreshold != 2 || gotTotal != 3 {
		t.Errorf("expected (2, 3, any-enrolled) forwarded, got (%d, %d, %q)", gotThreshold, gotTotal, gotPolicy)
	}
	if result == nil {
		t.Fatal("expected non-nil BootstrapResult")
	}
	// INV-1: each share is sealed to its holder and a root token needs a
	// root-token quorum of holders, so the init response carries neither.
	if result.RootToken != "" {
		t.Errorf("expected empty root token (share-free contract), got %q", result.RootToken)
	}
	if result.Threshold != 2 {
		t.Errorf("expected Threshold=2, got %d", result.Threshold)
	}
	if result.ShareCount != 3 {
		t.Errorf("expected ShareCount=3, got %d", result.ShareCount)
	}
}

func TestSealBinding_InitializeShamir_MarksConfigInitialized(t *testing.T) {
	log := testLogger(t)
	b := newSealTestBinding(t, &mockBarrierService{})
	cb := NewConfigBinding(log, nil, b.conn)
	b.SetConfigBinding(cb)

	if _, err := b.InitializeShamir(2, 3, ""); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if !cb.sessionInitialized.Load() {
		t.Error("expected ConfigBinding to be marked initialized after InitializeShamir")
	}
}

func TestSealBinding_InitializeShamir_ServiceError(t *testing.T) {
	log := testLogger(t)
	svc := &mockBarrierService{
		initializeShamirFn: func(_ context.Context, _, _ int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
			return nil, errBarrierTest
		},
	}
	b := newSealTestBinding(t, svc)
	cb := NewConfigBinding(log, nil, b.conn)
	b.SetConfigBinding(cb)

	_, err := b.InitializeShamir(2, 3, "")
	if !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected barrier service error, got %v", err)
	}
	if cb.sessionInitialized.Load() {
		t.Error("expected ConfigBinding NOT marked initialized on failure")
	}
}

func TestSealBinding_InitializeShamir_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	_, err := b.InitializeShamir(2, 3, "")
	if err == nil {
		t.Fatal("expected error from InitializeShamir when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SealBinding.Unseal
// ---------------------------------------------------------------------------

func TestSealBinding_Unseal_PassphraseRoute(t *testing.T) {
	var gotSecret string
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"}, nil
		},
		unsealFn: func(_ context.Context, secret []byte) error {
			gotSecret = string(secret)
			return nil
		},
	}
	b := newSealTestBinding(t, svc)

	progress, err := b.Unseal("master-passphrase")
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if gotSecret != "master-passphrase" {
		t.Errorf("expected passphrase forwarded to BarrierUnseal, got %q", gotSecret)
	}
	if progress.Sealed {
		t.Error("expected Sealed=false after passphrase unseal")
	}
}

// TestSealBinding_Unseal_ShamirRefused proves a Shamir barrier is never
// unsealed from a single credential through the desktop binding: the server
// refuses it (its shares go one authenticated holder at a time through
// OfficerBinding.SubmitShare), and the binding asks for no status first,
// because a sealed node answers the unseal call and nothing else.
func TestSealBinding_Unseal_ShamirRefused(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			t.Error("Unseal asked for barrier status; a sealed node refuses it")
			return nil, seal.ErrSealed
		},
		unsealFn: func(_ context.Context, _ []byte) error {
			return seal.ErrShamirUnsealRequiresQuorum
		},
	}
	b := newSealTestBinding(t, svc)

	if _, err := b.Unseal("a-share"); !errors.Is(err, seal.ErrShamirUnsealRequiresQuorum) {
		t.Fatalf("Unseal on a shamir barrier = %v, want seal.ErrShamirUnsealRequiresQuorum", err)
	}
}

func TestSealBinding_Unseal_PassphraseError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"}, nil
		},
		unsealFn: func(_ context.Context, _ []byte) error { return errBarrierTest },
	}
	b := newSealTestBinding(t, svc)

	if _, err := b.Unseal("wrong-passphrase"); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected unseal error propagated, got %v", err)
	}
}

func TestSealBinding_Unseal_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	_, err := b.Unseal("share")
	if err == nil {
		t.Fatal("expected error from Unseal when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SealBinding.Seal
// ---------------------------------------------------------------------------

// TestSealBinding_Seal_Success verifies Seal binds the SO PIN to the seal
// call's context (transport.WithSOPIN), where every network transport reads
// it; the embedded transport hands that context to the service unchanged.
func TestSealBinding_Seal_Success(t *testing.T) {
	var sealCalled, bound bool
	var got string
	svc := &mockBarrierService{
		sealFn: func(ctx context.Context) error {
			sealCalled = true
			soPIN, ok, err := transport.SOPINFromContext(ctx)
			got, bound = string(soPIN), ok
			return err
		},
	}
	b := newSealTestBinding(t, svc)

	if err := b.Seal("SO123"); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !sealCalled {
		t.Error("expected BarrierSeal to be invoked")
	}
	if !bound || got != "SO123" {
		t.Fatalf("seal call carried SO PIN %q (bound=%v), want %q", got, bound, "SO123")
	}
}

// TestSealBinding_Seal_EmptyPINBindsNone verifies an empty SO PIN binds
// nothing, for a server with no SO PIN provisioned.
func TestSealBinding_Seal_EmptyPINBindsNone(t *testing.T) {
	var bound bool
	svc := &mockBarrierService{
		sealFn: func(ctx context.Context) error {
			_, ok, err := transport.SOPINFromContext(ctx)
			bound = ok
			return err
		},
	}
	b := newSealTestBinding(t, svc)

	if err := b.Seal(""); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bound {
		t.Fatal("Seal with an empty SO PIN bound one")
	}
}

func TestSealBinding_Seal_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		sealFn: func(_ context.Context) error { return errBarrierTest },
	}
	b := newSealTestBinding(t, svc)

	if err := b.Seal("SO123"); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected seal error propagated, got %v", err)
	}
}

func TestSealBinding_Seal_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	err := b.Seal("SO123")
	if err == nil {
		t.Fatal("expected error from Seal when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

func TestSealBinding_ChangePassphrase_Success(t *testing.T) {
	var gotCurrent, gotNext, gotPIN string
	svc := &mockBarrierService{
		changePassphraseFn: func(ctx context.Context, current, next []byte) error {
			soPIN, _, err := transport.SOPINFromContext(ctx)
			gotCurrent, gotNext, gotPIN = string(current), string(next), string(soPIN)
			return err
		},
	}
	b := newSealTestBinding(t, svc, embedded.WithPINBackend(pintest.NewSOPINBackend(t)))

	if err := b.ChangePassphrase("old-passphrase", "new-passphrase", pintest.ThrottledSOPIN); err != nil {
		t.Fatalf("ChangePassphrase: %v", err)
	}
	if gotCurrent != "old-passphrase" || gotNext != "new-passphrase" || gotPIN != pintest.ThrottledSOPIN {
		t.Fatalf("service got (%q, %q, so-pin %q), want (old-passphrase, new-passphrase, the SO PIN)",
			gotCurrent, gotNext, gotPIN)
	}
}

// TestSealBinding_ChangePassphrase_RequiresSOPIN verifies the binding's
// passphrase change is refused, without reaching the barrier service, when
// the SO PIN is wrong or the node has no PIN backend.
func TestSealBinding_ChangePassphrase_RequiresSOPIN(t *testing.T) {
	cases := map[string]struct {
		opts  []embedded.Option
		soPIN string
		want  error
	}{
		"no pin backend": {soPIN: pintest.ThrottledSOPIN, want: pin.ErrBackendNotConfigured},
		"wrong pin":      {opts: []embedded.Option{embedded.WithPINBackend(pintest.NewSOPINBackend(t))}, soPIN: "wrong-pin-0000", want: pin.ErrPINInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			svc := &mockBarrierService{
				changePassphraseFn: func(context.Context, []byte, []byte) error {
					called = true
					return nil
				},
			}
			b := newSealTestBinding(t, svc, tc.opts...)
			if err := b.ChangePassphrase("old", "new", tc.soPIN); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if called {
				t.Fatal("a refused passphrase change reached the barrier service")
			}
		})
	}
}

func TestSealBinding_ChangePassphrase_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		changePassphraseFn: func(context.Context, []byte, []byte) error { return errBarrierTest },
	}
	b := newSealTestBinding(t, svc, embedded.WithPINBackend(pintest.NewSOPINBackend(t)))

	if err := b.ChangePassphrase("old", "new", pintest.ThrottledSOPIN); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected the service error propagated, got %v", err)
	}
}

func TestSealBinding_ChangePassphrase_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	var nce *NotConnectedError
	if err := b.ChangePassphrase("old", "new", ""); !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// SealBinding.Status
// ---------------------------------------------------------------------------

func TestSealBinding_Status_ShamirMapping(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:          true,
				Initialized:     true,
				Strategy:        "shamir",
				InitializedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
				ShamirThreshold: 2,
				ShamirTotal:     3,
			}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	status, err := b.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Sealed {
		t.Error("expected Sealed=true")
	}
	if !status.Initialized {
		t.Error("expected Initialized=true from the served flag")
	}
	if status.Strategy != "shamir" {
		t.Errorf("expected strategy shamir, got %q", status.Strategy)
	}
	if status.SharesRequired != 2 || status.SharesTotal != 3 {
		t.Errorf("expected M/N 2/3, got %d/%d", status.SharesRequired, status.SharesTotal)
	}
}

func TestSealBinding_Status_Uninitialized(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	status, err := b.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Initialized {
		t.Error("expected Initialized=false when the node serves it unset")
	}
	if status.SharesRequired != 0 || status.SharesTotal != 0 {
		t.Errorf("expected zero share counts for passphrase strategy, got %d/%d",
			status.SharesRequired, status.SharesTotal)
	}
}

func TestSealBinding_Status_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return nil, errBarrierTest
		},
	}
	b := newSealTestBinding(t, svc)

	if _, err := b.Status(); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected status error propagated, got %v", err)
	}
}

// TestSealBinding_Status_SealedRefusalIsInitializedAndSealed pins the GUI's
// reading of a node the cluster seal holds or cuts off: such a node refuses
// barrier status, and its own barrier is initialized, so the refusal reads as
// initialized and sealed, never as an error the GUI would report as a failed
// connection.
func TestSealBinding_Status_SealedRefusalIsInitializedAndSealed(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return nil, &transport.BarrierSealedError{Message: "barrier sealed"}
		},
	}
	b := newSealTestBinding(t, svc)

	status, err := b.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := SealStatus{Sealed: true, Initialized: true}
	if *status != want {
		t.Fatalf("Status = %+v, want %+v", *status, want)
	}
}

// TestSealBinding_Status_SealedInitializedAnswer pins the GUI's "needs init"
// check on a sealed node that is already initialized: its sealed surface
// answers with the initialized flag and nothing else -- no strategy, no
// initialization time -- and the GUI must not offer to initialize it.
func TestSealBinding_Status_SealedInitializedAnswer(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Initialized: true}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	status, err := b.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Initialized || !status.Sealed {
		t.Fatalf("Status = %+v, want sealed and initialized", *status)
	}
}

// TestSealBinding_Status_PreInitAnswerNeedsInit is the other half: a node
// waiting for its first boot answers, uninitialized.
func TestSealBinding_Status_PreInitAnswerNeedsInit(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "shamir", ShamirThreshold: 2, ShamirTotal: 3}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	status, err := b.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Initialized || !status.Sealed {
		t.Fatalf("Status = %+v, want sealed and uninitialized", *status)
	}
}

func TestSealBinding_Status_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	_, err := b.Status()
	if err == nil {
		t.Fatal("expected error from Status when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Credential ownership at the GUI boundary
//
// Every other method on SealBinding takes a string and runs it through
// secretBytes, which allocates, so the slice it wipes is its own.
// EnableEncryption is the one that receives the caller's struct, and the Wails
// frontend still holds that pointer after the promise settles. Wiping
// req.Passphrase there hands the GUI back a zeroed field of its own request
// object; the binding must copy first and wipe only the copy.
// ---------------------------------------------------------------------------

// TestSealBinding_EnableEncryption_LeavesTheCallersPassphraseIntact fails if
// the binding goes back to wiping req.Passphrase directly.
//
// The transport returns NotImplementedError for enable-encryption on the
// embedded path, which is deliberate here rather than incidental: the deferred
// wipe runs on the error path too, so the error return is the strictest place
// to observe which slice got zeroed.
func TestSealBinding_EnableEncryption_LeavesTheCallersPassphraseIntact(t *testing.T) {
	b := newSealTestBinding(t, &mockBarrierService{})

	const want = "caller-owned-passphrase"
	req := &transport.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Passphrase: []byte(want),
	}

	// Error is expected on the embedded transport; ownership is the assertion.
	_, _ = b.EnableEncryption(req)

	if string(req.Passphrase) != want {
		t.Fatalf("the caller's passphrase was zeroed by the binding: got %q, want %q — "+
			"EnableEncryption must copy before it wipes", req.Passphrase, want)
	}
}

// TestSealBinding_EnableEncryption_WipesItsOwnCopy is the other half: the fix
// must not be "delete the wipe". A nil passphrase must stay nil rather than
// becoming an empty non-nil slice, so an absent credential is still absent on
// the wire.
func TestSealBinding_EnableEncryption_WipesItsOwnCopy(t *testing.T) {
	b := newSealTestBinding(t, &mockBarrierService{})

	req := &transport.BarrierEnableEncryptionRequest{Strategy: "shamir"}
	_, _ = b.EnableEncryption(req)

	if req.Passphrase != nil {
		t.Fatalf("an absent passphrase must stay nil, got %#v", req.Passphrase)
	}
}

// ---------------------------------------------------------------------------
// SealBinding cluster sealing
// ---------------------------------------------------------------------------

// TestSealBinding_ClusterUnseal proves the desktop binding clears the node
// barrier's cluster seal record through the connected client and returns the
// record's new state.
func TestSealBinding_ClusterUnseal(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		clusterUnsealFn: func(context.Context) (*clusterseal.Report, error) {
			return &clusterseal.Report{Active: true, Epoch: 4, Principal: "ops", PrincipalTenant: "system",
				Node: "192.0.2.1:5000", At: at, Local: clusterseal.LocalServing}, nil
		},
	}
	b := newSealTestBinding(t, svc)
	got, err := b.ClusterUnseal()
	if err != nil {
		t.Fatalf("ClusterUnseal: %v", err)
	}
	if !got.Active || got.Sealed || got.Epoch != 4 || got.Local != "serving" || got.At != "2026-10-02T12:00:00Z" {
		t.Fatalf("ClusterUnseal = %+v", got)
	}
}

// TestSealBinding_ClusterUnseal_Errors proves the binding surfaces the
// service's refusal -- cluster sealing not active -- and refuses when no
// client is connected.
func TestSealBinding_ClusterUnseal_Errors(t *testing.T) {
	b := newSealTestBinding(t, &mockBarrierService{
		clusterUnsealFn: func(context.Context) (*clusterseal.Report, error) { return nil, clusterseal.ErrInactive },
	})
	if _, err := b.ClusterUnseal(); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("ClusterUnseal while inactive: err = %v", err)
	}

	log := testLogger(t)
	disconnected := NewSealBinding(log, nil, NewConnectionBinding(log))
	var nce *NotConnectedError
	if _, err := disconnected.ClusterUnseal(); !errors.As(err, &nce) {
		t.Fatalf("ClusterUnseal not connected: err = %v, want NotConnectedError", err)
	}
}

// TestSealBinding_Status_CarriesTheClusterSeal proves the desktop status
// shows the node barrier's cluster seal state, held included.
func TestSealBinding_Status_CarriesTheClusterSeal(t *testing.T) {
	b := newSealTestBinding(t, &mockBarrierService{
		statusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Strategy: "passphrase", Initialized: true, InitializedAt: time.Now(),
				ClusterSeal: &clusterseal.Report{Active: true, Sealed: true, Epoch: 3, Local: clusterseal.LocalHeld}}, nil
		},
	})
	st, err := b.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.ClusterSeal == nil || !st.ClusterSeal.Sealed || st.ClusterSeal.Local != "held" {
		t.Fatalf("ClusterSeal = %+v, want the held record", st.ClusterSeal)
	}
}

// TestSealBinding_Status_CarriesTheClusterMigration proves the desktop status
// shows a cluster-wide enable-encryption in progress, each owner's step and
// own report included.
func TestSealBinding_Status_CarriesTheClusterMigration(t *testing.T) {
	b := newSealTestBinding(t, &mockBarrierService{
		statusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Strategy: "none", Initialized: true, InitializedAt: time.Now(),
				ClusterMigration: &clustermig.Report{Target: "passphrase", Generation: 2, Acked: 1, Total: 2,
					Nodes: []clustermig.NodeReport{
						{NodeID: "n2", Step: clustermig.StepServing, Phase: "complete"},
						{NodeID: "n1", Step: clustermig.StepRecycling, Phase: "failed", Attempts: 2, Detail: "credential missing"},
					}}}, nil
		},
	})
	st, err := b.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := &transport.ClusterMigration{Target: "passphrase", Generation: 2, Acked: 1, Total: 2,
		Nodes: []transport.ClusterMigrationNode{
			{NodeID: "n2", Step: "serving", Phase: "complete"},
			{NodeID: "n1", Step: "recycling", Phase: "failed", Attempts: 2, Detail: "credential missing"},
		}}
	if !reflect.DeepEqual(st.ClusterMigration, want) {
		t.Fatalf("ClusterMigration = %+v, want %+v", st.ClusterMigration, want)
	}
}

// TestSealBinding_Status_NoClusterMigration proves the desktop status carries
// no migration when none is in progress, and reports one that could not be
// read as unconfirmed rather than absent.
func TestSealBinding_Status_NoClusterMigration(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *clustermig.Report
		want *transport.ClusterMigration
	}{
		{"none in progress", nil, nil},
		{"unconfirmed", &clustermig.Report{Unconfirmed: true}, &transport.ClusterMigration{Unconfirmed: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newSealTestBinding(t, &mockBarrierService{
				statusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
					return &service.BarrierStatusInfo{Strategy: "none", Initialized: true, InitializedAt: time.Now(), ClusterMigration: tc.in}, nil
				},
			})
			st, err := b.Status()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(st.ClusterMigration, tc.want) {
				t.Fatalf("ClusterMigration = %+v, want %+v", st.ClusterMigration, tc.want)
			}
		})
	}
}
