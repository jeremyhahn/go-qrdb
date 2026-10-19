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

// tenant_barrier_officer_cli_test.go drives the full tenant-scoped Shamir
// share-holder lifecycle through the real CLI binary against every
// transport: tenant barrier-init with an inline officer roster, list,
// claim (both the combined verb and the two-step claim-begin/
// claim-complete), and barrier-quorum submit for seal and unseal.
//
// A dedicated tenant's own IAM is encrypted behind the very barrier its
// officers exist to help initialize, so standalone tenant barrier-officer
// register/list/revoke are RBAC-gated operations that cannot reach it
// before the barrier is first initialized -- verified empirically against
// a live server (every transport refuses register/list pre-init with a
// retryable "tenant: barrier is sealed", for the tenant's own bootstrap
// identity, the same identity that drives barrier-init itself). Mirroring
// the system barrier's "qrdb init" (whose Officers field solves the
// identical problem), tenant barrier-init now takes the inline Shamir
// roster, validates it as one batch, and registers it atomically with
// initialization, through the same holder engine standalone registration
// uses -- entirely on the ALREADY-authorized trust-on-first-init path, no
// change to the shared default-deny authorization gate.
package integration_test

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// listTenantOfficers lists tenantID share holders over proto using the
// given connection args (the caller must hold tenant_barrier:officer_list,
// an ordinary RBAC permission reachable only once the barrier is
// initialized -- see provisionTenantIdentity).
func listTenantOfficers(t *testing.T, node *multiProtoNode, connArgs []string, tenantID string) []transport.OfficerStatus {
	t.Helper()
	full := append(append([]string{}, connArgs...), "--output", "json",
		"tenant", "barrier-officer", "list", "--tenant-id", tenantID)
	out, err := runCLI(t, node.cli, full...)
	if err != nil {
		t.Fatalf("list tenant officers: %v\noutput: %s", err, out)
	}
	var officers []transport.OfficerStatus
	decodeJSON(t, out, &officers)
	return officers
}

// tenantOfficerEndpoint returns the officer own mTLS endpoint for proto,
// in barrier_cli_test.go endpoint shape so (*officer).flags can be reused
// unmodified.
func tenantOfficerEndpoint(node *multiProtoNode, proto multiProtocol) endpoint {
	return endpoint{serverURL: node.serverURLFor(proto), caFile: node.ca.CertPath}
}

// runAsTenantOfficer runs the qrdb CLI authenticated as off own mTLS
// identity against node over proto, instead of a system or tenant-admin
// identity.
func runAsTenantOfficer(t *testing.T, node *multiProtoNode, proto multiProtocol, off *officer, args ...string) (string, error) {
	t.Helper()
	full := append(append([]string{}, off.flags(tenantOfficerEndpoint(node, proto))...), args...)
	return runCLI(t, node.cli, full...)
}

// claimTenantEnvelope claims off sealed envelope on tenantID barrier
// with the combined claim verb, over off own mTLS connection.
func claimTenantEnvelope(t *testing.T, node *multiProtoNode, proto multiProtocol, dir, tenantID string, off *officer) {
	t.Helper()
	off.envelope = filepath.Join(dir, fmt.Sprintf("%s-claim-%d.env", off.username, time.Now().UnixNano()))
	out, err := runAsTenantOfficer(t, node, proto, off, "tenant", "barrier-officer", "claim",
		"--tenant-id", tenantID, "--username", off.username, "--key", off.keyPath, "--out", off.envelope)
	if err != nil {
		t.Fatalf("claim tenant officer %s: %v\noutput: %s", off.username, err, out)
	}
}

// claimTenantEnvelopeSplit claims off sealed envelope with the two-step
// claim-begin / claim-complete verbs instead of the combined claim,
// signing the exact message claim-begin prints -- exercising the path a
// holder whose key signs outside this CLI (FIDO2, PIV, a hardware token)
// takes.
func claimTenantEnvelopeSplit(t *testing.T, node *multiProtoNode, proto multiProtocol, dir, tenantID string, off *officer) {
	t.Helper()
	out, err := runAsTenantOfficer(t, node, proto, off, "--output", "json", "tenant", "barrier-officer", "claim-begin",
		"--tenant-id", tenantID, "--username", off.username)
	if err != nil {
		t.Fatalf("claim-begin tenant officer %s: %v\noutput: %s", off.username, err, out)
	}
	var begin claimBeginJSON
	decodeJSON(t, out, &begin)

	message, err := hex.DecodeString(begin.SignMessageHex)
	if err != nil {
		t.Fatalf("claim-begin tenant officer %s: sign_message_hex not hex: %v", off.username, err)
	}
	sig := signNonceASN1(t, off.key, message)
	sigPath := filepath.Join(dir, fmt.Sprintf("%s-claim-sig-%d.der", off.username, time.Now().UnixNano()))
	if werr := os.WriteFile(sigPath, sig, 0o600); werr != nil {
		t.Fatalf("write claim signature for %s: %v", off.username, werr)
	}

	off.envelope = filepath.Join(dir, fmt.Sprintf("%s-split-%d.env", off.username, time.Now().UnixNano()))
	out, err = runAsTenantOfficer(t, node, proto, off, "tenant", "barrier-officer", "claim-complete",
		"--tenant-id", tenantID, "--username", off.username,
		"--nonce-hex", begin.NonceHex, "--sig-file", sigPath, "--out", off.envelope)
	if err != nil {
		t.Fatalf("claim-complete tenant officer %s: %v\noutput: %s", off.username, err, out)
	}
}

