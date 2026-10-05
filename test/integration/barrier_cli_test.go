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
// tests (admin_test.go, drain_test.go) use. This file reads the generated CA
// straight off disk and reconstructs a helpers_test.go `testCA` value (same
// package, unexported field access) purely so it can reuse waitForAdmin /
// waitForLeaderViaAdmin / httpPut / httpGet unmodified.
//
// `qrdb init` is the only system bootstrap and the only source of the
// bootstrap admin's credential: nothing writes an admin certificate to the
// data directory. Each test brings its node up with `qrdb init` against the
// sealed-window listener -- presenting no client certificate, since no admin
// exists yet -- through bootAdminIdentityVia, which also brings the system
// barrier up (--barrier-strategy, and --passphrase or the holders' CSRs) and
// writes the admin credential the call issues. Every later barrier
// subcommand is invoked with that admin identity (Owner role via
// pkg/firstboot -- permission.All()) EXCEPT the share-holder
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

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Node lifecycle
// ---------------------------------------------------------------------------

// sealNode holds the connection details and TLS material for a single
// self-managed-TLS qrdbd node started with an enabled (possibly
// uninitialized) barrier. adminCert/adminKey are the bootstrap admin's
// credential: issued by the node's own CA (ca.boundAdminCert) and bound by
// `qrdb init --admin-cert` (initSystem), the bring-your-own-certificate
// path. It authenticates nothing until init has bound it.
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

// noAuthFlags returns the qrdb CLI global flags reaching this node over REST
// with no client certificate: the only flags that work before `qrdb init`
// has issued the admin credential.
func (n *sealNode) noAuthFlags() []string {
	return []string{
		"--server", n.serverURL,
		"--ca-file", n.caFile,
		"--timeout", "30s",
	}
}

// sealNodeSOPIN and sealNodeUserPIN are the PINs every sealNode is
// bootstrapped with.
const (
	sealNodeSOPIN   = "SOPIN12345"
	sealNodeUserPIN = "USERPIN12345"
)

// initSystem bootstraps the node with `qrdb init` (bootAdminIdentityVia),
// binding n.adminCert as the bootstrap admin (--admin-cert, the
// bring-your-own-certificate path: the node's own CA issued it, so no
// trust pool is needed) -- extraArgs carry --barrier-strategy and its
// secret or holders. It returns init's JSON output.
func (n *sealNode) initSystem(t *testing.T, cli string, extraArgs ...string) string {
	t.Helper()
	return bootAdminIdentityVia(t, cli, n.noAuthFlags(), sealNodeSOPIN, sealNodeUserPIN, n.adminCert, n.adminKey, "",
		append([]string{"--admin-cert", n.adminCert}, extraArgs...)...)
}

// adoptIdentity carries a restarted node's CA handle and bootstrap admin
// credential over from prev, the same node before its restart: `qrdb init`
// bound prev's credential, and it is what authenticates after the restart.
func (n *sealNode) adoptIdentity(prev *sealNode) {
	n.ca, n.adminCert, n.adminKey = prev.ca, prev.adminCert, prev.adminKey
}

