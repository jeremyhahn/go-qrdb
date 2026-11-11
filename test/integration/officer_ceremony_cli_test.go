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
//     way. See TestCLI_BarrierOfficerRegister_SealedWindow_CLI_PerProtocol.
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
// Share holders follow docs/shamir-custody/: a claim delivers an HPKE
// envelope sealed to the holder's key, once, over a connection presenting
// the holder's certificate; `barrier quorum submit` opens it locally and
// submits the share with the holder's factor proof. No raw share crosses the
// CLI boundary anywhere in this file.
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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Node bootstrap helpers (self-generated CA -- required for ceremony/officer
// CSR signing; see cmd/qrdbd/seal_subsystem.go ceremonyCAFromBundle, which
// returns (nil, nil) when the TLS bundle carries no CA private key, i.e.
// under BYO --tls-ca-file/--tls-cert-file/--tls-key-file).
// ---------------------------------------------------------------------------

// selfGenCANode holds the connection details for a qrdbd node booted
// WITHOUT --tls-* flags, so the daemon generates and persists its own CA
// (ca.pem + ca-key.pem). Retaining the CA private key is what lets the
// ceremony sign officer CSRs. adminCertPath/adminKeyPath name where this
// test's OWN call to bootAdminIdentity (ceremony-initialize, run against
// restArgsNoAuth before any barrier unseal) writes the bootstrap admin's
// certificate and key; nothing writes them until a test does that.
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
	if !pollUntil(timeout, 50*time.Millisecond, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}) {
		t.Fatalf("timed out after %v waiting for %s", timeout, path)
	}
}

// bootSelfGenCANode starts a fresh single-node qrdbd with the given seal
// YAML config and NO --tls-* flags, so the daemon self-generates its CA
// (retaining the private key). It blocks until ca.pem exists (i.e. TLS
// resolution has completed) and returns the connection details; it does
// NOT wait for the barrier to be initialized, for full boot, or for an
// admin identity to exist -- callers drive the sealed-window listener and
// bootAdmin explicitly (go-qrdb#548): "qrdb init" is the only system
// bootstrap, bringing the system barrier up itself (via --barrier-strategy
// and, when the strategy needs one, --passphrase) in the same call that
// runs the ceremony, so no separate barrier-init call precedes it. First
// boot's bounded wait for the pending-admin-cert record that call writes
// covers the gap until the raft host comes up.
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

	credDir := t.TempDir()
	n := &selfGenCANode{
		dataDir:       dataDir,
		adminPort:     adminPort,
		grpcPort:      grpcPort,
		raftPort:      raftPort,
		caPath:        filepath.Join(dataDir, "ca.pem"),
		adminCertPath: filepath.Join(credDir, "bootstrap-admin-cert.pem"),
		adminKeyPath:  filepath.Join(credDir, "bootstrap-admin-key.pem"),
	}
	waitForFile(t, n.caPath, 30*time.Second)
	return n
}

// bootAdmin runs bootAdminIdentity (ceremony-initialize, generate path)
// against n's sealed-window listener -- reachable with no client
// certificate, since no admin exists yet -- and writes the result to
// n.adminCertPath/n.adminKeyPath, so n.restArgsAdmin() works once first
// boot has bound it. "qrdb init" is the only system bootstrap
// (go-qrdb#548): this call brings the system barrier up itself (pass
// --barrier-strategy, and --passphrase when the strategy needs one, via
// extraArgs) in the same request that runs the ceremony. First boot runs
// in the background once the barrier unseals and waits (bounded) for the
// pending-admin-cert record this call writes, so there is no race.
func (n *selfGenCANode) bootAdmin(t *testing.T, cli, soPin, userPin string, extraArgs ...string) string {
	t.Helper()
	return bootAdminIdentity(t, cli, n.caPath, n.adminPort, soPin, userPin, n.adminCertPath, n.adminKeyPath, "", extraArgs...)
}

// restArgsNoAuth returns qrdb CLI connection flags for the REST transport
// presenting no client certificate, the flags these flows drive the pre-host
// sealed-window listener with. The listener refuses every call but unseal
// with the typed sealed refusal, its health call included, and REST's
// reachability probe (sdk/go/transport/rest/transport.go Transport.reachable)
// counts that refusal as "up and sealed".
func (n *selfGenCANode) restArgsNoAuth() []string {
	return []string{
		"--server", fmt.Sprintf("https://127.0.0.1:%d", n.adminPort),
		"--transport", "rest",
		"--ca-file", n.caPath,
		"--timeout", "20s",
	}
}

// restArgsAdmin returns REST connection flags presenting the bootstrap admin
// identity `qrdb init` issued and first boot bound (bootAdmin), which RBAC
// maps to full platform access via the system-tenant rule.
func (n *selfGenCANode) restArgsAdmin() []string {
	return append(n.restArgsNoAuth(), "--cert-file", n.adminCertPath, "--key-file", n.adminKeyPath)
}

