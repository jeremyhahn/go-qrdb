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

// barrier_cli_test.go drives the `qrdb barrier` lifecycle commands as a real
// CLI subprocess against a freshly self-spawned qrdbd node, covering the
// surface that has NO other integration coverage: init, init-shamir, unseal,
// seal, recovery generate/status/delete, and the share-holder surface that
// backs Shamir M-of-N — enrolment, the one-time envelope claim, and the
// authenticated quorum rounds (unseal, seal, root-token, rekey, recover).
//
// Daemon boot configuration for an uninitialized barrier
// --------------------------------------------------------------------------
// The seal subsystem is entirely YAML-driven (cmd/qrdbd/start.go: "Seal
// config is always inherited from YAML (no CLI flag equivalent)") and is
// only constructed at all when `seal.enabled: true`
// (cmd/qrdbd/start.go: `if f.sealConfig.Enabled { ... }`). Every node in
// this file is therefore started with `--config <tmp>.yaml` carrying only a
// `seal:` section (`enabled: true`, `strategy: passphrase|shamir`, and for
// shamir the boot-time `shamir_threshold`/`shamir_total`, which the request
// parameters override at `init-shamir`/`rekey` time per
// pkg/service/barrier_service.go). Every other daemon flag is supplied on
// the command line exactly as in the sibling table-driven tests.
//
// No --tls-ca-file/--tls-cert-file/--tls-key-file flags are passed. That
// keeps resolveTLS on its single-node "path 3" (cmd/qrdbd/bootstrap.go):
// the daemon self-generates a CA WITH its private key and persists
// ca.pem/ca-key.pem/node-cert.pem/node-key.pem under --data-dir
// (pkg/clusterenroll.Resolve -> clustertls.PersistMaterial). That is a hard
// requirement, not a convenience: cmd/qrdbd/seal_subsystem.go's
// ceremonyCAFromBundle only signs Security Officer CSRs when the TLS bundle
// carries a CA private key ("Returns (nil, nil) when the bundle carries no
// CA private key ... officer registration correctly fails with
// ErrCANotInitialized"), which would make the whole Shamir officer/claim
// flow untestable under the external-CA pattern the other CLI integration
// tests (admin_test.go, drain_test.go) use. Because --auth-enabled defaults
// true and the CA carries its key, cmd/qrdbd/start.go also auto-generates
// {data-dir}/admin-cert.pem + admin-key.pem (CN=admin, OU=system) BEFORE the
// pre-host sealed-window listener blocks on unseal ("that operator needs
// the admin client cert to authenticate ... deferring cert generation until
// after unseal would deadlock that bootstrap"). This file reads those three
// generated files straight off disk instead of minting its own CA/cert
// pair, and reconstructs a helpers_test.go `testCA` value (same package,
// unexported field access) purely so it can reuse waitForAdmin /
// waitForLeaderViaAdmin / httpPut / httpGet unmodified.
//
// Every barrier subcommand is invoked with the daemon's own admin identity
// (Owner role via pkg/firstboot -- permission.All()) EXCEPT the share-holder
// claim and quorum submissions, which are identity-gated: a claim must come
// over a connection presenting the holder's own CA-issued certificate, and a
// quorum submission's mtls factor, when the connection carries a
// certificate, requires it to be the holder's. Those calls therefore present
// the holder's certificate instead of the admin certificate.
//
// Semantics exercised here (see docs/shamir-custody/):
//
//  1. A claim delivers an HPKE envelope sealed to the holder's key, once.
//     `barrier quorum submit` opens it locally with --key and submits the
//     share with the mtls factor response; no raw share ever crosses the CLI
//     boundary.
//  2. `barrier seal` refuses a Shamir barrier
//     (seal.ErrShamirBarrierRequiresQuorum); M holders seal it with
//     `quorum submit --op seal`.
//  3. `barrier rekey` opens a round that M current holders complete with
//     `--op rekey`; the new envelopes must then be claimed again, and every
//     earlier envelope stops working.
//  4. The recovery key is an independent domain (`--domain recovery`) with
//     its own holders; `recovery generate` requires the barrier UNSEALED and
//     `--op recover` requires it SEALED.
package integration_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Node lifecycle
// ---------------------------------------------------------------------------