// waitForFullBoot blocks until the full RBAC-backed server (not the pre-host
// sealed-window listener) serves on the admin port and the shard has a
// leader.
func (n *sealNode) waitForFullBoot(t *testing.T) {
	t.Helper()
	waitForHealthy(t, n.dataDir, 60*time.Second)
	waitForAdmin(t, n.ca.Pool, n.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, n.ca, []int{n.adminPort}, 60*time.Second)
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
// dataDir) a CA that carries its own private key. It blocks until the CA
// material exists on disk -- written very early in boot, strictly before
// the barrier is ever initialized -- and issues the bootstrap admin's
// certificate from that CA for initSystem to bind. A restarted node adopts
// its predecessor's identity (adoptIdentity) instead.
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
	waitForNonEmptyFile(t, caPath, 30*time.Second)
	ca := loadDaemonCA(t, dataDir)
	adminCert, adminKey := ca.boundAdminCert(t, t.TempDir())

	return &sealNode{
		cmd:       cmd,
		dataDir:   dataDir,
		adminPort: adminPort,
		serverURL: fmt.Sprintf("https://127.0.0.1:%d", adminPort),
		caFile:    caPath,
		adminCert: adminCert,
		adminKey:  adminKey,
		ca:        ca,
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
	waitForNonEmptyFile(t, certPath, 30*time.Second)

	if !nonEmptyFileAppears(keyPath, 5*time.Second) {
		ca, err := qrdbdharness.LoadCACertOnly(certPath)
		if err != nil {
			t.Fatalf("loadDaemonCA: %v", err)
		}
		return &testCA{CA: ca}
	}

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

// barrierScope names one holderTarget's CLI surface: the command words
// that reach its officer/quorum/rekey verbs, and the flag (and value) that
// binds them to one barrier instance. cmd/qrdb/barrier_holders.go's
// holderTarget backs the system (barrier --domain), tenant (tenant
// barrier-officer --tenant-id) and database (database barrier-officer
// --database) scopes with one implementation each; this lets every
// share-holder helper below drive all three through a single parameter
// instead of one copy per scope.
type barrierScope struct {
	officer []string
	quorum  []string
	rekey   []string
	flag    string
	value   string
}

// scopeArgs returns the flag/value pair that selects s's barrier
// instance, appended after a verb's own flags.
func (s barrierScope) scopeArgs() []string {
	return []string{s.flag, s.value}
}

// systemScope is the system barrier in the given quorum domain ("unseal"
// or "recovery").
func systemScope(domain string) barrierScope {
	return barrierScope{
		officer: []string{"barrier", "officer"},
		quorum:  []string{"barrier", "quorum"},
		rekey:   []string{"barrier", "rekey"},
		flag:    "--domain",
		value:   domain,
	}
}

// databaseScope is database dbName's barrier, resolved in the caller's
// tenant.
func databaseScope(dbName string) barrierScope {
	return barrierScope{
		officer: []string{"database", "barrier-officer"},
		quorum:  []string{"database", "barrier-quorum"},
		rekey:   []string{"database", "barrier-rekey"},
		flag:    "--database",
		value:   dbName,
	}
}

// quorumAll submits each holder's claimed envelope to the op's quorum
// round, in order, and returns the last outcome. A Shamir-protected
// barrier unconditionally rejects a single-caller seal/unseal; the quorum
// is the only way to seal, unseal, mint a root token or complete a rekey
// on one.
func quorumAll(t *testing.T, cli string, ep endpoint, scope barrierScope, op string, holders ...*officer) *transport.QuorumOutcome {
	t.Helper()
	var out *transport.QuorumOutcome
	for _, h := range holders {
		o, stderr, err := quorumSubmit(t, cli, ep, scope, op, h)
		if err != nil {
			t.Fatalf("quorum submit --op %s %s: %v\nstderr: %s", op, h.username, err, stderr)
		}
		out = o
	}
	return out
}

// quorumSubmit submits one holder's claimed envelope to the op's round,
// over the holder's own connection, presenting its certificate for the
// mtls factor.
func quorumSubmit(t *testing.T, cli string, ep endpoint, scope barrierScope, op string, h *officer) (*transport.QuorumOutcome, string, error) {
	t.Helper()
	args := append(h.flags(ep), "--output", "json")
	args = append(args, scope.quorum...)
	args = append(args, "submit", "--op", op, "--username", h.username,
		"--envelope", h.envelope, "--key", h.keyPath)
	args = append(args, scope.scopeArgs()...)
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

// initHolders generates a key pair and CSR for each of names, returning the
// officers (no certificate yet) and the `qrdb init --officers` flags naming
// them: M-of-N custody is set up by `qrdb init` itself, which enrols the
// holders from their CSRs and splits the unseal key to them.
func initHolders(t *testing.T, dir string, names ...string) ([]*officer, []string) {
	t.Helper()
	var holders []*officer
	var flags []string
	for _, name := range names {
		csrPath, key := generateOfficerCSR(t, dir, name)
		holders = append(holders, &officer{username: name, key: key})
		flags = append(flags, "--officers", name+"="+csrPath)
	}
	return holders, flags
}

// adoptInitHolders writes each holder's certificate from `qrdb init`'s JSON
// output alongside its own key, so the holder can claim its envelope and
// submit to quorums.
func adoptInitHolders(t *testing.T, dir, initOut string, holders []*officer) {
	t.Helper()
	var resp transport.CeremonyInitializeResponse
	decodeJSON(t, initOut, &resp)
	certs := make(map[string]string, len(resp.Holders))
	for _, h := range resp.Holders {
		certs[h.Username] = h.CertPEM
	}
	for _, h := range holders {
		certPEM, ok := certs[h.username]
		if !ok || certPEM == "" {
			t.Fatalf("qrdb init issued no certificate for holder %s: %s", h.username, initOut)
		}
		h.certPath, h.keyPath = writeOfficerKeyPair(t, dir, h.username, h.key, []byte(certPEM))
	}
}

// registerOfficer generates a CSR for username, registers it against
// scope's officer registry using the admin identity, and writes the
// server-signed certificate alongside the officer's own private key.
func registerOfficer(t *testing.T, cli, dir string, adminFlags []string, scope barrierScope, username string) *officer {
	t.Helper()
	csrPath, key := generateOfficerCSR(t, dir, username)

	args := append(append([]string{}, adminFlags...), "--output", "json")
	args = append(args, scope.officer...)
	args = append(args, "register", "--username", username, "--csr-file", csrPath)
	args = append(args, scope.scopeArgs()...)

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
func claimEnvelope(t *testing.T, cli, dir string, ep endpoint, scope barrierScope, off *officer) {
	t.Helper()
	off.envelope = filepath.Join(dir, fmt.Sprintf("%s-%s-%d.env", off.username, scope.value, time.Now().UnixNano()))
	args := append(off.flags(ep), scope.officer...)
	args = append(args, "claim", "--username", off.username, "--key", off.keyPath, "--out", off.envelope)
	args = append(args, scope.scopeArgs()...)
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

	waitForBarrierReachable(t, cli, node.noAuthFlags(), 60*time.Second)

	status := barrierStatus(t, cli, node.noAuthFlags())
	if !status.Sealed {
		t.Fatalf("expected fresh passphrase barrier to be sealed pre-init, got: %+v", status)
	}

	const passphrase = "correct-horse-battery-staple-1"
	node.initSystem(t, cli, "--barrier-strategy", "passphrase", "--passphrase", passphrase)

	// Wait for the full host + RBAC-backed server to replace the pre-host
	// sealed-window listener; the admin credential `qrdb init` issued
	// authenticates from here on.
	node.waitForFullBoot(t)
	flags := node.adminFlags()

	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after qrdb init (it brings the barrier up)")
	}
	if status.Strategy != "passphrase" {
		t.Errorf("expected strategy 'passphrase', got %q", status.Strategy)
	}
	if status.InitializedAt == "" {
		t.Error("expected non-empty InitializedAt after init")
	}

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

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)...); err != nil {
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
	ep := node.endpoint()

	waitForBarrierReachable(t, cli, node.noAuthFlags(), 60*time.Second)

	status := barrierStatus(t, cli, node.noAuthFlags())
	if !status.Sealed {
		t.Fatalf("expected fresh shamir barrier to be sealed pre-init, got: %+v", status)
	}

	// `qrdb init` enrols the holders from their CSRs and splits the unseal
	// key to them in the same call that issues the admin credential.
	holders, holderFlags := initHolders(t, workDir, "alice", "bob", "carol")
	initOut := node.initSystem(t, cli, append([]string{"--barrier-strategy", "shamir",
		"--threshold", strconv.Itoa(threshold), "--shares", strconv.Itoa(total)}, holderFlags...)...)
	adoptInitHolders(t, workDir, initOut, holders)
	alice, bob := holders[0], holders[1]

	node.waitForFullBoot(t)
	flags := node.adminFlags()

	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatal("expected barrier to be unsealed after qrdb init")
	}
	if status.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", status.Strategy)
	}

	claimEnvelope(t, cli, workDir, ep, systemScope(transport.DomainUnseal), alice)
	claimEnvelope(t, cli, workDir, ep, systemScope(transport.DomainUnseal), bob)
	firstAlice, firstBob := *alice, *bob

	quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "seal", alice, bob)
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatal("expected barrier to be sealed after the seal quorum")
	}

	// A below-threshold round is progress, not an error, and leaves the
	// barrier sealed.
	out := quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "unseal", alice)
	if out.Complete || out.Submitted != 1 || out.Required != threshold {
		t.Fatalf("one of two submissions = %+v, want 1/2 incomplete", out)
	}
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("a below-threshold round must not unseal the barrier")
	}
	// A holder counts once per round.
	if _, _, err := quorumSubmit(t, cli, ep, systemScope(transport.DomainUnseal), "unseal", alice); err == nil {
		t.Fatal("a second submission from the same holder was accepted")
	}
	if out = quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "unseal", bob); !out.Complete {
		t.Fatalf("second holder's submission = %+v, want complete", out)
	}
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatal("expected barrier to be unsealed after the unseal quorum")
	}

	out = quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "root-token", alice, bob)
	if !out.Complete || out.RootToken == "" {
		t.Fatalf("root-token round = %+v, want a root token", out)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "rekey", "--threshold", strconv.Itoa(threshold), "--so-pin", sealNodeSOPIN)...); err != nil {
		t.Fatalf("barrier rekey: %v\nstderr: %s", err, stderr)
	}
	if out = quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "rekey", alice, bob); !out.Complete {
		t.Fatalf("rekey round = %+v, want complete", out)
	}
	claimEnvelope(t, cli, workDir, ep, systemScope(transport.DomainUnseal), alice)
	claimEnvelope(t, cli, workDir, ep, systemScope(transport.DomainUnseal), bob)

	quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "seal", alice, bob)
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("expected barrier to be sealed after the post-rekey seal quorum")
	}

	// Negative: the pre-rekey envelopes are cryptographically revoked. Each
	// is still its holder's, so it is admitted, but the quorum it forms
	// reconstructs a key that no longer opens the barrier.
	quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "unseal", &firstAlice)
	if _, _, err := quorumSubmit(t, cli, ep, systemScope(transport.DomainUnseal), "unseal", &firstBob); err == nil {
		t.Fatal("a quorum of pre-rekey envelopes completed")
	}
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("revoked envelopes must not unseal the barrier")
	}

	quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "unseal", alice, bob)
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatal("expected barrier to be unsealed with the post-rekey envelopes")
	}

	// The recovery key: an independent domain with its own holders.
	// Generate + status while unsealed, then seal through the unseal-domain
	// quorum before exercising recover (which requires the barrier sealed).
	dave := registerOfficer(t, cli, workDir, flags, systemScope(transport.DomainRecovery), "dave")
	erin := registerOfficer(t, cli, workDir, flags, systemScope(transport.DomainRecovery), "erin")

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "recovery", "generate", "--threshold", "2", "--so-pin", sealNodeSOPIN)...); err != nil {
		t.Fatalf("barrier recovery generate: %v\nstderr: %s", err, stderr)
	}
	if !barrierHasRecoveryKeys(t, cli, flags) {
		t.Fatal("expected recovery keys to be present after generate")
	}

	claimEnvelope(t, cli, workDir, ep, systemScope(transport.DomainRecovery), dave)
	claimEnvelope(t, cli, workDir, ep, systemScope(transport.DomainRecovery), erin)

	quorumAll(t, cli, ep, systemScope(transport.DomainUnseal), "seal", alice, bob)
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("expected barrier to be sealed before exercising recover")
	}

	if out = quorumAll(t, cli, ep, systemScope(transport.DomainRecovery), "recover", dave, erin); !out.Complete {
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
	waitForBarrierReachable(t, cli, node.noAuthFlags(), 60*time.Second)

	const passphrase = "restart-persistence-passphrase-1"
	node.initSystem(t, cli, "--barrier-strategy", "passphrase", "--passphrase", passphrase)

	node.waitForFullBoot(t)
	flags := node.adminFlags()

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"kv", "put", "restart-key", "restart-value")...); err != nil {
		t.Fatalf("kv put: %v\nstderr: %s", err, stderr)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)...); err != nil {
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
	// present under dataDir) keeps the CA stable across the restart, and the
	// admin credential `qrdb init` bound stays bound.
	prev := node
	node = startSealNode(t, binary, dataDir, configPath, raftPorts[0], tcpPorts[0], tcpPorts[1])
	node.adoptIdentity(prev)
	flags = node.adminFlags()
	waitForBarrierReachable(t, cli, node.noAuthFlags(), 60*time.Second)

	status = barrierStatus(t, cli, node.noAuthFlags())
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

	node.waitForFullBoot(t)

	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "kv", "get", "restart-key")...)
	if err != nil {
		t.Fatalf("kv get after restart: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "restart-value") {
		t.Fatalf("kv get after restart: expected surviving value, got: %s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Flow 4: officer revoke and quorum begin
// ---------------------------------------------------------------------------

// TestBarrierCLI_OfficerRevoke drives `barrier officer revoke` over the qrdb
// CLI: a 2-of-3 Shamir barrier revokes one holder through a quorum-gated
// rekey that drops them, the remaining two holders complete it, each claims
// a fresh envelope, and the revoked holder's PRE-revoke envelope no longer
// forms a valid quorum (it was cryptographically retired by the rekey, the
// same mechanism `barrier rekey` itself uses -- see TestBarrierCLI_ShamirLifecycle).

// quorumBegin issues a quorum challenge for one holder ahead of submit,
// used by the split handshake (begin, then submit --challenge-id/--factor).
func quorumBegin(t *testing.T, cli string, ep endpoint, domain, op string, h *officer) (*transport.QuorumChallenge, string, error) {
	t.Helper()
	args := append(h.flags(ep), "--output", "json", "barrier", "quorum", "begin",
		"--domain", domain, "--op", op, "--username", h.username)
	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		return nil, stderr, err
	}
	var out transport.QuorumChallenge
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("quorum begin %s: decode JSON: %v\noutput: %s", h.username, err, stdout)
	}
	return &out, stderr, nil
}
func nonEmptyFileAppears(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func startSealNodeAllTransports(t *testing.T, binary, dataDir, configPath string, raftPort, adminPort, grpcPort, quicPort, mcpPort int, unixSocket string) *sealNodeAllTransports {
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
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
		"--config", configPath,
	}
	cmd := startNode(t, binary, args, "[barrier-mt] ")

	caPath := filepath.Join(dataDir, "ca.pem")
	waitForNonEmptyFile(t, caPath, 30*time.Second)
	ca := loadDaemonCA(t, dataDir)
	adminCert, adminKey := ca.boundAdminCert(t, t.TempDir())

	return &sealNodeAllTransports{
		cmd:        cmd,
		dataDir:    dataDir,
		adminPort:  adminPort,
		grpcPort:   grpcPort,
		quicPort:   quicPort,
		mcpPort:    mcpPort,
		unixSocket: unixSocket,
		caFile:     caPath,
		adminCert:  adminCert,
		adminKey:   adminKey,
		ca:         ca,
	}
}

type sealNodeAllTransports struct {
	cmd        *exec.Cmd
	dataDir    string
	adminPort  int
	grpcPort   int
	quicPort   int
	mcpPort    int
	unixSocket string
	caFile     string
	adminCert  string
	adminKey   string
	ca         *testCA
}

func (n *sealNodeAllTransports) portFor(proto string) int {
	switch proto {
	case "rest":
		return n.adminPort
	case "grpc":
		return n.grpcPort
	case "mcp":
		return n.mcpPort
	case "quic":
		return n.quicPort
	default:
		return 0
	}
}
func (n *sealNodeAllTransports) serverURLFor(proto multiProtocol) string {
	if proto.name == "unix" {
		return "unix://" + n.unixSocket
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", proto.scheme, n.portFor(proto.name))
}
func (n *sealNodeAllTransports) adminFlagsFor(proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", n.caFile,
		"--cert-file", n.adminCert,
		"--key-file", n.adminKey,
		"--timeout", "30s",
	}
}

// noAuthFlagsFor returns the qrdb CLI global flags reaching this node over
// proto with no client certificate -- the only flags that work before
// `qrdb init` has issued the admin credential. Mirrors sealNode.noAuthFlags.
func (n *sealNodeAllTransports) noAuthFlagsFor(proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", n.caFile,
		"--timeout", "30s",
	}
}