// restArgsIdentity returns REST connection flags presenting an arbitrary
// mTLS client identity (e.g. an enrolled officer's CA-signed certificate),
// used to drive the officer's own share-claim handshake.
func (n *selfGenCANode) restArgsIdentity(certPath, keyPath string) []string {
	return append(n.restArgsNoAuth(), "--cert-file", certPath, "--key-file", keyPath)
}

// unixArgsAdmin returns qrdb CLI connection flags for the Unix socket
// transport presenting the bootstrap admin identity `qrdb init` issued
// (bootAdmin). Unix (grpc-over-UDS) is "verified TLS 1.3 only",
// identical to TCP gRPC and REST: it authenticates and authorizes exactly
// like every other transport, so calls that need SealOfficerRegister /
// SealRecovery* permissions must present the admin client certificate the
// same way restArgsAdmin does for REST.
func (n *selfGenCANode) unixArgsAdmin(unixSocket string) []string {
	return []string{
		"--server", "unix://" + unixSocket,
		"--timeout", "20s",
		"--ca-file", n.caPath,
		"--cert-file", n.adminCertPath,
		"--key-file", n.adminKeyPath,
	}
}

// pollUntilSealedListenerUp repeatedly runs `qrdb <connArgs> barrier status
// --output json` until the node answers or the timeout elapses, and is how a
// test detects that the sealed-window listener has come up (no TCP/TLS-level
// readiness signal is otherwise available to a test running outside the
// daemon process). A sealed node answers barrier status with its initialized
// flag, so any status counts, as does the typed sealed refusal of a node the
// cluster seal holds.
func pollUntilSealedListenerUp(t *testing.T, cli string, connArgs []string, timeout time.Duration) {
	t.Helper()
	var lastOut string
	var lastErr error
	if !pollUntil(timeout, 250*time.Millisecond, func() bool {
		lastOut, lastErr = runQRDB(t, cli, connArgs, "barrier", "status", "--output", "json")
		return lastErr == nil || cliSealedRefusal(lastOut)
	}) {
		t.Fatalf("timed out after %v waiting for the sealed-window listener: %v\noutput: %s", timeout, lastErr, lastOut)
	}
}

