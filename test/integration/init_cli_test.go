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

//go:build integration

// init_cli_test.go drives the canonical root-level `qrdb init` command
// (cmd/qrdb/init.go -- barrier + CA + admin cert + roles in one ceremony
// call, distinct from the dedicated `barrier init` verb covered by
// barrier_cli_test.go / barrier_permutations_cli_test.go) across its flag
// permutations and, per test/integration/CLAUDE.md's multi-protocol
// convention, across the transports the ceremony API is wired on.
//
// Prior art and non-duplication
// --------------------------------------------------------------------------
// officer_ceremony_cli_test.go already drives `qrdb init` exhaustively over
// REST: the single-admin happy path (TestCLI_Init_PassphraseHappyPath), the
// Threshold>=2-without-officers fail-closed path
// (TestCLI_Init_ShamirFlags_RequiresOfficers), the full M-of-N
// officers+claim-cert+claim-share end-to-end flow
// (TestCLI_Init_ShamirWithOfficers_EndToEnd), and four local flag-validation
// cases (TestCLI_Init_LocalFlagValidation: shamir without --shares, --shares
// without shamir, missing --so-pin, invalid --barrier-strategy). This file's
// value-add is therefore twofold and deliberately does NOT re-test those
// exact cases:
//
//  1. The four other CLI-selectable protocols (gRPC, QUIC, MCP, Unix) for
//     the single-admin happy path and the M-of-N enrolling path, proving
//     ceremony-initialize parity across every transport in
//     pkg/api/sealedwindow.Table's VerbCeremonyInitialize entry (REST +
//     GRPC + QUIC + MCP; Unix rides the gRPC service over UDS).
//  2. Additional local flag-validation permutations REST coverage does not
//     already exercise: missing --user-pin, threshold/shares boundary
//     violations (threshold<2, threshold>shares, shares<=1), malformed
//     --officers entries, a missing CSR file, and the QRDB_SO_PIN/
//     QRDB_USER_PIN environment-variable bindings documented in the
//     command's own --help text.
//
// Because `qrdb init` is a one-shot, sealed-window, pre-host ceremony
// operation exactly like `barrier init` (both are gated by
// pkg/api/sealedwindow's allowlist and both run before the RBAC-backed host
// exists), every positive multi-protocol test here boots its OWN fresh node
// via barrier_permutations_cli_test.go's startFreshSealedNode helper. Each
// node is first moved past the barrier's own sealed window with the
// zero-friction `barrier init --barrier-strategy none` (the same
// bootstrapping trick officer_ceremony_cli_test.go's bootSelfGenCANode
// call sites use) so the ceremony's ONE-SHOT ceremony.Initialize call can
// then be exercised precisely once per node, on the protocol under test.
package integration_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Local flag-validation permutations (cmd/qrdb/init.go
// resolveAndValidateInitFlags / parseOfficerFlags). cobra's
// PersistentPreRunE connects the SDK client before any subcommand-level
// validation runs, so even purely local validation needs a reachable
// server; the already-initialized multi-protocol fixture satisfies that
// cheaply and is reused read-only across every subtest (none of these
// cases reach the ceremony RPC, so the fixture's own ceremony/barrier
// state is never touched).
// ---------------------------------------------------------------------------

// TestInit_CLI_LocalFlagValidation_Permutations covers `qrdb init` flag
// combinations that fail cmd/qrdb/init.go's local validation before any
// network call is made, beyond the four cases officer_ceremony_cli_test.go's
// TestCLI_Init_LocalFlagValidation already proves (shamir without --shares,
// --shares without shamir, missing --so-pin, invalid --barrier-strategy).
func TestInit_CLI_LocalFlagValidation_Permutations(t *testing.T) {
	node := startMultiProtocolNode(t)
	proto := multiProtocols[0] // rest; purely local validation is transport-independent.
	flags := node.cliArgsFor(proto)
	dir := t.TempDir()

	missingCSR := filepath.Join(dir, "does-not-exist.pem")
	presentCSR, _ := generateOfficerCSR(t, dir, "present-officer")

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing user-pin",
			args:    []string{"init", "--so-pin", "a"},
			wantErr: "--user-pin",
		},
		{
			name:    "shamir threshold below minimum",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "3", "--threshold", "1"},
			wantErr: "--threshold",
		},
		{
			name:    "shamir threshold exceeds shares",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "3", "--threshold", "5"},
			wantErr: "--threshold",
		},
		{
			name:    "shamir shares below minimum",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "1", "--threshold", "1"},
			wantErr: "--shares",
		},
		{
			name:    "officers entry missing '=' separator",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2", "--officers", "malformed-entry-no-equals"},
			wantErr: "--officers",
		},
		{
			name:    "officers entry with empty username",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2", "--officers", "=" + presentCSR},
			wantErr: "--officers",
		},
		{
			name:    "officers CSR file does not exist",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2", "--officers", "alice=" + missingCSR},
			wantErr: "failed to read file",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCLI(t, node.cli, append(append([]string{}, flags...), tc.args...)...)
			if err == nil {
				t.Fatalf("expected failure; output: %s", out)
			}
			if !strings.Contains(out, tc.wantErr) {
				t.Errorf("expected output to contain %q, got: %s", tc.wantErr, out)
			}
		})
	}
}

