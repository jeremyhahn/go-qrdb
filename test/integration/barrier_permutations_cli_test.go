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

// barrier_permutations_cli_test.go extends barrier_cli_test.go's
// exhaustive-but-https-only `barrier` lifecycle coverage (init, init-shamir,
// unseal, seal, and the share-holder quorum: seal, unseal, root-token) to the
// other CLI-selectable transports, looping protocol tables exactly as
// drain_test.go's TestAdminDrain_PerProtocol does.
//
// FIXED transport gap: of the five CLI-selectable transports, gRPC, native
// QUIC, and Unix (grpc-over-UDS) used to be unable to reach a daemon's
// PRE-HOST SEALED-WINDOW listener at all (i.e. could not drive `barrier
// init`/`init-shamir` from a cold, uninitialized barrier), because their SDK
// Connect() implementations (sdk/go/transport/{grpc,quic,unix}/transport.go)
// require a successful Health RPC before returning, and the gRPC/QUIC Health
// handlers (pkg/api/grpc/service.go, pkg/api/quic/handlers_admin.go)
// unconditionally delegated to adminSvc, which is nil throughout the
// pre-host sealed window (cmd/qrdbd/seal_subsystem.go's awaitUnseal never
// constructs an admin service -- that is the entire reason the sealed-window
// listener exists). The fix: those Health handlers now report the
// documented service.HealthStatusSealed ("sealed") verdict directly instead
// of erroring when adminSvc is nil, which the client-side "no error +
// non-empty status" success check already treats as a live connection. See
// TestBarrierPermutations_SealedWindowConnectivity_AllProtocols below for
// the proof. coldSealedWindowProtocols is therefore now the full
// multiProtocols set -- every lifecycle test in this file that starts from a
// cold sealed barrier exercises all five transports.
//
// Why a dedicated per-protocol fixture instead of reusing
// multi_protocol_helpers_test.go's startMultiProtocolNode: that fixture
// boots a node whose system barrier is ALREADY initialized and unsealed
// (via a REST-only pre-host call in initAndUnsealBarrier) so kv/database
// tests can start immediately. Barrier-permutation tests need the OPPOSITE
// starting point -- a cold, uninitialized barrier -- so the `barrier init`
// verb itself can be driven, from a cold start, over each working protocol.
// startFreshSealedNode below boots that cold-start fixture with every
// transport enabled (mirroring startMultiProtocolNode's transport flags)
// and NO --tls-* flags, so the daemon self-generates a CA WITH its private
// key -- required for the Shamir officer CSR-signing flow, exactly as
// barrier_cli_test.go's startSealNode documents.
//
// This file freely reuses the unexported fixtures barrier_cli_test.go
// already built for the https-only lifecycle (writeSealConfig, testCA,
// loadDaemonCA, waitForNonEmptyFile, runCLISplit, barrierStatus,
// barrierHasRecoveryKeys, waitForBarrierReachable, quorumAll, quorumSubmit,
// registerOfficer, claimEnvelope) since every one of them
// already takes a transport-agnostic `flags []string` / `serverURL string`
// parameter -- none of them hard-code https. Building a fresh per-protocol
// node and threading its own --server/--ca-file/--cert-file/--key-file
// flags through those exact same helpers is therefore sufficient to get
// full multi-protocol coverage with no duplicated protocol logic.
package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// coldSealedWindowProtocols is the set of CLI-selectable transports able to
// reach a daemon's pre-host sealed-window listener from a cold, uninitialized
// barrier: as of the sealed-window Health fix (see this file's package
// comment and TestBarrierPermutations_SealedWindowConnectivity_AllProtocols),
// that is all five -- REST, gRPC, native QUIC, MCP, and Unix (grpc-over-UDS).
// It remains a distinct variable (rather than every call site using
// multiProtocols directly) purely for the descriptive name: every lifecycle
// test in this file that must start from a cold sealed barrier loops over
// it. Once a node reaches full boot (RBAC-backed host up, admin service
// constructed), all five protocols work identically regardless, as proven by
// officer_ceremony_cli_test.go's post-boot grpcs://.../unix:// coverage and
// this file's own post-boot assertions.
var coldSealedWindowProtocols = multiProtocols

// ---------------------------------------------------------------------------
// Fresh, cold-start, all-five-transport fixture
// ---------------------------------------------------------------------------