// initSystem bootstraps the node with `qrdb init` over proto, binding
// n.adminCert as the bootstrap admin (--admin-cert, the bring-your-own-
// certificate path: the node's own CA issued it, so no trust pool is
// needed). Mirrors sealNode.initSystem for the multi-transport harness.
func (n *sealNodeAllTransports) initSystem(t *testing.T, cli string, proto multiProtocol, extraArgs ...string) string {
	t.Helper()
	return bootAdminIdentityVia(t, cli, n.noAuthFlagsFor(proto), sealNodeSOPIN, sealNodeUserPIN, n.adminCert, n.adminKey, "",
		append([]string{"--admin-cert", n.adminCert}, extraArgs...)...)
}

// waitForFullBoot mirrors sealNode.waitForFullBoot for the multi-transport
// harness.
func (n *sealNodeAllTransports) waitForFullBoot(t *testing.T) {
	t.Helper()
	waitForHealthy(t, n.dataDir, 60*time.Second)
	waitForAdmin(t, n.ca.Pool, n.adminPort, 90*time.Second)
	waitForLeaderViaAdmin(t, n.ca, []int{n.adminPort}, 60*time.Second)
}

func (n *sealNodeAllTransports) endpointFor(proto multiProtocol) endpoint {
	return endpoint{serverURL: n.serverURLFor(proto), caFile: n.caFile}
}
func TestBarrierCLI_OfficerRevoke(t *testing.T) {
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBarrierOfficerRevokeOverProtocol(t, proto)
		})
	}
}