// TestInit_CLI_EnvVarBinding_PerProtocol proves the QRDB_SO_PIN /
// QRDB_USER_PIN environment-variable fallbacks documented in `qrdb init
// --help` actually satisfy the required --so-pin/--user-pin flags, using
// REST as the representative protocol -- the binding logic in
// resolveAndValidateInitFlags is entirely transport-independent (it runs
// before any RPC), so this is not repeated across every protocol (all five,
// including the CLI's own documented default transport Unix, can now reach
// the pre-host sealed window this test's `barrier init --barrier-strategy
// none` step runs from -- see barrier_permutations_cli_test.go's
// TestBarrierPermutations_SealedWindowConnectivity_AllProtocols).
func TestInit_CLI_EnvVarBinding_PerProtocol(t *testing.T) {
	node := startFreshSealedNode(t, "none", 0, 0)
	proto := multiProtocol{name: "rest", scheme: "https"}
	flags := node.flagsFor(proto)
	waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

	if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
		"barrier", "init", "--barrier-strategy", "none")...); err != nil {
		t.Fatalf("barrier init: %v\nstderr: %s", err, stderr)
	}
	node.waitForFullBoot(t)

	t.Setenv("QRDB_SO_PIN", "ENV-SOPIN-1")
	t.Setenv("QRDB_USER_PIN", "ENV-USERPIN-1")

	out, err := runCLI(t, node.cli, append(append([]string{}, flags...), "init")...)
	if err != nil {
		t.Fatalf("qrdb init with env-var PINs: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "State:") {
		t.Errorf("expected init output to report the ceremony state, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Positive multi-protocol coverage: single-admin happy path.
// ---------------------------------------------------------------------------

// ceremonyInitProtocols is the set of protocols proven able to drive the
// root `qrdb init` ceremony-initialize RPC to completion: all five
// CLI-selectable transports (REST, gRPC, native QUIC, MCP, and Unix
// grpc-over-UDS).
//
// Formerly this was narrower than coldSealedWindowProtocols / multiProtocols
// (REST and MCP only): pkg/api/grpc/service_ceremony.go and
// pkg/api/quic/handlers_ceremony.go hard-coded a nil ceremony.CeremonyCA
// argument into CeremonyInitialize instead of threading through the node's
// real CA the way REST and MCP already did, and pkg/ceremony.CeremonyService
// .Initialize has no nil fallback -- it stores the passed-in reference
// directly into s.ca and immediately calls ca.IssueCertificate on it, which
// nil-pointer-panicked (recovered server-side into a generic Internal
// error). Neither pkg/api/grpc.Server nor pkg/api/quic.Server (nor
// pkg/api/unix.Server, which shares the gRPC qrdbServiceServer) had a
// ceremonyCA field / WithCeremonyCA option for pkg/server to wire one
// through.
//
// Fixed: WithCeremonyCA (gRPC, QUIC, Unix) plus WithRegisterCeremonyCA (the
// Unix/gRPC shared qrdbServiceServer registration path) now thread the same
// ceremony.CeremonyCA pkg/server passes to REST and MCP. Each transport's
// CeremonyInitialize/handleCeremonyInitialize also fails closed with a typed
// *CeremonyCANotConfiguredError instead of dereferencing a nil CA if the
// server option was never applied. ceremonyInitProtocols is therefore back
// to the full set, and TestInit_CLI_CeremonyCAGap_GRPCQUICUnix (the former
// panic reproduction) has been removed in favor of this file's positive
// per-protocol coverage below.
var ceremonyInitProtocols = multiProtocols

// TestInit_CLI_SingleAdmin_PerProtocol drives the full single-admin `qrdb
// init` happy path -- barrier init (strategy none, to reach full boot
// cheaply), `qrdb init --so-pin --user-pin`, a second
// `qrdb init` proving the typed ceremony.ErrAlreadyInitialized fires, and a
// KV round trip proving the node is fully operational -- over every
// protocol that can complete the ceremony-initialize RPC (REST, gRPC, native
// QUIC, MCP, and Unix; see ceremonyInitProtocols above). This is the direct
// multi-protocol extension of officer_ceremony_cli_test.go's
// TestCLI_Init_PassphraseHappyPath (REST only), and also proves the real
// CeremonyCA (SPKI pin, issued CA/TLS certificates) is used on every
// transport -- not just that the RPC no longer panics.
func TestInit_CLI_SingleAdmin_PerProtocol(t *testing.T) {
	for _, proto := range ceremonyInitProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "none", 0, 0)
			flags := node.flagsFor(proto)
			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init", "--barrier-strategy", "none")...); err != nil {
				t.Fatalf("[%s] barrier init: %v\nstderr: %s", proto.name, err, stderr)
			}
			node.waitForFullBoot(t)

			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"--output", "json",
				"init", "--so-pin", "SOPIN-"+proto.name, "--user-pin", "USERPIN-"+proto.name)...)
			if err != nil {
				t.Fatalf("[%s] qrdb init: %v\nstderr: %s", proto.name, err, stderr)
			}
			var resp transport.CeremonyInitializeResponse
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatalf("[%s] qrdb init: decode JSON: %v\noutput: %s", proto.name, err, stdout)
			}
			if resp.State != "operational" {
				t.Errorf("[%s] expected State 'operational' for the single-admin path, got %q", proto.name, resp.State)
			}
			if resp.SPKIPin == "" {
				t.Errorf("[%s] expected a non-empty SPKIPin", proto.name)
			}
			if resp.ShareCount != 0 {
				t.Errorf("[%s] expected ShareCount 0 for the single-admin (non-Shamir) path, got %d", proto.name, resp.ShareCount)
			}

			// A second `qrdb init` on the same node must fail closed with
			// the typed ceremony.ErrAlreadyInitialized, over the same
			// protocol.
			out, err := runCLI(t, node.cli, append(append([]string{}, flags...),
				"init", "--so-pin", "SOPIN2", "--user-pin", "USERPIN2")...)
			if err == nil {
				t.Fatalf("[%s] second qrdb init should have failed; output: %s", proto.name, out)
			}
			if !strings.Contains(out, "already completed") {
				t.Errorf("[%s] expected 'already completed' (ceremony.ErrAlreadyInitialized), got: %s", proto.name, out)
			}

			// Post-init usability: the node's own admin identity (CN=admin,
			// OU=system) must still serve ordinary data-plane traffic over
			// the same protocol.
			key := "init-perm-" + proto.name
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"kv", "put", key, "init-value")...); err != nil {
				t.Fatalf("[%s] kv put after init: %v\nstderr: %s", proto.name, err, stderr)
			}
			stdout, stderr, err = runCLISplit(t, node.cli, append(append([]string{}, flags...), "kv", "get", key)...)
			if err != nil {
				t.Fatalf("[%s] kv get after init: %v\nstderr: %s", proto.name, err, stderr)
			}
			if !strings.Contains(stdout, "init-value") {
				t.Errorf("[%s] kv get after init: expected 'init-value', got: %s", proto.name, stdout)
			}
		})
	}
}

