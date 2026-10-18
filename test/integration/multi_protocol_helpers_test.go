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

// multi_protocol_helpers_test.go generalizes drain_test.go's single-purpose
// drainNode/drainProtocol fixture into a reusable harness every CLI-binary,
// multi-protocol, table-driven test in this package can share: one
// multiProtoNode serves REST, gRPC, QUIC, MCP, and Unix simultaneously with
// auth enabled, TLS configured, and a passphrase-strategy system barrier
// already initialized and unsealed, so KV/database data-plane commands work
// immediately over any of the five transports. See
// test/integration/CLAUDE.md and drain_test.go's TestAdminDrain_PerProtocol
// for the convention this harness implements.
package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// multiProtocol describes a single CLI-selectable transport target. scheme
// is the URL scheme the qrdb CLI's --server flag expects to select this
// transport (see cmd/qrdb/root.go's schemePrefixes).
type multiProtocol struct {
	name   string
	scheme string
}

// multiProtocols is the canonical set of CLI-selectable transports every
// multi-protocol table-driven test in this package loops over. It mirrors
// drainProtocol's set in drain_test.go, the reference model for this
// convention (test/integration/CLAUDE.md).
var multiProtocols = []multiProtocol{
	{name: "rest", scheme: "https"},
	{name: "grpc", scheme: "grpcs"},
	{name: "quic", scheme: "quics"},
	{name: "mcp", scheme: "mcp"},
	{name: "unix", scheme: "unix"},
}

// multiProtoNode holds the connection details for a single-node qrdbd
// serving every transport with an already-unsealed, passphrase-keyed system
// barrier. It is the shared fixture every kv/database multi-protocol CLI
// test in this batch boots once (via startMultiProtocolNode) and reuses
// across its per-protocol subtests.
type multiProtoNode struct {
	cli          string
	ca           *testCA
	dataDir      string
	backupKeyDir string
	clientCert   string
	clientKey    string
	unixSocket   string
	restPort     int
	grpcPort     int
	mcpPort      int
	quicPort     int
	passphrase   string

	// proc is the node's process when its fixture retains it: a fixture whose
	// node restarts (startTenantEntitlementNode), which also keeps bin and
	// startArgs for restart, or startMultiProtocolCluster, for a test that
	// stops the node.
	bin       string
	startArgs []string
	proc      *exec.Cmd
}

// restart stops the node and relaunches it with the same start arguments,
// unseals its system barrier with its passphrase and waits until every
// transport serves again. Only a fixture that retained its process can
// restart; any other fails the test.
func (n *multiProtoNode) restart(t *testing.T, prefix string) {
	t.Helper()
	if n.proc == nil || n.bin == "" {
		t.Fatal("restart: this node's fixture did not retain its process")
	}
	stopNode(t, n.proc, 15*time.Second)
	n.proc = startNode(t, n.bin, n.startArgs, prefix)

	waitForBarrierStatusReachable(t, n.ca, n.restPort, 90*time.Second)
	unsealOnlyBarrierWithSecret(t, n.ca, n.restPort, []byte(n.passphrase))
	waitForHealthy(t, n.dataDir, 60*time.Second)
	waitForAdmin(t, n.ca.Pool, n.restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, n.ca, []int{n.restPort}, 60*time.Second)
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}
}

// portFor returns the TCP/UDP port serving the given named protocol ("rest",
// "grpc", "mcp", "quic"). "unix" has no port -- see serverURLFor.
func (n *multiProtoNode) portFor(proto string) int {
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
func (n *multiProtoNode) serverURLFor(proto multiProtocol) string {
	if proto.name == "unix" {
		return "unix://" + n.unixSocket
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", proto.scheme, n.portFor(proto.name))
}

// cliArgsFor returns the root-level qrdb CLI flags that select the given
// protocol and present the admin mTLS identity (CN=admin, OU=system) every
// transport in this harness authorizes as Owner platform-wide.
func (n *multiProtoNode) cliArgsFor(proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", n.ca.CertPath,
		"--cert-file", n.clientCert,
		"--key-file", n.clientKey,
		"--timeout", "20s",
	}
}

// runCLIProto runs the qrdb client CLI against the node over the given
// protocol, prepending cliArgsFor(proto) to args. It never mutates or
// aliases the node's backing connection-arg slices across calls.
func runCLIProto(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) (string, error) {
	t.Helper()
	full := append(append([]string{}, node.cliArgsFor(proto)...), args...)
	return runCLI(t, node.cli, full...)
}

// writeMultiProtocolSealConfig writes a daemon YAML config enabling the seal
// barrier with the passphrase strategy. A node booted with this config comes
// up in the pre-host sealed window and must be initialized and unsealed
// (see initAndUnsealBarrier) before the Raft host, and therefore every other
// transport, starts serving. extraYAML, when non-empty, is appended as further
// top-level sections (any but seal).
func writeMultiProtocolSealConfig(t *testing.T, extraYAML string) string {
	t.Helper()
	yaml := "seal:\n  enabled: true\n  strategy: passphrase\n" + extraYAML
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeMultiProtocolSealConfig: %v", err)
	}
	return cfgPath
}