func testBarrierOfficerRevokeOverProtocol(t *testing.T, proto multiProtocol) {
	t.Helper()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)
	workDir := t.TempDir()
	const threshold, total = 2, 3
	configPath := writeSealConfig(t, "shamir", threshold, total)
	dataDir := t.TempDir()
	// Unix socket paths are limited to ~108 bytes by the kernel and
	// t.TempDir() can exceed that, so place the socket under a short,
	// unique /tmp path (matches drainNode's identical rationale).
	sockDir, err := os.MkdirTemp("", "qrdb-mp-")
	if err != nil {
		t.Fatalf("testBarrierOfficerRevokeOverProtocol: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	node := startSealNodeAllTransports(t, binary, dataDir, configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], unixSocket)
	ep := node.endpointFor(proto)

	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(proto), 60*time.Second)
	scope := systemScope(transport.DomainUnseal)

	// `qrdb init` enrols the holders from their CSRs and splits the unseal
	// key to them in the same call that issues the admin credential.
	holders, holderFlags := initHolders(t, workDir, "alice", "bob", "carol")
	initOut := node.initSystem(t, cli, proto, append([]string{"--barrier-strategy", "shamir",
		"--threshold", strconv.Itoa(threshold), "--shares", strconv.Itoa(total)}, holderFlags...)...)
	adoptInitHolders(t, workDir, initOut, holders)
	alice, bob, carol := holders[0], holders[1], holders[2]
	flags := node.adminFlagsFor(proto)

	node.waitForFullBoot(t)
	claimEnvelope(t, cli, workDir, ep, scope, alice)
	claimEnvelope(t, cli, workDir, ep, scope, bob)
	claimEnvelope(t, cli, workDir, ep, scope, carol)
	preRevokeCarol := *carol

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "officer", "revoke", "--username", "carol", "--threshold", strconv.Itoa(threshold), "--so-pin", sealNodeSOPIN)...); err != nil {
		t.Fatalf("barrier officer revoke carol: %v\nstderr: %s", err, stderr)
	}
	if out := quorumAll(t, cli, ep, scope, "rekey", alice, bob); !out.Complete {
		t.Fatalf("rekey round = %+v, want complete", out)
	}

	if _, _, err := quorumSubmit(t, cli, ep, scope, "unseal", &preRevokeCarol); err == nil {
		t.Fatal("a revoked holder's pre-revoke envelope was accepted")
	}

	claimEnvelope(t, cli, workDir, ep, scope, alice)
	claimEnvelope(t, cli, workDir, ep, scope, bob)
	quorumAll(t, cli, ep, scope, "seal", alice, bob)
	if status := barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatal("expected barrier to be sealed after the post-revoke seal quorum")
	}
	if out := quorumAll(t, cli, ep, scope, "unseal", alice, bob); !out.Complete {
		t.Fatalf("post-revoke unseal quorum = %+v, want complete", out)
	}

	if _, _, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "officer", "revoke", "--username", "never-enrolled", "--threshold", strconv.Itoa(threshold), "--so-pin", sealNodeSOPIN)...); err == nil {
		t.Fatal("revoking a never-enrolled holder succeeded, want error")
	}
}