// tenantQuorumSubmit submits off claimed envelope to tenantID op round
// over off own mTLS connection, answering the mtls factor with its key.
func tenantQuorumSubmit(t *testing.T, node *multiProtoNode, proto multiProtocol, tenantID, op string, off *officer) *transport.QuorumOutcome {
	t.Helper()
	out, err := runAsTenantOfficer(t, node, proto, off, "--output", "json", "tenant", "barrier-quorum", "submit",
		"--tenant-id", tenantID, "--op", op, "--username", off.username,
		"--envelope", off.envelope, "--key", off.keyPath)
	if err != nil {
		t.Fatalf("tenant barrier-quorum submit --op %s %s: %v\noutput: %s", op, off.username, err, out)
	}
	var outcome transport.QuorumOutcome
	decodeJSON(t, out, &outcome)
	return &outcome
}

// TestTenantBarrierOfficer_ShamirLifecycle_PerProtocol drives the full
// tenant-scoped Shamir share-holder lifecycle -- inline-roster init, list,
// claim (both styles), and barrier-quorum seal/unseal -- through the real
// CLI binary against every transport, proving the happy path the
// package-level comment above describes.
func TestTenantBarrierOfficer_ShamirLifecycle_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementSelfManagedCANode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantBarrierOfficerShamirLifecycleOverProtocol(t, node, proto)
		})
	}
}