// sealNode holds the connection details and TLS material for a single
// self-managed-TLS qrdbd node started with an enabled (possibly
// uninitialized) barrier.
type sealNode struct {
	cmd       *exec.Cmd
	dataDir   string
	adminPort int
	serverURL string
	caFile    string
	adminCert string
	adminKey  string
	ca        *testCA
}

// adminFlags returns the qrdb CLI global flags that authenticate as the
// daemon's own bootstrap admin identity (Owner role) over REST.
func (n *sealNode) adminFlags() []string {
	return []string{
		"--server", n.serverURL,
		"--ca-file", n.caFile,
		"--cert-file", n.adminCert,
		"--key-file", n.adminKey,
		"--timeout", "30s",
	}
}

// writeSealConfig writes a minimal YAML config carrying only the `seal:`
// section. Every other daemon setting is supplied via CLI flags by the
// caller; cmd/qrdbd/start.go copies cfg.Seal unconditionally regardless of
// any other flag, so this file is sufficient to turn the seal subsystem on.
func writeSealConfig(t *testing.T, strategy string, threshold, total int) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("seal:\n")
	sb.WriteString("  enabled: true\n")
	sb.WriteString("  strategy: " + strategy + "\n")
	if strategy == "shamir" {
		fmt.Fprintf(&sb, "  shamir_threshold: %d\n", threshold)
		fmt.Fprintf(&sb, "  shamir_total: %d\n", total)
	}
	path := filepath.Join(t.TempDir(), "seal-config.yaml")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("writeSealConfig: %v", err)
	}
	return path
}

// startSealNode starts a single-node qrdbd with the given seal config and
// NO --tls-* flags, letting the daemon self-generate (and persist under
// dataDir) a CA that carries its own private key. It blocks until the
// daemon's admin client certificate and CA material exist on disk -- both
// are written very early in boot (cmd/qrdbd/start.go), strictly before the
// barrier is ever initialized, so this never races the barrier lifecycle.
func startSealNode(t *testing.T, binary, dataDir, configPath string, raftPort, adminPort, grpcPort int) *sealNode {
	t.Helper()
	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--enable-rest",
		"--config", configPath,
	}
	cmd := startNode(t, binary, args, "[barrier] ")

	caPath := filepath.Join(dataDir, "ca.pem")
	adminCertPath := filepath.Join(dataDir, "admin-cert.pem")
	adminKeyPath := filepath.Join(dataDir, "admin-key.pem")
	waitForNonEmptyFile(t, caPath, 30*time.Second)
	waitForNonEmptyFile(t, adminCertPath, 30*time.Second)
	waitForNonEmptyFile(t, adminKeyPath, 30*time.Second)

	return &sealNode{
		cmd:       cmd,
		dataDir:   dataDir,
		adminPort: adminPort,
		serverURL: fmt.Sprintf("https://127.0.0.1:%d", adminPort),
		caFile:    caPath,
		adminCert: adminCertPath,
		adminKey:  adminKeyPath,
		ca:        loadDaemonCA(t, dataDir),
	}
}

// stopSealNode sends SIGTERM and waits (with a hard-kill fallback) for the
// node to exit, so a restart test can bind the same ports immediately
// afterward.
func stopSealNode(t *testing.T, n *sealNode) {
	t.Helper()
	if n.cmd.Process == nil {
		return
	}
	_ = n.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- n.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = n.cmd.Process.Kill()
		<-done
	}
}

// loadDaemonCA reads the CA certificate and private key the daemon
// self-generated into dataDir and reconstructs a helpers_test.go testCA
// value (same package: unexported cert/key fields are accessible here) so
// this file can reuse waitForAdmin / waitForLeaderViaAdmin / httpPut /
// httpGet without modifying helpers_test.go.
func loadDaemonCA(t *testing.T, dataDir string) *testCA {
	t.Helper()
	certPath := filepath.Join(dataDir, "ca.pem")
	keyPath := filepath.Join(dataDir, "ca-key.pem")
	waitForNonEmptyFile(t, keyPath, 30*time.Second)

	ca, err := qrdbdharness.LoadCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("loadDaemonCA: %v", err)
	}
	return &testCA{CA: ca}
}

// waitForNonEmptyFile polls until path exists and is non-empty.
func waitForNonEmptyFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for non-empty file %s", timeout, path)
}