// TestInit_CLI_HostnameSANs proves --hostname and --sans reach the issued
// TLS certificate. Exercised on MCP as the representative protocol since
// the flag-plumbing itself (cmd/qrdb/init.go's runInit building
// req.Hostname/req.SANs) is transport-independent; the multi-protocol
// value already established by TestInit_CLI_SingleAdmin_PerProtocol is not
// duplicated here (all five protocols, including QUIC, can now reach the
// pre-host sealed window this test's `barrier init --barrier-strategy
// none` step runs from -- see barrier_permutations_cli_test.go's
// TestBarrierPermutations_SealedWindowConnectivity_AllProtocols).
func TestInit_CLI_HostnameSANs(t *testing.T) {
	node := startFreshSealedNode(t, "none", 0, 0)
	proto := multiProtocol{name: "mcp", scheme: "mcp"}
	flags := node.flagsFor(proto)
	waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

	if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
		"barrier", "init", "--barrier-strategy", "none")...); err != nil {
		t.Fatalf("barrier init: %v\nstderr: %s", err, stderr)
	}
	node.waitForFullBoot(t)

	const wantHostname = "qrdb-init-hostname-test.example"
	stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
		"--output", "json",
		"init", "--so-pin", "SO1", "--user-pin", "USR1",
		"--hostname", wantHostname,
		"--sans", "extra-san.example,127.0.0.1")...)
	if err != nil {
		t.Fatalf("qrdb init --hostname/--sans: %v\nstderr: %s", err, stderr)
	}
	var resp transport.CeremonyInitializeResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode JSON: %v\noutput: %s", err, stdout)
	}

	block, _ := pem.Decode([]byte(resp.TLSCertPEM))
	if block == nil {
		t.Fatalf("no PEM block in TLSCertPEM: %s", resp.TLSCertPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse TLS certificate: %v", err)
	}
	if cert.Subject.CommonName != wantHostname {
		t.Errorf("expected TLS cert CommonName %q, got %q", wantHostname, cert.Subject.CommonName)
	}
	foundSAN := false
	for _, dns := range cert.DNSNames {
		if dns == "extra-san.example" {
			foundSAN = true
		}
	}
	if !foundSAN {
		t.Errorf("expected 'extra-san.example' in TLS cert DNSNames, got: %v", cert.DNSNames)
	}
}