// testTenantBarrierOfficerShamirLifecycleOverProtocol creates a fresh
// dedicated-isolation tenant, initializes its Shamir barrier with alice and
// bob named inline, and drives list, both claim styles, and a seal/unseal
// quorum round, all over proto, asserting real output at each step.
func testTenantBarrierOfficerShamirLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	workDir := t.TempDir()
	tenantID := fmt.Sprintf("tbo-%s-%d", proto.name, time.Now().UnixNano())

	// The tenant's own bootstrap mTLS identity drives barrier-init: its IAM
	// does not exist yet, so this is the trust-on-first-init path, not RBAC
	// -- and that path admits ONLY the exact certificate the tenant's first
	// admin was onboarded with (createTenantWithAdminCert), never a
	// separately-minted tenant identity (generateClientCertForTenant is for
	// AFTER the barrier is up, once ordinary RBAC can bind a role to it).
	adminUsername := tenantID + "-admin"
	bootCertPath, bootKeyPath := createTenantWithAdminCert(t, node, proto, tenantID, "Tenant Officer Shamir Lifecycle", "raft", false, adminUsername)
	bootConn := tenantCLIArgsForProto(node, proto, bootCertPath, bootKeyPath)

	csrAlice, keyAlice := generateOfficerCSR(t, workDir, "alice")
	csrBob, keyBob := generateOfficerCSR(t, workDir, "bob")
	officerKeys := map[string]*ecdsa.PrivateKey{"alice": keyAlice, "bob": keyBob}

	initArgs := append(append([]string{}, bootConn...), "--output", "json", "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "shamir", "--threshold", "2", "--shares", "2",
		"--officers", "alice="+csrAlice, "--officers", "bob="+csrBob)
	out, err := runCLI(t, node.cli, initArgs...)
	if err != nil {
		t.Fatalf("[%s] tenant barrier-init with inline roster: %v\noutput: %s", proto.name, err, out)
	}
	var initResp transport.TenantBarrierInitResponse
	decodeJSON(t, out, &initResp)
	if initResp.Strategy != "shamir" || initResp.Threshold != 2 || initResp.TotalShares != 2 || initResp.ShareCount != 2 {
		t.Fatalf("[%s] unexpected tenant barrier-init response: %+v", proto.name, initResp)
	}
	if len(initResp.Holders) != 2 {
		t.Fatalf("[%s] expected 2 inline holder certs back from barrier-init, got %+v", proto.name, initResp.Holders)
	}

	officers := make(map[string]*officer, 2)
	for _, h := range initResp.Holders {
		key, ok := officerKeys[h.Username]
		if !ok {
			t.Fatalf("[%s] barrier-init returned an unexpected holder %q", proto.name, h.Username)
		}
		certPath, keyPath := writeOfficerKeyPair(t, workDir, h.Username, key, []byte(h.CertPEM))
		officers[h.Username] = &officer{username: h.Username, key: key, certPath: certPath, keyPath: keyPath}
	}
	alice, bob := officers["alice"], officers["bob"]
	if alice == nil || bob == nil {
		t.Fatalf("[%s] expected both alice and bob back from barrier-init, got %+v", proto.name, initResp.Holders)
	}

	// Post-init the tenant IAM is readable, so the sealed-window identity-gated
	// bypass that carried bootConn through barrier-init closes: barrier-status
	// (and every later holder-management call) now goes through ordinary RBAC,
	// exactly like the system barrier post-init. Provision that role before
	// the first status check rather than after.
	adminCertPath, adminKeyPath := provisionTenantIdentity(t, node, tenantID, "tenant-barrier-admin",
		[]string{"tenant_barrier:officer_list", "tenant_barrier:status", "tenant_barrier:seal", "admin:health"})
	adminConn := tenantCLIArgsForProto(node, proto, adminCertPath, adminKeyPath)

	statusArgs := append(append([]string{}, adminConn...), "--output", "json", "tenant", "barrier-status", "--id", tenantID)
	out, err = runCLI(t, node.cli, statusArgs...)
	if err != nil {
		t.Fatalf("[%s] tenant barrier-status: %v\noutput: %s", proto.name, err, out)
	}
	var status transport.TenantBarrierStatusResponse
	decodeJSON(t, out, &status)
	if !status.Initialized || status.Sealed || status.Strategy != "shamir" {
		t.Fatalf("[%s] expected an initialized, unsealed shamir barrier immediately after init, got %+v", proto.name, status)
	}

	before := listTenantOfficers(t, node, adminConn, tenantID)
	if len(before) != 2 {
		t.Fatalf("[%s] expected 2 enrolled officers before claim, got %+v", proto.name, before)
	}
	for _, o := range before {
		if !o.SharePending {
			t.Fatalf("[%s] officer %s: expected an unclaimed share pending before claim, got %+v", proto.name, o.Username, o)
		}
	}

	claimTenantEnvelope(t, node, proto, workDir, tenantID, alice)
	claimTenantEnvelopeSplit(t, node, proto, workDir, tenantID, bob)

	after := listTenantOfficers(t, node, adminConn, tenantID)
	if len(after) != 2 {
		t.Fatalf("[%s] expected 2 enrolled officers after claim, got %+v", proto.name, after)
	}
	for _, o := range after {
		if o.SharePending {
			t.Fatalf("[%s] officer %s: expected the share claimed after claim, got %+v", proto.name, o.Username, o)
		}
	}

	// A Shamir tenant barrier refuses a single-admin seal; only a seal
	// quorum of its own share holders may seal it.
	sealArgs := append(append([]string{}, adminConn...), "tenant", "barrier-seal", "--id", tenantID)
	out, err = runCLI(t, node.cli, sealArgs...)
	if err == nil {
		t.Fatalf("[%s] expected a single-admin tenant barrier-seal to be refused, got success: %s", proto.name, out)
	}
	if !strings.Contains(out, "shamir barrier requires a seal quorum") {
		t.Fatalf("[%s] expected the shamir-quorum-required error, got: %s", proto.name, out)
	}

	sealOutcome := tenantQuorumSubmit(t, node, proto, tenantID, "seal", alice)
	sealOutcome = tenantQuorumSubmit(t, node, proto, tenantID, "seal", bob)
	if !sealOutcome.Complete {
		t.Fatalf("[%s] expected the seal quorum to complete once both holders submitted, got %+v", proto.name, sealOutcome)
	}
	out, err = runCLI(t, node.cli, statusArgs...)
	if err != nil {
		t.Fatalf("[%s] tenant barrier-status after seal: %v\noutput: %s", proto.name, err, out)
	}
	decodeJSON(t, out, &status)
	if !status.Sealed {
		t.Fatalf("[%s] expected the tenant barrier sealed after the seal quorum, got %+v", proto.name, status)
	}

	unsealOutcome := tenantQuorumSubmit(t, node, proto, tenantID, "unseal", alice)
	unsealOutcome = tenantQuorumSubmit(t, node, proto, tenantID, "unseal", bob)
	if !unsealOutcome.Complete {
		t.Fatalf("[%s] expected the unseal quorum to complete once both holders submitted, got %+v", proto.name, unsealOutcome)
	}
	out, err = runCLI(t, node.cli, statusArgs...)
	if err != nil {
		t.Fatalf("[%s] tenant barrier-status after unseal: %v\noutput: %s", proto.name, err, out)
	}
	decodeJSON(t, out, &status)
	if status.Sealed {
		t.Fatalf("[%s] expected the tenant barrier unsealed after the unseal quorum, got %+v", proto.name, status)
	}
}

