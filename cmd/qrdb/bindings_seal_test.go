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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newSealTestBinding wires a SealBinding to an embedded transport backed by
// the given mock barrier service, mirroring how the desktop GUI connects.
func newSealTestBinding(t *testing.T, svc service.BarrierServicer) *SealBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithBarrierService(svc))
	return NewSealBinding(testLogger(t), nil, conn)
}

// errBarrierTest is a sentinel error returned by mock barrier functions.
var errBarrierTest = errors.New("barrier test failure")

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
		initializeFn: func(_ context.Context, secret string) error {
			gotSecret = secret
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
		initializeFn: func(_ context.Context, _ string) error { return errBarrierTest },
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
	var gotSecret string
	var gotThreshold, gotTotal int
	svc := &mockBarrierService{
		initializeShamirFn: func(_ context.Context, secret string, threshold, total int) (*service.ShamirInitInfo, error) {
			gotSecret = secret
			gotThreshold = threshold
			gotTotal = total
			return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total, ShareCount: total}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	result, err := b.InitializeShamir("passphrase", 2, 3)
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if gotSecret != "passphrase" || gotThreshold != 2 || gotTotal != 3 {
		t.Errorf("expected (passphrase, 2, 3) forwarded, got (%q, %d, %d)", gotSecret, gotThreshold, gotTotal)
	}
	if result == nil {
		t.Fatal("expected non-nil BootstrapResult")
	}
	// INV-1: shares are sealed to enrolled officers and the root token
	// requires a post-claim quorum, so the init response carries neither.
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

	if _, err := b.InitializeShamir("secret", 1, 2); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	if !cb.sessionInitialized.Load() {
		t.Error("expected ConfigBinding to be marked initialized after InitializeShamir")
	}
}

func TestSealBinding_InitializeShamir_ServiceError(t *testing.T) {
	log := testLogger(t)
	svc := &mockBarrierService{
		initializeShamirFn: func(_ context.Context, _ string, _, _ int) (*service.ShamirInitInfo, error) {
			return nil, errBarrierTest
		},
	}
	b := newSealTestBinding(t, svc)
	cb := NewConfigBinding(log, nil, b.conn)
	b.SetConfigBinding(cb)

	_, err := b.InitializeShamir("secret", 2, 3)
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

	_, err := b.InitializeShamir("secret", 2, 3)
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

func TestSealBinding_Unseal_ShamirPartialProgress(t *testing.T) {
	var gotShare string
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "shamir"}, nil
		},
		unsealShareFn: func(_ context.Context, share string) (*service.UnsealShareProgress, error) {
			gotShare = share
			return &service.UnsealShareProgress{Required: 3, Submitted: 1, Complete: false}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	progress, err := b.Unseal("officer-share-1")
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if gotShare != "officer-share-1" {
		t.Errorf("expected share forwarded to barrier service, got %q", gotShare)
	}
	if !progress.Sealed {
		t.Error("expected Sealed=true while quorum incomplete")
	}
	if progress.SharesProvided != 1 || progress.SharesRequired != 3 {
		t.Errorf("expected progress 1/3, got %d/%d", progress.SharesProvided, progress.SharesRequired)
	}
}

func TestSealBinding_Unseal_ShamirComplete(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "shamir"}, nil
		},
		unsealShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return &service.UnsealShareProgress{Required: 2, Submitted: 2, Complete: true}, nil
		},
	}
	b := newSealTestBinding(t, svc)

	progress, err := b.Unseal("officer-share-2")
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if progress.Sealed {
		t.Error("expected Sealed=false once quorum complete")
	}
	if progress.SharesProvided != 2 || progress.SharesRequired != 2 {
		t.Errorf("expected progress 2/2, got %d/%d", progress.SharesProvided, progress.SharesRequired)
	}
}

func TestSealBinding_Unseal_PassphraseRoute(t *testing.T) {
	var gotSecret string
	var shareCalled bool
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"}, nil
		},
		unsealFn: func(_ context.Context, secret string) error {
			gotSecret = secret
			return nil
		},
		unsealShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			shareCalled = true
			return nil, errBarrierTest
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
	if shareCalled {
		t.Error("expected BarrierUnsealWithShare NOT called for passphrase strategy")
	}
	if progress.Sealed {
		t.Error("expected Sealed=false after passphrase unseal")
	}
}

func TestSealBinding_Unseal_StatusError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return nil, errBarrierTest
		},
	}
	b := newSealTestBinding(t, svc)

	if _, err := b.Unseal("share"); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected status error propagated, got %v", err)
	}
}

func TestSealBinding_Unseal_ShamirShareError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "shamir"}, nil
		},
		unsealShareFn: func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
			return nil, errBarrierTest
		},
	}
	b := newSealTestBinding(t, svc)

	if _, err := b.Unseal("bad-share"); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected share error propagated, got %v", err)
	}
}

func TestSealBinding_Unseal_PassphraseError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{Sealed: true, Strategy: "passphrase"}, nil
		},
		unsealFn: func(_ context.Context, _ string) error { return errBarrierTest },
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

func TestSealBinding_Seal_Success(t *testing.T) {
	var sealCalled bool
	svc := &mockBarrierService{
		sealFn: func(_ context.Context) error {
			sealCalled = true
			return nil
		},
	}
	b := newSealTestBinding(t, svc)

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !sealCalled {
		t.Error("expected BarrierSeal to be invoked")
	}
}

func TestSealBinding_Seal_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		sealFn: func(_ context.Context) error { return errBarrierTest },
	}
	b := newSealTestBinding(t, svc)

	if err := b.Seal(); !errors.Is(err, errBarrierTest) {
		t.Fatalf("expected seal error propagated, got %v", err)
	}
}

func TestSealBinding_Seal_NotConnected(t *testing.T) {
	log := testLogger(t)
	b := NewSealBinding(log, nil, NewConnectionBinding(log))

	err := b.Seal()
	if err == nil {
		t.Fatal("expected error from Seal when not connected")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
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
		t.Error("expected Initialized=true for non-zero InitializedAt")
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
		t.Error("expected Initialized=false for zero InitializedAt")
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