// ---------------------------------------------------------------------------
// CLI invocation helpers
// ---------------------------------------------------------------------------

// runCLISplit runs the qrdb client binary with args and returns stdout and
// stderr separately (unlike helpers_test.go's runCLI, which combines them)
// so JSON emitted on stdout via --output json can be decoded reliably even
// when the process also logs to stderr.
func runCLISplit(t *testing.T, binary string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// waitForBarrierReachable retries `barrier status` until the daemon's
// sealed-window (or full) REST listener accepts the connection. Used once
// per node right after startSealNode, before the barrier has necessarily
// been initialized.
func waitForBarrierReachable(t *testing.T, cli string, flags []string, timeout time.Duration) {
	t.Helper()
	args := append(append([]string{}, flags...), "barrier", "status")
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastStderr string
	for time.Now().Before(deadline) {
		_, stderr, err := runCLISplit(t, cli, args...)
		if err == nil {
			return
		}
		lastErr, lastStderr = err, stderr
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for barrier status to become reachable: %v\nstderr: %s", timeout, lastErr, lastStderr)
}

// barrierStatus fetches `barrier status --output json` and decodes it,
// retrying briefly so a call issued immediately after init/init-shamir
// (which triggers the daemon to tear down its pre-host sealed-window
// listener and stand up the full RBAC-backed server) doesn't flake on the
// brief listener-swap window.
func barrierStatus(t *testing.T, cli string, flags []string) *transport.BarrierStatusResponse {
	t.Helper()
	args := append(append([]string{}, flags...), "--output", "json", "barrier", "status")
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	var lastOut, lastErrOut string
	for time.Now().Before(deadline) {
		stdout, stderr, err := runCLISplit(t, cli, args...)
		if err == nil {
			var resp transport.BarrierStatusResponse
			if decErr := json.Unmarshal([]byte(stdout), &resp); decErr != nil {
				t.Fatalf("barrier status: decode JSON: %v\noutput: %s", decErr, stdout)
			}
			return &resp
		}
		lastErr, lastOut, lastErrOut = err, stdout, stderr
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("barrier status: %v\nstdout:%s\nstderr:%s", lastErr, lastOut, lastErrOut)
	return nil
}

// barrierHasRecoveryKeys fetches `barrier recovery status --output json`.
func barrierHasRecoveryKeys(t *testing.T, cli string, flags []string) bool {
	t.Helper()
	args := append(append([]string{}, flags...), "--output", "json", "barrier", "recovery", "status")
	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		t.Fatalf("barrier recovery status: %v\nstdout:%s\nstderr:%s", err, stdout, stderr)
	}
	var resp transport.BarrierHasRecoveryKeysResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("barrier recovery status: decode JSON: %v\noutput: %s", err, stdout)
	}
	return resp.HasKeys
}

// quorumAll submits each holder's claimed envelope to the op's quorum round,
// in order, and returns the last outcome. `barrier seal` unconditionally
// rejects a Shamir-protected barrier (ErrShamirBarrierRequiresQuorum); the
// quorum is the only way to seal, unseal, mint a root token or complete a
// rekey on one.
func quorumAll(t *testing.T, cli string, ep endpoint, domain, op string, holders ...*officer) *transport.QuorumOutcome {
	t.Helper()
	var out *transport.QuorumOutcome
	for _, h := range holders {
		o, stderr, err := quorumSubmit(t, cli, ep, domain, op, h)
		if err != nil {
			t.Fatalf("quorum submit --op %s %s: %v\nstderr: %s", op, h.username, err, stderr)
		}
		out = o
	}
	return out
}

// quorumSubmit submits one holder's claimed envelope to the op's round over
// the holder's own connection, answering the mtls factor with its key.
func quorumSubmit(t *testing.T, cli string, ep endpoint, domain, op string, h *officer) (*transport.QuorumOutcome, string, error) {
	t.Helper()
	args := append(h.flags(ep), "--output", "json", "barrier", "quorum", "submit",
		"--domain", domain, "--op", op, "--username", h.username,
		"--envelope", h.envelope, "--key", h.keyPath)
	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		return nil, stderr, err
	}
	var out transport.QuorumOutcome
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("quorum submit %s: decode JSON: %v\noutput: %s", h.username, err, stdout)
	}
	return &out, stderr, nil
}