func TestBarrierCLI_QuorumBegin(t *testing.T) {
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBarrierQuorumBeginOverProtocol(t, proto)
		})
	}
}

func testBarrierQuorumBeginOverProtocol(t *testing.T, proto multiProtocol) {
	t.Helper()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)
	workDir := t.TempDir()
	const threshold, total = 2, 2
	configPath := writeSealConfig(t, "shamir", threshold, total)
	dataDir := t.TempDir()
	// Unix socket paths are limited to ~108 bytes by the kernel and
	// t.TempDir() can exceed that, so place the socket under a short,
	// unique /tmp path (matches drainNode's identical rationale).
	sockDir, err := os.MkdirTemp("", "qrdb-mp-")
	if err != nil {
		t.Fatalf("testBarrierQuorumBeginOverProtocol: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	node := startSealNodeAllTransports(t, binary, dataDir, configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], unixSocket)
	ep := node.endpointFor(proto)

	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(proto), 60*time.Second)
	scope := systemScope(transport.DomainUnseal)

	// `qrdb init` enrols the holders from their CSRs and splits the unseal
	// key to them in the same call that issues the admin credential.
	holders, holderFlags := initHolders(t, workDir, "alice", "bob")
	initOut := node.initSystem(t, cli, proto, append([]string{"--barrier-strategy", "shamir",
		"--threshold", strconv.Itoa(threshold), "--shares", strconv.Itoa(total)}, holderFlags...)...)
	adoptInitHolders(t, workDir, initOut, holders)
	alice, bob := holders[0], holders[1]
	flags := node.adminFlagsFor(proto)

	node.waitForFullBoot(t)
	claimEnvelope(t, cli, workDir, ep, scope, alice)

	claimEnvelope(t, cli, workDir, ep, scope, bob)

	quorumAll(t, cli, ep, scope, "seal", alice, bob)

	challenge, _, err := quorumBegin(t, cli, ep, "unseal", "unseal", alice)
	if err != nil {
		t.Fatal(err)
	}
	id := challenge.ChallengeID
	if id == "" {
		t.Error("expected non-empty challenge id")
	}
	factorName := challenge.Factor
	if factorName != "mtls" {
		t.Errorf("factor = %q", factorName)
	}

	preStatus := barrierStatus(t, cli, flags)
	if !preStatus.Sealed {
		t.Error("expected barrier to still be sealed before the quorum")
	}

	quorumAll(t, cli, ep, scope, "unseal", alice, bob)
	postStatus := barrierStatus(t, cli, flags)
	if postStatus.Sealed {
		t.Error("expected barrier to be unsealed after the quorum")
	}
	bogusOp := "not-a-real-operation"

	if _, _, err := quorumBegin(t, cli, ep, "unseal", bogusOp, alice); err == nil {
		t.Error("quorum begin for an invalid op should fail")
	}
}

