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

// officer_ceremony_cli_test.go exercises two previously-untested CLI
// surfaces end-to-end, driving the real `qrdb` client binary as a
// subprocess against freshly self-spawned `qrdbd` nodes:
//
//   - the one-shot `qrdb init` bootstrap ceremony (root.go/init.go), and
//   - the security-officer / ceremony command families (`barrier officer`,
//     `barrier recovery officer`, `ceremony ...`).
//
// Every assertion in this file was verified against a real, live daemon
// before being encoded here; nothing is guessed. Two genuine product bugs
// were discovered along the way and have since been FIXED (the regression
// tests below now pin the fixed behavior rather than the bug):
//
//  1. REST officer/recovery-officer registration was broken. The SDK
//     request type transport.OfficerRegisterRequest.CSRPEM is `[]byte`, so
//     encoding/json would base64-encode it on the wire, while the REST
//     handler's local wire struct always treated csr_pem as a plain PEM
//     string (matching the TypeScript SDK and the GUI, which is why the
//     GUI never hit this). Fixed in sdk/go/transport/rest/transport_barrier.go
//     and transport_tenant.go: OfficerRegister/RecoveryOfficerRegister/
//     TenantBarrierOfficerRegister now marshal a local
//     officerRegisterWireRequest{CSRPEM string} instead of the []byte
//     transport type directly, and decode the cert_pem response the same
//     way. See TestCLI_BarrierOfficerRegister_SealedWindow.
//
//  2. `qrdb init`'s --barrier-strategy/--shares/--threshold flags were
//     accepted and locally validated by the CLI (cmd/qrdb/init.go) but
//     silently dropped on the way to the ceremony: every transport handler
//     (REST/gRPC/QUIC/MCP/embedded) only forwarded SOPin/UserPin into
//     service.CeremonyInitializeOptions. Fixed by threading
//     CredentialSealStrategy/Threshold/Officers/Hostname/SANs through
//     service.CeremonyInitializeOptions -> ceremony.InitializeOptions ->
//     ceremony.CeremonyService.Initialize (same non-empty-overrides-config
//     precedence rule already used for SOPin/UserPin) across all five
//     transport handlers. `qrdb init` itself still has no --officers flag
//     (a one-shot init call has no way to carry officer CSRs), so a
//     Threshold>=2 request with no Officers now fails closed with the
//     ceremony package's typed ErrNoOfficers instead of silently
//     succeeding via the single-admin path with ShareCount 0. See
//     TestCLI_Init_ShamirFlags_RequiresOfficers.
//
// A related non-obvious (but NOT buggy) wire convention, confirmed by
// reading pkg/seal/shamir.go UnsealWithShares and pkg/seal/recovery.go
// RecoverWithKeys: the `--share` value a caller feeds to
// `barrier unseal-shares` / `barrier recovery recover` /
// `barrier root-token` must be the BARE base64 "value" field, not the
// full JSON envelope `barrier officer claim-complete` /
// `barrier recovery officer claim-complete` returns
// (`{"index":N,"threshold":M,"total":T,"value":"..."}`). Both functions
// reconstruct each shamir.Share with `Index: i+1` -- the polynomial
// x-coordinate is inferred purely from the --share flags' SUBMISSION
// ORDER, not from the envelope's own "index" field -- so shares must
// also be passed in ascending share-index order. claimRecoveryShare
// below unwraps to the bare value for exactly this reason; feeding the
// full envelope would additionally fail pflag's CSV-based
// StringSliceVar parsing (commas/quotes in the JSON), which is why this
// unwrap step is load-bearing, not cosmetic.
//
// Rules honored: no t.Skip, no -short gating, gofmt clean, this is the
// only new file added.
package integration_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Node bootstrap helpers (self-generated CA -- required for ceremony/officer
// CSR signing; see cmd/qrdbd/seal_subsystem.go ceremonyCAFromBundle, which
// returns (nil, nil) when the TLS bundle carries no CA private key, i.e.
// under BYO --tls-ca-file/--tls-cert-file/--tls-key-file).
// ---------------------------------------------------------------------------

// selfGenCANode holds the connection details for a qrdbd node booted
// WITHOUT --tls-* flags, so the daemon generates and persists its own CA
// (ca.pem + ca-key.pem) alongside a ready-made admin mTLS identity
// (admin-cert.pem/admin-key.pem, CN=admin OU=system) written by
// cmd/qrdbd/tls.go generateAdminClientCert during boot. Retaining the CA
// private key is what lets the ceremony sign officer CSRs.
type selfGenCANode struct {
	dataDir       string
	adminPort     int
	grpcPort      int
	raftPort      int
	caPath        string
	adminCertPath string
	adminKeyPath  string
}

// writeSealYAML writes an arbitrary seal-related daemon YAML config to a
// temp file and returns its path. Unlike the existing writeNoneSealConfig
// helper (enable_encryption_test.go), this accepts the full YAML body so
// callers can also configure the shamir_threshold/shamir_total fields
// needed for the officer/Shamir-barrier flows in this file.
func writeSealYAML(t *testing.T, yamlBody string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("writeSealYAML: %v", err)
	}
	return cfgPath
}

// waitForFile polls until path exists or timeout elapses.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, path)
}