// ---------------------------------------------------------------------------
// Security Officer CSR / claim helpers
// ---------------------------------------------------------------------------

// officer bundles a registered share holder's own key material, the
// CA-issued certificate paths used to authenticate its claim and quorum
// calls, and the path its latest claimed envelope was written to.
type officer struct {
	username string
	key      *ecdsa.PrivateKey
	certPath string
	keyPath  string
	envelope string
}

// endpoint names a daemon listener and the CA that verifies it.
type endpoint struct {
	serverURL string
	caFile    string
}

// endpoint returns the node's REST endpoint.
func (n *sealNode) endpoint() endpoint {
	return endpoint{serverURL: n.serverURL, caFile: n.caFile}
}

// flags returns the CLI global flags that authenticate as the holder
// against ep.
func (o *officer) flags(ep endpoint) []string {
	return []string{
		"--server", ep.serverURL, "--ca-file", ep.caFile,
		"--cert-file", o.certPath, "--key-file", o.keyPath,
		"--timeout", "30s",
	}
}

// generateOfficerCSR creates a fresh ECDSA P-256 key pair and a PEM-encoded
// certificate signing request for cn, writing the CSR to a file under dir
// so it can be passed to `barrier officer register --csr-file`.
func generateOfficerCSR(t *testing.T, dir, cn string) (csrPath string, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateOfficerCSR: generate key: %v", err)
	}
	template := &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("generateOfficerCSR: create CSR: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	csrPath = filepath.Join(dir, cn+"-csr.pem")
	if err := os.WriteFile(csrPath, csrPEM, 0o600); err != nil {
		t.Fatalf("generateOfficerCSR: write: %v", err)
	}
	return csrPath, key
}

// writeOfficerKeyPair PEM-encodes key and the server-signed certPEM to
// files under dir so they can be used as --cert-file/--key-file for the
// officer's own mTLS-authenticated claim calls.
func writeOfficerKeyPair(t *testing.T, dir, cn string, key *ecdsa.PrivateKey, certPEM []byte) (certPath, keyPath string) {
	t.Helper()
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("writeOfficerKeyPair: marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPath = filepath.Join(dir, cn+"-cert.pem")
	keyPath = filepath.Join(dir, cn+"-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("writeOfficerKeyPair: write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("writeOfficerKeyPair: write key: %v", err)
	}
	return certPath, keyPath
}

// signNonceASN1 signs the SHA-256 digest of nonce with key and returns the
// ASN.1 DER-encoded (r, s) signature, matching the encoding
// pkg/ceremony/crypto.go's verifyNonceSignature expects for ECDSA officer
// keys.
func signNonceASN1(t *testing.T, key *ecdsa.PrivateKey, nonce []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(nonce)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("signNonceASN1: sign: %v", err)
	}
	sig, err := asn1.Marshal(struct{ R, S *big.Int }{R: r, S: s})
	if err != nil {
		t.Fatalf("signNonceASN1: marshal: %v", err)
	}
	return sig
}

// registerOfficer generates a CSR for username, registers it against the
// barrier's officer registry using the admin identity, and writes the
// server-signed certificate alongside the officer's own private key. When
// recovery is true the `barrier recovery officer` scope is used instead of
// the unseal `barrier officer` scope.
func registerOfficer(t *testing.T, cli, dir string, adminFlags []string, username string, recovery bool) *officer {
	t.Helper()
	csrPath, key := generateOfficerCSR(t, dir, username)

	args := append(append([]string{}, adminFlags...), "--output", "json", "barrier", "officer", "register",
		"--username", username, "--csr-file", csrPath)
	if recovery {
		args = append(args, "--domain", "recovery")
	}

	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		t.Fatalf("register officer %s: %v\nstdout:%s\nstderr:%s", username, err, stdout, stderr)
	}
	var resp transport.OfficerRegisterResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("register officer %s: decode JSON: %v\noutput: %s", username, err, stdout)
	}

	certPath, keyPath := writeOfficerKeyPair(t, dir, username, key, []byte(resp.CertPEM))
	return &officer{username: username, key: key, certPath: certPath, keyPath: keyPath}
}

