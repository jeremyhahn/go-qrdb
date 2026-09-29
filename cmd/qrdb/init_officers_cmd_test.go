//go:build !integration

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

// init_officers_cmd_test.go verifies `qrdb init --officers`
// (parseOfficerFlags and its wiring into runInit / CeremonyInitializeRequest).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// capturingCeremonyService implements service.CeremonyServicer, overriding
// only CeremonyInitialize to capture the *service.CeremonyInitializeOptions
// the CLI actually sends -- notably Officers, which mockCeremonyService
// (ceremony_cli_helpers_test.go) discards. Every other method is inherited
// from the embedded nil interface.
type capturingCeremonyService struct {
	service.CeremonyServicer
	gotOpts *service.CeremonyInitializeOptions
	err     error
}

func (m *capturingCeremonyService) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.gotOpts = opts
	if m.err != nil {
		return nil, m.err
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "abc123",
		CACertPEM:  []byte("---CA---"),
		TLSCertPEM: []byte("---TLS---"),
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

// newTestRootCmdInitOfficers mirrors newTestRootCmdInit (init_coverage_test.go)
// but wires a capturingCeremonyService so --officers can be asserted on.
func newTestRootCmdInitOfficers(svc *capturingCeremonyService) *cobra.Command {
	rootCmd := newRootCmd()

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		format, _ := cmd.Root().PersistentFlags().GetString("output")
		if err := validateOutputFormat(format); err != nil {
			return err
		}
		outputFormat = format

		tr, err := embedded.New(
			newMockKVService(),
			newMockAdminService(),
			embedded.WithCeremonyService(svc),
			embedded.WithCeremonyCA(&mockCeremonyCA{}),
		)
		if err != nil {
			return err
		}
		c, err := sdk.NewClient(tr)
		if err != nil {
			return err
		}
		if err := c.Connect(cmd.Context()); err != nil {
			return err
		}
		client = c
		return nil
	}

	return rootCmd
}

// ---------------------------------------------------------------------------
// parseOfficerFlags -- direct unit tests
// ---------------------------------------------------------------------------

func TestParseOfficerFlags_Empty(t *testing.T) {
	got, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil officers for empty input, got %v", got)
	}
}

func TestParseOfficerFlags_ValidEntries(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR-PEM"))
	got, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, []string{"alice=" + csrPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Username != "alice" || got[0].CSRPEM != "ALICE-CSR-PEM" || got[0].Role != transport.RoleSecurityOfficer {
		t.Fatalf("unexpected officers: %+v", got)
	}
}

func TestParseOfficerFlags_MultipleEntries(t *testing.T) {
	alicePath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR"))
	bobPath := writeOfficerTempFile(t, "bob.csr", []byte("BOB-CSR"))
	got, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, []string{"alice=" + alicePath, "bob=" + bobPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Username != "alice" || got[1].Username != "bob" {
		t.Fatalf("unexpected officers: %+v", got)
	}
}

func TestParseOfficerFlags_MalformedEntry(t *testing.T) {
	_, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, []string{"no-equals-sign"})
	if err == nil {
		t.Fatal("expected an error for a malformed --officers entry")
	}
	var invalid *InvalidArgError
	if !errors.As(err, &invalid) {
		t.Errorf("expected *InvalidArgError, got: %v (%T)", err, err)
	}
}

func TestParseOfficerFlags_EmptyUsername(t *testing.T) {
	csrPath := writeOfficerTempFile(t, "x.csr", []byte("X"))
	_, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, []string{"=" + csrPath})
	if err == nil {
		t.Fatal("expected an error for an empty username")
	}
}

func TestParseOfficerFlags_MissingCSRFile(t *testing.T) {
	_, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, []string{"alice=" + filepath.Join(t.TempDir(), "missing.csr")})
	if err == nil {
		t.Fatal("expected an error for a missing CSR file")
	}
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Errorf("expected *FileReadError, got: %v (%T)", err, err)
	}
}

// ---------------------------------------------------------------------------
// qrdb init --officers -- end-to-end (in-process) success case
// ---------------------------------------------------------------------------