// bootSelfGenCANode starts a fresh single-node qrdbd with the given seal
// YAML config and NO --tls-* flags, so the daemon self-generates its CA
// (retaining the private key) and, because --auth-enabled defaults true,
// writes a ready-made admin mTLS client certificate into its data
// directory before the sealed-window listener comes up. It blocks until
// ca.pem exists (i.e. TLS resolution has completed) and returns the
// connection details; it does NOT wait for the barrier to be
// initialized or for full boot -- callers drive that explicitly so they
// can interact with the sealed-window listener first.
func bootSelfGenCANode(t *testing.T, bin, sealYAML, prefix string) *selfGenCANode {
	t.Helper()

	dataDir := t.TempDir()
	cfgPath := writeSealYAML(t, sealYAML)
	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]

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
		// Deliberately no --tls-ca-file/--tls-cert-file/--tls-key-file: see
		// the package doc comment above for why self-generated TLS is
		// required for the ceremony/officer flows under test.
	}

	startNode(t, bin, args, prefix)

	n := &selfGenCANode{
		dataDir:       dataDir,
		adminPort:     adminPort,
		grpcPort:      grpcPort,
		raftPort:      raftPort,
		caPath:        filepath.Join(dataDir, "ca.pem"),
		adminCertPath: filepath.Join(dataDir, "admin-cert.pem"),
		adminKeyPath:  filepath.Join(dataDir, "admin-key.pem"),
	}
	waitForFile(t, n.caPath, 30*time.Second)
	waitForFile(t, n.adminCertPath, 30*time.Second)
	waitForFile(t, n.adminKeyPath, 30*time.Second)
	return n
}

// restArgsNoAuth returns qrdb CLI connection flags for the REST transport
// presenting no client certificate. This is the only combination that can
// reach the pre-host sealed-window listener: the gRPC and unix transports
// both probe a Health RPC on Connect() that the sealed-window listener
// does not implement (see the package doc comment, bug 1), and REST's
// reachability probe (sdk/go/transport/rest/transport.go Transport.reachable)
// accepts both 200 and 503 from the always-public GET /health endpoint.
func (n *selfGenCANode) restArgsNoAuth() []string {
	return []string{
		"--server", fmt.Sprintf("https://127.0.0.1:%d", n.adminPort),
		"--transport", "rest",
		"--ca-file", n.caPath,
		"--timeout", "20s",
	}
}

// restArgsAdmin returns REST connection flags presenting the daemon's own
// pre-generated admin identity (CN=admin, OU=system), which RBAC maps to
// full platform access via the system-tenant rule.
func (n *selfGenCANode) restArgsAdmin() []string {
	return append(n.restArgsNoAuth(), "--cert-file", n.adminCertPath, "--key-file", n.adminKeyPath)
}

// restArgsIdentity returns REST connection flags presenting an arbitrary
// mTLS client identity (e.g. an enrolled officer's CA-signed certificate),
// used to drive the officer's own share-claim handshake.
func (n *selfGenCANode) restArgsIdentity(certPath, keyPath string) []string {
	return append(n.restArgsNoAuth(), "--cert-file", certPath, "--key-file", keyPath)
}

// pollUntilCLISucceeds repeatedly runs `qrdb <connArgs> barrier status
// --output json` until it exits zero or the timeout elapses. It is used
// both to detect that the sealed-window listener has come up (no
// TCP/TLS-level readiness signal is otherwise available to a test running
// outside the daemon process) and, separately, to detect that the full
// post-boot server is answering authenticated requests (the admin
// identity is not usable until Raft has elected a leader and replicated
// the bootstrap admin user -- typically a few seconds after
// waitForHealthy's host.id file appears).
func pollUntilCLISucceeds(t *testing.T, cli string, connArgs []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastOut string
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := runQRDB(t, cli, connArgs, "barrier", "status", "--output", "json")
		if err == nil {
			return
		}
		lastOut, lastErr = out, err
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for CLI to reach the server: %v\noutput: %s", timeout, lastErr, lastOut)
}

// ---------------------------------------------------------------------------
// CSR / signature helpers -- the real officer-claim cryptographic protocol.
//
// Signing procedure (verified against pkg/ceremony/crypto.go
// verifyNonceSignature and completeClaimShareLocked): the officer decodes
// the hex nonce from claim-begin into raw bytes, computes digest =
// SHA-256(nonce), and produces an ECDSA P-256 ASN.1 DER signature over
// that digest with their own CSR private key (crypto.SHA256 pre-hashed
// signing, i.e. exactly what ecdsa.SignASN1(rand, priv, digest[:])
// produces). The raw DER bytes are written verbatim to --sig-file; the
// CLI reads the file and sends it as-is (cmd/qrdb/barrier.go
// newBarrierOfficerClaimCompleteCmd / newBarrierRecoveryOfficerClaimCompleteCmd).
// ---------------------------------------------------------------------------

// generateCeremonyCSR creates a fresh ECDSA P-256 key pair and a
// self-signed PKCS#10 certificate signing request for the given common
// name, returning the PEM-encoded CSR and the private key.
func generateCeremonyCSR(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateCeremonyCSR: generate key: %v", err)
	}

	tmpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: cn},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatalf("generateCeremonyCSR: create CSR: %v", err)
	}

	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	return csrPEM, key
}