// startMultiProtocolNode boots a single-node qrdbd with every transport
// enabled (REST, gRPC, QUIC, MCP, Unix), authentication on, TLS configured,
// and a passphrase-strategy system barrier initialized and unsealed, so
// data-plane commands (kv, database) work immediately over any of the 5
// transports. It blocks until the node is healthy, has won leadership over
// the REST admin port, and every transport is confirmed serving via a live
// CLI round trip, then returns a handle with everything a per-protocol
// subtest needs.
//
// It runs raft.sm_type: mvcc (see startMultiProtocolNodeWithSM), which is
// what every MVCC-dependent command (kv get --at-revision, kv restore, ...)
// requires.
func startMultiProtocolNode(t *testing.T) *multiProtoNode {
	t.Helper()
	return startMultiProtocolNodeWithSM(t, "mvcc")
}

// startMultiProtocolNodeWithSM is startMultiProtocolNode parameterized on the
// daemon's state machine type. smType is passed as the "--sm" flag value; an
// empty smType omits the flag entirely so the daemon boots under its own
// default (raft.sm_type: pebble -- see cmd/qrdbd/start.go), which is what the
// default-state-machine MVCC-refusal coverage
// (kv_restore_default_sm_cli_test.go) needs: those tests must run against
// the actual out-of-the-box default, not merely an explicit "--sm pebble"
// that could silently drift from it.
//
// This node's TLS comes from ca.tlsFlags, a bring-your-own leaf certificate
// signed by the test's OWN CA -- the daemon never holds that CA's private
// key, so s.ceremonyCA is nil. qrdb init supports that directly
// (go-qrdb#548): with no CeremonyCA it does not self-issue a TLS
// certificate, pinning the operator's own configured leaf instead, and the
// bring-your-own-certificate admin-credential path (initAndUnsealBarrier's
// admin_cert_pem) needs no CA at all -- the test's own CA plays the
// operator's CA, present in ca's client-CA trust pool.
func startMultiProtocolNodeWithSM(t *testing.T, smType string) *multiProtoNode {
	t.Helper()
	return startMultiProtocolNodeWithArgs(t, smType)
}

// startMultiProtocolNodeWithArgs is startMultiProtocolNodeWithSM generalized
// to accept extra daemon flags (for example "--cluster-join-enabled"), so a
// test for a command needing one more daemon flag than the baseline harness
// provides does not need its own, mostly-duplicate node bootstrap.
func startMultiProtocolNodeWithArgs(t *testing.T, smType string, extraArgs ...string) *multiProtoNode {
	t.Helper()
	return startMultiProtocolNodeWithConfig(t, smType, "", extraArgs...)
}

// startMultiProtocolNodeWithConfig is startMultiProtocolNodeWithArgs with
// extraYAML appended to the daemon's config file, for a setting that has no
// daemon flag (see writeMultiProtocolSealConfig).
func startMultiProtocolNodeWithConfig(t *testing.T, smType, extraYAML string, extraArgs ...string) *multiProtoNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()
	cfgPath := writeMultiProtocolSealConfig(t, extraYAML)

	// Unix socket paths are limited to ~108 bytes by the kernel and
	// t.TempDir() can exceed that, so place the socket under a short,
	// unique /tmp path (matches drainNode's identical rationale).
	sockDir, err := os.MkdirTemp("", "qrdb-mp-")
	if err != nil {
		t.Fatalf("startMultiProtocolNodeWithSM: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	// The built-in "software" key-wrap provider confines every key file to
	// one configured directory (go-qrdb security review,
	// backup-encrypt-default #B4); every encrypted-backup test against
	// this harness needs it configured, kept separate from --data-dir like
	// production.
	backupKeyDir := t.TempDir()

	// The tenant DEK keyring resolves its default key reference through the
	// same provider, from its own key directory (tenant-dek/engineering.md,
	// Composition); every dek CLI test against this harness needs the
	// "system" tenant's key file there.
	dekKeyDir := t.TempDir()
	writeDEKKeyFile(t, dekKeyDir, "system")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	restPort := tcpPorts[0]
	grpcPort := tcpPorts[1]
	mcpPort := tcpPorts[2]
	quicPort := quicPorts[0]

	const passphrase = "multi-protocol-harness-passphrase"

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		// Enable every transport the multi-protocol tests target.
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
		"--backup-key-dir", backupKeyDir,
		"--dek-key-dir", dekKeyDir,
	}
	if smType != "" {
		args = append(args, "--sm", smType)
	}
	args = append(args, extraArgs...)
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, fmt.Sprintf("[multi-proto sm=%s] ", smType))

	// The barrier subsystem runs a pre-host sealed-window REST listener
	// (unauthenticated by design -- the secret itself is the
	// authorization) that must be initialized and unsealed before the Raft
	// host, and therefore every other transport, comes up.
	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	clientCert, clientKey := initAndUnsealBarrier(t, ca, restPort, passphrase)

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{restPort}, 60*time.Second)

	n := &multiProtoNode{
		cli:          cli,
		ca:           ca,
		dataDir:      dataDir,
		backupKeyDir: backupKeyDir,
		clientCert:   clientCert,
		clientKey:    clientKey,
		unixSocket:   unixSocket,
		restPort:     restPort,
		grpcPort:     grpcPort,
		mcpPort:      mcpPort,
		quicPort:     quicPort,
		passphrase:   passphrase,
	}

	// Confirm every transport is actually serving (not just that its
	// listener socket is bound) by round-tripping a real read-only CLI
	// command over each one before handing the node to callers.
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}

	return n
}