// startTenantEntitlementSelfManagedCANode boots a node identically to
// startTenantEntitlementNode (every transport, a passphrase-strategy system
// barrier already initialized and unsealed, the same volume-pool +
// permissive-entitlement config) but WITHOUT external --tls-* flags, so the
// daemon self-generates (and persists under --data-dir) a CA that carries
// its own private key -- exactly the "path 3" TLS resolution barrier_cli_test.go
// documents at length. That is a hard requirement here, not a convenience:
// cmd/qrdbd/seal_subsystem.go's ceremonyCAFromBundle only signs holder CSRs
// when the TLS bundle carries a CA private key, and
// pkg/server/lifecycle.go wires a TENANT's holder deps from that SAME system
// ceremony CA (coreSvc.HolderDeps()) -- so under the external-CA pattern
// startTenantEntitlementNode uses, a tenant's inline Shamir officer roster
// fails closed with "ceremony: nil CA provided", exactly like the system
// barrier's own officer registration would. This node makes the tenant-scoped
// Shamir officer lifecycle testable while keeping the volume-pool/raft
// entitlement config TestTenant_CLI_IsolationTypeSuccess_PerProtocol and its
// siblings rely on.
func startTenantEntitlementSelfManagedCANode(t *testing.T) *multiProtoNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	dataDir := t.TempDir()
	volumePoolDir := t.TempDir()
	cfgPath := writeTenantEntitlementConfig(t, volumePoolDir)

	sockDir, err := os.MkdirTemp("", "qrdb-tent-sm-")
	if err != nil {
		t.Fatalf("startTenantEntitlementSelfManagedCANode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	restPort := tcpPorts[0]
	grpcPort := tcpPorts[1]
	mcpPort := tcpPorts[2]
	quicPort := quicPorts[0]

	const passphrase = "tenant-entitlement-harness-passphrase"

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}

	startNode(t, bin, args, "[tenant-entitlement-sm] ")

	ca := loadDaemonCA(t, dataDir)

	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, passphrase)

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{restPort}, 60*time.Second)

	// boundAdminCert (not generateAdminClientCert) returns the SAME identity
	// initAndUnsealBarrier's admin_cert_pem BYOC input just bound to the
	// admin account at first boot -- auth.mtls_binding_policy's default
	// "required" refuses any OTHER CN=admin-shaped certificate, bound or
	// not, so generateAdminClientCert's fresh, never-bound cert is refused
	// with "presented certificate ... is not bound to admin in tenant
	// system" (go-qrdb's admin-binding hardening; see boundAdminCert's own
	// doc comment).
	clientCert, clientKey := ca.boundAdminCert(t, dataDir)

	n := &multiProtoNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		clientCert: clientCert,
		clientKey:  clientKey,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		passphrase: passphrase,
	}

	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}

	// The replicated catalog has no pool until an explicit API call creates
	// one (docs/volume-pools/architecture.md D2), and registerDedicatedBarrier
	// always resolves through the pooled registration path regardless of
	// isolation type, so register the node's configured "default" pool
	// before any caller places a tenant on it -- matching
	// startTenantEntitlementNode's identical step.
	createVolumePoolCLI(t, cli, n.cliArgsFor(multiProtocols[0]), "default")

	return n
}

// TestTenantBarrierInitShamirRoster_CLI_NegativePaths_PerProtocol drives the
// inline Shamir officer roster's negative paths through the real CLI
// binary, against every transport the roster is wired for: a roster whose
// size does not match --shares is refused before any request reaches the
// server (the CLI's own --officers/--custodians count check); a non-empty
// roster on a non-Shamir strategy is refused outright
// (RosterRequiresShamirError) rather than silently discarded; a second
// barrier-init over an already-initialized Shamir tenant barrier is refused
// (seal.ErrAlreadyInitialized) with nothing from the attempted roster
// persisted -- the original holder set, unchanged.
func TestTenantBarrierInitShamirRoster_CLI_NegativePaths_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startTenantEntitlementSelfManagedCANode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			testTenantBarrierInitShamirRosterNegativePathsOverProtocol(t, node, proto)
		})
	}
}