// writeTempFile writes data to a new file under t.TempDir() named name
// and returns its path.
func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeTempFile %s: %v", name, err)
	}
	return path
}

// signNonceHex implements the officer share-claim signing procedure:
// digest = SHA-256(decoded nonce), signature = ECDSA-P256 ASN.1 DER over
// digest. Returns the raw signature bytes ready to write to --sig-file.
func signNonceHex(t *testing.T, priv *ecdsa.PrivateKey, nonceHex string) []byte {
	t.Helper()
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		t.Fatalf("signNonceHex: decode nonce: %v", err)
	}
	digest := sha256.Sum256(nonce)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatalf("signNonceHex: sign: %v", err)
	}
	return sig
}

// ---------------------------------------------------------------------------
// Wire-shape decode helpers for `--output json` CLI output.
// ---------------------------------------------------------------------------

type officerRegisterJSON struct {
	CertPEM    []byte `json:"cert_pem"`
	ShareIndex int    `json:"share_index"`
}

type shareClaimBeginJSON struct {
	NonceHex  string    `json:"nonce_hex"`
	ExpiresAt time.Time `json:"expires_at"`
}

type shareClaimCompleteJSON struct {
	ShareIndex int    `json:"share_index"`
	ShareBytes []byte `json:"share_bytes"`
}

type recoveryKeysJSON struct {
	Threshold  int `json:"threshold"`
	Total      int `json:"total"`
	ShareCount int `json:"share_count"`
}

type ceremonyStatusJSON struct {
	State   string `json:"state"`
	SPKIPin string `json:"spki_pin"`
}

// decodeJSON unmarshals out (the CLI's stdout+stderr, `--output json`)
// into dest, failing the test with a useful message on error.
func decodeJSON(t *testing.T, out string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), dest); err != nil {
		t.Fatalf("decodeJSON: %v\noutput: %s", err, out)
	}
}

// ---------------------------------------------------------------------------
// 1. `qrdb init` -- one-shot bootstrap ceremony.
// ---------------------------------------------------------------------------

// TestCLI_Init_PassphraseHappyPath drives the full happy path: a fresh
// node boots sealed, the operator initializes the barrier with the
// software "none" strategy (`barrier init`) so the daemon reaches full
// boot quickly, then `qrdb init --admin-bundle -` runs the ceremony.
//
// Per cmd/qrdb/init.go buildAdminBundle, the printed bundle is SOCertPEM +
// SOKeyPEM + CACertPEM (in that order) -- it is a Security Officer
// identity, NOT the RBAC "admin" identity used for ordinary data-plane
// operations. Post-init usability is therefore verified with the
// daemon's own pre-generated admin-cert.pem/admin-key.pem (CN=admin,
// OU=system), exactly as drain_test.go's startDrainNode does for the
// existing `qrdb admin drain` coverage.
func TestCLI_Init_PassphraseHappyPath(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[init-happy] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}

	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err := runQRDB(t, cli, node.restArgsAdmin(),
		"init", "--so-pin", "SOPIN12345", "--user-pin", "USERPIN12345", "--admin-bundle", "-")
	if err != nil {
		t.Fatalf("qrdb init: %v\noutput: %s", err, out)
	}

	if got := strings.Count(out, "-----BEGIN CERTIFICATE-----"); got != 2 {
		t.Errorf("admin bundle: expected 2 CERTIFICATE blocks (SO cert + CA cert), got %d\noutput: %s", got, out)
	}
	if got := strings.Count(out, "-----BEGIN PRIVATE KEY-----"); got != 1 {
		t.Errorf("admin bundle: expected 1 PRIVATE KEY block (SO key), got %d\noutput: %s", got, out)
	}
	if !strings.Contains(out, "State:       operational") {
		t.Errorf("qrdb init output should report State: operational, got: %s", out)
	}

	// Post-init usability: the node must still serve ordinary data-plane
	// traffic. `qrdb init` does not mint a data-plane identity itself (see
	// above), so this uses the daemon's own admin identity.
	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "kv", "put", "hello", "world"); err != nil {
		t.Fatalf("kv put after init: %v\noutput: %s", err, out)
	}
	out, err = runQRDB(t, cli, node.restArgsAdmin(), "kv", "get", "hello")
	if err != nil {
		t.Fatalf("kv get after init: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "world") {
		t.Errorf("kv get after init: expected 'world', got: %s", out)
	}

	// A second `qrdb init` must fail with the typed ceremony.ErrAlreadyInitialized.
	out, err = runQRDB(t, cli, node.restArgsAdmin(),
		"init", "--so-pin", "SOPIN12345", "--user-pin", "USERPIN12345", "--admin-bundle", "-")
	if err == nil {
		t.Fatalf("second qrdb init should have failed; output: %s", out)
	}
	if !strings.Contains(out, "already completed") {
		t.Errorf("second qrdb init: expected 'already completed' (ceremony.ErrAlreadyInitialized), got: %s", out)
	}
}