// claimEnvelope claims the holder's sealed envelope over the holder's own
// mTLS connection and records where it was written. Every other claim
// consumer opens the envelope with the holder's key; this helper never
// sees a share.
func claimEnvelope(t *testing.T, cli, dir string, ep endpoint, off *officer, recovery bool) {
	t.Helper()
	domain := "unseal"
	if recovery {
		domain = "recovery"
	}
	off.envelope = filepath.Join(dir, fmt.Sprintf("%s-%s-%d.env", off.username, domain, time.Now().UnixNano()))
	args := append(off.flags(ep), "barrier", "officer", "claim", "--domain", domain,
		"--username", off.username, "--key", off.keyPath, "--out", off.envelope)
	if stdout, stderr, err := runCLISplit(t, cli, args...); err != nil {
		t.Fatalf("claim %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
}

// ---------------------------------------------------------------------------
// Flow 1: passphrase lifecycle
// ---------------------------------------------------------------------------

// TestBarrierCLI_PassphraseLifecycle drives a full passphrase barrier
// lifecycle over the qrdb CLI against a fresh node: uninitialized status,
// init, unseal/seal cycling (including a wrong-passphrase negative), and a
// KV write/read round-trip proving the barrier actually gates the data
// plane.
func TestBarrierCLI_PassphraseLifecycle(t *testing.T) {
	binary := buildBinary(t)
	cli := buildCLIBinary(t)

	configPath := writeSealConfig(t, "passphrase", 0, 0)
	dataDir := t.TempDir()
	raftPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 2)

	node := startSealNode(t, binary, dataDir, configPath, raftPorts[0], tcpPorts[0], tcpPorts[1])
	flags := node.adminFlags()

	waitForBarrierReachable(t, cli, flags, 60*time.Second)

	status := barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatalf("expected fresh passphrase barrier to be sealed pre-init, got: %+v", status)
	}

	const passphrase = "correct-horse-battery-staple-1"
	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "init", "--barrier-strategy", "passphrase", "--passphrase", passphrase)...); err != nil {
		t.Fatalf("barrier init: %v\nstderr: %s", err, stderr)
	}

	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed immediately after init (Initialize auto-unseals)")
	}
	if status.Strategy != "passphrase" {
		t.Errorf("expected strategy 'passphrase', got %q", status.Strategy)
	}
	if status.InitializedAt == "" {
		t.Error("expected non-empty InitializedAt after init")
	}

	// Wait for the full host + RBAC-backed server to replace the pre-host
	// sealed-window listener before exercising data-plane operations.
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, node.ca, []int{node.adminPort}, 60*time.Second)

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"kv", "put", "barrier-cli-key", "barrier-cli-value")...); err != nil {
		t.Fatalf("kv put: %v\nstderr: %s", err, stderr)
	}
	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "kv", "get", "barrier-cli-key")...)
	if err != nil {
		t.Fatalf("kv get: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "barrier-cli-value") {
		t.Fatalf("kv get: expected value in output, got: %s", stdout)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "seal", "-y")...); err != nil {
		t.Fatalf("barrier seal: %v\nstderr: %s", err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed after 'barrier seal -y'")
	}

	// Negative: a wrong passphrase must fail and must not unseal.
	if _, _, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal", "--passphrase", "definitely-the-wrong-passphrase")...); err == nil {
		t.Fatal("expected non-zero exit for wrong-passphrase unseal")
	}
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("wrong-passphrase unseal must not have left the barrier unsealed")
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal", "--passphrase", passphrase)...); err != nil {
		t.Fatalf("barrier unseal: %v\nstderr: %s", err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after the correct passphrase")
	}

	stdout, stderr, err = runCLISplit(t, cli, append(append([]string{}, flags...), "kv", "get", "barrier-cli-key")...)
	if err != nil {
		t.Fatalf("kv get after seal/unseal cycle: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "barrier-cli-value") {
		t.Fatalf("kv get after seal/unseal cycle: expected surviving value, got: %s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Flow 2: Shamir M-of-N lifecycle
// ---------------------------------------------------------------------------

// TestBarrierCLI_ShamirLifecycle drives a full 2-of-3 Shamir barrier
// lifecycle over the qrdb CLI: holder enrolment, share-free init-shamir, the
// one-time envelope claim, and the authenticated quorum for seal, unseal
// (with a below-threshold round that leaves the barrier sealed), root-token
// and rekey (with a negative proving the pre-rekey envelopes are revoked),
// and the independent recovery domain (generate/status/recover/delete) with
// its own holders.
func TestBarrierCLI_ShamirLifecycle(t *testing.T) {
	binary := buildBinary(t)
	cli := buildCLIBinary(t)
	workDir := t.TempDir()

	const threshold, total = 2, 3
	configPath := writeSealConfig(t, "shamir", threshold, total)
	dataDir := t.TempDir()
	raftPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 2)

	node := startSealNode(t, binary, dataDir, configPath, raftPorts[0], tcpPorts[0], tcpPorts[1])
	flags := node.adminFlags()
	ep := node.endpoint()

	waitForBarrierReachable(t, cli, flags, 60*time.Second)

	status := barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatalf("expected fresh shamir barrier to be sealed pre-init, got: %+v", status)
	}

	// Holders enrol while the barrier is still uninitialized/sealed: the
	// ceremony registry caches records until the barrier is first seen
	// unsealed, and the SAME ceremony service is threaded from the pre-host
	// sealed listener into the post-unseal boot.
	alice := registerOfficer(t, cli, workDir, flags, "alice", false)
	bob := registerOfficer(t, cli, workDir, flags, "bob", false)
	registerOfficer(t, cli, workDir, flags, "carol", false)

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "init-shamir",
		"--threshold", strconv.Itoa(threshold), "--total", strconv.Itoa(total))...); err != nil {
		t.Fatalf("barrier init-shamir: %v\nstderr: %s", err, stderr)
	}

	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed immediately after init-shamir")
	}
	if status.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", status.Strategy)
	}

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, node.ca, []int{node.adminPort}, 60*time.Second)

	claimEnvelope(t, cli, workDir, ep, alice, false)
	claimEnvelope(t, cli, workDir, ep, bob, false)
	firstAlice, firstBob := *alice, *bob

	quorumAll(t, cli, ep, "unseal", "seal", alice, bob)
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed after the seal quorum")
	}

	// A below-threshold round is progress, not an error, and leaves the
	// barrier sealed.
	out := quorumAll(t, cli, ep, "unseal", "unseal", alice)
	if out.Complete || out.Submitted != 1 || out.Required != threshold {
		t.Fatalf("one of two submissions = %+v, want 1/2 incomplete", out)
	}
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("a below-threshold round must not unseal the barrier")
	}
	// A holder counts once per round.
	if _, _, err := quorumSubmit(t, cli, ep, "unseal", "unseal", alice); err == nil {
		t.Fatal("a second submission from the same holder was accepted")
	}
	if out = quorumAll(t, cli, ep, "unseal", "unseal", bob); !out.Complete {
		t.Fatalf("second holder's submission = %+v, want complete", out)
	}
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatal("expected barrier to be unsealed after the unseal quorum")
	}

	out = quorumAll(t, cli, ep, "unseal", "root-token", alice, bob)
	if !out.Complete || out.RootToken == "" {
		t.Fatalf("root-token round = %+v, want a root token", out)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "rekey", "--threshold", strconv.Itoa(threshold))...); err != nil {
		t.Fatalf("barrier rekey: %v\nstderr: %s", err, stderr)
	}
	if out = quorumAll(t, cli, ep, "unseal", "rekey", alice, bob); !out.Complete {
		t.Fatalf("rekey round = %+v, want complete", out)
	}
	claimEnvelope(t, cli, workDir, ep, alice, false)
	claimEnvelope(t, cli, workDir, ep, bob, false)

	quorumAll(t, cli, ep, "unseal", "seal", alice, bob)
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("expected barrier to be sealed after the post-rekey seal quorum")
	}

	// Negative: the pre-rekey envelopes are cryptographically revoked. Each
	// is still its holder's, so it is admitted, but the quorum it forms
	// reconstructs a key that no longer opens the barrier.
	quorumAll(t, cli, ep, "unseal", "unseal", &firstAlice)
	if _, _, err := quorumSubmit(t, cli, ep, "unseal", "unseal", &firstBob); err == nil {
		t.Fatal("a quorum of pre-rekey envelopes completed")
	}
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("revoked envelopes must not unseal the barrier")
	}

	quorumAll(t, cli, ep, "unseal", "unseal", alice, bob)
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatal("expected barrier to be unsealed with the post-rekey envelopes")
	}

	// The recovery key: an independent domain with its own holders.
	// Generate + status while unsealed, then seal through the unseal-domain
	// quorum before exercising recover (which requires the barrier sealed).
	dave := registerOfficer(t, cli, workDir, flags, "dave", true)
	erin := registerOfficer(t, cli, workDir, flags, "erin", true)

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "recovery", "generate", "--threshold", "2")...); err != nil {
		t.Fatalf("barrier recovery generate: %v\nstderr: %s", err, stderr)
	}
	if !barrierHasRecoveryKeys(t, cli, flags) {
		t.Fatal("expected recovery keys to be present after generate")
	}

	claimEnvelope(t, cli, workDir, ep, dave, true)
	claimEnvelope(t, cli, workDir, ep, erin, true)

	quorumAll(t, cli, ep, "unseal", "seal", alice, bob)
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("expected barrier to be sealed before exercising recover")
	}

	if out = quorumAll(t, cli, ep, "recovery", "recover", dave, erin); !out.Complete {
		t.Fatalf("recover round = %+v, want complete", out)
	}
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatal("expected barrier to be unsealed after the recover quorum")
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "recovery", "delete")...); err != nil {
		t.Fatalf("barrier recovery delete: %v\nstderr: %s", err, stderr)
	}
	if barrierHasRecoveryKeys(t, cli, flags) {
		t.Fatal("expected recovery keys to be absent after delete")
	}
}