// pollUntilCLISucceeds repeatedly runs `qrdb <connArgs> barrier status
// --output json` until it reports an unsealed barrier or the timeout elapses.
// It detects that the full post-boot server is answering requests (the admin
// identity is not usable until Raft has elected a leader and replicated
// the bootstrap admin user -- typically a few seconds after
// waitForHealthy's host.id file appears). A zero exit alone is not enough:
// the sealed-window listener answers status too, with sealed set.
func pollUntilCLISucceeds(t *testing.T, cli string, connArgs []string, timeout time.Duration) {
	t.Helper()
	var lastOut string
	var lastErr error
	if !pollUntil(timeout, 250*time.Millisecond, func() bool {
		lastOut, lastErr = runQRDB(t, cli, connArgs, "barrier", "status", "--output", "json")
		var status cliBarrierStatusJSON
		return lastErr == nil && json.Unmarshal([]byte(lastOut), &status) == nil && !status.Sealed
	}) {
		t.Fatalf("timed out after %v waiting for CLI to reach the server: %v\noutput: %s", timeout, lastErr, lastOut)
	}
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

// writeECPrivateKeyPEM SEC1-PEM-encodes key and writes it to a temp file
// named name, returning its path. Shared by every officer-claim flow in
// this file that must present an officer's own private key as an mTLS
// client identity via restArgsIdentity.
func writeECPrivateKeyPEM(t *testing.T, name string, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("writeECPrivateKeyPEM %s: marshal key: %v", name, err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	return writeTempFile(t, name, keyPEM)
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
	CertPEM string `json:"cert_pem"`
	Role    string `json:"role"`
}

type shareClaimBeginJSON struct {
	NonceHex  string    `json:"nonce_hex"`
	ExpiresAt time.Time `json:"expires_at"`
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
// into dest, failing the test with a useful message on error. Content
// before the first '{' is dropped: `qrdb init`'s generate-path warning
// (go-qrdb#548) is operator-facing prose on stderr, interleaved ahead of
// the JSON payload in CombinedOutput, not part of the response.
func decodeJSON(t *testing.T, out string, dest any) {
	t.Helper()
	// The JSON value may be an object or an array (e.g. a `list` command's
	// response): skip to whichever of '{' or '[' starts it first, past any
	// leading log/banner text, rather than assuming '{' -- stopping at the
	// wrong one (an array's '{' is its first ELEMENT, not its start) truncates
	// the enclosing brackets and leaves a trailing comma json.Unmarshal
	// refuses.
	obj := strings.IndexByte(out, '{')
	arr := strings.IndexByte(out, '[')
	start := obj
	if start < 0 || (arr >= 0 && arr < start) {
		start = arr
	}
	if start > 0 {
		out = out[start:]
	}
	if err := json.Unmarshal([]byte(out), dest); err != nil {
		t.Fatalf("decodeJSON: %v\noutput: %s", err, out)
	}
}

// ---------------------------------------------------------------------------
// 1. `qrdb init` -- one-shot bootstrap ceremony.
// ---------------------------------------------------------------------------

// TestCLI_Init_PassphraseHappyPath drives the full happy path: `qrdb init`
// is the only system bootstrap (go-qrdb#548) -- it brings the system barrier
// up itself (strategy "none" here, via --barrier-strategy) in the same call
// that runs the ceremony against the sealed-window listener (no admin exists
// yet to authenticate as), and first boot binds the certificate init already
// minted.
//
// The SO identity is CSR-based; the server never holds officer private
// keys, so init returns no credential bundle. Post-init usability is
// verified with the certificate bootAdminIdentity obtained from init
// itself -- the admin's ACTUAL credential, now that it is bound rather
// than merely matching CN=admin, OU=system by convention.
func TestCLI_Init_PassphraseHappyPath(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[init-happy] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	out := node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", "--barrier-strategy", "none")
	var initResp struct {
		State string `json:"state"`
	}
	decodeJSON(t, out, &initResp)
	if initResp.State != "operational" {
		t.Errorf("qrdb init state = %q, want operational; output: %s", initResp.State, out)
	}

	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	// Post-init usability: the node must still serve ordinary data-plane
	// traffic, authenticated with the certificate `qrdb init` minted and
	// first boot bound.
	if out, err := runQRDB(t, cli, node.restArgsAdmin(), "kv", "put", "hello", "world"); err != nil {
		t.Fatalf("kv put after init: %v\noutput: %s", err, out)
	}
	getOut, err := runQRDB(t, cli, node.restArgsAdmin(), "kv", "get", "hello")
	if err != nil {
		t.Fatalf("kv get after init: %v\noutput: %s", err, getOut)
	}
	if !strings.Contains(getOut, "world") {
		t.Errorf("kv get after init: expected 'world', got: %s", getOut)
	}

	// A second `qrdb init`, now that the admin already holds a live
	// binding, is refused as a redundant retry (service.AdminAlreadyBoundError)
	// rather than re-running the whole ceremony.
	retryOut, err := runQRDB(t, cli, node.restArgsAdmin(),
		"init", "--so-pin", "SOPIN12345", "--user-pin", "USERPIN12345", "--barrier-strategy", "none",
		"--admin-generate")
	if err == nil {
		t.Fatalf("second qrdb init should have failed; output: %s", retryOut)
	}
	if !strings.Contains(retryOut, "already holds a certificate binding") {
		t.Errorf("second qrdb init: expected the admin-already-bound refusal, got: %s", retryOut)
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
// sealed system barrier, proven by TestCLI_BarrierOfficerRegister_SealedWindow_CLI_PerProtocol.)
func TestCLI_Init_ShamirFlags_RequiresOfficers(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin, "seal:\n  enabled: true\n  strategy: none\n", "[init-shamir-flags] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	// No admin identity is needed: ceremony-initialize runs against the
	// sealed-window listener, before any barrier unseal, and this test
	// never reaches a successful ceremony.
	out, err := runQRDB(t, cli, node.restArgsNoAuth(),
		"init",
		"--so-pin", "SOPIN12345", "--user-pin", "USERPIN12345",
		"--barrier-strategy", "shamir", "--shares", "5", "--threshold", "3",
		"--admin-generate")
	if err == nil {
		t.Fatalf("qrdb init --barrier-strategy shamir with no officers should fail closed now that Threshold reaches the ceremony; output: %s", out)
	}
	if !strings.Contains(out, "M-of-N requires at least one officer") {
		t.Errorf("expected the typed ceremony.ErrNoOfficers message, got: %s", out)
	}

	// The ceremony must not be left half-initialized: Initialize's own
	// validateConfig failure must leave the state machine at
	// awaiting_init, not silently advance it. The node's pre-init answer
	// still reports the barrier uninitialized, and the same node still
	// takes its first boot, which ceremony-initialize refuses past
	// awaiting_init.
	out, err = runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "status", "--output", "json")
	if err != nil {
		t.Fatalf("barrier status after the refused init: %v\noutput: %s", err, out)
	}
	var status transport.BarrierStatusResponse
	decodeJSON(t, out, &status)
	if status.Initialized || !status.Sealed {
		t.Fatalf("barrier status after the refused init = %+v, want sealed and uninitialized", status)
	}
	node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", "--barrier-strategy", "none")
}

// TestCLI_Init_ShamirWithOfficers_EndToEnd is the SUCCESS case companion to
// TestCLI_Init_ShamirFlags_RequiresOfficers: against a node whose barrier is
// configured for Shamir and not yet initialized, `qrdb init
// --barrier-strategy shamir --officers ... --custodians ...` enrols every
// named holder, has the barrier split its unseal key to them, and reaches
// ceremony.StateEnrolling with one sealed envelope per holder. Each holder
// then claims its envelope over its own certificate, which brings the
// ceremony to StateOperational, and a quorum of those envelopes seals and
// unseals the barrier.
func TestCLI_Init_ShamirWithOfficers_EndToEnd(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin,
		"seal:\n  enabled: true\n  strategy: shamir\n  shamir_threshold: 2\n  shamir_total: 3\n",
		"[init-shamir-officers] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	type initHolder struct {
		username, role, keyPath, certPath, envelope string
	}
	holders := []*initHolder{{username: "alice", role: "so"}, {username: "bob", role: "so"}, {username: "carol", role: "custodian"}}
	var holderFlags []string
	for _, h := range holders {
		csrPEM, key := generateCeremonyCSR(t, h.username)
		h.keyPath = writeECPrivateKeyPEM(t, h.username+"-key.pem", key)
		flag := "--officers"
		if h.role == "custodian" {
			flag = "--custodians"
		}
		holderFlags = append(holderFlags, flag, h.username+"="+writeTempFile(t, h.username+".csr", csrPEM))
	}

	// `qrdb init` runs against the sealed window with no client
	// certificate and issues the admin credential (generate path) in the
	// same call that enrols the holders.
	out := node.bootAdmin(t, cli, "SOPIN12345", "USERPIN12345", append([]string{
		"--barrier-strategy", "shamir", "--shares", "3", "--threshold", "2",
	}, holderFlags...)...)
	var initResp transport.CeremonyInitializeResponse
	decodeJSON(t, out, &initResp)
	if initResp.State != "enrolling" || initResp.ShareCount != 3 || len(initResp.Holders) != 3 {
		t.Fatalf("init = state %q, share_count %d, %d holders; want enrolling, 3, 3: %s",
			initResp.State, initResp.ShareCount, len(initResp.Holders), out)
	}
	for i, h := range holders {
		got := initResp.Holders[i]
		if got.Username != h.username || got.Role != h.role || !strings.Contains(got.CertPEM, "-----BEGIN CERTIFICATE-----") {
			t.Fatalf("holder %d = %+v, want %s (%s) with a certificate", i, got, h.username, h.role)
		}
		h.certPath = writeTempFile(t, h.username+"-cert.pem", []byte(got.CertPEM))
	}

	// Initializing the barrier unseals it and brings up the full server.
	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	statusOut, err := runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status after init: %v\noutput: %s", err, statusOut)
	}
	var status ceremonyStatusJSON
	decodeJSON(t, statusOut, &status)
	if status.State != "enrolling" {
		t.Fatalf("expected ceremony state enrolling after M-of-N init, got: %q", status.State)
	}

	for _, h := range holders {
		h.envelope = filepath.Join(t.TempDir(), h.username+".env")
		if out, err := runQRDB(t, cli, node.restArgsIdentity(h.certPath, h.keyPath),
			"barrier", "officer", "claim", "--username", h.username, "--key", h.keyPath, "--out", h.envelope); err != nil {
			t.Fatalf("claim for %s: %v\noutput: %s", h.username, err, out)
		}
	}

	statusOut, err = runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status after all claims: %v\noutput: %s", err, statusOut)
	}
	decodeJSON(t, statusOut, &status)
	if status.State != "operational" {
		t.Errorf("expected ceremony state operational once every holder has claimed, got: %q", status.State)
	}

	// The claimed envelopes work: an officer and a custodian seal, then
	// unseal, the barrier.
	for _, op := range []string{"seal", "unseal"} {
		for _, h := range []*initHolder{holders[0], holders[2]} {
			if out, err := runQRDB(t, cli, node.restArgsIdentity(h.certPath, h.keyPath),
				"barrier", "quorum", "submit", "--op", op, "--username", h.username,
				"--envelope", h.envelope, "--key", h.keyPath); err != nil {
				t.Fatalf("quorum submit --op %s %s: %v\noutput: %s", op, h.username, err, out)
			}
		}
		out, err := runQRDB(t, cli, node.restArgsAdmin(), "barrier", "status", "--output", "json")
		// A sealed node answers status with its initialized flag and
		// nothing else.
		if op == "seal" {
			if err != nil {
				t.Fatalf("barrier status after the seal quorum: %v\noutput: %s", err, out)
			}
			var sealed transport.BarrierStatusResponse
			decodeJSON(t, out, &sealed)
			if !sealed.Sealed || !sealed.Initialized || sealed.Strategy != "" {
				t.Fatalf("barrier status after the seal quorum = %+v, want sealed, initialized and nothing more", sealed)
			}
			continue
		}
		if err != nil {
			t.Fatalf("barrier status after %s: %v\noutput: %s", op, err, out)
		}
		var bs transport.BarrierStatusResponse
		decodeJSON(t, out, &bs)
		if bs.Sealed {
			t.Fatalf("after the %s quorum: sealed = %v", op, bs.Sealed)
		}
	}
}

// TestCLI_Init_LocalFlagValidation exercises the CLI-local flag
// validation in cmd/qrdb/init.go resolveAndValidateInitFlags. cobra's
// PersistentPreRunE connects the SDK client before any subcommand-level
// validation runs (see newRootCmd), so even purely local validation
// requires a reachable server; a "none"-strategy node satisfies that
// cheaply.
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
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

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
			name: "shares without shamir strategy",
			// An explicit non-Shamir strategy and a valid admin credential
			// flag, so the case fails on --shares rather than on the default
			// passphrase strategy's --passphrase or the credential flag.
			args:    []string{"init", "--so-pin", "a", "--user-pin", "b", "--barrier-strategy", "none", "--admin-generate", "--shares", "3"},
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

// TestCLI_BarrierInitShamir_RefusedOnSystemBarrier verifies `barrier
// init-shamir` against a fresh node's system barrier: `qrdb init` is the
// only system bootstrap (go-qrdb#548; go-qrdb#560 review item 8), so
// BarrierService.BarrierInitializeShamir refuses unconditionally on any
// ceremony-backed service -- qrdbd's own system barrier, always one --
// with *SystemBarrierInitRefusedError, before ever reaching the
// officer-count check this test used to pin (sealSharesToOfficers'
// ErrInsufficientOfficers, which an officer-less fresh node also used to
// satisfy, but which this refusal now makes unreachable for the system
// barrier on any officer count).
func TestCLI_BarrierInitShamir_RefusedOnSystemBarrier(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin,
		"seal:\n  enabled: true\n  strategy: shamir\n  shamir_threshold: 2\n  shamir_total: 3\n",
		"[init-shamir-refused] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "init-shamir",
		"--threshold", "2", "--total", "3")
	if err == nil {
		t.Fatalf("barrier init-shamir against the system barrier should fail; output: %s", out)
	}
	if !strings.Contains(out, `the system barrier is brought up by "qrdb init"`) {
		t.Errorf("expected the system-bootstrap refusal, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// 4. Ceremony command family (`ceremony status`, `ceremony sign-csr`).
// ---------------------------------------------------------------------------

// TestCLI_Ceremony_StatusAndSubcommands drives the `ceremony` subcommands
// against a real node and asserts on the server's actual response:
//
//   - `ceremony status`: the typed sealed refusal before `qrdb init` (a sealed
//     node serves only the unseal call), enrolling after an
//     M-of-N init, operational once every holder has claimed.
//   - `ceremony sign-csr`: issues holder certificates only while the ceremony
//     is enrolling. It succeeds with the correct --so-pin (the one supplied to
//     `qrdb init`), is refused by the PIN backend for a wrong one, and is
//     refused outright once the ceremony is operational.
func TestCLI_Ceremony_StatusAndSubcommands(t *testing.T) {
	bin := buildBinary(t)
	cli := buildCLIBinary(t)

	node := bootSelfGenCANode(t, bin,
		"seal:\n  enabled: true\n  strategy: shamir\n  shamir_threshold: 2\n  shamir_total: 2\n",
		"[ceremony] ")
	pollUntilSealedListenerUp(t, cli, node.restArgsNoAuth(), 30*time.Second)

	// Before init a sealed node gives one answer beyond unseal: barrier
	// status, reporting the barrier uninitialized. Ceremony status is not
	// that answer and gets the typed sealed refusal, not a state.
	out, err := runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "status", "--output", "json")
	if err != nil {
		t.Fatalf("barrier status (pre-init): %v\noutput: %s", err, out)
	}
	var preInit transport.BarrierStatusResponse
	decodeJSON(t, out, &preInit)
	if preInit.Initialized || !preInit.Sealed {
		t.Fatalf("barrier status (pre-init) = %+v, want sealed and uninitialized", preInit)
	}
	out, err = runQRDB(t, cli, node.restArgsNoAuth(), "barrier", "status")
	if err != nil {
		t.Fatalf("barrier status (pre-init, text): %v\noutput: %s", err, out)
	}
	for _, want := range []string{"Sealed:          true", "Initialized:     false"} {
		if !strings.Contains(out, want) {
			t.Fatalf("barrier status (pre-init, text) lacks %q\noutput: %s", want, out)
		}
	}
	out, err = runQRDB(t, cli, node.restArgsNoAuth(), "ceremony", "status", "--output", "json")
	if err == nil || !cliSealedRefusal(out) {
		t.Fatalf("ceremony status (pre-init): err = %v, want the sealed refusal\noutput: %s", err, out)
	}
	var status ceremonyStatusJSON

	// An M-of-N `qrdb init` enrols the two officers and leaves the ceremony
	// enrolling until both have claimed their envelopes.
	type officer struct{ username, keyPath, certPath string }
	officers := []*officer{{username: "alice"}, {username: "bob"}}
	var officerFlags []string
	for _, o := range officers {
		csrPEM, key := generateCeremonyCSR(t, o.username)
		o.keyPath = writeECPrivateKeyPEM(t, o.username+"-key.pem", key)
		officerFlags = append(officerFlags, "--officers", o.username+"="+writeTempFile(t, o.username+".csr", csrPEM))
	}
	const soPin = "SOPIN12345"
	out = node.bootAdmin(t, cli, soPin, "USERPIN12345", append([]string{
		"--barrier-strategy", "shamir", "--shares", "2", "--threshold", "2",
	}, officerFlags...)...)
	var initResp transport.CeremonyInitializeResponse
	decodeJSON(t, out, &initResp)
	if len(initResp.Holders) != len(officers) {
		t.Fatalf("qrdb init returned %d holders, want %d: %s", len(initResp.Holders), len(officers), out)
	}
	for i, o := range officers {
		o.certPath = writeTempFile(t, o.username+"-cert.pem", []byte(initResp.Holders[i].CertPEM))
	}

	waitForHealthy(t, node.dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, node.restArgsAdmin(), 60*time.Second)

	out, err = runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status (post-init): %v\noutput: %s", err, out)
	}
	decodeJSON(t, out, &status)
	if status.State != "enrolling" {
		t.Errorf("ceremony status post-init: state = %q, want %q", status.State, "enrolling")
	}
	if status.SPKIPin == "" {
		t.Errorf("ceremony status post-init: spki_pin should be non-empty")
	}

	csrPEM, _ := generateCeremonyCSR(t, "svc1")
	csrPath := writeTempFile(t, "svc1.csr", csrPEM)
	signCSR := func(username, pin string) (string, error) {
		return runQRDB(t, cli, node.restArgsAdmin(),
			"ceremony", "sign-csr", "--username", username, "--so-pin", pin,
			"--csr-file", csrPath, "--role", "custodian")
	}

	out, err = signCSR("svc1", soPin)
	if err != nil {
		t.Fatalf("ceremony sign-csr with the correct SO PIN should succeed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("ceremony sign-csr: expected a signed certificate in the output, got: %s", out)
	}

	out, err = signCSR("svc2", "wrong-pin")
	if err == nil {
		t.Fatalf("ceremony sign-csr with the wrong SO PIN should fail; output: %s", out)
	}
	if !strings.Contains(out, "invalid PIN") {
		t.Errorf("expected pin.ErrPINInvalid from the PIN backend, got: %s", out)
	}

	// Every officer claims; the ceremony becomes operational and sign-csr,
	// a pre-operational verb, is refused even with the correct SO PIN.
	for _, o := range officers {
		envelope := filepath.Join(t.TempDir(), o.username+".env")
		if out, err := runQRDB(t, cli, node.restArgsIdentity(o.certPath, o.keyPath),
			"barrier", "officer", "claim", "--username", o.username, "--key", o.keyPath, "--out", envelope); err != nil {
			t.Fatalf("claim for %s: %v\noutput: %s", o.username, err, out)
		}
	}
	out, err = runQRDB(t, cli, node.restArgsAdmin(), "ceremony", "status", "--output", "json")
	if err != nil {
		t.Fatalf("ceremony status (after claims): %v\noutput: %s", err, out)
	}
	decodeJSON(t, out, &status)
	if status.State != "operational" {
		t.Fatalf("ceremony status after every claim: state = %q, want %q", status.State, "operational")
	}

	out, err = signCSR("svc3", soPin)
	if err == nil {
		t.Fatalf("ceremony sign-csr once operational should be refused; output: %s", out)
	}
	if !strings.Contains(out, "sign-csr requires") {
		t.Errorf("expected the ceremony-state refusal, got: %s", out)
	}
}

// TestCeremony_SignCSR_CLI_PerProtocol drives `qrdb ceremony sign-csr`
// against every CLI-selectable protocol: the correct-SO-PIN happy path (a
// signed certificate is returned) and the wrong-SO-PIN refusal, both over
// the same protocol as the rest of that subtest's requests. sign-csr is an
// ordinary RBAC-gated verb (no holder certificate required), unlike
// officer/quorum, so there is no architectural reason to restrict it to
// REST the way TestCLI_Ceremony_StatusAndSubcommands above does.
func TestCeremony_SignCSR_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range ceremonyInitProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			testCeremonySignCSROverProtocol(t, proto)
		})
	}
}

