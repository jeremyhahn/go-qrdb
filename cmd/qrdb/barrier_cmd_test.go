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

//go:build !integration

// barrier_cmd_test.go verifies the canonical barrier verb tree exposed
// by cmd/qrdb. The tests pin every verb, every flag, and every typed
// error to lock the gold-standard surface that xkmsctl, quicbaasctl and
// platformctl must mirror.
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock BarrierServicer
// ---------------------------------------------------------------------------

// mockBarrierService implements service.BarrierServicer with configurable
// function fields for per-test behavior injection.
type mockBarrierService struct {
	initializeFn       func(ctx context.Context, secret []byte) error
	unsealFn           func(ctx context.Context, secret []byte) error
	sealFn             func(ctx context.Context) error
	statusFn           func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn func(ctx context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	genRecoveryKeysFn  func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	deleteRecoveryFn   func(ctx context.Context) error
	hasRecoveryKeysFn  func(ctx context.Context) (bool, error)

	officerRegisterFn    func(ctx context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn        func(ctx context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn    func(ctx context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn func(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	quorumBeginFn        func(ctx context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error)
	quorumSubmitFn       func(ctx context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error)
	rekeyStartFn         func(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy) (*service.QuorumRoundInfo, error)
	officerRevokeFn      func(ctx context.Context, domain seal.QuorumDomain, username string, threshold int) (*service.QuorumRoundInfo, error)
}

var _ service.BarrierServicer = (*mockBarrierService)(nil)

// errBarrierMockUnset answers a holder method the test did not configure, so
// an unexpected call fails loudly instead of answering a zero value.
var errBarrierMockUnset = errors.New("mockBarrierService: holder method not configured by this test")

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.sealFn != nil {
		return m.sealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.BarrierStatusInfo{
		Sealed:         false,
		Strategy:       "passphrase",
		HardwareBacked: false,
		InitializedAt:  time.Now(),
	}, nil
}

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, threshold, total int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, threshold, total, policy)
	}
	return &service.ShamirInitInfo{ShareCount: total, Threshold: threshold, TotalShares: total}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.genRecoveryKeysFn != nil {
		return m.genRecoveryKeysFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{ShareCount: threshold + 1, Threshold: threshold, Total: threshold + 1}, nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryFn != nil {
		return m.deleteRecoveryFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return false, nil
}

func (m *mockBarrierService) BarrierOfficerRegister(ctx context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, domain, req)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierOfficerList(ctx context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx, domain)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierShareClaimBegin(ctx context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.shareClaimBeginFn != nil {
		return m.shareClaimBeginFn(ctx, domain, username, thumbprint)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierShareClaimComplete(ctx context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.shareClaimCompleteFn != nil {
		return m.shareClaimCompleteFn(ctx, domain, username, nonceHex, signature, thumbprint)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierQuorumBegin(ctx context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if m.quorumBeginFn != nil {
		return m.quorumBeginFn(ctx, req)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierQuorumSubmit(ctx context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	if m.quorumSubmitFn != nil {
		return m.quorumSubmitFn(ctx, req)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierRekeyStart(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy) (*service.QuorumRoundInfo, error) {
	if m.rekeyStartFn != nil {
		return m.rekeyStartFn(ctx, domain, threshold, policy)
	}
	return nil, errBarrierMockUnset
}

func (m *mockBarrierService) BarrierOfficerRevoke(ctx context.Context, domain seal.QuorumDomain, username string, threshold int) (*service.QuorumRoundInfo, error) {
	if m.officerRevokeFn != nil {
		return m.officerRevokeFn(ctx, domain, username, threshold)
	}
	return nil, errBarrierMockUnset
}

// newBarrierTestRoot builds a root command with an injected barrier service
// and a real PIN backend, as every composer wires one.
func newBarrierTestRoot(t *testing.T, svc service.BarrierServicer) *cobra.Command {
	t.Helper()
	return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBarrierService(svc), embedded.WithPINBackend(pintest.NewBackend(t)))
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier status
// ---------------------------------------------------------------------------

// TestBarrierStatusCmd_TextOutput verifies the table-format renderer
// prints the canonical field labels.
func TestBarrierStatusCmd_TextOutput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, label := range []string{"Sealed", "Strategy", "HardwareBacked", "InitializedAt"} {
		if !strings.Contains(out, label) {
			t.Errorf("expected %q in status output: %q", label, out)
		}
	}
}

// TestBarrierStatusCmd_JSONOutput verifies the persistent --output flag
// is honored by the status verb.
func TestBarrierStatusCmd_JSONOutput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "--output", "json", "barrier", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "sealed") {
		t.Errorf("expected sealed field in JSON output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier init
// ---------------------------------------------------------------------------

// TestBarrierInitCmd_SoftwareSuccess covers the happy path with the
// default (passphrase) strategy.
func TestBarrierInitCmd_SoftwareSuccess(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "passphrase", "--passphrase", "passphrase123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// TestBarrierInitCmd_NoneStrategy verifies the none strategy does not
// require --passphrase.
func TestBarrierInitCmd_NoneStrategy(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "init", "--barrier-strategy", "none")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// TestBarrierInitCmd_NoneRejectsSecret verifies --passphrase is rejected
// for --barrier-strategy=none (dev mode is cleartext by design).
func TestBarrierInitCmd_NoneRejectsSecret(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "none", "--passphrase", "should-not-be-here")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
	if invalid.Arg != "--passphrase" {
		t.Errorf("expected error on --passphrase, got %s", invalid.Arg)
	}
}

// TestBarrierInitCmd_MissingSecret verifies passphrase strategy requires
// --passphrase.
func TestBarrierInitCmd_MissingSecret(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init", "--barrier-strategy", "passphrase")
	var missing *MissingArgError
	if !errors.As(err, &missing) {
		t.Fatalf("expected *MissingArgError, got %T: %v", err, err)
	}
}

// TestBarrierInitCmd_InvalidStrategy verifies the strategy enum gate.
func TestBarrierInitCmd_InvalidStrategy(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "exotic", "--passphrase", "s")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
}

// TestBarrierInitCmd_ShamirPointsAtInitShamir verifies `barrier init`
// refuses the shamir strategy locally: a Shamir barrier is initialized over
// its enrolled holders by `barrier init-shamir`, never from a passphrase.
func TestBarrierInitCmd_ShamirPointsAtInitShamir(t *testing.T) {
	called := false
	root := newBarrierTestRoot(t, &mockBarrierService{initializeFn: func(context.Context, []byte) error {
		called = true
		return nil
	}})
	_, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "shamir", "--passphrase", "a passphrase")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) || invalid.Arg != "--barrier-strategy" || !strings.Contains(invalid.Reason, "init-shamir") {
		t.Fatalf("err = %v, want an InvalidArgError on --barrier-strategy naming init-shamir", err)
	}
	if called {
		t.Fatal("BarrierInitialize was called for the shamir strategy")
	}
}

// TestBarrierInitCmd_UserPinRequiresSOPin verifies the dual-control
// invariant: supplying --user-pin without --so-pin is rejected.
func TestBarrierInitCmd_UserPinRequiresSOPin(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "passphrase", "--passphrase", "s", "--user-pin", "USR123")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
}

// TestBarrierInitCmd_SOPinRequiresUserPin verifies the dual-control
// invariant is bidirectional: supplying --so-pin without --user-pin is
// rejected (FIPS 140-3 / PCI-DSS v4; parity with xkmsctl/quicbaas).
func TestBarrierInitCmd_SOPinRequiresUserPin(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "passphrase", "--passphrase", "s", "--so-pin", "SO123")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
	if invalid.Arg != "--so-pin" {
		t.Errorf("expected error on --so-pin, got %s", invalid.Arg)
	}
}

// TestBarrierInitCmd_NoneRejectsPINs verifies --so-pin / --user-pin are
// rejected with the typed PINsNotAllowedError when
// --barrier-strategy=none (no PIN store exists for the passthrough
// strategy; canonical Phase 9c contract shared with the sibling CLIs).
func TestBarrierInitCmd_NoneRejectsPINs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"so_pin_only", []string{"--so-pin", "SO123"}},
		{"user_pin_only", []string{"--user-pin", "USR123"}},
		{"both_pins", []string{"--so-pin", "SO1234", "--user-pin", "USR123"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newBarrierTestRoot(t, &mockBarrierService{})
			args := append([]string{"barrier", "init", "--barrier-strategy", "none"}, tc.args...)
			_, err := executeCommand(root, args...)
			var pins *PINsNotAllowedError
			if !errors.As(err, &pins) {
				t.Fatalf("expected *PINsNotAllowedError, got %T: %v", err, err)
			}
			if pins.Strategy != "none" || pins.Op != "init" {
				t.Errorf("unexpected error fields: %+v", pins)
			}
		})
	}
}

// TestBarrierInitCmd_ValidPINPairSuccess verifies a complete SO/User
// PIN pair passes validation alongside the passphrase strategy.
func TestBarrierInitCmd_ValidPINPairSuccess(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "init",
		"--barrier-strategy", "passphrase", "--passphrase", "s",
		"--so-pin", "SO1234", "--user-pin", "USR123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier init-shamir
// ---------------------------------------------------------------------------

// TestBarrierInitShamirCmd_Success covers the happy path.
func TestBarrierInitShamirCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "init-shamir",
		"--threshold", "2", "--total", "3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, label := range []string{"Threshold", "TotalShares", "Shares"} {
		if !strings.Contains(out, label) {
			t.Errorf("expected %q in output: %q", label, out)
		}
	}
}

// TestBarrierInitShamirCmd_JSONOutput verifies --output json is honored.
func TestBarrierInitShamirCmd_JSONOutput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "--output", "json", "barrier", "init-shamir",
		"--threshold", "2", "--total", "3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "shares") {
		t.Errorf("expected shares in JSON output: %q", out)
	}
}

// TestBarrierInitShamirCmd_ThresholdGate verifies threshold must be >= 2.
func TestBarrierInitShamirCmd_ThresholdGate(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init-shamir",
		"--threshold", "1", "--total", "3")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
}

