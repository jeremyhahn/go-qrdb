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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

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
	initializeShamirFn func(ctx context.Context, secret []byte, threshold, total int) (*service.ShamirInitInfo, error)
	unsealShareFn      func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	unsealSharesFn     func(ctx context.Context, shares []string) error
	rekeyFn            func(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error)
	genRecoveryKeysFn  func(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error)
	recoverFn          func(ctx context.Context, keys []string) error
	deleteRecoveryFn   func(ctx context.Context) error
	hasRecoveryKeysFn  func(ctx context.Context) (bool, error)
	genRootTokenFn     func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)

	officerRegisterFn         func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn             func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	officerClaimStatusFn      func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn         func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn      func(ctx context.Context, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	shamirSealVoteFn          func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	recoveryOfficerRegisterFn func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	recoveryClaimBeginFn      func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	recoveryClaimCompleteFn   func(ctx context.Context, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
}

var _ service.BarrierServicer = (*mockBarrierService)(nil)

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

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, secret []byte, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, secret, threshold, total)
	}
	return &service.ShamirInitInfo{
		ShareCount:  3,
		Threshold:   threshold,
		TotalShares: total,
	}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShare(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.unsealShareFn != nil {
		return m.unsealShareFn(ctx, share)
	}
	return &service.UnsealShareProgress{Required: 2, Submitted: 1, Complete: false}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShares(ctx context.Context, shares []string) error {
	if m.unsealSharesFn != nil {
		return m.unsealSharesFn(ctx, shares)
	}
	return nil
}

func (m *mockBarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.rekeyFn != nil {
		return m.rekeyFn(ctx, threshold, total)
	}
	return &service.ShamirInitInfo{
		ShareCount:  2,
		Threshold:   threshold,
		TotalShares: total,
	}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	if m.genRecoveryKeysFn != nil {
		return m.genRecoveryKeysFn(ctx, threshold, total)
	}
	return &service.RecoveryKeysInfo{
		ShareCount: 2,
		Threshold:  threshold,
		Total:      total,
	}, nil
}

func (m *mockBarrierService) BarrierOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, req)
	}
	return &service.BarrierOfficerRegisterResult{}, nil
}

func (m *mockBarrierService) BarrierOfficerList(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx)
	}
	return nil, nil
}

func (m *mockBarrierService) BarrierOfficerClaimStatus(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if m.officerClaimStatusFn != nil {
		return m.officerClaimStatusFn(ctx)
	}
	return nil, nil
}

func (m *mockBarrierService) BarrierShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.shareClaimBeginFn != nil {
		return m.shareClaimBeginFn(ctx, username, thumbprint)
	}
	return &service.BarrierShareClaimChallenge{}, nil
}

func (m *mockBarrierService) BarrierShareClaimComplete(ctx context.Context, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.shareClaimCompleteFn != nil {
		return m.shareClaimCompleteFn(ctx, username, nonceHex, signature, thumbprint)
	}
	return &service.BarrierShareClaimResult{}, nil
}

func (m *mockBarrierService) BarrierShamirSealVote(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.shamirSealVoteFn != nil {
		return m.shamirSealVoteFn(ctx, share)
	}
	return &service.UnsealShareProgress{}, nil
}

func (m *mockBarrierService) BarrierRecoveryOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if m.recoveryOfficerRegisterFn != nil {
		return m.recoveryOfficerRegisterFn(ctx, req)
	}
	return &service.BarrierOfficerRegisterResult{}, nil
}

func (m *mockBarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if m.recoveryClaimBeginFn != nil {
		return m.recoveryClaimBeginFn(ctx, username, thumbprint)
	}
	return &service.BarrierShareClaimChallenge{}, nil
}

func (m *mockBarrierService) BarrierRecoveryShareClaimComplete(ctx context.Context, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if m.recoveryClaimCompleteFn != nil {
		return m.recoveryClaimCompleteFn(ctx, username, nonceHex, signature, thumbprint)
	}
	return &service.BarrierShareClaimResult{}, nil
}

func (m *mockBarrierService) BarrierRecoverWithKeys(ctx context.Context, keys []string) error {
	if m.recoverFn != nil {
		return m.recoverFn(ctx, keys)
	}
	return nil
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

func (m *mockBarrierService) BarrierGenerateRootToken(ctx context.Context, shares []string) (*service.RootTokenInfo, error) {
	if m.genRootTokenFn != nil {
		return m.genRootTokenFn(ctx, shares)
	}
	return &service.RootTokenInfo{Token: "root-token-abc", CreatedAt: time.Now()}, nil
}

// newBarrierTestRoot builds a root command with an injected barrier service.
func newBarrierTestRoot(t *testing.T, svc service.BarrierServicer) *cobra.Command {
	t.Helper()
	return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
		embedded.WithBarrierService(svc))
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
		{"both_pins", []string{"--so-pin", "SO123", "--user-pin", "USR123"}},
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
		"--so-pin", "SO123", "--user-pin", "USR123")
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
		"--passphrase", "seed", "--threshold", "2", "--total", "3")
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
		"--passphrase", "seed", "--threshold", "2", "--total", "3")
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
		"--passphrase", "seed", "--threshold", "1", "--total", "3")
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
		"--passphrase", "seed", "--threshold", "3", "--total", "2")
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
		"--passphrase", "seed", "--threshold", "2", "--total", "3",
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
		"--passphrase", "seed", "--threshold", "2", "--total", "3",
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
		"--passphrase", "seed", "--threshold", "2", "--total", "3",
		"--so-pin", "SO123", "--user-pin", "USR123")
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
// Canonical surface: barrier unseal-shares
// ---------------------------------------------------------------------------