// officerListUsernames runs `barrier officer list --domain <domain>` using
// adminFlags and returns the enrolled usernames, proving the list verb's
// actual response shape (not just its exit code).
func officerListUsernames(t *testing.T, cli string, adminFlags []string, domain string) []string {
	t.Helper()
	args := append(append([]string{}, adminFlags...), "--output", "json",
		"barrier", "officer", "list", "--domain", domain)
	stdout, stderr, err := runCLISplit(t, cli, args...)
	if err != nil {
		t.Fatalf("barrier officer list --domain %s: %v\nstderr: %s", domain, err, stderr)
	}
	var officers []*transport.OfficerStatus
	if err := json.Unmarshal([]byte(stdout), &officers); err != nil {
		t.Fatalf("barrier officer list --domain %s: decode JSON: %v\noutput: %s", domain, err, stdout)
	}
	names := make([]string, 0, len(officers))
	for _, o := range officers {
		names = append(names, o.Username)
	}
	return names
}

// claimEnvelopeTwoStep claims off's envelope via the external-signer
// claim-begin/claim-complete split, as opposed to claimEnvelope's
// single-step `claim`: it signs the server-issued claim message itself
// (signNonceASN1) the way a key that signs outside this CLI would,
// proving the two-step handshake independently of the single-step path.
func claimEnvelopeTwoStep(t *testing.T, cli, dir string, ep endpoint, off *officer, recovery bool) {
	t.Helper()
	domain := "unseal"
	if recovery {
		domain = "recovery"
	}

	beginArgs := append(off.flags(ep), "--output", "json", "barrier", "officer", "claim-begin",
		"--domain", domain, "--username", off.username)
	stdout, stderr, err := runCLISplit(t, cli, beginArgs...)
	if err != nil {
		t.Fatalf("claim-begin %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
	var begin struct {
		NonceHex       string `json:"nonce_hex"`
		SignMessageHex string `json:"sign_message_hex"`
	}
	if err := json.Unmarshal([]byte(stdout), &begin); err != nil {
		t.Fatalf("claim-begin %s: decode JSON: %v\noutput: %s", off.username, err, stdout)
	}
	if begin.NonceHex == "" || begin.SignMessageHex == "" {
		t.Fatalf("claim-begin %s: empty nonce/message in response: %s", off.username, stdout)
	}

	msg, err := hex.DecodeString(begin.SignMessageHex)
	if err != nil {
		t.Fatalf("claim-begin %s: sign_message_hex not hex: %v", off.username, err)
	}
	sig := signNonceASN1(t, off.key, msg)
	sigPath := filepath.Join(dir, fmt.Sprintf("%s-%s-%d.sig", off.username, domain, time.Now().UnixNano()))
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		t.Fatalf("claim-begin %s: write sig file: %v", off.username, err)
	}

	off.envelope = filepath.Join(dir, fmt.Sprintf("%s-%s-%d.env", off.username, domain, time.Now().UnixNano()))
	completeArgs := append(off.flags(ep), "barrier", "officer", "claim-complete",
		"--domain", domain, "--username", off.username,
		"--nonce-hex", begin.NonceHex, "--sig-file", sigPath, "--out", off.envelope)
	if stdout, stderr, err := runCLISplit(t, cli, completeArgs...); err != nil {
		t.Fatalf("claim-complete %s: %v\nstdout:%s\nstderr:%s", off.username, err, stdout, stderr)
	}
}

// TestBarrierCLI_PassphraseLifecycle_PerProtocol is
// TestBarrierCLI_PassphraseLifecycle extended to all 5 CLI-selectable
// transports: passphrase barrier init, status, seal/unseal cycling
// (including a wrong-passphrase negative), and a KV write/read round trip,
// each proving the passphrase domain's commands gate the data plane
// identically regardless of transport.
func TestBarrierCLI_PassphraseLifecycle_PerProtocol(t *testing.T) {
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBarrierPassphraseLifecycleOverProtocol(t, proto)
		})
	}
}