// TestBarrierInitShamirCmd_TotalLessThanThreshold verifies total >=
// threshold.
func TestBarrierInitShamirCmd_TotalLessThanThreshold(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init-shamir",
		"--threshold", "3", "--total", "2")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
}

// TestBarrierInitShamirCmd_UserPinRequiresSOPin verifies the
// dual-control invariant on init-shamir: --user-pin without --so-pin
// is rejected.
func TestBarrierInitShamirCmd_UserPinRequiresSOPin(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init-shamir",
		"--threshold", "2", "--total", "3",
		"--user-pin", "USR123")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
	if invalid.Arg != "--user-pin" {
		t.Errorf("expected error on --user-pin, got %s", invalid.Arg)
	}
}

// TestBarrierInitShamirCmd_SOPinRequiresUserPin verifies the
// dual-control invariant is bidirectional on init-shamir: --so-pin
// without --user-pin is rejected (parity with xkmsctl/quicbaas).
func TestBarrierInitShamirCmd_SOPinRequiresUserPin(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init-shamir",
		"--threshold", "2", "--total", "3",
		"--so-pin", "SO123")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
	if invalid.Arg != "--so-pin" {
		t.Errorf("expected error on --so-pin, got %s", invalid.Arg)
	}
}

// TestBarrierInitShamirCmd_ValidPINPairSuccess verifies a complete
// SO/User PIN pair passes init-shamir validation.
func TestBarrierInitShamirCmd_ValidPINPairSuccess(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "init-shamir",
		"--threshold", "2", "--total", "3",
		"--so-pin", "SO1234", "--user-pin", "USR123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier unseal
// ---------------------------------------------------------------------------

func TestBarrierUnsealCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "unseal", "--passphrase", "passphrase123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

func TestBarrierUnsealCmd_MissingSecret(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "unseal")
	if err == nil {
		t.Fatal("expected error for missing --passphrase flag")
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier seal
// ---------------------------------------------------------------------------

// TestBarrierSealCmd_AssumeYes verifies seal succeeds when the operator
// supplies -y to skip the prompt.
func TestBarrierSealCmd_AssumeYes(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "seal", "-y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// TestBarrierSealCmd_AssumeYesLong verifies the long form of -y.
func TestBarrierSealCmd_AssumeYesLong(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "seal", "--assume-yes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// TestBarrierSealCmd_NoConfirmAborts verifies that without --assume-yes
// and with stdin closed the command aborts cleanly.
func TestBarrierSealCmd_NoConfirmAborts(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	// Empty stdin: scanner.Scan() returns false → typed abort error.
	root.SetIn(bytes.NewReader(nil))
	_, err := executeCommand(root, "barrier", "seal")
	var abort *UserAbortError
	if !errors.As(err, &abort) {
		t.Fatalf("expected *UserAbortError, got %T: %v", err, err)
	}
}

// TestBarrierSealCmd_ConfirmWithY verifies typing 'y' at the prompt
// completes the seal.
func TestBarrierSealCmd_ConfirmWithY(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	root.SetIn(strings.NewReader("y\n"))
	out, err := executeCommand(root, "barrier", "seal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// TestBarrierSealCmd_AcceptsSOPin verifies the --so-pin flag is parsed
// successfully even though wire-level propagation is the sibling agent's
// concern (interface ripple out of scope here).
func TestBarrierSealCmd_AcceptsSOPin(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "seal", "--so-pin", "SO123", "-y")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier rekey
// ---------------------------------------------------------------------------

// TestBarrierRekeyCmd_PassphraseFlagRemoved guards against the dead
// new_secret field resurfacing: rekey carries no secret material, so
// --passphrase must be an unknown flag.
func TestBarrierRekeyCmd_PassphraseFlagRemoved(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "rekey", "--passphrase", "new-pp")
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("expected unknown flag error for --passphrase, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier recovery generate / recover
// ---------------------------------------------------------------------------

func TestBarrierRecoveryGenerateCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "recovery", "generate",
		"--threshold", "2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Threshold") || !strings.Contains(out, "ShareCount") {
		t.Errorf("expected recovery key output, got: %q", out)
	}
}

func TestBarrierRecoveryGenerateCmd_JSONOutput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "--output", "json", "barrier", "recovery", "generate",
		"--threshold", "2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "share_count") {
		t.Errorf("expected share_count in JSON output: %q", out)
	}
}

func TestBarrierRecoveryGenerateCmd_AcceptsSOPin(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "recovery", "generate",
		"--threshold", "2", "--so-pin", "SO123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Operational verbs (not part of canonical surface, retained for parity)
// ---------------------------------------------------------------------------

func TestBarrierRecoveryDeleteCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "recovery", "delete")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

func TestBarrierRecoveryStatusCmd_HasNoKeys(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) { return false, nil },
	})
	out, err := executeCommand(root, "barrier", "recovery", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "HasKeys") || !strings.Contains(out, "false") {
		t.Errorf("unexpected status output: %q", out)
	}
}

