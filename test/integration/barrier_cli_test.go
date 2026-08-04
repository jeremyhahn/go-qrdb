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
// seal, rekey, root-token, recovery generate/status/recover/delete, and the
// Security Officer officer/claim handshake that backs Shamir M-of-N.
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
// (Owner role via pkg/firstboot -- permission.All()) EXCEPT the officer
// claim-begin/claim-complete handshake, which is deliberately
// identity-gated by mTLS certificate thumbprint
// (pkg/ceremony/ceremony_officer.go: ErrCertThumbprintMismatch) rather than
// RBAC, so those two calls present the claiming officer's own CA-signed
// certificate instead of the admin certificate.
//
// Semantic risks / findings surfaced while wiring this file (verified by
// reading source, not assumed):
//
//  1. Share-envelope mismatch: `barrier officer claim-complete` /
//     `barrier recovery officer claim-complete` return ShareBytes that are
//     the JSON-marshaled go-quicraft shamir.Share struct
//     ({"index":N,"threshold":M,"total":T,"value":"..."}), because
//     pkg/ceremony/ceremony_officer.go's sealSharesToOfficers stores
//     `json.Marshal(shares[idx])` verbatim and share_store.Claim returns
//     exactly what was stored. Every consumer of a raw share string
//     (`barrier unseal-shares`, `barrier shamir-seal-vote`,
//     `barrier rekey`'s implicit reuse, `barrier root-token`,
//     `barrier recovery recover`) instead expects only the base64 "value"
//     field (pkg/seal/shamir.go, root_token.go, seal_vote.go, recovery.go
//     all do `Value: val` directly with no JSON unwrap). There is no CLI
//     step that performs this unwrap; claimShare() below does it manually.
//     A real operator/script driving the CLI end-to-end would hit this same
//     friction. Flagged here rather than silently patched in product code.
//  2. `barrier seal` rejects Shamir-protected barriers outright
//     (seal.ErrShamirBarrierRequiresQuorum from
//     pkg/service/barrier_service.go's BarrierSeal); only
//     `barrier shamir-seal-vote`, run to quorum, can seal one. The task
//     description's "seal -y" step therefore only applies to the
//     passphrase lifecycle; the Shamir lifecycle uses sealViaVotes().
//  3. `barrier rekey` requires the barrier to already be UNSEALED
//     (pkg/seal/rekey.go: "the root key is available in memory") and,
//     unlike `init-shamir`, has NO zero-value fallback to the boot-time
//     M/N (pkg/service/barrier_service.go's BarrierRekey passes threshold/
//     total straight through to Rekey, which rejects threshold<2
//     unconditionally) -- --threshold/--total must always be supplied
//     explicitly to `barrier rekey`.
//  4. `barrier recovery generate`/`status`/`delete` require the barrier to
//     be UNSEALED; `barrier recovery recover` requires it to be SEALED
//     (pkg/seal/recovery.go). The recovery flow below generates while
//     unsealed, then reseals via a fresh Shamir quorum vote before
//     exercising `recovery recover`.
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
	"encoding/hex"
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

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("loadDaemonCA: read cert: %v", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("loadDaemonCA: read key: %v", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatalf("loadDaemonCA: no PEM block in %s", certPath)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("loadDaemonCA: parse cert: %v", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatalf("loadDaemonCA: no PEM block in %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("loadDaemonCA: parse key: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &testCA{
		CertPEM:  certPEM,
		CertPath: certPath,
		Pool:     pool,
		cert:     cert,
		key:      key,
	}
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

// sealViaVotes submits shares as Shamir quorum seal votes until the barrier
// transitions to sealed. `barrier seal` unconditionally rejects a
// Shamir-protected barrier (ErrShamirBarrierRequiresQuorum); this is the
// only sealing path available to one.
func sealViaVotes(t *testing.T, cli string, flags []string, shares ...string) {
	t.Helper()
	for i, share := range shares {
		_, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "shamir-seal-vote", "--share", share)...)
		if err != nil {
			t.Fatalf("shamir-seal-vote #%d: %v\nstderr: %s", i+1, err, stderr)
		}
	}
}

// writeSharesFile writes newline-delimited shares to a file under dir for
// exercising the `--shares-file` variant of `barrier unseal-shares`.
func writeSharesFile(t *testing.T, dir, name string, shares ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(shares, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writeSharesFile: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Security Officer CSR / claim helpers
// ---------------------------------------------------------------------------

// officer bundles a registered Security Officer's own key material and the
// server-signed certificate paths used to authenticate the claim handshake.
type officer struct {
	username string
	key      *ecdsa.PrivateKey
	certPath string
	keyPath  string
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

	args := append(append([]string{}, adminFlags...), "--output", "json", "barrier")
	if recovery {
		args = append(args, "recovery", "officer", "register")
	} else {
		args = append(args, "officer", "register")
	}
	args = append(args, "--username", username, "--csr-file", csrPath)

	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		t.Fatalf("register officer %s: %v\nstdout:%s\nstderr:%s", username, err, stdout, stderr)
	}
	var resp transport.OfficerRegisterResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("register officer %s: decode JSON: %v\noutput: %s", username, err, stdout)
	}

	certPath, keyPath := writeOfficerKeyPair(t, dir, username, key, resp.CertPEM)
	return &officer{username: username, key: key, certPath: certPath, keyPath: keyPath}
}

// claimShare performs the officer's mTLS-authenticated claim-begin /
// claim-complete handshake and returns the reconstructed Shamir share
// VALUE string, unwrapped from the JSON envelope the ceremony share store
// persists internally (see finding #1 in the file header). recovery
// selects the recovery-officer claim routes.
func claimShare(t *testing.T, cli, dir, serverURL, caFile string, off *officer, recovery bool) string {
	t.Helper()
	officerFlags := []string{
		"--server", serverURL, "--ca-file", caFile,
		"--cert-file", off.certPath, "--key-file", off.keyPath,
		"--timeout", "30s",
	}
	sub := []string{"officer"}
	if recovery {
		sub = []string{"recovery", "officer"}
	}

	beginArgs := append(append([]string{}, officerFlags...), "--output", "json", "barrier")
	beginArgs = append(beginArgs, sub...)
	beginArgs = append(beginArgs, "claim-begin", "--username", off.username)

	stdout, stderr, err := runCLISplit(t, cli, beginArgs...)
	if err != nil {
		t.Fatalf("claim-begin %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
	var begin transport.ShareClaimBeginResponse
	if err := json.Unmarshal([]byte(stdout), &begin); err != nil {
		t.Fatalf("claim-begin %s: decode JSON: %v\noutput: %s", off.username, err, stdout)
	}

	nonce, err := hex.DecodeString(begin.NonceHex)
	if err != nil {
		t.Fatalf("claim-begin %s: decode nonce hex: %v", off.username, err)
	}
	sig := signNonceASN1(t, off.key, nonce)
	sigPath := filepath.Join(dir, off.username+"-sig.bin")
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatalf("claim-complete %s: write sig file: %v", off.username, err)
	}

	completeArgs := append(append([]string{}, officerFlags...), "--output", "json", "barrier")
	completeArgs = append(completeArgs, sub...)
	completeArgs = append(completeArgs, "claim-complete",
		"--username", off.username, "--nonce-hex", begin.NonceHex, "--sig-file", sigPath)

	stdout, stderr, err = runCLISplit(t, cli, completeArgs...)
	if err != nil {
		t.Fatalf("claim-complete %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
	var complete transport.ShareClaimCompleteResponse
	if err := json.Unmarshal([]byte(stdout), &complete); err != nil {
		t.Fatalf("claim-complete %s: decode JSON: %v\noutput: %s", off.username, err, stdout)
	}

	var envelope struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(complete.ShareBytes, &envelope); err != nil {
		t.Fatalf("claim-complete %s: decode share envelope: %v\nraw: %s", off.username, err, complete.ShareBytes)
	}
	if envelope.Value == "" {
		t.Fatalf("claim-complete %s: empty share value in envelope: %s", off.username, complete.ShareBytes)
	}
	return envelope.Value
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
// lifecycle over the qrdb CLI: officer enrollment, share-free init-shamir,
// the mTLS officer claim handshake, quorum seal-vote, unseal-shares (both
// --share and --shares-file forms), root-token, rekey (with a negative
// proving the pre-rekey shares are revoked), and the independent recovery
// key scope (generate/status/recover/delete) with its own officer set.
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

	waitForBarrierReachable(t, cli, flags, 60*time.Second)

	status := barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatalf("expected fresh shamir barrier to be sealed pre-init, got: %+v", status)
	}

	// Officers register while the barrier is still uninitialized/sealed:
	// pkg/ceremony's registry falls back to an in-memory cache
	// (registryWritable := !s.barrier.IsSealed()) and the SAME
	// CeremonyService instance is threaded from the pre-host sealed
	// listener into the post-unseal boot, so the registrations remain
	// visible to init-shamir's SealAndDistribute.
	alice := registerOfficer(t, cli, workDir, flags, "alice", false)
	bob := registerOfficer(t, cli, workDir, flags, "bob", false)
	registerOfficer(t, cli, workDir, flags, "carol", false)

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "init-shamir",
		"--passphrase", "unused-in-direct-shamir-mode",
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

	aliceShare1 := claimShare(t, cli, workDir, node.serverURL, node.caFile, alice, false)
	bobShare1 := claimShare(t, cli, workDir, node.serverURL, node.caFile, bob, false)

	sealViaVotes(t, cli, flags, aliceShare1, bobShare1)
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed after quorum seal-vote")
	}

	// Negative: below-threshold shares must fail and leave the barrier sealed.
	if _, _, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal-shares", "--share", aliceShare1)...); err == nil {
		t.Fatal("expected non-zero exit for below-threshold unseal-shares")
	}
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("insufficient-share unseal-shares must not unseal the barrier")
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal-shares", "--share", aliceShare1, "--share", bobShare1)...); err != nil {
		t.Fatalf("barrier unseal-shares --share: %v\nstderr: %s", err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after quorum unseal-shares")
	}

	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"--output", "json", "barrier", "root-token", "--share", aliceShare1, "--share", bobShare1)...)
	if err != nil {
		t.Fatalf("barrier root-token: %v\nstderr: %s", err, stderr)
	}
	var rootToken transport.BarrierRootTokenResponse
	if err := json.Unmarshal([]byte(stdout), &rootToken); err != nil {
		t.Fatalf("barrier root-token: decode JSON: %v\noutput: %s", err, stdout)
	}
	if rootToken.Token == "" {
		t.Fatal("expected non-empty root token")
	}

	// Rekey requires the barrier to be unsealed (it is) and, unlike
	// init-shamir, has no zero-value fallback -- always pass explicit M/N.
	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "rekey", "--threshold", strconv.Itoa(threshold), "--total", strconv.Itoa(total))...); err != nil {
		t.Fatalf("barrier rekey: %v\nstderr: %s", err, stderr)
	}

	aliceShare2 := claimShare(t, cli, workDir, node.serverURL, node.caFile, alice, false)
	bobShare2 := claimShare(t, cli, workDir, node.serverURL, node.caFile, bob, false)
	if aliceShare2 == aliceShare1 || bobShare2 == bobShare1 {
		t.Fatal("expected rekey to distribute cryptographically fresh shares")
	}

	sealViaVotes(t, cli, flags, aliceShare2, bobShare2)
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed after the post-rekey quorum seal-vote")
	}

	// Negative: the pre-rekey shares are cryptographically revoked.
	staleFile := writeSharesFile(t, workDir, "stale-shares.txt", aliceShare1, bobShare1)
	if _, _, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal-shares", "--shares-file", staleFile)...); err == nil {
		t.Fatal("expected non-zero exit when unsealing with pre-rekey (revoked) shares")
	}
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("revoked shares must not unseal the barrier")
	}

	freshFile := writeSharesFile(t, workDir, "fresh-shares.txt", aliceShare2, bobShare2)
	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal-shares", "--shares-file", freshFile)...); err != nil {
		t.Fatalf("barrier unseal-shares --shares-file: %v\nstderr: %s", err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed via --shares-file")
	}

	// Recovery keys: an independent scope from unseal shares, with its own
	// officer registry. Generate + status while unsealed (both required),
	// then reseal via the current valid unseal shares before exercising
	// recover (which requires the barrier to be sealed).
	dave := registerOfficer(t, cli, workDir, flags, "dave", true)
	erin := registerOfficer(t, cli, workDir, flags, "erin", true)

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "recovery", "generate", "--threshold", "2", "--total", "2")...); err != nil {
		t.Fatalf("barrier recovery generate: %v\nstderr: %s", err, stderr)
	}
	if !barrierHasRecoveryKeys(t, cli, flags) {
		t.Fatal("expected recovery keys to be present after generate")
	}

	daveShare := claimShare(t, cli, workDir, node.serverURL, node.caFile, dave, true)
	erinShare := claimShare(t, cli, workDir, node.serverURL, node.caFile, erin, true)

	sealViaVotes(t, cli, flags, aliceShare2, bobShare2)
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed before exercising recovery recover")
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "recovery", "recover", "--share", daveShare, "--share", erinShare)...); err != nil {
		t.Fatalf("barrier recovery recover: %v\nstderr: %s", err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after recovery recover")
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