// ---------------------------------------------------------------------------
// Flow 3: restart persistence
// ---------------------------------------------------------------------------

// TestBarrierCLI_RestartPersistence verifies that a passphrase barrier's
// initialized state (and the data it protects) survives a full daemon
// restart on the same data directory. The passphrase strategy caches no
// secret on disk, so the code path under test asserts the barrier comes
// back SEALED (not auto-unsealed) after restart, requiring the operator to
// unseal again with the original passphrase before the data plane is
// readable.
func TestBarrierCLI_RestartPersistence(t *testing.T) {
	binary := buildBinary(t)
	cli := buildCLIBinary(t)

	configPath := writeSealConfig(t, "passphrase", 0, 0)
	dataDir := t.TempDir()
	raftPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 2)

	node := startSealNode(t, binary, dataDir, configPath, raftPorts[0], tcpPorts[0], tcpPorts[1])
	flags := node.adminFlags()
	waitForBarrierReachable(t, cli, flags, 60*time.Second)

	const passphrase = "restart-persistence-passphrase-1"
	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "init", "--barrier-strategy", "passphrase", "--passphrase", passphrase)...); err != nil {
		t.Fatalf("barrier init: %v\nstderr: %s", err, stderr)
	}

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, node.ca, []int{node.adminPort}, 60*time.Second)

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"kv", "put", "restart-key", "restart-value")...); err != nil {
		t.Fatalf("kv put: %v\nstderr: %s", err, stderr)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "seal", "-y")...); err != nil {
		t.Fatalf("barrier seal: %v\nstderr: %s", err, stderr)
	}
	status := barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed before restart")
	}
	initializedAt := status.InitializedAt

	stopSealNode(t, node)

	// Restart on the SAME data dir, config, and ports. resolveTLS's
	// cached-certificate path (ca.pem/node-cert.pem/node-key.pem already
	// present under dataDir) keeps the CA and admin identity stable across
	// the restart.
	node = startSealNode(t, binary, dataDir, configPath, raftPorts[0], tcpPorts[0], tcpPorts[1])
	flags = node.adminFlags()
	waitForBarrierReachable(t, cli, flags, 60*time.Second)

	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to still require an explicit unseal after restart (passphrase caches no secret)")
	}
	if status.InitializedAt == "" || status.InitializedAt != initializedAt {
		t.Errorf("expected InitializedAt to survive restart unchanged: before=%q after=%q", initializedAt, status.InitializedAt)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal", "--passphrase", passphrase)...); err != nil {
		t.Fatalf("barrier unseal after restart: %v\nstderr: %s", err, stderr)
	}

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, node.ca.Pool, node.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, node.ca, []int{node.adminPort}, 60*time.Second)

	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "kv", "get", "restart-key")...)
	if err != nil {
		t.Fatalf("kv get after restart: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "restart-value") {
		t.Fatalf("kv get after restart: expected surviving value, got: %s", stdout)
	}
}
