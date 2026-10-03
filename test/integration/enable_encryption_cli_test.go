// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This file drives `qrdb barrier enable-encryption` through the real client
// CLI binary (cmd/qrdb), as a subprocess talking REST-over-TLS to a live
// qrdbd node. It complements the API-level coverage in
// enable_encryption_test.go and cluster_enable_encryption_test.go (which post
// directly to the REST admin API): here the CLI is the thing under test.
//
// REST, not gRPC, is used as the CLI's transport: the SDK's gRPC
// Transport.Connect performs a gRPC Health RPC to verify connectivity
// (sdk/go/transport/grpc/transport.go), which the sealed-window listener does
// not implement, so a gRPC-backed CLI can never reach `barrier unseal` while
// sealed. The REST Transport.Connect is explicitly sealed-aware -- it treats
// both 200 (healthy) and 503 (sealed) as reachable (sdk/go/transport/rest/transport.go)
// -- matching how every proven barrier/seal flow in this test suite (and the
// production CLI's own documented sealed-window unseal path) already talks to
// the server. The admin port these tests connect to is the same REST server
// enable_encryption_test.go posts directly to.
//
// Test setup mirrors enable_encryption_test.go's writeNoneSealConfig /
// writeNoneSealVolumeConfig servers and the mtlsHTTPClient(t, ca, ...)
// convention. That helper mints a CN=admin, OU=system client cert on every
// call, which maps onto the "admin" user + Owner role first-boot always
// creates on the system tenant (see cmd/qrdbd/start.go bootstrapAuthEnforcement
// + runFirstBoot) -- so ca.boundAdminCert(t, dataDir), which mints
// the identical CN=admin/OU=system identity as on-disk PEM files, is the
// correct credential for the CLI's --cert-file/--key-file. The daemon's own
// TLS listener identity (--tls-cert-file/--tls-key-file) is a SEPARATE,
// unrelated node certificate (ca.generateNodeCert), never used as a client
// credential.

// restConnArgs returns the root-level qrdb CLI flags that select the REST
// transport (over TLS, mTLS-authenticated as the given cert/key pair) against
// the node's admin/REST port.
func restConnArgs(ca *testCA, certPath, keyPath string, adminPort int) []string {
	return []string{
		"--server", fmt.Sprintf("https://127.0.0.1:%d", adminPort),
		"--ca-file", ca.CertPath,
		"--cert-file", certPath,
		"--key-file", keyPath,
		"--timeout", "20s",
	}
}

// runQRDB runs the qrdb client CLI with connArgs (the connection/TLS flags)
// followed by subArgs (the verb and its flags), without mutating or aliasing
// connArgs's backing array across calls.
func runQRDB(t *testing.T, cli string, connArgs []string, subArgs ...string) (string, error) {
	t.Helper()
	args := append(append([]string{}, connArgs...), subArgs...)
	return runCLI(t, cli, args...)
}

// cliBarrierStatusJSON is the subset of transport.BarrierStatusResponse this
// file asserts on, decoded from `qrdb --output json barrier status`.
type cliBarrierStatusJSON struct {
	Sealed          bool   `json:"sealed"`
	Strategy        string `json:"strategy"`
	ShamirThreshold int    `json:"shamir_threshold"`
	ShamirTotal     int    `json:"shamir_total"`
}

// cliBarrierStatus runs `qrdb barrier status` with JSON output and decodes
// the result. It fails the test on a non-zero exit or undecodable output.
func cliBarrierStatus(t *testing.T, cli string, connArgs []string) cliBarrierStatusJSON {
	t.Helper()
	out, err := runQRDB(t, cli, connArgs, "--output", "json", "barrier", "status")
	if err != nil {
		t.Fatalf("qrdb barrier status: %v\noutput: %s", err, out)
	}
	var status cliBarrierStatusJSON
	if jerr := json.Unmarshal([]byte(out), &status); jerr != nil {
		t.Fatalf("decode `qrdb barrier status` JSON: %v\noutput: %s", jerr, out)
	}
	return status
}

// cliBarrierEncNode holds the connection details and reusable start
// arguments for a single-node, none-strategy daemon driven by these CLI
// tests. restart() lets a test SIGTERM and relaunch the same node (same
// data dir, ports, and node certificate) to prove a keyed barrier survives a
// real process restart, not just the in-process recycle.
type cliBarrierEncNode struct {
	ca            *testCA
	dataDir       string
	adminPort     int
	grpcPort      int
	startArgs     []string
	adminCertPath string
	adminKeyPath  string
	connArgs      []string
	proc          *exec.Cmd
}