func testCeremonySignCSROverProtocol(t *testing.T, proto multiProtocol) {
	t.Helper()
	// `ceremony sign-csr` only issues holder certificates while the
	// ceremony is enrolling (PreOperationalOnly), so this must bootstrap
	// an M-of-N Shamir ceremony and leave it enrolling -- mirroring
	// TestCLI_Ceremony_StatusAndSubcommands -- rather than the single-
	// admin "none" strategy, which goes straight to operational and never
	// passes through enrolling at all.
	const threshold, total = 2, 2
	node := startFreshSealedNode(t, "shamir", threshold, total)
	waitForBarrierReachable(t, node.cli, node.noAuthFlagsFor(proto), 60*time.Second)
	workDir := t.TempDir()
	_, holderFlags := initHolders(t, workDir, "alice-"+proto.name, "bob-"+proto.name)
	node.initSystem(t, proto, append([]string{
		"--barrier-strategy", "shamir", "--shares", strconv.Itoa(total), "--threshold", strconv.Itoa(threshold),
	}, holderFlags...)...)
	node.waitForFullBoot(t)
	flags := node.flagsFor(proto)
	// waitForFullBoot asks REST only; the full server starts its QUIC,
	// gRPC and unix listeners after REST, so wait until the transport
	// under test answers an authenticated call before the one-shot
	// sign-csr below.
	waitForBarrierReachable(t, node.cli, flags, 60*time.Second)
	soPin := sealNodeSOPIN
	csrPEM, _ := generateCeremonyCSR(t, "svc-"+proto.name)
	csrPath := writeTempFile(t, "svc-"+proto.name+".csr", csrPEM)

	out, err := runQRDB(t, node.cli, flags,
		"ceremony", "sign-csr", "--username", "svc1", "--so-pin", soPin,
		"--csr-file", csrPath, "--role", "custodian")
	if err != nil {
		t.Fatalf("ceremony sign-csr with the correct SO PIN should succeed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("ceremony sign-csr: expected a signed certificate in the output, got: %s", out)
	}

	out, err = runQRDB(t, node.cli, flags,
		"ceremony", "sign-csr", "--username", "svc2", "--so-pin", "wrong-pin",
		"--csr-file", csrPath, "--role", "custodian")
	if err == nil {
		t.Fatalf("ceremony sign-csr with the wrong SO PIN should fail; output: %s", out)
	}
	if !strings.Contains(out, "invalid PIN") {
		t.Errorf("expected pin.ErrPINInvalid from the PIN backend, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// Officer register and recovery flows on every transport.
// ---------------------------------------------------------------------------

// TestCLI_BarrierOfficerRegister_SealedWindow_CLI_PerProtocol proves
// system-barrier officer registration during the sealed window (before
// `barrier init-shamir`, whose SealAndDistribute step requires the enrolled
// officer count to exactly equal --total -- see
// pkg/ceremony/ceremony_officer.go sealSharesToOfficers) on every
// CLI-selectable transport: the pre-host sealed-window listener accepts
// `barrier officer register` identically over REST, gRPC, QUIC, MCP, and
// Unix: every transport's Connect counts the typed sealed refusal as
// reachable (see barrier_permutations_cli_test.go's doc comment), so all five
// are reachable
// from a cold, uninitialized barrier.
func TestCLI_BarrierOfficerRegister_SealedWindow_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range coldSealedWindowProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			const threshold, total = 2, 3
			node := startFreshSealedNode(t, "shamir", threshold, total)
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			workDir := t.TempDir()
			csrPath, _ := generateOfficerCSR(t, workDir, "off-"+proto.name)
			args := append(append([]string{}, noAuth...), "--output", "json", "barrier", "officer", "register",
				"--username", "off-"+proto.name, "--csr-file", csrPath)
			stdout, stderr, err := runCLISplit(t, node.cli, args...)
			if err != nil {
				t.Fatalf("[%s] officer register against the sealed-window listener: %v\nstderr: %s", proto.name, err, stderr)
			}
			var reg officerRegisterJSON
			decodeJSON(t, stdout, &reg)
			if reg.Role != "so" || !strings.Contains(reg.CertPEM, "-----BEGIN CERTIFICATE-----") {
				t.Fatalf("[%s] expected a Security Officer with a CA-issued certificate, got: %s", proto.name, stdout)
			}

			// The officer list is not an anonymous sealed-window verb: the
			// listener runs without authentication, and the list is served
			// post-unseal to authenticated callers.
			listArgs := append(append([]string{}, noAuth...), "barrier", "officer", "list", "--output", "json")
			if out, _, err := runCLISplit(t, node.cli, listArgs...); err == nil {
				t.Fatalf("[%s] barrier officer list succeeded anonymously on the sealed-window listener: %s", proto.name, out)
			}
		})
	}
}