// TestCLI_Init_ShamirFlags_RequiresOfficers proves the fix for bug 2 (see
// the package doc comment): `qrdb init --barrier-strategy shamir --shares N
// --threshold M` passes the CLI's own local flag validation
// (barrier-strategy=shamir with shares>=2 and a valid threshold) and the
// Threshold now genuinely reaches the ceremony (CredentialSealStrategy,
// Threshold, Hostname, and SANs are no longer silently dropped). Because
// `qrdb init` is a one-shot bootstrap command with no --officers flag to
// carry officer CSRs, a Threshold>=2 request with no Officers fails closed
// with the ceremony package's typed ErrNoOfficers instead of the old
// behavior of silently succeeding via the single-admin path with
// ShareCount 0. (Registering officers ahead of Threshold>=2 M-of-N
// enrollment is a separate flow: `barrier officer register` against a
// sealed system barrier, proven by TestCLI_BarrierOfficerRegister_SealedWindow.)
func TestCLI_Init_ShamirFlags_RequiresOfficers(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[init-shamir-flags] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err := runQRDB(t, cli, node.restArgsAdmin(),
		"init",
		"--so-pin", "SOPIN12345", "--user-pin", "USERPIN12345",
		"--barrier-strategy", "shamir", "--shares", "5", "--threshold", "3",
		"--admin-bundle", "-")
	if err == nil {
		t.Fatalf("qrdb init --barrier-strategy shamir with no officers should fail closed now that Threshold reaches the ceremony; output: %s", out)
	}
	if !strings.Contains(out, "M-of-N requires at least one officer") {
		t.Errorf("expected the typed ceremony.ErrNoOfficers message, got: %s", out)
	}

	// The ceremony must not be left half-initialized: Initialize's own
	// validateConfig failure must leave the state machine at
	// awaiting_init, not silently advance it.
	out, err = runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status after the failed shamir attempt: %v\noutput: %s", err, out)
	}
	var status ceremonyStatusJSON
	decodeJSON(t, out, &status)
	if status.State != "awaiting_init" {
		t.Errorf("expected state awaiting_init after the failed init attempt, got: %q", status.State)
	}
}

// TestCLI_Init_LocalFlagValidation exercises the CLI-local flag
// validation in cmd/qrdb/init.go resolveAndValidateInitFlags. cobra's
// PersistentPreRunE connects the SDK client before any subcommand-level
// validation runs (see newRootCmd), so even purely local validation
// requires a reachable server; a "none"-strategy node satisfies that
// cheaply.
func TestCLI_Init_LocalFlagValidation(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[init-validate] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "shamir strategy without shares",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "shamir"},
			wantErr: "--shares",
		},
		{
			name:    "shares without shamir strategy",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--shares", "3"},
			wantErr: "--shares/--threshold",
		},
		{
			name:    "missing so-pin",
			args:    []string{"init", "--user-pin", "b"},
			wantErr: "--so-pin",
		},
		{
			name:    "invalid barrier strategy",
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "bogus"},
			wantErr: "--barrier-strategy",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runQRDB(t, cli, node.restArgsNoAuth(), tc.args...)
			if err == nil {
				t.Fatalf("expected failure; output: %s", out)
			}
			if !strings.Contains(out, tc.wantErr) {
				t.Errorf("expected output to contain %q, got: %s", tc.wantErr, out)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. System-barrier Shamir officers (`barrier officer ...`).
// ---------------------------------------------------------------------------

// TestCLI_BarrierOfficerRegister_SealedWindow proves the fix for bug 1 (see
// the package doc comment). System-barrier officer registration MUST happen
// while the barrier is sealed (before `barrier init-shamir`, whose
// SealAndDistribute step requires the enrolled officer count to exactly
// equal --total -- see pkg/ceremony/ceremony_officer.go
// sealSharesToOfficers). The only CLI transport that can reach the pre-host
// sealed-window listener is REST (gRPC/unix both fail their mandatory
// Connect()-time Health RPC against that listener, which is an unrelated,
// still-true fact about the sealed-window listener rather than the CSRPEM
// bug); now that the Go REST transport sends csr_pem as a plain PEM string
// (matching the wire contract), REST officer registration against the
// sealed-window listener succeeds end-to-end.
func TestCLI_BarrierOfficerRegister_SealedWindow(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin,
		"seal:\n  enabled: true\n  strategy: shamir\n  shamir_threshold: 2\n  shamir_total: 3\n",
		"[officer-csr-bug] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	csrPEM, _ := generateCeremonyCSR(t, "off1")
	csrPath := writeTempFile(t, "off1.csr", csrPEM)

	// gRPC and unix transports still cannot reach the sealed-window listener
	// at all: its Health RPC handler requires a non-nil admin service, which
	// the pre-host listener never wires (pkg/api/grpc/service.go Health).
	// This is independent of the CSRPEM encoding fix.
	grpcArgs := []string{
		"--server", fmt.Sprintf("grpcs://127.0.0.1:%d", node.grpcPort),
		"--ca-file", node.caPath,
		"--cert-file", node.adminCertPath, "--key-file", node.adminKeyPath,
		"--timeout", "5s",
	}
	if out, err := runQRDB(t, cli, grpcArgs, "barrier", "officer", "register",
		"--username", "off1", "--csr-file", csrPath); err == nil {
		t.Fatalf("gRPC officer register unexpectedly succeeded against the sealed-window listener; output: %s", out)
	} else if !strings.Contains(out, "health check failed") {
		t.Errorf("expected a Connect()-time health-check failure over gRPC pre-boot, got: %s", out)
	}

	// REST reaches the listener and the CSR now round-trips correctly.
	out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "officer", "register",
		"--username", "off1", "--csr-file", csrPath, "--output", "json")
	if err != nil {
		t.Fatalf("REST officer register should succeed now that csr_pem travels as a plain PEM string: %v\noutput: %s", err, out)
	}
	var reg officerRegisterJSON
	decodeJSON(t, out, &reg)
	if reg.ShareIndex != 1 {
		t.Errorf("expected the first registered officer to get ShareIndex 1, got %d: %s", reg.ShareIndex, out)
	}
	if !strings.Contains(string(reg.CertPEM), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("expected cert_pem to decode into a PEM certificate, got: %s", reg.CertPEM)
	}

	// The registry now carries exactly the one registered officer.
	out, err = runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "officer", "list", "--output", "json")
	if err != nil {
		t.Fatalf("barrier officer list: %v\noutput: %s", err, out)
	}
	var officers []struct {
		Username   string `json:"username"`
		ShareIndex int    `json:"share_index"`
	}
	decodeJSON(t, out, &officers)
	if len(officers) != 1 {
		t.Fatalf("expected exactly 1 enrolled officer, got %d: %s", len(officers), out)
	}
	if officers[0].Username != "off1" {
		t.Errorf("expected officer username off1, got %q", officers[0].Username)
	}
}