// adminCSRArgsForTenant generates a fresh local key/CSR for a throwaway
// admin account named adminUsername (via "ca csr", pure local keygen) and
// returns the "tenant create" flags that name it as the tenant's first
// admin: a tenant is created with its first admin, or not at all, so every
// "tenant create" call in this package needs these two flags.
func adminCSRArgsForTenant(t *testing.T, node *multiProtoNode, proto multiProtocol, adminUsername string) []string {
	t.Helper()
	csrDir := t.TempDir()
	out, err := runCLIProto(t, node, proto, "ca", "csr", "--username", adminUsername, "--out", csrDir)
	if err != nil {
		t.Fatalf("[%s] ca csr for tenant admin %s: %v\noutput: %s", proto.name, adminUsername, err, out)
	}
	return []string{"--admin-username", adminUsername, "--admin-csr", filepath.Join(csrDir, "csr.pem")}
}

// waitForCLIProtoReady polls the given protocol with a lightweight
// read-only CLI command ("kv exists", an ordinary KVServicer.Get -- see
// cmd/qrdb/kv.go's newKVExistsCmd) until it succeeds or the timeout elapses.
//
// This deliberately does NOT use "kv revision": CurrentRevision is an
// MVCC-only query (MVCCRevisionQuery) that a raft.sm_type: pebble shard
// refuses with *MVCCBackendRequiredError, so it would never succeed against
// a startMultiProtocolNodeWithSM(t, "pebble") node and this readiness probe
// would spin until timeout on every non-MVCC harness. "kv exists" round-trips
// the transport and the service/engine layer identically on every state
// machine.
func waitForCLIProtoReady(t *testing.T, node *multiProtoNode, proto multiProtocol, timeout time.Duration) {
	t.Helper()
	var lastOut string
	var lastErr error
	if pollUntil(timeout, 200*time.Millisecond, func() bool {
		lastOut, lastErr = runCLIProto(t, node, proto, "kv", "exists", "__multi_proto_ready_probe__")
		return lastErr == nil
	}) {
		return
	}
	t.Fatalf("[%s] timed out after %v waiting for transport to serve requests: %v\noutput: %s",
		proto.name, timeout, lastErr, lastOut)
}

// selfIssuedCANode is a single-node qrdbd booted with NO --tls-* flags, so it
// self-generates and retains its own cluster CA (Key A) in its data
// directory, with every transport enabled and a full `qrdb init` ceremony
// already completed -- barrier unsealed, CA minted, admin cert and default
// roles created. It embeds *multiProtoNode so every existing
// runCLIProto-based test helper works unmodified against it.
//
// This is the one harness in the package that can sign member-side CSR
// joins at all: --cluster-join-enabled only starts the join listener/route
// when the daemon holds the cluster CA's own private key
// (cmd/qrdbd/start.go calls AdminService.SetClusterJoinTokenStore only
// inside its `svcNode.ClusterCAProv != nil` branch), which a BYO-TLS node
// (bundle.ExplicitTLS, every other harness in this package) never does --
// see TestAdminJoinToken_CLI_BYOTLSRefusal, which proves the typed refusal
// that gives this node its reason to exist.
type selfIssuedCANode struct {
	*multiProtoNode

	// leafCertPath is the node's own TLS leaf certificate (node-cert.pem),
	// the SAME certificate presented by every transport listener on this
	// node, including the admin HTTPS listener adminURL names. Its SPKI pin
	// (spkipin.ComputeSPKIPin) is what a joining node's
	// --bootstrap-spki-pin / --bootstrap-addr (trust-anchor fetch) and
	// --bootstrap-join-url (CSR submission) both pin against.
	leafCertPath string

	// adminURL is this node's admin HTTPS base URL (the SPKI/Direct join
	// route, /v1/enroll/join, lives on it) -- what a real joining node's
	// --bootstrap-addr / --bootstrap-join-url dial, and what this package's
	// own clusterenroll.JoinCSRHTTP calls dial directly without spawning a
	// second process.
	adminURL string
}