// testTenantBarrierInitShamirRosterNegativePathsOverProtocol drives the
// negative-path table above against a fresh dedicated-isolation tenant over
// proto.
func testTenantBarrierInitShamirRosterNegativePathsOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol) {
	t.Helper()
	workDir := t.TempDir()
	tenantID := fmt.Sprintf("tbo-neg-%s-%d", proto.name, time.Now().UnixNano())

	adminUsername := tenantID + "-admin"
	bootCertPath, bootKeyPath := createTenantWithAdminCert(t, node, proto, tenantID, "Tenant Officer Negative Paths", "raft", false, adminUsername)
	bootConn := tenantCLIArgsForProto(node, proto, bootCertPath, bootKeyPath)

	csrAlice, _ := generateOfficerCSR(t, workDir, "alice")
	csrBob, _ := generateOfficerCSR(t, workDir, "bob")

	// A roster whose size does not equal --shares is refused up front, by
	// the CLI itself, before any request ever reaches the server.
	mismatchArgs := append(append([]string{}, bootConn...), "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "shamir", "--threshold", "2", "--shares", "3",
		"--officers", "alice="+csrAlice, "--officers", "bob="+csrBob)
	out, err := runCLI(t, node.cli, mismatchArgs...)
	if err == nil {
		t.Fatalf("[%s] barrier-init with a roster size mismatch succeeded, want refusal: %s", proto.name, out)
	}
	if !strings.Contains(out, "must equal the number of --officers plus --custodians") {
		t.Fatalf("[%s] expected a roster-size-mismatch refusal, got: %s", proto.name, out)
	}

	// A non-empty roster on a non-Shamir strategy is refused outright
	// (RosterRequiresShamirError), not silently discarded.
	nonShamirArgs := append(append([]string{}, bootConn...), "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "none", "--shares", "1",
		"--officers", "alice="+csrAlice)
	out, err = runCLI(t, node.cli, nonShamirArgs...)
	if err == nil {
		t.Fatalf("[%s] barrier-init --strategy none with a non-empty roster succeeded, want refusal: %s", proto.name, out)
	}
	if !strings.Contains(out, "requires --strategy shamir") {
		t.Fatalf("[%s] expected a RosterRequiresShamirError refusal, got: %s", proto.name, out)
	}

	// The real, successful init: now actually bring the barrier up.
	initArgs := append(append([]string{}, bootConn...), "--output", "json", "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "shamir", "--threshold", "2", "--shares", "2",
		"--officers", "alice="+csrAlice, "--officers", "bob="+csrBob)
	out, err = runCLI(t, node.cli, initArgs...)
	if err != nil {
		t.Fatalf("[%s] tenant barrier-init with inline roster: %v\noutput: %s", proto.name, err, out)
	}

	// A second barrier-init attempt, with a COMPLETELY DIFFERENT roster,
	// must be refused -- and leave the original holder set untouched.
	csrCarol, _ := generateOfficerCSR(t, workDir, "carol")
	csrDave, _ := generateOfficerCSR(t, workDir, "dave")
	reinitArgs := append(append([]string{}, bootConn...), "tenant", "barrier-init",
		"--id", tenantID, "--strategy", "shamir", "--threshold", "2", "--shares", "2",
		"--officers", "carol="+csrCarol, "--officers", "dave="+csrDave)
	out, err = runCLI(t, node.cli, reinitArgs...)
	if err == nil {
		t.Fatalf("[%s] re-init over an already-initialized shamir tenant barrier succeeded, want refusal: %s", proto.name, out)
	}
	if !strings.Contains(out, "barrier already initialized") {
		t.Fatalf("[%s] expected an already-initialized refusal, got: %s", proto.name, out)
	}

	adminCertPath, adminKeyPath := provisionTenantIdentity(t, node, tenantID, "tenant-barrier-admin-neg",
		[]string{"tenant_barrier:officer_list"})
	adminConn := tenantCLIArgsForProto(node, proto, adminCertPath, adminKeyPath)
	after := listTenantOfficers(t, node, adminConn, tenantID)
	if len(after) != 2 {
		t.Fatalf("[%s] expected the original 2 officers after the refused re-init, got %d: %+v", proto.name, len(after), after)
	}
	for _, name := range []string{"carol", "dave"} {
		for _, o := range after {
			if o.Username == name {
				t.Fatalf("[%s] officer list after refused re-init contains %q, which the refused attempt must never have registered", proto.name, name)
			}
		}
	}
}