// startCLIEncNode boots a fresh single-node daemon with an unencrypted
// (strategy "none") system barrier, waits for it to reach full serving, and
// returns a handle with the REST connection flags a `qrdb` subprocess needs
// to reach it as the bootstrapped system admin identity.
func startCLIEncNode(t *testing.T, bin string, prefix string) *cliBarrierEncNode {
	t.Helper()

	ca := generateTestCA(t)
	nodeCertPath, nodeKeyPath := ca.generateNodeCert(t)
	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir := t.TempDir()
	cfgPath := writeNoneSealConfig(t)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCertPath,
		"--tls-key-file", nodeKeyPath,
	}

	n := &cliBarrierEncNode{
		ca:        ca,
		dataDir:   dataDir,
		adminPort: adminPort,
		grpcPort:  grpcPort,
		startArgs: args,
	}
	n.proc = startNode(t, bin, args, prefix)

	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	n.adminCertPath, n.adminKeyPath = ca.boundAdminCert(t, dataDir)
	n.connArgs = restConnArgs(ca, n.adminCertPath, n.adminKeyPath, adminPort)
	return n
}

// restart SIGTERMs the node's current process, waits for it to exit, and
// relaunches it with the exact same start arguments (data dir, ports, node
// certificate). It does not wait for the barrier to become reachable or
// unsealed -- callers do that, since a keyed barrier comes back up sealed.
func (n *cliBarrierEncNode) restart(t *testing.T, bin, prefix string) {
	t.Helper()

	if n.proc != nil && n.proc.Process != nil {
		if err := n.proc.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("restart: signal previous process: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- n.proc.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = n.proc.Process.Kill()
			t.Fatalf("restart: timed out waiting for previous process to exit")
		}
	}
	n.proc = startNode(t, bin, n.startArgs, prefix)
}

// ---------------------------------------------------------------------------
// System scope: full CLI lifecycle, restart survival, negatives.
// ---------------------------------------------------------------------------