// startSelfIssuedCAMultiProtocolNode boots a single-node qrdbd with every
// transport enabled (REST, gRPC, QUIC, MCP, Unix), NO --tls-* flags (so the
// daemon self-generates and retains its own cluster CA -- see
// selfIssuedCANode's doc comment), and --cluster-join-enabled. It drives the
// real CLI binary through the canonical bootstrap sequence -- `barrier init
// --barrier-strategy none` to clear the sealed window, then a full `qrdb
// init` ceremony -- exactly as officer_ceremony_cli_test.go's
// TestCLI_Init_PassphraseHappyPath does for a single-transport node, then
// confirms every one of the 5 transports is live before returning.
func startSelfIssuedCAMultiProtocolNode(t *testing.T) *selfIssuedCANode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	dataDir := t.TempDir()
	cfgPath := writeSealYAML(t, "seal:\n  enabled: true\n  strategy: none\n")

	// Unix socket paths are limited to ~108 bytes by the kernel and
	// t.TempDir() can exceed that -- see startMultiProtocolNodeWithSM's
	// identical rationale.
	sockDir, err := os.MkdirTemp("", "qrdb-sica-")
	if err != nil {
		t.Fatalf("startSelfIssuedCAMultiProtocolNode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp
	restPort, grpcPort, mcpPort := tcpPorts[0], tcpPorts[1], tcpPorts[2]
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
		"--auth-enabled",
		"--cluster-join-enabled",
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
		// Deliberately no --tls-ca-file/--tls-cert-file/--tls-key-file: see
		// selfIssuedCANode's doc comment.
	}
	startNode(t, bin, args, "[self-issued-ca] ")

	caPath := filepath.Join(dataDir, "ca.pem")
	leafCertPath := filepath.Join(dataDir, "node-cert.pem")
	credDir := t.TempDir()
	adminCertPath := filepath.Join(credDir, "admin-cert.pem")
	adminKeyPath := filepath.Join(credDir, "admin-key.pem")
	waitForFile(t, caPath, 30*time.Second)
	waitForFile(t, leafCertPath, 30*time.Second)

	// loadDaemonCA (barrier_cli_test.go) adopts the self-issued CA the daemon
	// wrote into dataDir. ca-key.pem is still on disk here: the daemon removes
	// it only once confirmClusterCAPersisted (cmd/qrdbd/clusterca.go) has the
	// key durably in the system store, which needs the barrier the `qrdb init`
	// below brings up.
	ca := loadDaemonCA(t, dataDir)

	adminURL := fmt.Sprintf("https://127.0.0.1:%d", restPort)
	noAuthArgs := []string{"--server", adminURL, "--transport", "rest", "--ca-file", caPath, "--timeout", "20s"}
	adminArgs := append(append([]string{}, noAuthArgs...), "--cert-file", adminCertPath, "--key-file", adminKeyPath)

	// The sealed-window listener is REST-only and unauthenticated by
	// design -- see officer_ceremony_cli_test.go's restArgsNoAuth doc
	// comment -- so the bootstrap ceremony below always drives it over
	// REST, regardless of which transports the fully-booted host will
	// later serve. `qrdb init` is the only system bootstrap (go-qrdb#548):
	// one call brings the barrier up and issues the admin credential.
	pollUntilSealedListenerUp(t, cli, noAuthArgs, 30*time.Second)
	bootAdminIdentityVia(t, cli, noAuthArgs, "SOPIN-selfissued-1", "USERPIN-selfissued-1",
		adminCertPath, adminKeyPath, "", "--barrier-strategy", "none")

	waitForHealthy(t, dataDir, 60*time.Second)
	pollUntilCLISucceeds(t, cli, adminArgs, 60*time.Second)

	n := &multiProtoNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		clientCert: adminCertPath,
		clientKey:  adminKeyPath,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
	}

	// Confirm every transport is actually serving before handing the node
	// to callers, exactly as startMultiProtocolNodeWithSM does.
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}

	return &selfIssuedCANode{multiProtoNode: n, leafCertPath: leafCertPath, adminURL: adminURL}
}