func TestBarrierRecoveryStatusCmd_HasKeys(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{
		hasRecoveryKeysFn: func(_ context.Context) (bool, error) { return true, nil },
	})
	out, err := executeCommand(root, "barrier", "recovery", "status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("expected true in status output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Canonical verb-tree wiring test
// ---------------------------------------------------------------------------

// canonicalBarrierVerbs enumerates the canonical barrier verbs, including
// the share-holder officer and quorum verbs, that every CLI binary in the
// stack MUST expose. Tests pin both the path and the required flag set per
// verb.
var canonicalBarrierVerbs = []struct {
	path  []string
	flags []string
}{
	{path: []string{"barrier", "status"}, flags: nil},
	{
		path:  []string{"barrier", "init"},
		flags: []string{"barrier-strategy", "passphrase", "so-pin", "user-pin"},
	},
	{
		path:  []string{"barrier", "init-shamir"},
		flags: []string{"threshold", "total", "policy", "so-pin", "user-pin"},
	},
	{
		path:  []string{"barrier", "unseal"},
		flags: []string{"passphrase"},
	},
	{
		path:  []string{"barrier", "seal"},
		flags: []string{"so-pin", "assume-yes"},
	},
	{
		path:  []string{"barrier", "rekey"},
		flags: []string{"domain", "threshold", "policy", "so-pin"},
	},
	{
		path:  []string{"barrier", "recovery", "generate"},
		flags: []string{"threshold", "policy", "so-pin"},
	},
	{
		path:  []string{"barrier", "officer", "register"},
		flags: []string{"domain", "username", "role", "csr-file", "encryption-kem", "encryption-key-file", "designated", "factor"},
	},
	{path: []string{"barrier", "officer", "list"}, flags: []string{"domain"}},
	{path: []string{"barrier", "officer", "claim"}, flags: []string{"domain", "username", "key", "out"}},
	{path: []string{"barrier", "officer", "revoke"}, flags: []string{"domain", "username", "threshold", "so-pin"}},
	{path: []string{"barrier", "quorum", "begin"}, flags: []string{"domain", "op", "username", "factor"}},
	{
		path:  []string{"barrier", "quorum", "submit"},
		flags: []string{"domain", "op", "username", "envelope", "key", "encryption-key-file", "challenge-id", "factor", "response-file", "response-metadata"},
	},
}

// TestCanonicalBarrierVerbTree pins the entire canonical verb tree and
// every required flag in a single table-driven test. This is the
// authoritative gate other CLIs must mirror.
func TestCanonicalBarrierVerbTree(t *testing.T) {
	root := newRootCmd()
	for _, tc := range canonicalBarrierVerbs {
		name := strings.Join(tc.path, "/")
		t.Run(name, func(t *testing.T) {
			cmd, _, err := root.Find(tc.path)
			if err != nil {
				t.Fatalf("verb %s not registered: %v", name, err)
			}
			for _, flagName := range tc.flags {
				if cmd.Flags().Lookup(flagName) == nil {
					t.Errorf("verb %s missing flag --%s", name, flagName)
				}
			}
		})
	}
}

// TestCanonicalBarrierVerbTree_TransportDefault pins the root-level
// --transport persistent flag and its canonical default.
func TestCanonicalBarrierVerbTree_TransportDefault(t *testing.T) {
	root := newRootCmd()
	flag := root.PersistentFlags().Lookup("transport")
	if flag == nil {
		t.Fatal("root --transport persistent flag missing")
	}
	if flag.DefValue != "unix" {
		t.Errorf("expected --transport default 'unix', got %q", flag.DefValue)
	}
}