// TestInitCmd_ShamirWithOfficers_Success proves the admin enrols Security
// Officers and key custodians the same way: --officers and --custodians CSRs
// are read from disk and forwarded, each with its role, together with the
// factor policy, through CeremonyInitializeRequest -> CeremonyInitializeOptions.
func TestInitCmd_ShamirWithOfficers_Success(t *testing.T) {
	alicePath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR-PEM"))
	bobPath := writeOfficerTempFile(t, "bob.csr", []byte("BOB-CSR-PEM"))

	svc := &capturingCeremonyService{}
	root := newTestRootCmdInitOfficers(svc)

	out, err := executeCommand(root, "init",
		"--so-pin", "sopin", "--user-pin", "userpin",
		"--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2",
		"--officers", "alice="+alicePath,
		"--custodians", "bob="+bobPath,
		"--policy", "any-enrolled",
	)
	if err != nil {
		t.Fatalf("init --officers --custodians: %v\noutput: %s", err, out)
	}

	if svc.gotOpts == nil {
		t.Fatal("CeremonyInitialize was not called")
	}
	if len(svc.gotOpts.Officers) != 2 {
		t.Fatalf("expected 2 officers forwarded, got %d: %+v", len(svc.gotOpts.Officers), svc.gotOpts.Officers)
	}
	if o := svc.gotOpts.Officers[0]; o.Username != "alice" || o.CSRPEM != "ALICE-CSR-PEM" || o.Role != seal.RoleSecurityOfficer {
		t.Errorf("holder[0] mismatch: %+v", o)
	}
	if o := svc.gotOpts.Officers[1]; o.Username != "bob" || o.CSRPEM != "BOB-CSR-PEM" || o.Role != seal.RoleCustodian {
		t.Errorf("holder[1] mismatch: %+v", o)
	}
	if svc.gotOpts.Policy != seal.FactorPolicyAnyEnrolled {
		t.Errorf("policy = %q, want any-enrolled", svc.gotOpts.Policy)
	}
	if svc.gotOpts.Threshold != 2 {
		t.Errorf("expected threshold 2 forwarded, got %d", svc.gotOpts.Threshold)
	}
}

// TestInitCmd_SharesMustMatchHolders proves --shares is N: the key is split
// to exactly the named holders, so a --shares that disagrees with the number
// of --officers plus --custodians is refused before any call, rather than the
// split silently following the holder count.
func TestInitCmd_SharesMustMatchHolders(t *testing.T) {
	alicePath := writeOfficerTempFile(t, "alice.csr", []byte("ALICE-CSR-PEM"))
	bobPath := writeOfficerTempFile(t, "bob.csr", []byte("BOB-CSR-PEM"))
	for name, shares := range map[string]string{"more shares than holders": "3", "fewer shares than holders": "2"} {
		t.Run(name, func(t *testing.T) {
			svc := &capturingCeremonyService{}
			root := newTestRootCmdInitOfficers(svc)
			args := []string{"init", "--so-pin", "sopin", "--user-pin", "userpin",
				"--barrier-strategy", "shamir", "--shares", shares, "--threshold", "2",
				"--officers", "alice=" + alicePath, "--custodians", "bob=" + bobPath}
			if shares == "2" {
				args = append(args, "--custodians", "carol="+bobPath)
			}
			_, err := executeCommand(root, args...)
			var invalid *InvalidArgError
			if !errors.As(err, &invalid) || invalid.Arg != "--shares" {
				t.Fatalf("err = %v, want an InvalidArgError on --shares", err)
			}
			if svc.gotOpts != nil {
				t.Fatal("CeremonyInitialize was called with a holder count that disagrees with --shares")
			}
		})
	}
}

// TestInitCmd_OfficersFlagMalformed_LocalError proves a malformed
// --officers entry fails before any network call, via the same typed
// InvalidArgError parseOfficerFlags returns directly.
func TestInitCmd_OfficersFlagMalformed_LocalError(t *testing.T) {
	svc := &capturingCeremonyService{}
	root := newTestRootCmdInitOfficers(svc)

	_, err := executeCommand(root, "init",
		"--so-pin", "sopin", "--user-pin", "userpin",
		"--officers", "not-a-valid-entry",
	)
	if err == nil {
		t.Fatal("expected an error for a malformed --officers entry")
	}
	if svc.gotOpts != nil {
		t.Error("CeremonyInitialize should not have been called for a local validation failure")
	}
}

// TestInitCmd_NoOfficers_ServiceRejects proves that omitting --officers
// still reaches the ceremony (Officers is nil, not silently populated),
// letting the server's own M-of-N validation apply -- this is the
// existing fail-closed contract TestCLI_Init_ShamirFlags_RequiresOfficers
// pins at the integration layer.
func TestInitCmd_NoOfficers_ServiceRejects(t *testing.T) {
	svc := &capturingCeremonyService{err: errors.New("ceremony: M-of-N requires at least one officer")}
	root := newTestRootCmdInitOfficers(svc)

	_, err := executeCommand(root, "init",
		"--so-pin", "sopin", "--user-pin", "userpin",
		"--barrier-strategy", "shamir", "--shares", "3", "--threshold", "2",
	)
	if err == nil {
		t.Fatal("expected the service's ErrNoOfficers-equivalent error to propagate")
	}
	if svc.gotOpts == nil {
		t.Fatal("CeremonyInitialize should have been called")
	}
	if len(svc.gotOpts.Officers) != 0 {
		t.Errorf("expected no officers forwarded, got %+v", svc.gotOpts.Officers)
	}
}

// writeOfficerTempFile writes content to name under a fresh temp dir and
// returns its path.
func writeOfficerTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