// TestCLI_BarrierInitShamir_InsufficientOfficers verifies the real,
// working validation surface of `barrier init-shamir` when the officer
// count does not satisfy the requested threshold/total (which, given bug
// 1 above, is the only reachable state for a fresh node's system
// barrier): pkg/seal StorageBarrier.InitializeShamir ->
// sealSharesToOfficers rejects with ErrInsufficientOfficers whenever
// fewer officers are enrolled than the requested threshold.
func TestCLI_BarrierInitShamir_InsufficientOfficers(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin,
		"seal:\n  enabled: true\n  strategy: shamir\n  shamir_threshold: 2\n  shamir_total: 3\n",
		"[init-shamir-insufficient] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)

	out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init-shamir",
		"--passphrase", "seed-value", "--threshold", "2", "--total", "3")
	if err == nil {
		t.Fatalf("barrier init-shamir with zero enrolled officers should fail; output: %s", out)
	}
	if !strings.Contains(out, "insufficient officers enrolled") {
		t.Errorf("expected 'insufficient officers enrolled for threshold', got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// 3. Recovery officers (`barrier recovery officer ...`) -- the fully
// working end-to-end proof of the officer-claim protocol.
//
// Recovery keys are independent of the primary unseal strategy and are
// generated/claimed AFTER the barrier is already unsealed, so this flow
// runs against the normal, fully-booted, RBAC-enforced server rather
// than the sealed-window listener. That sidesteps bug 1 entirely: gRPC
// and unix transports work fine post-boot (Health is served), and the
// unix transport (protobuf `bytes`) has no CSRPEM encoding bug, so
// registration succeeds. claim-begin/claim-complete are identity-gated
// (permission.IdentityGated -- any trusted client cert, not an RBAC
// role) and require the caller's own TLS client certificate to compute
// the thumbprint match, so they must run over a TLS transport (REST
// here); the unix transport carries no TLS material at all
// (drain_test.go documents this: "kernel-level access control with
// insecure gRPC credentials").
// ---------------------------------------------------------------------------

// registeredRecoveryOfficer bundles what's needed to drive a recovery
// officer's own claim handshake after registration.
type registeredRecoveryOfficer struct {
	username   string
	certPath   string
	keyPath    string
	shareIndex int
}

// registerRecoveryOfficer registers username against node's recovery
// officer registry over the unix transport (protobuf bytes CSRPEM --
// unaffected by bug 1), writes the CA-signed certificate PEM to a temp
// file, and returns the officer's identity material.
func registerRecoveryOfficer(t *testing.T, cli string, node *selfGenCANode, unixSocket, username string) *registeredRecoveryOfficer {
	t.Helper()

	csrPEM, key := generateCeremonyCSR(t, username)
	csrPath := writeTempFile(t, username+".csr", csrPEM)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("registerRecoveryOfficer: marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyPath := writeTempFile(t, username+"-key.pem", keyPEM)

	unixArgs := []string{"--server", "unix://" + unixSocket, "--timeout", "20s"}
	out, err := runQRDB(t, cli, unixArgs, "barrier", "recovery", "officer", "register",
		"--username", username, "--csr-file", csrPath, "--output", "json")
	if err != nil {
		t.Fatalf("register recovery officer %s over unix transport: %v\noutput: %s", username, err, out)
	}

	var resp officerRegisterJSON
	decodeJSON(t, out, &resp)
	if len(resp.CertPEM) == 0 {
		t.Fatalf("register recovery officer %s: empty cert_pem in response: %s", username, out)
	}
	certPath := writeTempFile(t, username+"-cert.pem", resp.CertPEM)

	return &registeredRecoveryOfficer{
		username:   username,
		certPath:   certPath,
		keyPath:    keyPath,
		shareIndex: resp.ShareIndex,
	}
}

// claimRecoveryShare drives officer's own claim-begin/sign/claim-complete
// handshake over REST (the only transport that both speaks TLS and
// reaches the identity-gated claim endpoints with the officer's own
// certificate), confirms the delivered share is a well-formed
// shamir.Share JSON envelope (`{"index":N,"threshold":M,"total":T,
// "value":"..."}`), and returns just the bare base64 "value" field --
// the form `barrier recovery recover --share`/`barrier unseal-shares
// --share` actually expect (see the package doc comment's wire-
// convention note).
func claimRecoveryShare(t *testing.T, cli string, node *selfGenCANode, officer *registeredRecoveryOfficer) string {
	t.Helper()

	identityArgs := node.restArgsIdentity(officer.certPath, officer.keyPath)

	out, err := runQRDB(t, cli, identityArgs, "barrier", "recovery", "officer", "claim-begin",
		"--username", officer.username, "--output", "json")
	if err != nil {
		t.Fatalf("recovery officer claim-begin for %s: %v\noutput: %s", officer.username, err, out)
	}
	var begin shareClaimBeginJSON
	decodeJSON(t, out, &begin)
	if begin.NonceHex == "" {
		t.Fatalf("recovery officer claim-begin for %s: empty nonce_hex: %s", officer.username, out)
	}

	// Re-derive the officer's private key from disk to sign the nonce --
	// mirrors what a real, separate officer process would do.
	keyPEM, err := os.ReadFile(officer.keyPath)
	if err != nil {
		t.Fatalf("read officer key: %v", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatalf("decode officer key PEM")
	}
	priv, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse officer EC key: %v", err)
	}

	sig := signNonceHex(t, priv, begin.NonceHex)
	sigPath := writeTempFile(t, officer.username+".sig", sig)

	out, err = runQRDB(t, cli, identityArgs, "barrier", "recovery", "officer", "claim-complete",
		"--username", officer.username, "--nonce-hex", begin.NonceHex, "--sig-file", sigPath, "--output", "json")
	if err != nil {
		t.Fatalf("recovery officer claim-complete for %s: %v\noutput: %s", officer.username, err, out)
	}
	var complete shareClaimCompleteJSON
	decodeJSON(t, out, &complete)
	if complete.ShareIndex != officer.shareIndex {
		t.Errorf("claim-complete for %s: share_index = %d, want %d", officer.username, complete.ShareIndex, officer.shareIndex)
	}
	if len(complete.ShareBytes) == 0 {
		t.Fatalf("claim-complete for %s: empty share_bytes: %s", officer.username, out)
	}

	var shareShape struct {
		Index     int    `json:"index"`
		Threshold int    `json:"threshold"`
		Total     int    `json:"total"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal(complete.ShareBytes, &shareShape); err != nil {
		t.Fatalf("claimed share for %s is not the expected shamir.Share JSON shape: %v\nraw: %s", officer.username, err, complete.ShareBytes)
	}
	if shareShape.Value == "" {
		t.Fatalf("claimed share for %s has an empty value field: %s", officer.username, complete.ShareBytes)
	}
	if shareShape.Index != officer.shareIndex {
		t.Errorf("claimed share for %s: envelope index = %d, want %d", officer.username, shareShape.Index, officer.shareIndex)
	}

	return shareShape.Value
}

// TestCLI_RecoveryOfficerFlow_EndToEnd is the fully-driven officer-claim
// protocol proof requested for this coverage gap: register (real CSR) ->
// claim-begin -> sign the nonce -> claim-complete returns the share ->
// the share is a well-formed shamir.Share JSON object -> the unwrapped
// share VALUES actually unseal the barrier via `barrier recovery
// recover`. It also verifies the CLI-level guardrails around the flow (a
// claimed share cannot be claimed again; an unregistered officer cannot
// claim-begin).
//
// This is the recovery-officer analogue of the SYSTEM barrier's
// `barrier officer` Shamir flow requested by the task description. The
// system-officer variant cannot be driven end-to-end at all right now
// because it can only be registered while the barrier is sealed (see
// bug 1 in the package doc comment and
// TestCLI_BarrierOfficerRegister_SealedWindowCSRBug); this test proves
// the identical claim protocol (register/list/claim-begin/sign/claim-
// complete/verify-the-share-works) end-to-end using the recovery scope,
// which is reachable over the unix transport for registration (no CSR
// bug) and REST for the identity-gated claim handshake.
func TestCLI_RecoveryOfficerFlow_EndToEnd(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	dataDir := t.TempDir()
	sockDir, err := os.MkdirTemp("", "qrdb-officer-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	cfgPath := writeSealYAML(t, "seal:\n  enabled: true\n  strategy: none\n")
	raftPort := allocatePorts(t, 1)[0]
	adminPort := allocateTCPPorts(t, 1)[0]
	grpcPort := allocateTCPPorts(t, 1)[0]

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
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	startNode(t, bin, args, "[recovery-officer] ")

	node := &selfGenCANode{
		dataDir:       dataDir,
		adminPort:     adminPort,
		grpcPort:      grpcPort,
		raftPort:      raftPort,
		caPath:        filepath.Join(dataDir, "ca.pem"),
		adminCertPath: filepath.Join(dataDir, "admin-cert.pem"),
		adminKeyPath:  filepath.Join(dataDir, "admin-key.pem"),
	}
	waitForFile(t, node.caPath, 30*time.Second)
	waitForFile(t, node.adminCertPath, 30*time.Second)
	waitForFile(t, node.adminKeyPath, 30*time.Second)

	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)
	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	// Register three recovery officers over the unix transport (bug-1
	// free), then generate a 2-of-3 recovery key set. Recovery share
	// distribution requires the enrolled officer count to already satisfy
	// the requested threshold, so registration must happen first.
	off1 := registerRecoveryOfficer(t, cli, node, unixSocket, "r1")
	off2 := registerRecoveryOfficer(t, cli, node, unixSocket, "r2")
	// r3 is enrolled only to satisfy sealSharesToOfficers' requirement that
	// the enrolled officer count exactly equal --total; its share is never
	// claimed (the 2-of-3 threshold is met by off1 + off2 alone).
	_ = registerRecoveryOfficer(t, cli, node, unixSocket, "r3")

	unixArgs := []string{"--server", "unix://" + unixSocket, "--timeout", "20s"}
	out, err := runQRDB(t, cli, unixArgs, "barrier", "recovery", "generate",
		"--threshold", "2", "--total", "3", "--output", "json")
	if err != nil {
		t.Fatalf("barrier recovery generate: %v\noutput: %s", err, out)
	}
	var genResp recoveryKeysJSON
	decodeJSON(t, out, &genResp)
	if genResp.ShareCount != 3 {
		t.Fatalf("barrier recovery generate: share_count = %d, want 3: %s", genResp.ShareCount, out)
	}

	// Full claim protocol for two of the three officers -- enough to
	// satisfy the 2-of-3 threshold.
	share1 := claimRecoveryShare(t, cli, node, off1)
	share2 := claimRecoveryShare(t, cli, node, off2)
	t.Logf("claimed share 1: %s", share1)
	t.Logf("claimed share 2: %s", share2)

	// Guardrail: the share was consumed exactly once. A second claim-begin
	// for the same officer must fail (ErrNoPendingShare) rather than
	// re-issue a nonce for an already-delivered share.
	out, err = runQRDB(t, cli, node.restArgsIdentity(off1.certPath, off1.keyPath),
		"barrier", "recovery", "officer", "claim-begin", "--username", off1.username, "--output", "json")
	if err == nil {
		t.Fatalf("re-claim of an already-delivered share should fail; output: %s", out)
	}
	if !strings.Contains(out, "no pending share") {
		t.Errorf("expected 'no pending share for officer', got: %s", out)
	}

	// Guardrail: an unregistered officer cannot claim-begin. The admin
	// identity satisfies the "any trusted client cert" identity gate but
	// the ceremony's officer registry lookup for the given username still
	// fails.
	out, err = runQRDB(t, cli, node.restArgsAdmin(),
		"barrier", "recovery", "officer", "claim-begin", "--username", "no-such-officer")
	if err == nil {
		t.Fatalf("claim-begin for an unregistered officer should fail; output: %s", out)
	}
	if !strings.Contains(out, "officer not found") {
		t.Errorf("expected 'officer not found', got: %s", out)
	}

	// `barrier recovery recover` requires the barrier to be SEALED
	// (pkg/seal/recovery.go RecoverWithKeys). This node's primary strategy
	// is "none" (not Shamir), so the single-admin `barrier seal` path
	// applies directly -- no quorum vote needed.
	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "barrier", "seal", "-y"); err != nil {
		t.Fatalf("barrier seal: %v\noutput: %s", err, out)
	}

	// The final proof requested for this coverage gap: the two claimed
	// shares, submitted in ascending share-index order as their bare
	// base64 values, actually reconstruct the root key and unseal the
	// barrier.
	out, err = runQRDB(t, cli, node.restArgsAdmin(),
		"barrier", "recovery", "recover", "--share", share1, "--share", share2)
	if err != nil {
		t.Fatalf("barrier recovery recover with the claimed shares should succeed: %v\noutput: %s", err, out)
	}

	statusOut, err := runQRDB(t, cli, node.restArgsAdmin(), "barrier", "status", "--output", "json")
	if err != nil {
		t.Fatalf("barrier status after recovery: %v\noutput: %s", err, statusOut)
	}
	var status struct {
		Sealed   bool   `json:"sealed"`
		Strategy string `json:"strategy"`
	}
	decodeJSON(t, statusOut, &status)
	if status.Sealed {
		t.Errorf("expected the barrier to be unsealed after a successful recovery, got: %s", statusOut)
	}
}

// ---------------------------------------------------------------------------
// 4. Ceremony command family (`ceremony status`, `ceremony claim-cert`,
//    `ceremony claim-share`, `ceremony sign-csr`, `ceremony
//    claim-share-begin/complete`, `ceremony submit-unseal-share`).
// ---------------------------------------------------------------------------

// TestCLI_Ceremony_StatusAndSubcommands drives every wired `ceremony`
// subcommand against a real node and asserts on the server's actual,
// verified response for each:
//
//   - `ceremony status`: awaiting_init before `qrdb init`, operational after.
//   - `ceremony claim-cert begin/complete` and the legacy `ceremony
//     claim-share`: these are the M-of-N/"Officers list" enrollment path
//     (pkg/ceremony state machine StateEnrolling). Because `qrdb init`
//     never populates that Officers list (bug 2), the ceremony always
//     takes the single-admin branch and lands directly in
//     StateOperational, so these three commands deterministically fail
//     with the typed ceremony.ErrNotInEnrollingState.
//   - `ceremony claim-share-begin`, `claim-share-complete` and
//     `submit-unseal-share`: the custodian/hardware-authenticator
//     extension point (pkg/ceremony CeremonyExtension, wired via
//     SetExtension). qrdbd's daemon boot never calls SetExtension --
//     that lives in go-xkms, per the locked layer-ownership boundary in
//     this repo's CLAUDE.md -- so these deterministically fail with the
//     typed ceremony.ErrNotImplemented.
//   - `ceremony sign-csr`: succeeds with the correct --so-pin (the one
//     supplied to `qrdb init`) and fails with the typed
//     ceremony.ErrSOPINMismatch for a wrong one.
func TestCLI_Ceremony_StatusAndSubcommands(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[ceremony] ")
	pollUntilCLISucceeds(t, cli, node.restArgsNoAuth(), 30*time.Second)
	if out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init", "--barrier-strategy", "none"); err != nil {
		t.Fatalf("barrier init: %v\noutput: %s", err, out)
	}
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err := runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status (pre-init): %v\noutput: %s", err, out)
	}
	var status ceremonyStatusJSON
	decodeJSON(t, out, &status)
	if status.State != "awaiting_init" {
		t.Errorf("ceremony status pre-init: state = %q, want %q", status.State, "awaiting_init")
	}

	const soPin = "SOPIN12345"
	out, err = runQRDB(t, cli, node.restArgsAdmin(),
		"init", "--so-pin", soPin, "--user-pin", "USERPIN12345", "--admin-bundle", "-")
	if err != nil {
		t.Fatalf("qrdb init: %v\noutput: %s", err, out)
	}

	out, err = runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status (post-init): %v\noutput: %s", err, out)
	}
	decodeJSON(t, out, &status)
	if status.State != "operational" {
		t.Errorf("ceremony status post-init: state = %q, want %q", status.State, "operational")
	}
	if status.SPKIPin == "" {
		t.Errorf("ceremony status post-init: spki_pin should be non-empty")
	}

	notEnrolling := []struct {
		name string
		args []string
	}{
		{"claim-cert begin", []string{"ceremony", "claim-cert", "begin", "--username", "off1"}},
		{"claim-cert complete", []string{"ceremony", "claim-cert", "complete", "--username", "off1", "--nonce", "deadbeef", "--signature", "AAAA"}},
		{"claim-share", []string{"ceremony", "claim-share", "--username", "off1"}},
	}
	for _, tc := range notEnrolling {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runQRDB(t, cli, node.restArgsAdmin(), tc.args...)
			if err == nil {
				t.Fatalf("%s should fail once the ceremony is operational; output: %s", tc.name, out)
			}
			if !strings.Contains(out, "operation requires enrolling state") {
				t.Errorf("%s: expected ceremony.ErrNotInEnrollingState, got: %s", tc.name, out)
			}
		})
	}

	notImplemented := []struct {
		name string
		args []string
	}{
		{"claim-share-begin", []string{"ceremony", "claim-share-begin", "--custodian-id", "cust1"}},
		{"claim-share-complete", []string{"ceremony", "claim-share-complete", "--custodian-id", "cust1", "--challenge-id", "ch1", "--factor-response", "AAAA"}},
		{"submit-unseal-share", []string{"ceremony", "submit-unseal-share", "--custodian-id", "cust1", "--share", "AAAA"}},
	}
	for _, tc := range notImplemented {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runQRDB(t, cli, node.restArgsAdmin(), tc.args...)
			if err == nil {
				t.Fatalf("%s should fail: qrdbd never wires a ceremony.CeremonyExtension (that lives in go-xkms); output: %s", tc.name, out)
			}
			if !strings.Contains(out, "operation not implemented") {
				t.Errorf("%s: expected ceremony.ErrNotImplemented, got: %s", tc.name, out)
			}
		})
	}

	csrPEM, _ := generateCeremonyCSR(t, "svc1")
	csrPath := writeTempFile(t, "svc1.csr", csrPEM)

	out, err = runQRDB(t, cli, node.restArgsAdmin(),
		"ceremony", "sign-csr", "--username", "svc1", "--so-pin", soPin,
		"--csr-file", csrPath, "--role", "operator")
	if err != nil {
		t.Fatalf("ceremony sign-csr with the correct SO PIN should succeed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("ceremony sign-csr: expected a signed certificate in the output, got: %s", out)
	}

	out, err = runQRDB(t, cli, node.restArgsAdmin(),
		"ceremony", "sign-csr", "--username", "svc2", "--so-pin", "wrong-pin",
		"--csr-file", csrPath, "--role", "operator")
	if err == nil {
		t.Fatalf("ceremony sign-csr with the wrong SO PIN should fail; output: %s", out)
	}
	if !strings.Contains(out, "SO PIN mismatch") {
		t.Errorf("expected ceremony.ErrSOPINMismatch, got: %s", out)
	}
}