// permNode holds the connection details for a single-node qrdbd booted with
// every transport enabled (REST, gRPC, QUIC, MCP, Unix) and a seal
// subsystem constructed per the requested strategy, but NOT yet
// initialized: the pre-host sealed-window listener (pkg/server/sealed_listener.go)
// is left running for the test to drive `barrier init`/`init-shamir` and the
// rest of the lifecycle against, over any of the five protocols. The daemon
// self-generates its CA (no --tls-* flags), so ca/adminCert/adminKey below
// come straight off disk exactly as barrier_cli_test.go's sealNode does.
type permNode struct {
	cli        string
	ca         *testCA
	dataDir    string
	unixSocket string
	restPort   int
	grpcPort   int
	mcpPort    int
	quicPort   int
	adminCert  string
	adminKey   string
}

// portFor returns the TCP/UDP port serving the given named protocol.
func (n *permNode) portFor(proto string) int {
	switch proto {
	case "rest":
		return n.restPort
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

// serverURLFor returns the --server value that selects the given protocol
// against this node.
func (n *permNode) serverURLFor(proto multiProtocol) string {
	if proto.name == "unix" {
		return "unix://" + n.unixSocket
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", proto.scheme, n.portFor(proto.name))
}

// flagsFor returns the qrdb CLI global flags that select the given
// protocol and present the daemon's own bootstrap admin identity
// (CN=admin, OU=system). That identity is ignored by the pre-host
// sealed-window listener (no RBAC is installed there -- the barrier
// credential itself is the authorization) and recognized as Owner by the
// full RBAC-backed server once the barrier is unsealed, so the same flag
// set works unmodified across the cold-start and post-boot phases of every
// test in this file.
func (n *permNode) flagsFor(proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", n.ca.CertPath,
		"--cert-file", n.adminCert,
		"--key-file", n.adminKey,
		"--timeout", "30s",
	}
}

// startFreshSealedNode boots a single-node qrdbd with every transport
// enabled and the seal barrier configured per strategy/threshold/total
// (see writeSealConfig), but leaves the barrier uninitialized. total and
// threshold are ignored (pass 0, 0) for the "none" and "passphrase"
// strategies.
func startFreshSealedNode(t *testing.T, strategy string, threshold, total int) *permNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	dataDir := t.TempDir()
	cfgPath := writeSealConfig(t, strategy, threshold, total)

	// Unix socket paths are limited to ~108 bytes by the kernel; keep the
	// socket under a short /tmp path exactly as multi_protocol_helpers_test.go
	// does for the same reason.
	sockDir, err := os.MkdirTemp("", "qrdb-perm-")
	if err != nil {
		t.Fatalf("startFreshSealedNode: socket dir: %v", err)
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
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
		// Deliberately no --tls-ca-file/--tls-cert-file/--tls-key-file: the
		// daemon self-generates a CA WITH its private key, required for
		// ceremonyCAFromBundle to sign Security Officer CSRs during the
		// sealed window (see barrier_cli_test.go's package doc comment).
	}
	startNode(t, bin, args, "[barrier-perm] ")

	caPath := filepath.Join(dataDir, "ca.pem")
	adminCertPath := filepath.Join(dataDir, "admin-cert.pem")
	adminKeyPath := filepath.Join(dataDir, "admin-key.pem")
	waitForNonEmptyFile(t, caPath, 30*time.Second)
	waitForNonEmptyFile(t, adminCertPath, 30*time.Second)
	waitForNonEmptyFile(t, adminKeyPath, 30*time.Second)

	return &permNode{
		cli:        cli,
		ca:         loadDaemonCA(t, dataDir),
		dataDir:    dataDir,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		adminCert:  adminCertPath,
		adminKey:   adminKeyPath,
	}
}

// waitForFullBoot blocks until the full RBAC-backed host (not just the
// pre-host sealed-window listener) is serving on the REST admin port,
// which every protocol's readiness depends on since QUIC/gRPC/MCP/Unix
// only start alongside it.
func (n *permNode) waitForFullBoot(t *testing.T) {
	t.Helper()
	waitForHealthy(t, n.dataDir, 60*time.Second)
	waitForAdmin(t, n.ca.Pool, n.restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, n.ca, []int{n.restPort}, 60*time.Second)
}

// ---------------------------------------------------------------------------
// Strategy "none": identity passthrough, no secret material at all.
// ---------------------------------------------------------------------------

// TestBarrierPermutations_None_PerProtocol drives `barrier init
// --barrier-strategy none` -> `barrier status` -> a KV round trip over
// each of the five CLI-selectable protocols, proving the "none" strategy
// (cleartext, dev-only, no passphrase/PIN allowed) reaches an
// immediately-unsealed, fully operational state identically on every
// transport.
func TestBarrierPermutations_None_PerProtocol(t *testing.T) {
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "none", 0, 0)
			flags := node.flagsFor(proto)

			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			status := barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected fresh none-strategy barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init", "--barrier-strategy", "none")...); err != nil {
				t.Fatalf("[%s] barrier init --barrier-strategy none: %v\nstderr: %s", proto.name, err, stderr)
			}

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected none-strategy barrier to be unsealed immediately after init", proto.name)
			}
			if status.Strategy != "none" {
				t.Errorf("[%s] expected strategy 'none', got %q", proto.name, status.Strategy)
			}

			node.waitForFullBoot(t)

			key := "barrier-perm-none-" + proto.name
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"kv", "put", key, "none-value")...); err != nil {
				t.Fatalf("[%s] kv put: %v\nstderr: %s", proto.name, err, stderr)
			}
			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "kv", "get", key)...)
			if err != nil {
				t.Fatalf("[%s] kv get: %v\nstderr: %s", proto.name, err, stderr)
			}
			if !strings.Contains(stdout, "none-value") {
				t.Fatalf("[%s] kv get: expected value in output, got: %s", proto.name, stdout)
			}

			// "none" carries no secret, so PINs/passphrase are rejected at
			// init and there is nothing to unseal with; confirm the
			// dual-control PIN rejection is enforced identically over
			// every protocol too (typed PINsNotAllowedError -- checked
			// against a SECOND fresh none-strategy node since this one is
			// already initialized).
		})
	}
}