func testBarrierPassphraseLifecycleOverProtocol(t *testing.T, proto multiProtocol) {
	t.Helper()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)

	configPath := writeSealConfig(t, "passphrase", 0, 0)
	dataDir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "qrdb-mp-")
	if err != nil {
		t.Fatalf("testBarrierPassphraseLifecycleOverProtocol: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3)

	node := startSealNodeAllTransports(t, binary, dataDir, configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], unixSocket)

	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(proto), 60*time.Second)

	status := barrierStatus(t, cli, node.noAuthFlagsFor(proto))
	if !status.Sealed {
		t.Fatalf("[%s] expected fresh passphrase barrier to be sealed pre-init, got: %+v", proto.name, status)
	}

	const passphrase = "correct-horse-battery-staple-1"
	node.initSystem(t, cli, proto, "--barrier-strategy", "passphrase", "--passphrase", passphrase)

	// Wait for the full host + RBAC-backed server to replace the pre-host
	// sealed-window listener; the admin credential `qrdb init` issued
	// authenticates from here on.
	node.waitForFullBoot(t)
	flags := node.adminFlagsFor(proto)

	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatalf("[%s] expected barrier to be unsealed immediately after init", proto.name)
	}
	if status.Strategy != "passphrase" {
		t.Errorf("[%s] expected strategy 'passphrase', got %q", proto.name, status.Strategy)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"kv", "put", "barrier-cli-key", "barrier-cli-value")...); err != nil {
		t.Fatalf("[%s] kv put: %v\nstderr: %s", proto.name, err, stderr)
	}
	stdout, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "kv", "get", "barrier-cli-key")...)
	if err != nil {
		t.Fatalf("[%s] kv get: %v\nstderr: %s", proto.name, err, stderr)
	}
	if !strings.Contains(stdout, "barrier-cli-value") {
		t.Fatalf("[%s] kv get: expected value in output, got: %s", proto.name, stdout)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "seal", "-y")...); err != nil {
		t.Fatalf("[%s] barrier seal: %v\nstderr: %s", proto.name, err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatalf("[%s] expected barrier to be sealed after 'barrier seal -y'", proto.name)
	}

	if _, _, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal", "--passphrase", "definitely-the-wrong-passphrase")...); err == nil {
		t.Fatalf("[%s] expected non-zero exit for wrong-passphrase unseal", proto.name)
	}
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatalf("[%s] wrong-passphrase unseal must not have left the barrier unsealed", proto.name)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "unseal", "--passphrase", passphrase)...); err != nil {
		t.Fatalf("[%s] barrier unseal: %v\nstderr: %s", proto.name, err, stderr)
	}
	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatalf("[%s] expected barrier to be unsealed after the correct passphrase", proto.name)
	}

	stdout, stderr, err = runCLISplit(t, cli, append(append([]string{}, flags...), "kv", "get", "barrier-cli-key")...)
	if err != nil {
		t.Fatalf("[%s] kv get after seal/unseal cycle: %v\nstderr: %s", proto.name, err, stderr)
	}
	if !strings.Contains(stdout, "barrier-cli-value") {
		t.Fatalf("[%s] kv get after seal/unseal cycle: expected surviving value, got: %s", proto.name, stdout)
	}
}

// TestBarrierCLI_ShamirLifecycle_PerProtocol is TestBarrierCLI_ShamirLifecycle
// extended to all 5 CLI-selectable transports: holder enrolment and listing,
// init-shamir, the one-time envelope claim over BOTH the single-step `claim`
// and the external-signer `claim-begin`/`claim-complete` split, the
// authenticated quorum for seal/unseal/root-token, the explicit `barrier
// rekey` round (with a negative proving the pre-rekey envelopes are
// revoked), and the independent recovery domain
// (generate/claim/recover/delete/status) with its own holders.
func TestBarrierCLI_ShamirLifecycle_PerProtocol(t *testing.T) {
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testBarrierShamirLifecycleOverProtocol(t, proto)
		})
	}
}

