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
// Every CLI-selectable transport reaches a daemon's PRE-HOST SEALED-WINDOW
// listener from a cold, uninitialized barrier. A sealed node serves the
// unseal call and nothing else, so its health call answers the typed sealed
// refusal (Unavailable with reason SEALED); each SDK transport's Connect
// (sdk/go/transport/{grpc,quic,unix,rest,mcp}/transport.go) counts that
// refusal as "up and sealed" rather than unreachable. A genuinely unreachable
// daemon still fails Connect. See
// TestBarrierPermutations_SealedWindowConnectivity_AllProtocols below for the
// proof. coldSealedWindowProtocols is therefore the full multiProtocols set
// -- every lifecycle test in this file that starts from a cold sealed barrier
// exercises all five transports.
//
// Why a dedicated per-protocol fixture instead of reusing
// multi_protocol_helpers_test.go's startMultiProtocolNode: that fixture
// boots a node whose system barrier is ALREADY initialized and unsealed
// (via a REST-only pre-host call in initAndUnsealBarrier) so kv/database
// tests can start immediately. Barrier-permutation tests need the OPPOSITE
// starting point -- a cold, uninitialized barrier -- so `qrdb init`, the only
// system bootstrap, can be driven from a cold start over each working
// protocol, and the system `barrier init` it replaced proven refused.
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
// barrier: every transport's Connect counts the typed sealed refusal as
// reachable (see this file's package comment and
// TestBarrierPermutations_SealedWindowConnectivity_AllProtocols), so that is
// all five -- REST, gRPC, native QUIC, MCP, and Unix (grpc-over-UDS).
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
// is left running for the test to drive `qrdb init` and the rest of the
// lifecycle against, over any of the five protocols. The daemon
// self-generates its CA (no --tls-* flags), read straight off disk exactly
// as barrier_cli_test.go's sealNode does; adminCert/adminKey are the
// bootstrap admin's credential, issued by that CA and bound by
// `qrdb init --admin-cert` (initSystem).
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

// noAuthFlagsFor returns the qrdb CLI global flags that select the given
// protocol and present no client certificate: the only flags that work
// before `qrdb init` has issued the admin credential.
func (n *permNode) noAuthFlagsFor(proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", n.ca.CertPath,
		"--timeout", "30s",
	}
}

// initSystem bootstraps the node with `qrdb init` over the given protocol
// (bootAdminIdentityVia), binding n.adminCert as the bootstrap admin
// (--admin-cert) -- extraArgs carry --barrier-strategy and its secret or
// holders. It returns init's JSON output.
func (n *permNode) initSystem(t *testing.T, proto multiProtocol, extraArgs ...string) string {
	t.Helper()
	return bootAdminIdentityVia(t, n.cli, n.noAuthFlagsFor(proto), sealNodeSOPIN, sealNodeUserPIN, n.adminCert, n.adminKey, "",
		append([]string{"--admin-cert", n.adminCert}, extraArgs...)...)
}

// flagsFor returns the qrdb CLI global flags that select the given
// protocol and present the bootstrap admin identity `qrdb init` issued,
// recognized as Owner by the full RBAC-backed server once the barrier is
// unsealed.
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
	waitForNonEmptyFile(t, caPath, 30*time.Second)
	ca := loadDaemonCA(t, dataDir)
	adminCert, adminKey := ca.boundAdminCert(t, t.TempDir())

	return &permNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		adminCert:  adminCert,
		adminKey:   adminKey,
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

// TestBarrierPermutations_None_PerProtocol drives `qrdb init
// --barrier-strategy none` -> `barrier status` -> a KV round trip over
// each of the five CLI-selectable protocols, proving the "none" strategy
// (cleartext, dev-only, no passphrase/PIN allowed) reaches an
// immediately-unsealed, fully operational state identically on every
// transport.
func TestBarrierPermutations_None_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node := startFreshSealedNode(t, "none", 0, 0)
			noAuth := node.noAuthFlagsFor(proto)

			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			status := barrierStatus(t, node.cli, noAuth)
			if !status.Sealed {
				t.Fatalf("[%s] expected fresh none-strategy barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			node.initSystem(t, proto, "--barrier-strategy", "none")
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected none-strategy barrier to be unsealed after qrdb init", proto.name)
			}
			if status.Strategy != "none" {
				t.Errorf("[%s] expected strategy 'none', got %q", proto.name, status.Strategy)
			}

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
		})
	}
}