// TestCLI_BarrierEnableEncryption_SystemScope_Lifecycle drives the entire
// none -> passphrase system-scope migration through the `qrdb` client CLI
// subprocess: seed data with `kv put`, confirm the unencrypted starting state
// with `barrier status`, run `barrier enable-encryption`, unseal the
// recycled barrier with `barrier unseal`, and verify the seeded data survives
// -- including across a full OS-process restart (not just the in-process
// recycle). It then exercises two negative paths: a redundant
// enable-encryption call against an already-keyed barrier, and a missing
// `-y` confirmation.
func TestCLI_BarrierEnableEncryption_SystemScope_Lifecycle(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startCLIEncNode(t, bin, "[cli-enc-sys] ")

	// Phase 1: seed data through the CLI before the barrier is encrypted.
	seed := map[string]string{"alpha": "one", "bravo": "two", "charlie": "three"}
	for k, v := range seed {
		if out, err := runQRDB(t, cli, node.connArgs, "kv", "put", k, v); err != nil {
			t.Fatalf("kv put %s: %v\noutput: %s", k, err, out)
		} else if !strings.Contains(out, "OK") {
			t.Fatalf("kv put %s: expected OK, got: %s", k, out)
		}
	}
	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("kv get %s = %q, want %q", k, strings.TrimSpace(out), v)
		}
	}

	// Confirm the pre-migration barrier is genuinely unencrypted, via the CLI.
	if status := cliBarrierStatus(t, cli, node.connArgs); status.Strategy != "none" || status.Sealed {
		t.Fatalf("pre-migration `barrier status` = %+v, want strategy=none sealed=false", status)
	}

	// Phase 2: enable encryption via the CLI, exercising the explicit
	// (default-valued) --mechanism/--availability flags plus -y.
	const passphrase = "cli-enable-encryption-system-pass"
	out, err := runQRDB(t, cli, node.connArgs,
		"barrier", "enable-encryption",
		"--strategy", "passphrase",
		"--mechanism", "swap",
		"--availability", "quiesced",
		"--passphrase", passphrase,
		"-y",
	)
	if err != nil {
		t.Fatalf("barrier enable-encryption: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "migration_started") || !strings.Contains(out, "scope: system") {
		t.Fatalf("barrier enable-encryption: unexpected output: %s", out)
	}

	// The migration recycles the data plane in process; the barrier comes
	// back up keyed-and-SEALED (the daemon does not hold the new
	// credential). Wait for that transition via the proven REST poller, then
	// unseal it -- through the CLI.
	waitForBarrierSealedAgain(t, node.ca, node.adminPort, 120*time.Second)
	waitForBarrierStatusReachable(t, node.ca, node.adminPort, 90*time.Second)

	if out, err := runQRDB(t, cli, node.connArgs, "barrier", "unseal", "--passphrase", passphrase); err != nil {
		t.Fatalf("barrier unseal: %v\noutput: %s", err, out)
	} else if !strings.Contains(out, "OK") {
		t.Fatalf("barrier unseal: expected OK, got: %s", out)
	}

	waitForHealthy(t, node.dataDir, 90*time.Second)
	waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)

	if status := cliBarrierStatus(t, cli, node.connArgs); status.Strategy != "passphrase" || status.Sealed {
		t.Fatalf("post-unseal `barrier status` = %+v, want strategy=passphrase sealed=false", status)
	}

	// Every datum written before the migration must survive re-encryption.
	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil {
			t.Fatalf("post-migration kv get %s: %v\noutput: %s", k, err, out)
		}
		if strings.TrimSpace(out) != v {
			t.Fatalf("post-migration kv get %s = %q, want %q (DATA LOSS)", k, strings.TrimSpace(out), v)
		}
	}
	// New writes work under the keyed barrier.
	if out, err := runQRDB(t, cli, node.connArgs, "kv", "put", "echo", "five"); err != nil {
		t.Fatalf("post-migration kv put echo: %v\noutput: %s", err, out)
	}
	if out, err := runQRDB(t, cli, node.connArgs, "kv", "get", "echo"); err != nil || strings.TrimSpace(out) != "five" {
		t.Fatalf("post-migration kv get echo = %q (err=%v), want five", out, err)
	}

	// Phase 3: full OS-process restart (SIGTERM + relaunch), not just the
	// in-process recycle. A keyed barrier does NOT auto-unseal across a cold
	// restart (unlike "none"), so the CLI must unseal it again.
	node.restart(t, bin, "[cli-enc-sys-r2] ")
	waitForBarrierStatusReachable(t, node.ca, node.adminPort, 90*time.Second)

	if out, err := runQRDB(t, cli, node.connArgs, "barrier", "unseal", "--passphrase", passphrase); err != nil {
		t.Fatalf("post-restart barrier unseal: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 90*time.Second)
	waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)

	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil || strings.TrimSpace(out) != v {
			t.Fatalf("post-restart kv get %s = %q (err=%v), want %q (DATA LOSS across restart)", k, out, err, v)
		}
	}

	// Phase 4 (negative): enable-encryption on an ALREADY-KEYED barrier.
	//
	// handleBarrierEnableEncryption (pkg/api/rest/handlers_barrier.go) only
	// validates opts.Validate() (target strategy / mechanism / availability /
	// Shamir M-of-N shape) synchronously; the actual "source must be none"
	// guard (seal.ErrMigrationSourceNotNone) is enforced deep inside the
	// migration, which the handler always runs in a background goroutine
	// AFTER responding 200/"migration_started" (see the doc comment on
	// migrationStartGrace). So a redundant call against an already-keyed
	// barrier is NOT reported as a CLI error -- the typed error is only
	// logged server-side. What this test can and does verify from the CLI is
	// that the rejection is a true no-op: the barrier's strategy/sealed state
	// and the previously-migrated data are byte-for-byte unaffected.
	redundantOut, redundantErr := runQRDB(t, cli, node.connArgs,
		"barrier", "enable-encryption",
		"--strategy", "passphrase",
		"--passphrase", "some-other-passphrase",
		"-y",
	)
	if redundantErr != nil {
		t.Fatalf("redundant barrier enable-encryption: %v\noutput: %s", redundantErr, redundantOut)
	}
	if !strings.Contains(redundantOut, "migration_started") {
		t.Fatalf("redundant barrier enable-encryption: unexpected output: %s", redundantOut)
	}
	// Give the async goroutine time to reach and reject the source-strategy
	// guard before asserting nothing changed.
	time.Sleep(3 * time.Second)
	if status := cliBarrierStatus(t, cli, node.connArgs); status.Strategy != "passphrase" || status.Sealed {
		t.Fatalf("barrier state changed by a rejected redundant migration: %+v", status)
	}
	for k, v := range seed {
		out, err := runQRDB(t, cli, node.connArgs, "kv", "get", k)
		if err != nil || strings.TrimSpace(out) != v {
			t.Fatalf("post-redundant-migration kv get %s = %q (err=%v), want %q (unexpected corruption)", k, out, err, v)
		}
	}

	// Phase 5 (negative): missing -y confirmation. confirmDestructive reads
	// a y/N answer from stdin; runCLI's subprocess has no stdin attached, so
	// the child reads the null device, Scan() returns false immediately, and
	// the command aborts with a typed UserAbortError before any request is
	// sent.
	abortOut, abortErr := runQRDB(t, cli, node.connArgs,
		"barrier", "enable-encryption",
		"--strategy", "passphrase",
		"--passphrase", "unused",
	)
	if abortErr == nil {
		t.Fatalf("barrier enable-encryption without -y: expected non-zero exit, got success: %s", abortOut)
	}
	if !strings.Contains(abortOut, "aborted by user") {
		t.Fatalf("barrier enable-encryption without -y: expected an aborted-by-user message, got: %s", abortOut)
	}
	// The barrier must be untouched by the aborted attempt.
	if status := cliBarrierStatus(t, cli, node.connArgs); status.Strategy != "passphrase" || status.Sealed {
		t.Fatalf("barrier state changed by an aborted (-y missing) migration: %+v", status)
	}
}