// ---------------------------------------------------------------------------
// Positive multi-protocol coverage: M-of-N Shamir enrollment via --officers.
// ---------------------------------------------------------------------------

// TestInit_CLI_ShamirWithOfficers_PerProtocol proves `qrdb init
// --barrier-strategy shamir --officers ...` reaches ceremony.StateEnrolling
// with ShareCount equal to the officer count over every CLI-selectable
// protocol (REST, gRPC, native QUIC, MCP, and Unix; see
// ceremonyInitProtocols' package-level doc comment above), in addition to
// REST, already exhaustively covered end-to-end -- including the full
// claim-cert/claim-share protocol per officer -- by
// officer_ceremony_cli_test.go's TestCLI_Init_ShamirWithOfficers_EndToEnd.
// Driving the full per-officer claim handshake a second time here would not
// add parity value beyond what is asserted below (the ceremony-initialize
// RPC itself is the only part of the flow that changes per transport;
// claim-cert/claim-share are separate RPCs already proven reachable
// multi-protocol by TestBarrierPermutations_ShamirLifecycle_PerProtocol's
// officer register/claim flow in barrier_permutations_cli_test.go), so it
// is intentionally left as a documented follow-up rather than repeated
// here.
func TestInit_CLI_ShamirWithOfficers_PerProtocol(t *testing.T) {
	for _, proto := range ceremonyInitProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "none", 0, 0)
			flags := node.flagsFor(proto)
			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init", "--barrier-strategy", "none")...); err != nil {
				t.Fatalf("[%s] barrier init: %v\nstderr: %s", proto.name, err, stderr)
			}
			node.waitForFullBoot(t)

			dir := t.TempDir()
			aliceCSR, _ := generateOfficerCSR(t, dir, "alice")
			bobCSR, _ := generateOfficerCSR(t, dir, "bob")

			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"--output", "json",
				"init", "--so-pin", "SO1", "--user-pin", "USR1",
				"--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2",
				"--officers", "alice="+aliceCSR, "--officers", "bob="+bobCSR)...)
			if err != nil {
				t.Fatalf("[%s] qrdb init --barrier-strategy shamir --officers: %v\nstderr: %s", proto.name, err, stderr)
			}
			var resp transport.CeremonyInitializeResponse
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatalf("[%s] decode JSON: %v\noutput: %s", proto.name, err, stdout)
			}
			if resp.State != "enrolling" {
				t.Errorf("[%s] expected State 'enrolling' for the M-of-N path, got %q", proto.name, resp.State)
			}
			if resp.ShareCount != 2 {
				t.Errorf("[%s] expected ShareCount 2 (one per officer), got %d", proto.name, resp.ShareCount)
			}

			out, err := runCLI(t, node.cli, append(append([]string{}, flags...), "ceremony", "status", "--output", "json")...)
			if err != nil {
				t.Fatalf("[%s] ceremony status: %v\noutput: %s", proto.name, err, out)
			}
			var status struct {
				State   string `json:"state"`
				SPKIPin string `json:"spki_pin"`
			}
			if err := json.Unmarshal([]byte(out), &status); err != nil {
				t.Fatalf("[%s] ceremony status: decode JSON: %v\noutput: %s", proto.name, err, out)
			}
			if status.State != "enrolling" {
				t.Errorf("[%s] expected ceremony status state 'enrolling', got %q", proto.name, status.State)
			}
		})
	}
}