// TestBarrierPermutations_SystemBarrierInitRefused_PerProtocol proves the
// system `barrier init` on a fresh node is refused identically over every
// protocol, naming `qrdb init` -- the only system bootstrap -- and leaves
// the barrier sealed.
func TestBarrierPermutations_SystemBarrierInitRefused_PerProtocol(t *testing.T) {
	t.Parallel()
	// One fresh node serves every transport: a refused init changes no
	// state, and each subtest asserts exactly that before the next runs.
	node := startFreshSealedNode(t, "none", 0, 0)
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			_, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, noAuth...),
				"barrier", "init", "--barrier-strategy", "none")...)
			if err == nil {
				t.Fatalf("[%s] system barrier init on a fresh node succeeded; it is refused in favour of qrdb init", proto.name)
			}
			if !strings.Contains(stderr, "qrdb init") {
				t.Errorf("[%s] expected the refusal to name qrdb init, got stderr: %s", proto.name, stderr)
			}

			status := barrierStatus(t, node.cli, noAuth)
			if !status.Sealed {
				t.Errorf("[%s] a refused barrier init must leave the barrier sealed, got: %+v", proto.name, status)
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
	t.Parallel()
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node := startFreshSealedNode(t, "passphrase", 0, 0)
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			status := barrierStatus(t, node.cli, noAuth)
			if !status.Sealed {
				t.Fatalf("[%s] expected fresh passphrase barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			const passphrase = "barrier-perm-passphrase-1"
			node.initSystem(t, proto, "--barrier-strategy", "passphrase", "--passphrase", passphrase)
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed after qrdb init", proto.name)
			}
			if status.Strategy != "passphrase" {
				t.Errorf("[%s] expected strategy 'passphrase', got %q", proto.name, status.Strategy)
			}

			key := "barrier-perm-pp-" + proto.name
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...),
				"kv", "put", key, "pp-value")...); err != nil {
				t.Fatalf("[%s] kv put: %v\nstderr: %s", proto.name, err, stderr)
			}

			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)...); err != nil {
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
// barrier_cli_test.go's TestBarrierCLI_ShamirLifecycle's core (holders
// named in `qrdb init`, claim, and the seal, unseal and root-token
// quorums) looped over every CLI-selectable protocol. Rekey and the
// independent recovery-key scope are exhaustively covered on REST already
// (barrier_cli_test.go) and are intentionally left out of this per-protocol
// loop to bound the number of daemons this file boots; see the file-level
// comment.
func TestBarrierPermutations_ShamirLifecycle_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			workDir := t.TempDir()
			const threshold, total = 2, 3
			node := startFreshSealedNode(t, "shamir", threshold, total)
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			status := barrierStatus(t, node.cli, noAuth)
			if !status.Sealed {
				t.Fatalf("[%s] expected fresh shamir barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			// `qrdb init`, over the SAME protocol under test, enrols the
			// holders from their CSRs and splits the unseal key to them.
			holders, holderFlags := initHolders(t, workDir, "alice-"+proto.name, "bob-"+proto.name, "carol-"+proto.name)
			initOut := node.initSystem(t, proto, append([]string{"--barrier-strategy", "shamir",
				"--threshold", strconv.Itoa(threshold), "--shares", strconv.Itoa(total)}, holderFlags...)...)
			adoptInitHolders(t, workDir, initOut, holders)
			alice, bob := holders[0], holders[1]

			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed after qrdb init", proto.name)
			}
			if status.Strategy != "shamir" {
				t.Errorf("[%s] expected strategy 'shamir', got %q", proto.name, status.Strategy)
			}

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
//  1. Every SDK transport's Connect() probes the node's health call before
//     returning nil.
//  2. A sealed node serves the unseal call and nothing else: the health
//     call, and `barrier status` with it, get the typed sealed refusal
//     (Unavailable with reason SEALED on every transport).
//  3. Connect() counts that refusal as a live, sealed node, and the CLI
//     reports `barrier status` as the refusal, which barrierStatus reads as
//     Sealed. A genuinely unreachable daemon (nothing listening,
//     TLS/handshake failure, etc.) still fails Connect().
func TestBarrierPermutations_SealedWindowConnectivity_AllProtocols(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node := startFreshSealedNode(t, "passphrase", 0, 0)
			noAuth := node.noAuthFlagsFor(proto)

			// Connect() + `barrier status` against a daemon that has never
			// been initialized is the connectivity assertion under test.
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			status := barrierStatus(t, node.cli, noAuth)
			if !status.Sealed {
				t.Fatalf("[%s] expected a fresh passphrase barrier to be sealed pre-init, got: %+v", proto.name, status)
			}

			passphrase := "sealed-window-connectivity-" + proto.name
			node.initSystem(t, proto, "--barrier-strategy", "passphrase", "--passphrase", passphrase)
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)

			status = barrierStatus(t, node.cli, flags)
			if status.Sealed {
				t.Fatalf("[%s] expected barrier to be unsealed after qrdb init", proto.name)
			}

			// Drive an explicit seal -> unseal cycle to prove `barrier
			// unseal` itself (not just init's implicit unseal) works over
			// this transport post-boot too.
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)...); err != nil {
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

// TestBarrierPermutations_PreInitStatus_PerProtocol pins the initialized flag
// a sealed node serves on its sealed surface, over every transport: before
// its first boot barrier status answers sealed and uninitialized, and once
// the barrier is initialized and sealed it answers sealed and initialized,
// with nothing more in either answer. Ceremony status, which is not that
// answer, gets the typed sealed refusal. Both the JSON and the text output
// are asserted, because the text is what an operator reads.
func TestBarrierPermutations_PreInitStatus_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range coldSealedWindowProtocols {
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node := startFreshSealedNode(t, "passphrase", 0, 0)
			noAuth := node.noAuthFlagsFor(proto)
			waitForBarrierReachable(t, node.cli, noAuth, 60*time.Second)

			assertSealedStatus := func(stage string, flags []string, initialized bool) {
				t.Helper()
				stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "--output", "json", "barrier", "status")...)
				if err != nil {
					t.Fatalf("[%s] barrier status (%s): %v\nstderr: %s", proto.name, stage, err, stderr)
				}
				var got transport.BarrierStatusResponse
				decodeJSON(t, stdout, &got)
				want := transport.BarrierStatusResponse{Sealed: true, Initialized: initialized}
				if got.Sealed != want.Sealed || got.Initialized != want.Initialized || got.Strategy != "" || got.InitializedAt != "" {
					t.Fatalf("[%s] barrier status (%s) = %+v, want only sealed=true initialized=%t", proto.name, stage, got, initialized)
				}
				stdout, stderr, err = runCLISplit(t, node.cli, append(append([]string{}, flags...), "barrier", "status")...)
				if err != nil {
					t.Fatalf("[%s] barrier status (%s, text): %v\nstderr: %s", proto.name, stage, err, stderr)
				}
				for _, line := range []string{"Sealed:          true", fmt.Sprintf("Initialized:     %t", initialized)} {
					if !strings.Contains(stdout, line) {
						t.Fatalf("[%s] barrier status (%s, text) lacks %q\nstdout: %s", proto.name, stage, line, stdout)
					}
				}
			}

			assertSealedStatus("pre-init", noAuth, false)

			stdout, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, noAuth...), "--output", "json", "ceremony", "status")...)
			if err == nil || !cliSealedRefusal(stdout+stderr) {
				t.Fatalf("[%s] ceremony status (pre-init): err = %v, want the sealed refusal\nstdout: %s\nstderr: %s", proto.name, err, stdout, stderr)
			}

			passphrase := "pre-init-status-" + proto.name
			node.initSystem(t, proto, "--barrier-strategy", "passphrase", "--passphrase", passphrase)
			node.waitForFullBoot(t)
			flags := node.flagsFor(proto)
			if _, stderr, err := runCLISplit(t, node.cli, append(append([]string{}, flags...), "barrier", "seal", "-y", "--so-pin", sealNodeSOPIN)...); err != nil {
				t.Fatalf("[%s] barrier seal: %v\nstderr: %s", proto.name, err, stderr)
			}

			assertSealedStatus("initialized, sealed", flags, true)
			assertSealedStatus("initialized, sealed, anonymous", noAuth, true)
		})
	}
}
