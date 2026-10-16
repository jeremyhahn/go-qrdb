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
// holders + envelope-claim + quorum end-to-end flow
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
	"fmt"
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
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2", "--officers", "malformed-entry-no-equals", "--admin-generate"},
			wantErr: "--officers",
		},
		{
			name:    "officers entry with empty username",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2", "--officers", "=" + presentCSR, "--admin-generate"},
			wantErr: "--officers",
		},
		{
			name:    "officers CSR file does not exist",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2", "--officers", "alice=" + missingCSR, "--admin-generate"},
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
// before any RPC), so this is not repeated across every protocol. `qrdb
// init` is the only system bootstrap, so the call under test is the one that
// brings the fresh node up, over the sealed-window listener.
func TestInit_CLI_EnvVarBinding_PerProtocol(t *testing.T) {
	node := startFreshSealedNode(t, "none", 0, 0)
	proto := multiProtocol{name: "rest", scheme: "https"}
	noAuth := node.noAuthFlagsFor(proto)
	waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

	t.Setenv("QRDB_SO_PIN", "ENV-SOPIN-1")
	t.Setenv("QRDB_USER_PIN", "ENV-USERPIN-1")

	out, err := runCLI(t, node.cli, append(append([]string{}, noAuth...),
		"init", "--barrier-strategy", "none", "--admin-cert", node.adminCert)...)
	if err != nil {
		t.Fatalf("qrdb init with env-var PINs: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "State:") {
		t.Errorf("expected init output to report the ceremony state, got: %s", out)
	}

	// The bootstrap took effect: the admin certificate init bound
	// authenticates once the full server is up.
	node.waitForFullBoot(t)
	if out, err := runCLI(t, node.cli, append(node.flagsFor(proto), "barrier", "status")...); err != nil {
		t.Fatalf("barrier status as the bound admin: %v\noutput: %s", err, out)
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
// init` happy path -- `qrdb init --so-pin --user-pin` bootstrapping a fresh
// node (strategy none, binding the operator's own certificate), a second
// `qrdb init` proving a retry is refused, and a KV round trip proving the
// node is fully operational -- over every
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
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, noAuth...),
				"--output", "json",
				"init", "--so-pin", "SOPIN-"+proto.name, "--user-pin", "USERPIN-"+proto.name,
				"--barrier-strategy", "none", "--admin-cert", node.adminCert)...)
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

			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)

			// A second `qrdb init` on the same node must fail closed over
			// the same protocol: the admin already holds a live binding.
			// The SO PIN must be the SAME one the first call provisioned:
			// the reissue path now verifies the SO PIN before anything
			// else, including whether the admin is already bound, so a
			// wrong PIN here would be refused for THAT reason instead of
			// the one this test means to exercise.
			out, err := runCLI(t, node.cli, append(append([]string{}, flags...),
				"init", "--so-pin", "SOPIN-"+proto.name, "--user-pin", "USERPIN-"+proto.name, "--barrier-strategy", "none",
				"--admin-generate")...)
			if err == nil {
				t.Fatalf("[%s] second qrdb init should have failed; output: %s", proto.name, out)
			}
			if !strings.Contains(out, "already holds a certificate binding") {
				t.Errorf("[%s] expected the admin-already-bound refusal, got: %s", proto.name, out)
			}

			// Post-init usability: the admin identity init bound must serve
			// ordinary data-plane traffic over the same protocol.
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
// duplicated here. `qrdb init` is the only system bootstrap, so the call
// under test brings the fresh node up over the sealed-window listener.
func TestInit_CLI_HostnameSANs(t *testing.T) {
	node := startFreshSealedNode(t, "none", 0, 0)
	proto := multiProtocol{name: "mcp", scheme: "mcp"}
	noAuth := node.noAuthFlagsFor(proto)
	waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

	const wantHostname = "qrdb-init-hostname-test.example"
	stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, noAuth...),
		"--output", "json",
		"init", "--so-pin", "SOPIN1", "--user-pin", "USRPIN1",
		"--barrier-strategy", "none", "--admin-cert", node.adminCert,
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
// REST, already exhaustively covered end-to-end -- including each holder's
// envelope claim and a quorum -- by officer_ceremony_cli_test.go's
// TestCLI_Init_ShamirWithOfficers_EndToEnd. The claim and quorum are separate
// RPCs already proven reachable multi-protocol by
// TestBarrierPermutations_ShamirLifecycle_PerProtocol, so only the
// ceremony-initialize RPC is looped here. The node boots with a Shamir
// barrier left uninitialized: `qrdb init` runs against the sealed window and
// the ceremony initializes the barrier over the named holders.
func TestInit_CLI_ShamirWithOfficers_PerProtocol(t *testing.T) {
	for _, proto := range ceremonyInitProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "shamir", 2, 2)
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			dir := t.TempDir()
			aliceCSR, _ := generateOfficerCSR(t, dir, "alice")
			bobCSR, _ := generateOfficerCSR(t, dir, "bob")

			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, noAuth...),
				"--output", "json",
				"init", "--so-pin", "SOPIN1", "--user-pin", "USRPIN1",
				"--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2",
				"--admin-cert", node.adminCert,
				"--officers", "alice="+aliceCSR, "--custodians", "bob="+bobCSR)...)
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
			if resp.ShareCount != 2 || len(resp.Holders) != 2 {
				t.Errorf("[%s] expected ShareCount 2 and 2 holder certificates, got %d and %d", proto.name, resp.ShareCount, len(resp.Holders))
			}
			// Initializing the barrier unseals it and brings up the full server.
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)

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