// TestCLI_RecoveryOfficerFlow_CLI_PerProtocol drives the recovery domain end
// to end on every CLI-selectable transport: enrol three recovery holders,
// generate a 2-of-3 recovery key, claim two envelopes (a second claim and a
// claim for an unknown holder are refused), seal the barrier, and recover it
// with a quorum of the two -- a submission answered with the wrong key being
// refused on the way. Recovery-domain officer
// register, claim, `barrier recovery generate`, `barrier seal`, and
// `barrier quorum submit` all run identically over REST, gRPC, QUIC, MCP,
// and Unix, reusing the same systemScope/officer/quorum abstractions
// TestBarrierCLI_ShamirLifecycle_PerProtocol uses for the system
// barrier's unseal domain -- scoped here to transport.DomainRecovery.
func TestCLI_RecoveryOfficerFlow_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			workDir := t.TempDir()
			node := startFreshSealedNode(t, "none", 0, 0)
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			// `qrdb init` is the only system bootstrap (go-qrdb#548): it
			// brings the system barrier up itself (strategy "none" here),
			// so no separate barrier-init call precedes it.
			node.initSystem(t, proto, "--barrier-strategy", "none")
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)
			ep := endpoint{serverURL: node.serverURLFor(proto), caFile: node.ca.CertPath}
			scope := systemScope(transport.DomainRecovery)

			off1 := registerOfficer(t, node.cli, workDir, flags, scope, "r1-"+proto.name)
			off2 := registerOfficer(t, node.cli, workDir, flags, scope, "r2-"+proto.name)
			off3 := registerOfficer(t, node.cli, workDir, flags, scope, "r3-"+proto.name)

			out, err := runQRDB(t, node.cli, flags, "barrier", "recovery", "generate",
				"--threshold", "2", "--so-pin", sealNodeSOPIN, "--output", "json")
			if err != nil {
				t.Fatalf("[%s] barrier recovery generate: %v\noutput: %s", proto.name, err, out)
			}
			var genResp recoveryKeysJSON
			decodeJSON(t, out, &genResp)
			if genResp.ShareCount != 3 {
				t.Fatalf("[%s] barrier recovery generate: share_count = %d, want 3 (one per enrolled holder): %s", proto.name, genResp.ShareCount, out)
			}

			claimEnvelope(t, node.cli, workDir, ep, scope, off1)
			claimEnvelope(t, node.cli, workDir, ep, scope, off2)

			// An envelope is delivered once.
			claimBeginArgs := append(off1.flags(ep), scope.officer...)
			claimBeginArgs = append(claimBeginArgs, "claim-begin", "--username", off1.username)
			claimBeginArgs = append(claimBeginArgs, scope.scopeArgs()...)
			if out, err := runQRDB(t, node.cli, nil, claimBeginArgs...); err == nil || !strings.Contains(out, "no pending share") {
				t.Fatalf("[%s] a second claim was not refused with 'no pending share': %v\noutput: %s", proto.name, err, out)
			}

			// An unknown holder cannot claim.
			if out, err := runQRDB(t, node.cli, flags, "barrier", "officer", "claim-begin",
				"--domain", "recovery", "--username", "no-such-officer"); err == nil || !strings.Contains(out, "officer not found") {
				t.Fatalf("[%s] a claim for an unknown holder was not refused with 'officer not found': %v\noutput: %s", proto.name, err, out)
			}

			// The recover op requires the barrier SEALED; this node's
			// primary strategy is "none", so the single-caller seal
			// applies.
			if out, err := runQRDB(t, node.cli, flags, "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN); err != nil {
				t.Fatalf("[%s] barrier seal: %v\noutput: %s", proto.name, err, out)
			}

			// The re-sealed node admits a share claim without
			// authentication, so the claim binds only a client
			// certificate the listener verifies: a caller presenting none
			// is refused, and the holder's own certificate completes the
			// claim.
			anonArgs := append(append([]string(nil), noAuth...), scope.officer...)
			anonArgs = append(anonArgs, "claim-begin", "--username", off3.username)
			anonArgs = append(anonArgs, scope.scopeArgs()...)
			if out, err := runQRDB(t, node.cli, nil, anonArgs...); err == nil {
				t.Fatalf("[%s] a sealed-window claim with no client certificate was accepted: %s", proto.name, out)
			}
			claimEnvelope(t, node.cli, workDir, ep, scope, off3)

			// Signing with another holder's key cannot answer r1's factor:
			// mTLS identity stays off1's own cert/key (the claimant), only
			// the envelope-opening --key differs.
			wrongKeyArgs := append(off1.flags(ep), "--output", "json")
			wrongKeyArgs = append(wrongKeyArgs, scope.quorum...)
			wrongKeyArgs = append(wrongKeyArgs, "submit", "--op", "recover", "--username", off1.username,
				"--envelope", off1.envelope, "--key", off3.keyPath)
			wrongKeyArgs = append(wrongKeyArgs, scope.scopeArgs()...)
			if out, _, err := runCLISplit(t, node.cli, wrongKeyArgs...); err == nil {
				t.Fatalf("[%s] a submission signed with another holder's key was accepted: %s", proto.name, out)
			}
			if _, stderr, err := quorumSubmit(t, node.cli, ep, scope, "recover", off1); err != nil {
				t.Fatalf("[%s] recover submission for r1: %v\nstderr: %s", proto.name, err, stderr)
			}
			outcome, stderr, err := quorumSubmit(t, node.cli, ep, scope, "recover", off2)
			if err != nil {
				t.Fatalf("[%s] recover submission for r2: %v\nstderr: %s", proto.name, err, stderr)
			}
			if !outcome.Complete {
				t.Fatalf("[%s] the second recovery holder did not complete the round: %+v", proto.name, outcome)
			}

			status := barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Errorf("[%s] expected the barrier to be unsealed after a successful recovery, got: %+v", proto.name, status)
			}
		})
	}
}