func testBarrierShamirLifecycleOverProtocol(t *testing.T, proto multiProtocol) {
	t.Helper()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)
	workDir := t.TempDir()

	const threshold, total = 2, 3
	configPath := writeSealConfig(t, "shamir", threshold, total)
	dataDir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "qrdb-mp-")
	if err != nil {
		t.Fatalf("testBarrierShamirLifecycleOverProtocol: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3)

	node := startSealNodeAllTransports(t, binary, dataDir, configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], unixSocket)
	ep := node.endpointFor(proto)

	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(proto), 60*time.Second)

	status := barrierStatus(t, cli, node.noAuthFlagsFor(proto))
	if !status.Sealed {
		t.Fatalf("[%s] expected fresh shamir barrier to be sealed pre-init, got: %+v", proto.name, status)
	}

	unsealScope := systemScope(transport.DomainUnseal)
	recoveryScope := systemScope(transport.DomainRecovery)

	// `qrdb init` enrols the holders from their CSRs and splits the unseal
	// key to them in the same call that issues the admin credential.
	holders, holderFlags := initHolders(t, workDir, "alice", "bob", "carol")
	initOut := node.initSystem(t, cli, proto, append([]string{"--barrier-strategy", "shamir",
		"--threshold", strconv.Itoa(threshold), "--shares", strconv.Itoa(total)}, holderFlags...)...)
	adoptInitHolders(t, workDir, initOut, holders)
	alice, bob := holders[0], holders[1]
	flags := node.adminFlagsFor(proto)

	status = barrierStatus(t, cli, flags)
	if status.Sealed {
		t.Fatalf("[%s] expected barrier to be unsealed immediately after qrdb init", proto.name)
	}
	if status.Strategy != "shamir" {
		t.Errorf("[%s] expected strategy %q, got %q", proto.name, "shamir", status.Strategy)
	}

	node.waitForFullBoot(t)

	if names := officerListUsernames(t, cli, flags, "unseal"); !contains(names, "alice") || !contains(names, "bob") || !contains(names, "carol") {
		t.Fatalf("[%s] barrier officer list --domain unseal = %v, want alice/bob/carol", proto.name, names)
	}

	claimEnvelope(t, cli, workDir, ep, unsealScope, alice)
	claimEnvelopeTwoStep(t, cli, workDir, ep, bob, false)
	firstAlice, firstBob := *alice, *bob

	quorumAll(t, cli, ep, unsealScope, "seal", alice, bob)
	status = barrierStatus(t, cli, flags)
	if !status.Sealed {
		t.Fatalf("[%s] expected barrier to be sealed after the seal quorum", proto.name)
	}

	out := quorumAll(t, cli, ep, unsealScope, "unseal", alice)
	if out.Complete || out.Submitted != 1 || out.Required != threshold {
		t.Fatalf("[%s] one of two submissions = %+v, want 1/2 incomplete", proto.name, out)
	}
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatalf("[%s] a below-threshold round must not unseal the barrier", proto.name)
	}
	if _, _, err := quorumSubmit(t, cli, ep, unsealScope, "unseal", alice); err == nil {
		t.Fatalf("[%s] a second submission from the same holder was accepted", proto.name)
	}
	if out = quorumAll(t, cli, ep, unsealScope, "unseal", bob); !out.Complete {
		t.Fatalf("[%s] second holder submission = %+v, want complete", proto.name, out)
	}
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatalf("[%s] expected barrier to be unsealed after the unseal quorum", proto.name)
	}

	out = quorumAll(t, cli, ep, unsealScope, "root-token", alice, bob)
	if !out.Complete || out.RootToken == "" {
		t.Fatalf("[%s] root-token round = %+v, want a root token", proto.name, out)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "rekey", "--threshold", strconv.Itoa(threshold), "--so-pin", sealNodeSOPIN)...); err != nil {
		t.Fatalf("[%s] barrier rekey: %v\nstderr: %s", proto.name, err, stderr)
	}
	if out = quorumAll(t, cli, ep, unsealScope, "rekey", alice, bob); !out.Complete {
		t.Fatalf("[%s] rekey round = %+v, want complete", proto.name, out)
	}
	claimEnvelope(t, cli, workDir, ep, unsealScope, alice)
	claimEnvelope(t, cli, workDir, ep, unsealScope, bob)

	quorumAll(t, cli, ep, unsealScope, "seal", alice, bob)
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatalf("[%s] expected barrier to be sealed after the post-rekey seal quorum", proto.name)
	}

	quorumAll(t, cli, ep, unsealScope, "unseal", &firstAlice)
	if _, _, err := quorumSubmit(t, cli, ep, unsealScope, "unseal", &firstBob); err == nil {
		t.Fatalf("[%s] a quorum of pre-rekey envelopes completed", proto.name)
	}
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatalf("[%s] revoked envelopes must not unseal the barrier", proto.name)
	}

	quorumAll(t, cli, ep, unsealScope, "unseal", alice, bob)
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatalf("[%s] expected barrier to be unsealed with the post-rekey envelopes", proto.name)
	}

	dave := registerOfficer(t, cli, workDir, flags, recoveryScope, "dave")
	erin := registerOfficer(t, cli, workDir, flags, recoveryScope, "erin")

	// `barrier officer list` serves only the unseal domain
	// (systemHolders.list refuses --domain recovery outright,
	// cmd/qrdb/barrier_holders.go); that refusal is itself part of the
	// command's contract and must hold on every transport.
	if _, _, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "officer", "list", "--domain", "recovery")...); err == nil {
		t.Fatalf("[%s] barrier officer list --domain recovery unexpectedly succeeded", proto.name)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...),
		"barrier", "recovery", "generate", "--threshold", "2", "--so-pin", sealNodeSOPIN)...); err != nil {
		t.Fatalf("[%s] barrier recovery generate: %v\nstderr: %s", proto.name, err, stderr)
	}
	if !barrierHasRecoveryKeys(t, cli, flags) {
		t.Fatalf("[%s] expected recovery keys to be present after generate", proto.name)
	}

	claimEnvelope(t, cli, workDir, ep, recoveryScope, dave)
	claimEnvelopeTwoStep(t, cli, workDir, ep, erin, true)

	quorumAll(t, cli, ep, unsealScope, "seal", alice, bob)
	if status = barrierStatus(t, cli, flags); !status.Sealed {
		t.Fatalf("[%s] expected barrier to be sealed before exercising recover", proto.name)
	}

	if out = quorumAll(t, cli, ep, recoveryScope, "recover", dave, erin); !out.Complete {
		t.Fatalf("[%s] recover round = %+v, want complete", proto.name, out)
	}
	if status = barrierStatus(t, cli, flags); status.Sealed {
		t.Fatalf("[%s] expected barrier to be unsealed after the recover quorum", proto.name)
	}

	if _, stderr, err := runCLISplit(t, cli, append(append([]string{}, flags...), "barrier", "recovery", "delete")...); err != nil {
		t.Fatalf("[%s] barrier recovery delete: %v\nstderr: %s", proto.name, err, stderr)
	}
	if barrierHasRecoveryKeys(t, cli, flags) {
		t.Fatalf("[%s] expected recovery keys to be absent after delete", proto.name)
	}
}