// TestBarrierPermutations_None_PINsRejected_PerProtocol proves
// PINsNotAllowedError fires identically over every protocol: `barrier
// init --barrier-strategy none --so-pin ... --user-pin ...` must fail
// closed before any state change.
func TestBarrierPermutations_None_PINsRejected_PerProtocol(t *testing.T) {
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "none", 0, 0)
			flags := node.flagsFor(proto)
			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			_, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init", "--barrier-strategy", "none",
				"--so-pin", "SO1", "--user-pin", "USR1")...)
			if err == nil {
				t.Fatalf("[%s] expected barrier init --barrier-strategy none with PINs to fail", proto.name)
			}
			if !strings.Contains(stderr, "not allowed when --barrier-strategy=none") {
				t.Errorf("[%s] expected PINsNotAllowedError message, got stderr: %s", proto.name, stderr)
			}

			status := barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Errorf("[%s] rejected init must leave the barrier sealed, got: %+v", proto.name, status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Strategy "passphrase": single-secret lifecycle.
// ---------------------------------------------------------------------------

// TestBarrierPermutations_PassphraseLifecycle_PerProtocol mirrors
// barrier_cli_test.go's TestBarrierCLI_PassphraseLifecycle (init, unseal
// wrong/right, seal, data-plane proof) but loops the full canonical
// multiProtocols table instead of exercising REST alone.
func TestBarrierPermutations_PassphraseLifecycle_PerProtocol(t *testing.T) {
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "passphrase", 0, 0)
			flags := node.flagsFor(proto)
			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			status := barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected fresh passphrase barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			const passphrase = "barrier-perm-passphrase-1"
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init", "--barrier-strategy", "passphrase", "--passphrase", passphrase)...); err != nil {
				t.Fatalf("[%s] barrier init: %v\nstderr: %s", proto.name, err, stderr)
			}

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed immediately after init", proto.name)
			}
			if status.Strategy != "passphrase" {
				t.Errorf("[%s] expected strategy 'passphrase', got %q", proto.name, status.Strategy)
			}

			node.waitForFullBoot(t)

			key := "barrier-perm-pp-" + proto.name
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"kv", "put", key, "pp-value")...); err != nil {
				t.Fatalf("[%s] kv put: %v\nstderr: %s", proto.name, err, stderr)
			}

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "barrier", "seal", "-y")...); err != nil {
				t.Fatalf("[%s] barrier seal: %v\nstderr: %s", proto.name, err, stderr)
			}
			status = barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected barrier to be sealed after 'barrier seal -y'", proto.name)
			}

			// Negative: a wrong passphrase must fail and must not unseal.
			if _, _, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "unseal", "--passphrase", "definitely-wrong")...); err == nil {
				t.Fatalf("[%s] expected non-zero exit for wrong-passphrase unseal", proto.name)
			}
			status = barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] wrong-passphrase unseal must not have left the barrier unsealed", proto.name)
			}

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "unseal", "--passphrase", passphrase)...); err != nil {
				t.Fatalf("[%s] barrier unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed after the correct passphrase", proto.name)
			}

			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "kv", "get", key)...)
			if err != nil {
				t.Fatalf("[%s] kv get after seal/unseal cycle: %v\nstderr: %s", proto.name, err, stderr)
			}
			if !strings.Contains(stdout, "pp-value") {
				t.Fatalf("[%s] kv get after seal/unseal cycle: expected surviving value, got: %s", proto.name, stdout)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Strategy "shamir": M-of-N officer lifecycle.
// ---------------------------------------------------------------------------

// TestBarrierPermutations_ShamirLifecycle_PerProtocol mirrors
// barrier_cli_test.go's TestBarrierCLI_ShamirLifecycle's core (holder
// enrolment, init-shamir, claim, and the seal, unseal and root-token
// quorums) looped over every CLI-selectable protocol. Rekey and the
// independent recovery-key scope are exhaustively covered on REST already
// (barrier_cli_test.go) and are intentionally left out of this per-protocol
// loop to bound the number of daemons this file boots; see the file-level
// comment.
func TestBarrierPermutations_ShamirLifecycle_PerProtocol(t *testing.T) {
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			workDir := t.TempDir()
			const threshold, total = 2, 3
			node := startFreshSealedNode(t, "shamir", threshold, total)
			flags := node.flagsFor(proto)
			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			status := barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected fresh shamir barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			// Officers register against the sealed-window listener (no RBAC
			// there) over the SAME protocol under test.
			alice := registerOfficer(t, node.cli, workDir, flags, systemScope(transport.DomainUnseal), "alice-"+proto.name)
			bob := registerOfficer(t, node.cli, workDir, flags, systemScope(transport.DomainUnseal), "bob-"+proto.name)
			registerOfficer(t, node.cli, workDir, flags, systemScope(transport.DomainUnseal), "carol-"+proto.name)

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init-shamir",
				"--threshold", strconv.Itoa(threshold), "--total", strconv.Itoa(total))...); err != nil {
				t.Fatalf("[%s] barrier init-shamir: %v\nstderr: %s", proto.name, err, stderr)
			}

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed immediately after init-shamir", proto.name)
			}
			if status.Strategy != "shamir" {
				t.Errorf("[%s] expected strategy 'shamir', got %q", proto.name, status.Strategy)
			}

			node.waitForFullBoot(t)

			ep := endpoint{serverURL: node.serverURLFor(proto), caFile: node.ca.CertPath}
			claimEnvelope(t, node.cli, workDir, ep, systemScope(transport.DomainUnseal), alice)
			claimEnvelope(t, node.cli, workDir, ep, systemScope(transport.DomainUnseal), bob)

			quorumAll(t, node.cli, ep, systemScope(transport.DomainUnseal), "seal", alice, bob)
			status = barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected barrier to be sealed after the seal quorum", proto.name)
			}

			// A below-threshold round is progress, not an error, and leaves
			// the barrier sealed.
			if out := quorumAll(t, node.cli, ep, systemScope(transport.DomainUnseal), "unseal", alice); out.Complete {
				t.Fatalf("[%s] one of two submissions completed the round: %+v", proto.name, out)
			}
			status = barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] a below-threshold round must not unseal the barrier", proto.name)
			}

			if out := quorumAll(t, node.cli, ep, systemScope(transport.DomainUnseal), "unseal", bob); !out.Complete {
				t.Fatalf("[%s] the second submission did not complete the round: %+v", proto.name, out)
			}
			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed after the unseal quorum", proto.name)
			}

			if out := quorumAll(t, node.cli, ep, systemScope(transport.DomainUnseal), "root-token", alice, bob); !out.Complete || out.RootToken == "" {
				t.Fatalf("[%s] root-token round = %+v, want a root token", proto.name, out)
			}

			key := "barrier-perm-shamir-" + proto.name
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"kv", "put", key, "shamir-value")...); err != nil {
				t.Fatalf("[%s] kv put after shamir unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "kv", "get", key)...)
			if err != nil {
				t.Fatalf("[%s] kv get after shamir unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
			if !strings.Contains(stdout, "shamir-value") {
				t.Fatalf("[%s] kv get after shamir unseal: expected surviving value, got: %s", proto.name, stdout)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Sealed-window connectivity: all five transports reach a cold daemon.
// ---------------------------------------------------------------------------

// TestBarrierPermutations_SealedWindowConnectivity_AllProtocols proves that
// a completely fresh, cold, uninitialized daemon's pre-host sealed-window
// listener is reachable -- and drives a full barrier init/unseal cold-boot
// lifecycle -- over ALL FIVE CLI-selectable transports (REST, gRPC, native
// QUIC, MCP, and Unix/grpc-over-UDS).
//
// This used to be a real, confirmed, cross-transport bug: gRPC, native
// QUIC, and Unix could not connect AT ALL while the barrier was sealed, so
// none of the sealed-window operations pkg/api/sealedwindow.Table
// allowlists for those three transports (barrier init/init-shamir/unseal,
// ceremony initialize, officer register/claim, ...) were actually reachable
// on them despite being nominally allowlisted. REST and MCP were always
// unaffected.
//
// Root cause and fix (verified by reading source, not assumed):
//
//  1. The gRPC and Unix SDK transports' Connect() (sdk/go/transport/grpc/
//     transport.go's connectHealthy, reused verbatim by
//     sdk/go/transport/unix/transport.go) and the native QUIC SDK
//     transport's Connect() (sdk/go/transport/quic/transport.go) all
//     require a successful Health RPC before Connect returns nil -- but
//     that check was already lenient (no error AND a non-empty Status
//     string), never requiring Status=="ok" specifically.
//  2. The gRPC Health RPC handler (pkg/api/grpc/service.go's
//     qrdbServiceServer.Health) and the QUIC admin.Health handler
//     (pkg/api/quic/handlers_admin.go's handleHealth) used to
//     unconditionally delegate to s.adminSvc, which is nil throughout the
//     pre-host sealed window -- cmd/qrdbd/seal_subsystem.go's awaitUnseal
//     only wires WithBarrierService/WithCeremonyService/
//     WithCredentialService/WithBarrierRegistry into the sealed-window
//     server, never WithAdminService (the admin/KV/data-plane services do
//     not exist yet -- that is the entire reason the sealed-window
//     listener exists), so both handlers ALWAYS errored during the sealed
//     window, failing Connect()'s health probe.
//  3. The fix: both Health handlers now report the documented
//     service.HealthStatusSealed ("sealed") verdict directly -- with a nil
//     error -- when adminSvc is nil, instead of erroring. Connect()'s
//     existing "no error + non-empty status" check already treats that as
//     a live connection, so no SDK-side change was needed: a genuinely
//     unreachable daemon (nothing listening, TLS/handshake failure, etc.)
//     still fails Connect() exactly as before; only a reachable,
//     correctly-sealed daemon is now recognized as connected.
func TestBarrierPermutations_SealedWindowConnectivity_AllProtocols(t *testing.T) {
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startFreshSealedNode(t, "passphrase", 0, 0)
			flags := node.flagsFor(proto)

			// Connect() + `barrier status` against a daemon that has never
			// been initialized is the connectivity assertion under test.
			waitForBarrierReachable(t, node.cli, flags, 60*time.Second)

			status := barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected a fresh passphrase barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			passphrase := "sealed-window-connectivity-" + proto.name
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "init", "--barrier-strategy", "passphrase", "--passphrase", passphrase)...); err != nil {
				t.Fatalf("[%s] barrier init: %v\nstderr: %s", proto.name, err, stderr)
			}

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed immediately after init", proto.name)
			}

			node.waitForFullBoot(t)

			// Drive an explicit seal -> unseal cycle to prove `barrier
			// unseal` itself (not just init's implicit unseal) works over
			// this transport post-boot too.
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "barrier", "seal", "-y")...); err != nil {
				t.Fatalf("[%s] barrier seal: %v\nstderr: %s", proto.name, err, stderr)
			}
			status = barrierStatus(t, node.cli, flags)
			if !status.Sealed {
				t.Fatalf("[%s] expected barrier to be sealed after 'barrier seal -y'", proto.name)
			}

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"barrier", "unseal", "--passphrase", passphrase)...); err != nil {
				t.Fatalf("[%s] barrier unseal: %v\nstderr: %s", proto.name, err, stderr)
			}
			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed after 'barrier unseal'", proto.name)
			}
		})
	}
}