// ---------------------------------------------------------------------------
// Follower refusal (coordinator decision, go-qrdb#548): a second qrdb init
// against an already-bootstrapped cluster is refused on a follower, naming
// the leader, before it issues or signs anything; on the leader it is
// refused too, but for the unrelated reason that the admin already holds a
// live binding (CeremonyServiceWithAdminRebind's AdminAlreadyBoundError).
// ---------------------------------------------------------------------------

// adminConnArgs returns the qrdb CLI connection flags that authenticate as
// the cluster's bound bootstrap admin against the node at port, over REST.
func adminConnArgs(t *testing.T, ca *testCA, port int) []string {
	t.Helper()
	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	return []string{
		"--server", fmt.Sprintf("https://127.0.0.1:%d", port),
		"--transport", "rest",
		"--ca-file", ca.CertPath,
		"--cert-file", certPath,
		"--key-file", keyPath,
		"--timeout", "20s",
	}
}

// TestCLI_Init_RetryOnAnyNodeIsAnsweredByLeader proves, end to end on a real
// 3-node cluster, that a second `qrdb init` lands the same way whichever node
// receives it: a follower forwards it to the leader in one hop (every node
// serves every endpoint), and the leader refuses because the admin already
// holds a live binding (*service.AdminAlreadyBoundError). No node refuses
// locally with "must run on the Raft leader".
func TestCLI_Init_RetryOnAnyNodeIsAnsweredByLeader(t *testing.T) {
	cli := buildCLIBinary(t)
	// SPIFFE node certificates are what let a follower forward the request
	// on behalf of the original caller; without them the leader would
	// authorize it as the forwarding node, which holds no account.
	adminPorts, _, ca := startThreeNodeClusterSPIFFE(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 30*time.Second)
	for _, port := range adminPorts {
		if port != leaderPort {
			waitForSettledFollowerView(t, ca, port, 30*time.Second)
		}
	}

	for _, port := range adminPorts {
		// The SO PIN must match the one the cluster was initialized with
		// (harnessSOPIN/harnessUserPIN, via initAndUnsealBarrier): the
		// reissue path verifies the SO PIN before anything else, including
		// whether the admin is already bound.
		out, err := runQRDB(t, cli, adminConnArgs(t, ca, port),
			"init", "--so-pin", harnessSOPIN, "--user-pin", harnessUserPIN, "--barrier-strategy", "none",
			"--admin-generate")
		if err == nil {
			t.Fatalf("retry qrdb init on port %d should have been refused; output: %s", port, out)
		}
		if strings.Contains(out, "must run on the Raft leader") {
			t.Errorf("port %d refused locally instead of forwarding to the leader: %s", port, out)
			continue
		}
		if !strings.Contains(out, "already holds a certificate binding") {
			t.Errorf("port %d: want the leader's already-bound refusal, got: %s", port, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Production boot proof (coordinator decision, go-qrdb#548): each of the
// three admin-credential paths -- bring-your-own-certificate on a BYO-TLS
// node, CSR-sign and generate on a qrdb-managed-CA node -- ends in a real
// admin login under the default auth.mtls_binding_policy "required". BYOC
// is the only path a BYO-TLS node supports; CSR-sign and generate need a
// CeremonyCA to sign with, which only a qrdb-managed-CA node has.
// ---------------------------------------------------------------------------

// TestCLI_Init_ProductionBootProof_BYOC_OnBYOTLS is the BYOC half: the
// node's own TLS comes from an externally-generated CA/leaf (ca.tlsFlags,
// startSingleNode) -- the daemon never holds that CA's private key -- so
// qrdb init brings the system barrier up and pins the operator's own
// server leaf (no TLS certificate is self-issued), and the SAME CA that
// issues the node's TLS material issues the admin's certificate too
// (initAndUnsealBarrier's admin_cert_pem). The bound admin then logs in
// over the real CLI.
func TestCLI_Init_ProductionBootProof_BYOC_OnBYOTLS(t *testing.T) {
	cli := buildCLIBinary(t)
	adminPort, ca := startSingleNode(t, "pebble")

	out, err := runQRDB(t, cli, adminConnArgs(t, ca, adminPort), "--output", "json", "admin", "status")
	if err != nil {
		t.Fatalf("admin login (BYOC, BYO-TLS): %v\noutput: %s", err, out)
	}
	var status struct {
		NodeID string `json:"node_id"`
	}
	if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
		t.Fatalf("decode status JSON: %v\noutput: %s", jerr, out)
	}
	if status.NodeID == "" {
		t.Errorf("expected a non-empty node_id, got: %s", out)
	}
}

// TestCLI_Init_ProductionBootProof_CSRAndGenerate_OnManagedCA covers the
// other two paths together, both on a qrdb-managed-CA node (no --tls-*
// flags: the daemon self-generates its own CA, retaining the private
// key -- bootSelfGenCANode): CSR-sign (the admin's key never leaves this
// test, only the CSR does) and generate (the server mints the key pair,
// dev/test/evaluation only). Each ends in a real admin login over the CLI.
func TestCLI_Init_ProductionBootProof_CSRAndGenerate_OnManagedCA(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	t.Run("csr-sign", func(t *testing.T) {
		node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[boot-proof-csr] ")
		pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

		dir := t.TempDir()
		keyPath := filepath.Join(dir, "admin-key.pem")
		csrPath := generateAdminCSR(t, dir, keyPath)

		bootAdminIdentity(t, cli, node.caPath, node.adminPort, "SOPIN12345", "USERPIN12345",
			node.adminCertPath, node.adminKeyPath, csrPath, "--barrier-strategy", "none")
		// bootAdminIdentity leaves keyPath untouched for the CSR path (the
		// server never sees it); node.adminKeyPath is never written for
		// this path either, so the key generated above is this identity's
		// real key.
		node.adminKeyPath = keyPath

		waitForHealthy(t, node.dataDir, 60*time.Second)
		pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

		out, err := runQRDB(t, cli, node.restArgsAdmin(), "--output", "json", "admin", "status")
		if err != nil {
			t.Fatalf("admin login (CSR-sign, managed CA): %v\noutput: %s", err, out)
		}
		var status struct {
			NodeID string `json:"node_id"`
		}
		if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
			t.Fatalf("decode status JSON: %v\noutput: %s", jerr, out)
		}
		if status.NodeID == "" {
			t.Errorf("expected a non-empty node_id, got: %s", out)
		}
	})

	t.Run("generate", func(t *testing.T) {
		node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[boot-proof-generate] ")
		pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

		node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", "--barrier-strategy", "none")

		waitForHealthy(t, node.dataDir, 60*time.Second)
		pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

		out, err := runQRDB(t, cli, node.restArgsAdmin(), "--output", "json", "admin", "status")
		if err != nil {
			t.Fatalf("admin login (generate, managed CA): %v\noutput: %s", err, out)
		}
		var status struct {
			NodeID string `json:"node_id"`
		}
		if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
			t.Fatalf("decode status JSON: %v\noutput: %s", jerr, out)
		}
		if status.NodeID == "" {
			t.Errorf("expected a non-empty node_id, got: %s", out)
		}
	})
}