// ---------------------------------------------------------------------------
// System scope: --mechanism / --availability permutations.
// ---------------------------------------------------------------------------

// TestCLI_BarrierEnableEncryption_SystemScope_MechanismAvailabilityMatrix
// exercises every --mechanism x --availability permutation the CLI accepts
// (seal.MigrationMechanism / seal.MigrationAvailability both currently
// support exactly two values each) against a fresh node per case, verifying
// each permutation completes and preserves data. pkg/database/migrate_node.go
// dispatches on Mechanism (ReencryptPebbleStore for swap, ReencryptStoreFiles
// for in-place) -- both are real, distinct code paths, not a stub. The
// Availability field, by contrast, does not currently change observable
// system-scope behavior: nodeMigrationProvider.Quiesce
// (sdk/go/node/migrate_encryption.go) unconditionally tears down the data
// plane for a system-scope migration regardless of quiesced/online, so both
// values are accepted and produce the same in-process recycle. Both values
// are still exercised here to prove the CLI plumbs the flag through end to
// end (validated server-side by seal.MigrationOptions.Validate).
func TestCLI_BarrierEnableEncryption_SystemScope_MechanismAvailabilityMatrix(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	cases := []struct {
		mechanism    string
		availability string
	}{
		{mechanism: "swap", availability: "quiesced"},
		{mechanism: "swap", availability: "online"},
		{mechanism: "in-place", availability: "quiesced"},
		{mechanism: "in-place", availability: "online"},
	}

	for _, tc := range cases {
		t.Run(tc.mechanism+"/"+tc.availability, func(t *testing.T) {
			node := startCLIEncNode(t, bin, "[cli-enc-matrix] ")

			if out, err := runQRDB(t, cli, node.connArgs, "kv", "put", "k1", "v1"); err != nil {
				t.Fatalf("kv put: %v\noutput: %s", err, out)
			}

			passphrase := "matrix-" + tc.mechanism + "-" + tc.availability
			out, err := runQRDB(t, cli, node.connArgs,
				"barrier", "enable-encryption",
				"--strategy", "passphrase",
				"--mechanism", tc.mechanism,
				"--availability", tc.availability,
				"--passphrase", passphrase,
				"-y",
			)
			if err != nil {
				t.Fatalf("barrier enable-encryption (%s/%s): %v\noutput: %s", tc.mechanism, tc.availability, err, out)
			}
			if !strings.Contains(out, "migration_started") {
				t.Fatalf("barrier enable-encryption (%s/%s): unexpected output: %s", tc.mechanism, tc.availability, out)
			}

			waitForBarrierSealedAgain(t, node.ca, node.adminPort, 120*time.Second)
			waitForBarrierStatusReachable(t, node.ca, node.adminPort, 90*time.Second)

			if out, err := runQRDB(t, cli, node.connArgs, "barrier", "unseal", "--passphrase", passphrase); err != nil {
				t.Fatalf("barrier unseal (%s/%s): %v\noutput: %s", tc.mechanism, tc.availability, err, out)
			}
			waitForHealthy(t, node.dataDir, 90*time.Second)
			waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)

			status := cliBarrierStatus(t, cli, node.connArgs)
			if status.Strategy != "passphrase" || status.Sealed {
				t.Fatalf("post-migration status (%s/%s) = %+v, want strategy=passphrase sealed=false", tc.mechanism, tc.availability, status)
			}

			out, err = runQRDB(t, cli, node.connArgs, "kv", "get", "k1")
			if err != nil || strings.TrimSpace(out) != "v1" {
				t.Fatalf("post-migration kv get k1 (%s/%s) = %q (err=%v), want v1 (DATA LOSS)", tc.mechanism, tc.availability, out, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// System scope: --strategy shamir target.
// ---------------------------------------------------------------------------

// registerShamirOfficer registers username as a system-barrier Security
// Officer (using the ADMIN identity `connArgs`). The officer's own CSR/key
// are generated locally; only the CSR
// (public) ever leaves this process. Registration requires a daemon that
// retains its own CA private key (a self-generated CA — see
// bootSelfGenCANode), since the ceremony signs the officer's certificate;
// under BYO --tls-ca-file/--tls-cert-file/--tls-key-file (startCLIEncNode's
// node identity) the daemon has no CA key and registration fails closed with
// ceremony.ErrCANotInitialized (verified: this is the same precondition
// officer_ceremony_cli_test.go documents and exercises).
func registerShamirOfficer(t *testing.T, cli string, connArgs []string, username string) {
	t.Helper()
	registerShamirOfficerWithIdentity(t, cli, connArgs, username)
}

// registerShamirOfficerWithIdentity is registerShamirOfficer plus the
// officer's own cert/key file paths, for callers that also need to drive the
// officer's own mTLS-authenticated share-claim handshake.
func registerShamirOfficerWithIdentity(t *testing.T, cli string, connArgs []string, username string) (certPath, keyPath string) {
	t.Helper()
	csrPEM, key := generateCeremonyCSR(t, username)
	csrPath := writeTempFile(t, username+".csr", csrPEM)
	out, err := runQRDB(t, cli, connArgs, "barrier", "officer", "register",
		"--username", username, "--csr-file", csrPath, "--output", "json")
	if err != nil {
		t.Fatalf("barrier officer register %s: %v\noutput: %s", username, err, out)
	}
	var reg officerRegisterJSON
	decodeJSON(t, out, &reg)
	certPath = writeTempFile(t, username+"-cert.pem", []byte(reg.CertPEM))
	keyDER, kerr := x509.MarshalECPrivateKey(key)
	if kerr != nil {
		t.Fatalf("marshal officer key: %v", kerr)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyPath = writeTempFile(t, username+"-key.pem", keyPEM)
	return certPath, keyPath
}

// waitForCLIBarrierSealed polls `qrdb --output json barrier status` (through
// the CLI subprocess, tolerating the connection gap while the in-process
// migration recycle tears the server down and brings it back up) until it
// reports sealed==true. It is waitForBarrierSealedAgain's CLI-only analogue
// for nodes booted without a *testCA handle (e.g. bootSelfGenCANode, whose
// daemon-generated CA this package does not wrap in a *testCA).
func waitForCLIBarrierSealed(t *testing.T, cli string, connArgs []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := runQRDB(t, cli, connArgs, "--output", "json", "barrier", "status")
		if err == nil {
			var status cliBarrierStatusJSON
			if jerr := json.Unmarshal([]byte(out), &status); jerr == nil && status.Sealed {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for the migrated barrier to come back sealed", timeout)
}

// TestCLI_BarrierEnableEncryption_SystemScope_ShamirTarget_Completes proves
// the fix for the confirmed product bug the (now-removed)
// ...ShamirTargetNeverCompletes test pinned: pkg/clustermig's replicated
// migration-intent record now carries ShamirThreshold/ShamirTotal end to end
// (pkg/clustermig/store.go ClusterMigration fields, threaded through
// Coordinator.Request and the daemon's LocalRecycler), so
// seal.MigrationOptions.Validate() no longer rejects the reconciler-driven
// recycle on every retry. With enough Security Officers pre-registered (the
// same operational precondition a fresh `barrier init-shamir` already has —
// see pkg/seal.MintKeyedShamirSibling), the migration now runs to completion:
// the CLI call is accepted, the node recycles its data plane in process (the
// same in-process reboot a passphrase target already goes through), and the
// node comes back up SEALED with the barrier CORRECTLY self-describing as a
// genuine threshold-of-total Shamir barrier (not silently downgraded to
// passphrase — pkg/seal.AdoptMigratedRootKey now adopts the Shamir wrapped-
// root-key blob and registers the M-of-N scheme, not just the passphrase-
// shaped envelope the migration mechanism originally only supported).
//
// The holders then claim the envelopes the migration distributed, from the
// SEALED post-migration node, and two of them unseal it through the quorum:
// the in-process restart carries the ceremony service across, so the
// envelopes survive it. Every datum written before the migration is intact.
func TestCLI_BarrierEnableEncryption_SystemScope_ShamirTarget_Completes(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	// A self-generated-CA node (see registerShamirOfficer's doc comment): the
	// daemon retains its CA private key, so it can sign officer CSRs.
	// shamir_threshold/shamir_total are pre-provisioned in the static config
	// (valid even while strategy: none — pkg/config only enforces the M-of-N
	// shape when strategy=="shamir"): cmd/qrdbd/seal_subsystem.go's
	// PersistedStrategyFrom restart-survival fallback reads them from this
	// SAME static config on the post-migration reboot to register a real
	// seal.ShamirStrategy for the now-migrated barrier, exactly the
	// precondition a deployment planning to enable Shamir later must
	// provision up front (mirrors a fresh Shamir init's own requirement).
	node := bootSelfGenCANode(t, bin,
		"seal:\n  enabled: true\n  strategy: none\n  shamir_threshold: 2\n  shamir_total: 3\n",
		"[cli-enc-shamir] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)
	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	// Pre-register 3 Security Officers (== --shamir-total) — the same
	// operational precondition a fresh `barrier init-shamir` already
	// enforces (pkg/ceremony's sealSharesToOfficers requires the enrolled
	// count to exactly satisfy the request).
	type migHolder struct{ name, cert, key, envelope string }
	var holders []*migHolder
	for _, name := range []string{"off1", "off2", "off3"} {
		cert, key := registerShamirOfficerWithIdentity(t, cli, node.restArgsAdmin(), name)
		holders = append(holders, &migHolder{name: name, cert: cert, key: key})
	}

	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "kv", "put", "shamir-key", "shamir-value"); err != nil {
		t.Fatalf("kv put: %v\noutput: %s", err, out)
	}

	out, err := runQRDB(t, cli, node.restArgsAdmin(),
		"barrier", "enable-encryption",
		"--strategy", "shamir",
		"--shamir-threshold", "2",
		"--shamir-total", "3",
		"-y",
	)
	if err != nil {
		t.Fatalf("barrier enable-encryption --strategy shamir: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "migration_started") {
		t.Fatalf("barrier enable-encryption --strategy shamir: unexpected output: %s", out)
	}

	// The migration now genuinely runs to completion: it recycles the data
	// plane in process (same reboot mechanics as a passphrase target) and the
	// node comes back up SEALED as a keyed barrier. Before the field-drop fix
	// this deadline was NEVER reached — the reconciler retried
	// ErrShamirThresholdInvalid forever without ever tearing down the data
	// plane.
	waitForCLIBarrierSealed(t, cli, node.restArgsNoAuth(), 120*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 90*time.Second)

	// The barrier self-describes correctly WHILE SEALED (StorageBarrier.Status
	// reads the persisted strategy from the sealed root-key blob without
	// requiring an unseal first): it must report the REAL shamir strategy and
	// M/N, not a silently mislabeled passphrase barrier.
	status := cliBarrierStatus(t, cli, node.restArgsNoAuth())
	if status.Strategy != "shamir" || !status.Sealed {
		t.Fatalf("post-migration `barrier status` = %+v, want strategy=shamir sealed=true", status)
	}
	if status.ShamirThreshold != 2 || status.ShamirTotal != 3 {
		t.Fatalf("post-migration shamir scheme = %d/%d, want 2/3", status.ShamirThreshold, status.ShamirTotal)
	}

	// The holders claim their envelopes from the sealed node, over their own
	// certificates.
	for _, h := range holders {
		h.envelope = filepath.Join(t.TempDir(), h.name+".env")
		if out, err := runQRDB(t, cli, node.restArgsIdentity(h.cert, h.key),
			"barrier", "officer", "claim", "--username", h.name, "--key", h.key, "--out", h.envelope); err != nil {
			t.Fatalf("claim for %s after the migration: %v\noutput: %s", h.name, err, out)
		}
	}

	// Two of them unseal it through the quorum.
	for _, h := range holders[:2] {
		if out, err := runQRDB(t, cli, node.restArgsIdentity(h.cert, h.key),
			"barrier", "quorum", "submit", "--op", "unseal", "--username", h.name,
			"--envelope", h.envelope, "--key", h.key); err != nil {
			t.Fatalf("unseal submission for %s: %v\noutput: %s", h.name, err, out)
		}
	}
	waitForHealthy(t, node.dataDir, 90*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 90*time.Second)
	status = cliBarrierStatus(t, cli, node.restArgsAdmin())
	if status.Strategy != "shamir" || status.Sealed {
		t.Fatalf("after the unseal quorum: %+v, want strategy=shamir sealed=false", status)
	}
	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "kv", "get", "shamir-key"); err != nil || strings.TrimSpace(out) != "shamir-value" {
		t.Fatalf("kv get shamir-key after the migration = %q (err=%v), want shamir-value (DATA LOSS)", out, err)
	}
}

// TestCLI_BarrierEnableEncryption_SystemScope_Shamir_ThresholdGreaterThanTotal
// proves an out-of-range Shamir M-of-N is rejected with a typed error AT
// REQUEST TIME, synchronously, before the CLI call even returns — not
// silently accepted and left to fail invisibly on a background retry loop
// (the exact operator-visibility gap the original bug report described).
// The barrier must be provably unaffected: still "none", still serving.
func TestCLI_BarrierEnableEncryption_SystemScope_Shamir_ThresholdGreaterThanTotal(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := startCLIEncNode(t, bin, "[cli-enc-shamir-bad] ")

	if out, err := runQRDB(t, cli, node.connArgs, "kv", "put", "k", "v"); err != nil {
		t.Fatalf("kv put: %v\noutput: %s", err, out)
	}

	out, err := runQRDB(t, cli, node.connArgs,
		"barrier", "enable-encryption",
		"--strategy", "shamir",
		"--shamir-threshold", "4",
		"--shamir-total", "3",
		"-y",
	)
	if err == nil {
		t.Fatalf("barrier enable-encryption (threshold=4 > total=3) should be rejected synchronously; output: %s", out)
	}

	// The barrier must be immediately, provably unaffected: the rejection
	// happens before any intent is ever written, let alone any data-plane
	// teardown.
	status := cliBarrierStatus(t, cli, node.connArgs)
	if status.Strategy != "none" || status.Sealed {
		t.Fatalf("barrier state changed by a rejected shamir request: %+v", status)
	}
	if out, err := runQRDB(t, cli, node.connArgs, "kv", "get", "k"); err != nil || strings.TrimSpace(out) != "v" {
		t.Fatalf("kv get k = %q (err=%v), want v (node should still be fully serving)", out, err)
	}
}

// ---------------------------------------------------------------------------
// Tenant scope.
// ---------------------------------------------------------------------------

// TestCLI_BarrierEnableEncryption_TenantScope mirrors the per-tenant flow in
// TestEnableEncryption_TenantScope_OnlineRecycleNoServerRestart (tenant
// creation, role/user provisioning, and the tenant's self-managed dedicated
// barrier are all driven over the REST admin API exactly as that proven test
// does), but the operation under test -- POST /v1/barrier/enable-encryption
// scoped by --tenant -- is driven through the `qrdb` CLI subprocess. The CLI's
// `tenant barrier-init` verb does not currently expose --strategy/--passphrase
// (only --threshold/--shares), so it cannot drive a "none" dedicated-barrier
// setup; REST remains the correct tool for that precondition. The CLI's `kv`
// commands also have no --database/tenant-scoping flag (no counterpart to the
// SDK's transport.WithDatabase / X-QRDB-Database), so tenant data survival is
// checked with the existing dbScoped REST helpers, matching the proven test.
// After the CLI-driven migration, a CLI `barrier status` (system scope, no
// --tenant) additionally confirms the system barrier stayed untouched.
func TestCLI_BarrierEnableEncryption_TenantScope(t *testing.T) {
	const tenantID = "acme-cli"
	const dbName = "appdata"

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	nodeCert, nodeKey := ca.generateNodeCert(t)

	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]
	dataDir := t.TempDir()
	volumePoolDir := t.TempDir()
	cfgPath := writeNoneSealVolumeConfig(t, volumePoolDir)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "pebble",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", nodeCert,
		"--tls-key-file", nodeKey,
	}
	startNode(t, bin, args, "[cli-enc-tenant] ")

	waitForBarrierStatusReachable(t, ca, adminPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, adminPort, "")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPort, 90*time.Second)

	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	// Create an IsolationVolume tenant with a SELF-MANAGED dedicated barrier.
	tBody, tStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", adminPort),
		map[string]interface{}{
			"id":                  tenantID,
			"name":                "Acme CLI",
			"isolation_type":      "volume",
			"barrier_auto_unseal": false,
		},
	)
	if tStatus != http.StatusCreated && tStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", tStatus, tBody)
	}

	// The tenant's dedicated barrier must be initialized BEFORE any of its
	// IAM can exist (the tenant's roles and users are encrypted under the
	// tenant DEK; the store resolver fails closed until the barrier is up).
	// The init itself is trust-on-first-init by the tenant's own identity —
	// see pkg/api/rest authorizeTenantBarrierInit.
	tenantCert, tenantKey := ca.generateClientCertForTenant(t, tenantID)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCert, tenantKey, 15*time.Second)

	// Initialize the self-managed dedicated barrier as "none" (unencrypted) --
	// the source state for enable-encryption.
	if initBody, initStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/init", adminPort, tenantID),
		map[string]interface{}{"strategy": "none", "secret": []byte(nil)},
	); initStatus != http.StatusOK && initStatus != http.StatusNoContent && initStatus != http.StatusCreated {
		t.Fatalf("tenant barrier init: status %d, body: %s", initStatus, initBody)
	}

	// Role + user so the tenant cert can create a database and do KV I/O.
	// Created AFTER the barrier init so the records land in the tenant's
	// own encrypted IAM store.
	roleBody, roleStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", adminPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": tenantID,
			"permissions": []string{
				"databases:create", "databases:create_volume", "databases:read", "databases:list",
				"kv:put", "kv:get",
				"tenant_barrier:status", "tenant_barrier:init",
			},
		},
	)
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("create role: status %d, body: %s", roleStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
		t.Fatalf("decode role id: %v body=%s", err, roleBody)
	}
	if _, uStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/users", adminPort),
		map[string]interface{}{"username": "admin", "email": "admin@" + tenantID + ".example.com", "tenant_id": tenantID, "role_ids": []string{roleResp.ID}},
	); uStatus != http.StatusCreated && uStatus != http.StatusOK {
		t.Fatalf("create user: status %d", uStatus)
	}

	if strategy, _, ok := tenantBarrierStatusJSON(t, tenantClient, adminPort, tenantID); !ok || strategy != "none" {
		t.Fatalf("pre-migration tenant barrier strategy = %q (ok=%v), want none", strategy, ok)
	}

	dbBody, dbStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", adminPort),
		map[string]interface{}{"name": dbName, "engine": "kv", "backend": "pebble", "replicas": 1},
	)
	if dbStatus != http.StatusCreated && dbStatus != http.StatusOK {
		t.Fatalf("create tenant database: status %d, body: %s", dbStatus, dbBody)
	}

	seed := map[string]string{"tk1": "tv1", "tk2": "tv2"}
	for k, v := range seed {
		deadline := time.Now().Add(30 * time.Second)
		var putStatus int
		var putErr error
		for time.Now().Before(deadline) {
			putStatus, putErr = dbScopedPutTolerant(t, tenantClient, adminPort, dbName, k, []byte(v))
			if putErr == nil && (putStatus == http.StatusOK || putStatus == http.StatusNoContent || putStatus == http.StatusCreated) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if putErr != nil || (putStatus != http.StatusOK && putStatus != http.StatusNoContent && putStatus != http.StatusCreated) {
			t.Fatalf("tenant put %q: status %d err %v", k, putStatus, putErr)
		}
	}

	// The operation under test: `qrdb barrier enable-encryption --tenant`,
	// driven as a CLI subprocess authenticated as the SYSTEM admin identity
	// (matching the REST test: a tenant migration is admin-gated at the
	// permission layer, not self-service by the tenant's own cert).
	adminCertPath, adminKeyPath := ca.boundAdminCert(t, dataDir)
	connArgs := restConnArgs(ca, adminCertPath, adminKeyPath, adminPort)

	const tenantPass = "cli-tenant-acme-passphrase"
	out, err := runQRDB(t, cli, connArgs,
		"barrier", "enable-encryption",
		"--tenant", tenantID,
		"--strategy", "passphrase",
		"--passphrase", tenantPass,
		"-y",
	)
	if err != nil {
		t.Fatalf("barrier enable-encryption --tenant: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "migration_started") || !strings.Contains(out, "scope: tenant") {
		t.Fatalf("barrier enable-encryption --tenant: unexpected output: %s", out)
	}

	// The tenant barrier transitions to passphrase and reopens UNSEALED (the
	// recycle unseals it with the supplied credential); the system barrier
	// and daemon keep serving -- no server restart.
	deadline := time.Now().Add(120 * time.Second)
	migrated := false
	for time.Now().Before(deadline) {
		if strategy, sealed, ok := tenantBarrierStatusJSON(t, tenantClient, adminPort, tenantID); ok && strategy == "passphrase" && !sealed {
			migrated = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !migrated {
		t.Fatalf("tenant barrier did not migrate to an unsealed passphrase barrier within timeout")
	}

	// The system barrier is untouched -- verified through the CLI.
	if status := cliBarrierStatus(t, cli, connArgs); status.Strategy != "none" || status.Sealed {
		t.Fatalf("system barrier changed by tenant migration: %+v", status)
	}

	// Every tenant datum survives the per-tenant recycle.
	for k, v := range seed {
		var data []byte
		var st int
		rd := time.Now().Add(60 * time.Second)
		for time.Now().Before(rd) {
			data, st = dbScopedGetTolerant(t, tenantClient, adminPort, dbName, k)
			if st == http.StatusOK {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if st != http.StatusOK || string(data) != v {
			t.Fatalf("post-migration tenant %q = %q (status %d), want %q (DATA LOSS)", k, string(data), st, v)
		}
	}
}