// TestBarrierUnsealSharesCmd_RepeatedFlag verifies the repeatable
// --share form.
func TestBarrierUnsealSharesCmd_RepeatedFlag(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "unseal-shares",
		"--share", "s1", "--share", "s2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
}

// TestBarrierUnsealSharesCmd_FileForm verifies the --shares-file form.
func TestBarrierUnsealSharesCmd_FileForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shares.txt")
	body := "# leading comment\nshare-a\n\nshare-b\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write shares file: %v", err)
	}

	got := make([][]string, 0, 1)
	svc := &mockBarrierService{
		unsealSharesFn: func(_ context.Context, shares []string) error {
			got = append(got, append([]string(nil), shares...))
			return nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	_, err := executeCommand(root, "barrier", "unseal-shares", "--shares-file", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one server call, got %d", len(got))
	}
	want := []string{"share-a", "share-b"}
	if len(got[0]) != len(want) || got[0][0] != want[0] || got[0][1] != want[1] {
		t.Errorf("share contents mismatch: got %v want %v", got[0], want)
	}
}

// TestBarrierUnsealSharesCmd_BothInputsRejected verifies that --share
// and --shares-file are mutually exclusive.
func TestBarrierUnsealSharesCmd_BothInputsRejected(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "unseal-shares",
		"--share", "s1", "--shares-file", "/dev/null")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
}

// TestBarrierUnsealSharesCmd_NoInput verifies that at least one input
// form must be supplied.
func TestBarrierUnsealSharesCmd_NoInput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "unseal-shares")
	var missing *MissingArgError
	if !errors.As(err, &missing) {
		t.Fatalf("expected *MissingArgError, got %T: %v", err, err)
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

func TestBarrierRekeyCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "rekey",
		"--threshold", "2", "--total", "3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Threshold") {
		t.Errorf("expected Threshold in output: %q", out)
	}
}

// TestBarrierRekeyCmd_DefaultMN verifies rekey works without Shamir
// flags (the server reuses the configured M/N).
func TestBarrierRekeyCmd_DefaultMN(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "rekey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Threshold") {
		t.Errorf("expected Threshold in output: %q", out)
	}
}

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

// TestBarrierRekeyCmd_UnpairedShamirFlags verifies --threshold without
// --total is rejected.
func TestBarrierRekeyCmd_UnpairedShamirFlags(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "rekey", "--threshold", "3")
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *InvalidArgError, got %T: %v", err, err)
	}
}

// TestBarrierRekeyCmd_JSONOutput verifies --output json is honored.
func TestBarrierRekeyCmd_JSONOutput(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "--output", "json", "barrier", "rekey",
		"--threshold", "2", "--total", "3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "shares") {
		t.Errorf("expected shares in JSON output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Canonical surface: barrier recovery generate / recover
// ---------------------------------------------------------------------------

func TestBarrierRecoveryGenerateCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "recovery", "generate",
		"--threshold", "2", "--total", "3")
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
		"--threshold", "2", "--total", "3")
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
		"--threshold", "2", "--total", "3", "--so-pin", "SO123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBarrierRecoveryRecoverCmd_RepeatedShare verifies the repeatable
// --share form.
func TestBarrierRecoveryRecoverCmd_RepeatedShare(t *testing.T) {
	got := make([][]string, 0, 1)
	svc := &mockBarrierService{
		recoverFn: func(_ context.Context, keys []string) error {
			got = append(got, append([]string(nil), keys...))
			return nil
		},
	}
	root := newBarrierTestRoot(t, svc)
	out, err := executeCommand(root, "barrier", "recovery", "recover",
		"--share", "key-a", "--share", "key-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output: %q", out)
	}
	if len(got) != 1 || len(got[0]) != 2 || got[0][0] != "key-a" || got[0][1] != "key-b" {
		t.Errorf("unexpected key payload: %v", got)
	}
}

func TestBarrierRecoveryRecoverCmd_MissingShare(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "recovery", "recover")
	if err == nil {
		t.Fatal("expected error for missing --share")
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

func TestBarrierRootTokenCmd_Success(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	out, err := executeCommand(root, "barrier", "root-token",
		"--share", "s1", "--share", "s2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "root-token-abc") {
		t.Errorf("expected root token in output: %q", out)
	}
}

func TestBarrierRootTokenCmd_MissingShare(t *testing.T) {
	root := newBarrierTestRoot(t, &mockBarrierService{})
	_, err := executeCommand(root, "barrier", "root-token")
	if err == nil {
		t.Fatal("expected error for missing --share")
	}
}

// ---------------------------------------------------------------------------
// Canonical verb-tree wiring test
// ---------------------------------------------------------------------------

// canonicalBarrierVerbs enumerates the seven canonical verbs (plus the
// two `recovery` sub-verbs) that every CLI binary in the stack MUST
// expose. Tests pin both the path and the required flag set per verb.
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
		flags: []string{"passphrase", "threshold", "total", "so-pin", "user-pin"},
	},
	{
		path:  []string{"barrier", "unseal"},
		flags: []string{"passphrase"},
	},
	{
		path:  []string{"barrier", "unseal-shares"},
		flags: []string{"share", "shares-file"},
	},
	{
		path:  []string{"barrier", "seal"},
		flags: []string{"so-pin", "assume-yes"},
	},
	{
		path:  []string{"barrier", "rekey"},
		flags: []string{"so-pin", "threshold", "total"},
	},
	{
		path:  []string{"barrier", "recovery", "generate"},
		flags: []string{"threshold", "total", "so-pin"},
	},
	{
		path:  []string{"barrier", "recovery", "recover"},
		flags: []string{"share"},
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
