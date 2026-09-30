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

// Package conformance holds the guarded-verb conformance matrix: one suite
// that walks every combination of storage backend, deployment mode and
// transport, and asserts the guarded write plane behaves identically across
// all of them (go-qrdb#117, go-qrdb#121).
//
// It runs entirely IN-PROCESS. Every other multi-cell suite in this repository
// forks the qrdbd binary, which costs 60-90s per node and confines the tests to
// the `integration` build tag that the ordinary CI test job never sets. Booting
// nodes through sdk/go/node instead puts the whole matrix in the normal test
// pass, where a regression turns something red the same day: standalone cells
// cost roughly 20ms each and raft cells a few seconds, dominated by leader
// election.
package conformance

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// callTimeout bounds every data-plane call the matrix makes.
//
// This is not politeness. Before go-qrdb#129 a guarded verb against a state
// machine that did not implement it tripped the shard's circuit breaker, after
// which a linearizable read never returned at all — the first reproduction of
// that defect hung for a full 15-minute `go test` timeout inside one Get. A
// wedged cell must fail this suite in seconds and name itself, not stall the
// run and take the whole package's output with it.
const callTimeout = 30 * time.Second

func boundedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	t.Cleanup(cancel)
	return ctx
}

// ---------------------------------------------------------------------------
// Cell definition
// ---------------------------------------------------------------------------

// mode is the deployment shape: a single non-raft node, or a raft group.
// "standalone" is not an engine — it is single-node-non-raft (go-qrdb#117).
type mode string

const (
	standalone mode = "standalone"
	raft       mode = "raft"
)

// transportKind selects how the client reaches the node.
type transportKind string

const (
	embeddedTransport transportKind = "embedded"
	grpcTransport     transportKind = "grpc"
	quicTransport     transportKind = "quic"
	unixTransport     transportKind = "unix"
	restTransport     transportKind = "rest"

	mcpTransport transportKind = "mcp"
)

// transportKinds is the transport axis of the matrix.
//
// go-qrdb#121 walked embedded and remote-gRPC because those were the only two
// that carried transport.GuardedKVClient. go-qrdb#124/#125/#126 added the verbs
// to QUIC, unix and REST, and go-qrdb#127 to MCP; the cells go here rather than
// into per-transport suites, because the promise being tested is that the
// guarded plane behaves IDENTICALLY however it is reached, and parallel suites
// would let several definitions of "works" drift apart — the exact failure the
// matrix exists to prevent.
var transportKinds = []transportKind{
	embeddedTransport,
	grpcTransport,
	quicTransport,
	unixTransport,
	restTransport,
	mcpTransport,
}

// THE EXCLUSION THAT USED TO LIVE HERE IS GONE (go-qrdb#438).
//
// A predicate carriesTypedCapabilityRefusal gated the (then) typedRefusal
// cells and returned false for exactly one transport. MCP could not carry the refusal
// the MULTI-KEY transaction plane owes on a state machine with no MVCC store:
// a qrdb tool reported a service-layer failure as prose, go-qrdb had no
// JSON-RPC error-code mapping (conventions/errors.md §5), and classifyRefusal
// deliberately refuses to accept prose — it had been tightened from a
// substring match to a type assertion precisely so a wedged cell could not
// score as "refused as required".
//
// That exclusion is the sharpest thing go-qrdb#438 was asked to fix, because
// the missing mapping did not merely block a feature: it created pressure to
// LOOSEN classifyRefusal back to a substring match, and the lane that met it
// declined and recorded the gap instead. The gap is now closed from the other
// end. pkg/api/mcp/wire declares a code per gRPC status code, the service
// layer's CapabilityUnsupportedError classifies to codes.Unimplemented, MCP
// renders that as wire.CodeUnimplemented, and the SDK reconstitutes
// transport.CapabilityUnsupportedError from the CODE.
//
// So the predicate is DELETED rather than made to return true everywhere: a
// gate with no false case is an invitation to add one. MCP's cells are in the
// matrix on the same terms as every other transport's, and classifyRefusal is
// unchanged — not one clause of it was relaxed to admit them, which is the
// only way admitting them is worth anything. Since go-qrdb#136 the capability
// refusal it classifies is the leased write the non-default raft sm_types
// cannot honour (assertLeasedWriteRefusedSafely).

// remote reports whether this transport crosses a real listener and therefore
// needs TLS material and a seeded RBAC identity. Only the embedded transport
// does not.
func (tk transportKind) remote() bool {
	return tk != embeddedTransport
}

// outcome is what a cell is required to do with the guarded plane.
type outcome int

const (
	// fullPassage: every guarded verb works, with identical semantics to
	// every other fullPassage cell.
	fullPassage outcome = iota

	// Two more outcomes existed here and are DELETED rather than left unused
	// — an outcome no cell can select is an invitation to select it, and the
	// selection would look like a fix. guardedWriteRefusal (go-qrdb#175 to
	// #216) was for the one backend that could not carry a guarded result;
	// typedRefusal (go-qrdb#121 to #136) was for the raft sm_types with no
	// multi-key plane, which refused transactions with a typed error and had
	// to keep serving afterwards. Since go-qrdb#136 every sm_type serves
	// transactions natively, so every cell passes in full. The keep-serving
	// half of typedRefusal still runs, on the refusal those cells DO make — a
	// revision compare (assertRevisionCompareBehaviour).
)

func (o outcome) String() string {
	return "full-passage"
}

// cell is one square of the matrix.
type cell struct {
	backend   database.BackendType
	mode      mode
	transport transportKind
	want      outcome

	// smType/smStorage carry the raft row's backend axis. It is a DIFFERENT
	// config knob from the standalone row's: databases[].backend steers only
	// NewStandaloneNode, while a raft group's storage comes from raft.sm_type
	// and raft.sm_storage. The two rows are not a symmetric 2x3 and pretending
	// otherwise is how the raft cells came to look like a conformance gap
	// rather than a capability that was never built (go-qrdb#135/#136).
	smType    string
	smStorage string
}

// name identifies the cell.
//
// A RAFT cell is named by its sm_type, not by its databases[].backend, because
// sm_type is the axis that actually varies down the raft row — the comment on
// matrix() has said so since go-qrdb#135, while the name said otherwise. Naming
// them by backend also made two raft cells collide as soon as a third sm_type
// needed covering, which is what go-qrdb#175 hit when it added the "fast" cell.
// Standalone cells keep the backend, which IS their axis.
func (c cell) name() string {
	if c.mode == raft {
		smType := c.smType
		if smType == "" {
			smType = "default"
		}
		return fmt.Sprintf("%s/%s/%s", smType, c.mode, c.transport)
	}
	return fmt.Sprintf("%s/%s/%s", c.backend, c.mode, c.transport)
}

// matrix is the go-qrdb#121 grid: {file, memory, pebble} x {standalone, raft}
// x {embedded, remote-gRPC, QUIC, unix, REST}.
//
// It was 12 cells when only embedded and gRPC carried transport.GuardedKVClient,
// 35 once QUIC, unix and REST did (go-qrdb#124/#125/#126), 39 when MCP ran the
// four full-passage cells and not the three typed-refusal ones (go-qrdb#127),
// and is 42 now that MCP's error surface can carry a typed refusal and it runs
// all seven (go-qrdb#438). The engine and mode axes are unchanged; only the
// transport axis grew, so a new transport's cells inherit the same expected
// outcome its siblings have and a transport that quietly weakened a verb shows
// up as a difference from them.
//
// Every cell expects full passage. The raft row's non-default sm_types were
// scoped to a typed refusal of the multi-key plane for v0.12; go-qrdb#136 gave
// every state machine a native transaction plane over its own keyspace, and
// flipping those cells to full passage is that issue's acceptance test here.
// pkg/server/lifecycle's TestGuardedPlaneConformsOnEverySMType walks every
// sm_type over every sm_storage; this matrix walks the transports.
func matrix() []cell {
	var cells []cell
	for _, tk := range transportKinds {
		cells = append(cells,
			// Standalone: databases[].backend selects the engine directly.
			cell{backend: database.BackendMemory, mode: standalone, transport: tk, want: fullPassage},
			cell{backend: database.BackendFile, mode: standalone, transport: tk, want: fullPassage},
			cell{backend: database.BackendPebble, mode: standalone, transport: tk, want: fullPassage},

			// Raft, default sm_type: the MVCC state machine over PebbleDB.
			cell{backend: database.BackendPebble, mode: raft, transport: tk, want: fullPassage,
				smType: ""},
		)

		cells = append(cells,
			// Raft, non-default sm_types: the native transaction plane over
			// the state machine's own keyspace (go-qrdb#136), on a volatile
			// map, on durable file storage, and on the sharded write-through
			// cache.
			cell{backend: database.BackendMemory, mode: raft, transport: tk, want: fullPassage,
				smType: "memory"},
			cell{backend: database.BackendFile, mode: raft, transport: tk, want: fullPassage,
				smType: "disk", smStorage: "file"},
			cell{backend: database.BackendPebble, mode: raft, transport: tk, want: fullPassage,
				smType: "fast", smStorage: "memory"},
		)
	}
	return cells
}

// ---------------------------------------------------------------------------
// Bring-up
// ---------------------------------------------------------------------------

// freeTCPPort reserves and releases a loopback port. A zero GRPCPort would be
// rewritten to the fixed default 63002 by buildNodeServerConfig, which
// collides between parallel test binaries.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// freeUDPPort is freeTCPPort's counterpart for the QUIC cells. QUIC rides UDP,
// and a port free on TCP says nothing about the UDP port of the same number —
// binding the TCP one and hoping would be a coin flip run under load, which is
// how a matrix acquires a flake that looks like a transport bug.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if err := conn.Close(); err != nil {
		t.Fatalf("release udp port: %v", err)
	}
	return port
}

// unixSocketPath returns a short-lived socket path for a unix cell.
//
// It deliberately does NOT use t.TempDir(). A sockaddr_un path is capped at
// 108 bytes on Linux, and t.TempDir() embeds the full subtest name — here
// something like "TestGuardedVerbConformanceMatrix/pebble/standalone/unix" —
// which pushes the path over the cap and fails the bind with a message that
// says nothing about length.
func unixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "qrdbsock")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// raftPortSeed hands each raft cell a distinct advertised address. A
// single-member group runs with the transport disabled so nothing binds, but
// distinct addresses keep the node identities apart.
var raftPortSeed = 63940

func (c cell) config(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	// Exactly one listener per cell. A cell tests ONE transport, and starting
	// the others would spend ports and goroutines on paths this cell never
	// calls — 30 cells' worth of them.
	cfg.Protocols.REST = c.transport == restTransport
	cfg.Protocols.GRPC = c.transport == grpcTransport
	cfg.Protocols.QUIC = c.transport == quicTransport
	cfg.Protocols.Unix = c.transport == unixTransport
	cfg.Protocols.MCP = c.transport == mcpTransport
	switch c.transport {
	case grpcTransport:
		cfg.Server.GRPCPort = freeTCPPort(t)
	case quicTransport:
		cfg.Server.QUICPort = freeUDPPort(t)
	case restTransport:
		cfg.Server.AdminPort = freeTCPPort(t)
	case unixTransport:
		cfg.Unix.SocketPath = unixSocketPath(t)
	case mcpTransport:
		cfg.MCP.Port = freeTCPPort(t)
	}

	switch c.mode {
	case standalone:
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(c.backend)}}
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
	case raft:
		raftPortSeed++
		cfg.Raft.RaftAddress = fmt.Sprintf("127.0.0.1:%d", raftPortSeed)
		cfg.Raft.ReplicaID = 1
		cfg.Raft.SMType = c.smType
		cfg.Raft.SMStorage = c.smStorage
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
		cfg.Storage.VolumePools = []qrdbconfig.VolumePoolConfig{{Name: "default", Paths: []string{t.TempDir()}}}
		// The non-default cells run with the barrier off: the disk cell's
		// file storage cannot be encrypted and is refused under it
		// (sdk/go/node.clusteredCreateFunc), and one seal setting across the
		// row's non-default cells keeps them comparable. What the barrier
		// allows and refuses per sm_type is pinned in sdk/go/node's
		// TestClusteredCreateFunc_UnderTheSealBarrier.
		cfg.Seal.Enabled = c.smType == ""
		if cfg.Seal.Enabled {
			cfg.Seal.Strategy = "none"
		}
	}
	return cfg
}

// keepsRevisions reports whether the cell's engine keeps MVCC revisions and
// so answers create_revision / mod_revision compares: the MVCC engine on a
// standalone memory or pebble backend, and the default raft state machine.
// The file engine and every other raft sm_type compare on value and version
// only, and refuse revision compares.
func (c cell) keepsRevisions() bool {
	if c.mode == raft {
		return c.smType == ""
	}
	return c.backend != database.BackendFile
}

// clients is the guarded surface under test, however it was reached.
type clients struct {
	kv      transport.KVClient
	guarded transport.GuardedKVClient
	txn     transport.TransactionClient
}

// boot brings the cell up and returns a client for it.
func (c cell) boot(t *testing.T) clients {
	t.Helper()
	cfg := c.config(t)

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if c.transport.remote() {
		ca, clientKP, serverOpts = remoteServerTLS(t)
	}

	var n *node.Node
	var err error
	switch c.mode {
	case standalone:
		n, err = node.NewStandaloneNode(context.Background(), cfg, serverOpts)
	case raft:
		members := map[uint64]string{1: cfg.Raft.RaftAddress}
		n, err = node.NewClusteredNode(context.Background(), cfg, members, serverOpts)
	}
	if err != nil {
		t.Fatalf("boot %s: %v", c.name(), err)
	}
	t.Cleanup(func() { _ = n.Close() })

	// Readiness first, on the node's own in-process client, and before
	// anything else touches the store. Seeding the RBAC identity writes
	// through the same shard, so on a raft cell it fails with "not leader"
	// unless the election has completed — a failure that would look like an
	// authorization problem and has nothing to do with one.
	embeddedKV := n.Client().KV()
	if c.mode == raft {
		awaitWritable(t, c, embeddedKV)
	}

	if c.transport.remote() {
		seedGuardedIdentity(t, n)
		return dialRemote(t, c, n, ca, clientKP)
	}

	guarded, ok := embeddedKV.(transport.GuardedKVClient)
	if !ok {
		t.Fatalf("%s: the embedded KV client does not implement transport.GuardedKVClient", c.name())
	}
	return clients{kv: embeddedKV, guarded: guarded, txn: n.Client().Transaction()}
}

// remoteServerTLS builds the mTLS material a remote listener needs. Every qrdb
// transport is TLS-only by design — the client certificate IS the identity the
// auth middleware authenticates — so there is no insecure shortcut here, and
// server.WithTLS feeds REST, gRPC, QUIC and unix from this one config (the
// QUIC listener replaces its ALPN list with its own "qrdb-quic/2").
func remoteServerTLS(t *testing.T) (*qrdbdharness.CA, *qrdbdharness.KeyPair, []server.ServerOption) {
	t.Helper()
	certDir := t.TempDir()

	ca, err := qrdbdharness.NewCA(certDir)
	if err != nil {
		t.Fatalf("new CA: %v", err)
	}
	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("server TLS cert: %v", err)
	}
	clientKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("matrix", tenant.SystemTenantID))
	if err != nil {
		t.Fatalf("issue client cert: %v", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
	return ca, clientKP, []server.ServerOption{server.WithTLS(tlsCfg)}
}

// seedGuardedIdentity grants the matrix's client certificate exactly the
// permissions the guarded plane needs, and nothing else.
//
// Running the remote cells through real mTLS and real RBAC is deliberate. The
// guarded verbs are reachable over gRPC with ordinary kv:put / kv:delete, and
// that reachability is precisely what made go-qrdb#129 severe rather than
// theoretical; a matrix that bypassed authorization would not be testing the
// path a consumer actually uses.
func seedGuardedIdentity(t *testing.T, n *node.Node) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)

	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "conformance-matrix",
		TenantID: tenant.SystemTenantID,
		Permissions: []permission.Permission{
			permission.KVPut, permission.KVGet, permission.KVDelete,
			permission.KVList, permission.KVExists,
			permission.TransactionExecute,
			permission.AdminHealth, permission.AdminStatus,
		},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "matrix",
		TenantID: tenant.SystemTenantID,
		RoleIDs:  []string{r.ID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// dialRemote connects a real SDK transport of the cell's kind to the node's
// in-process listener, then asserts the KV client actually carries the guarded
// capability.
//
// That assertion is the point of the remote cells and not a formality. The
// capability is detected by type assertion, so a transport that gained the
// verbs' NAMES without their atomicity would satisfy it and every consumer
// would then build a lock on top; the batteries that follow are what prove the
// atomicity is really there, and this line is what makes them run at all.
func dialRemote(t *testing.T, c cell, n *node.Node, ca *qrdbdharness.CA, kp *qrdbdharness.KeyPair) clients {
	t.Helper()

	clientCert, err := kp.TLSCertificate()
	if err != nil {
		t.Fatalf("client TLS cert: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      ca.Pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}

	tr := c.transport.dial(t, n, tlsCfg)

	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })
	if err := sdkClient.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("%s connect: %v", c.transport, err)
	}

	kv := sdkClient.KV()
	guarded, ok := kv.(transport.GuardedKVClient)
	if !ok {
		t.Fatalf("the %s KV client does not implement transport.GuardedKVClient", c.transport)
	}
	return clients{kv: kv, guarded: guarded, txn: sdkClient.Transaction()}
}

// dial constructs the SDK transport for this kind against the node's bound
// listener. Each branch fails the cell when the listener did not bind rather
// than dialing an empty address, so "the protocol never started" is never
// reported as "the guarded verb failed".
func (tk transportKind) dial(t *testing.T, n *node.Node, tlsCfg *tls.Config) transport.Transport {
	t.Helper()

	addr := func(got, protocol string) string {
		if got == "" {
			t.Fatalf("the node reports no %s address; the listener did not bind", protocol)
		}
		return got
	}

	var (
		tr  transport.Transport
		err error
	)
	switch tk {
	case grpcTransport:
		tr, err = grpctransport.New(
			transport.WithAddress(addr(n.Server().GRPCAddr(), "gRPC")),
			transport.WithTLSConfig(tlsCfg),
		)
	case quicTransport:
		tr, err = quictransport.New(
			transport.WithAddress(addr(n.Server().QUICAddr(), "QUIC")),
			transport.WithTLSConfig(tlsCfg),
		)
	case unixTransport:
		// ServerName must be the SAN the leaf actually carries: a unix socket
		// has no host name to derive one from, and the harness CA issues for
		// 127.0.0.1.
		tr, err = unixtransport.New(
			transport.WithAddress(addr(n.Server().UnixAddr(), "unix")),
			transport.WithTLSConfig(tlsCfg),
		)
	case restTransport:
		tr, err = resttransport.New(
			transport.WithAddress("https://"+addr(n.Server().RESTAddr(), "REST")),
			transport.WithTLSConfig(tlsCfg),
		)
	case mcpTransport:
		// MCP is Streamable HTTP at a PATH, not a bare host:port, which is
		// why its address is assembled rather than passed straight through.
		tr, err = mcptransport.New(
			transport.WithAddress("https://"+addr(n.Server().MCPAddr(), "MCP")+"/mcp"),
			transport.WithTLSConfig(tlsCfg),
		)
	default:
		t.Fatalf("transport kind %q has no remote dialer", tk)
	}
	if err != nil {
		t.Fatalf("new %s transport: %v", tk, err)
	}
	return tr
}

// awaitWritable blocks until a freshly started raft shard has elected a leader
// and committed its first entry of the term, then reports how long that took.
//
// Bounded and assert-or-fail: a shard that never becomes writable fails the
// cell rather than letting every later assertion fail for a reason that has
// nothing to do with the guarded verbs. The elapsed time is logged so a wait
// that has quietly become a sleep is visible rather than silent.
func awaitWritable(t *testing.T, c cell, kv transport.KVClient) {
	t.Helper()
	ctx := boundedCtx(t)
	start := time.Now()
	deadline := start.Add(callTimeout)
	for {
		err := kv.Put(ctx, "conformance/readiness", []byte("1"))
		if err == nil {
			t.Logf("%s: writable after %s", c.name(), time.Since(start).Round(time.Millisecond))
			return
		}
		var notLeader *transport.NotLeaderError
		if !errors.As(err, &notLeader) && !errors.Is(err, quicraft.ErrReadIndexNotReady) {
			// Not a transient election error. For a typed-refusal cell the
			// battery itself will characterise this; for a full-passage cell
			// it is a genuine failure the battery will report.
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: never became writable within %s: %v", c.name(), callTimeout, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Refusal classification
// ---------------------------------------------------------------------------

// grpcStatusOf walks the error chain for a gRPC status. The SDK transports wrap
// the status error in their own RequestError, so status.FromError on the
// outermost error alone reports nothing and every remote classification below
// would silently never fire.
func grpcStatusOf(err error) (*status.Status, bool) {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if se, ok := e.(interface{ GRPCStatus() *status.Status }); ok {
			return se.GRPCStatus(), true
		}
	}
	return nil, false
}

// classifyRefusal reports why err is NOT a capability refusal, or nil when it
// is one. Split out from the assertion so the classification itself is
// directly testable — an assertion helper that only ever calls t.Errorf cannot
// be shown to reject anything, which is the same shape of gap it exists to
// close.
//
// It holds EVERY transport to the typed refusal. REST used to be excused on
// /tx (the go-qrdb#126 named exclusion): the server answered 501 both for an
// absent capability and for a catch-all "refused on its merits" type, so a
// REST client could not promote a 501 to transport.CapabilityUnsupportedError
// without telling a caller that passed a bad TTL "this node cannot do leases".
// go-qrdb#557 gave the merits refusals types of their own (400, 409), leaving
// 501 meaning the capability refusal alone, and the REST client now types
// every 501 — so the exclusion is retired rather than kept as a dormant
// allowance.
func classifyRefusal(err error) error {
	if err == nil {
		return errors.New("no error at all; a silent success tells the caller it arbitrated " +
			"a race that was never arbitrated")
	}

	// A wedged or unresponsive cell. This is the case that makes the whole
	// check necessary rather than pedantic.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("the call exhausted its %s budget (%w). That is a WEDGED or "+
			"unresponsive cell, not a refusal — it is the go-qrdb#129 failure mode these "+
			"cells exist to detect", callTimeout, err)
	}

	// Consensus-liveness: the cell never settled, so nothing was decided.
	var notLeader *transport.NotLeaderError
	if errors.As(err, &notLeader) {
		return fmt.Errorf("not-leader (%w); the shard had no leader, so this says nothing "+
			"about whether the capability is present", err)
	}

	// Remote failures about reaching, or being allowed to call, the server —
	// not about what the server can do.
	if st, ok := grpcStatusOf(err); ok {
		switch st.Code() {
		case codes.Unauthenticated, codes.PermissionDenied:
			return fmt.Errorf("AUTHORIZATION failure (%s: %w); the matrix seeds an identity with "+
				"kv:put/kv:delete/transaction:execute precisely so a refusal here means the "+
				"capability is absent, not that the caller was not allowed to ask", st.Code(), err)
		case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
			return fmt.Errorf("transport availability failure (%s: %w); the server was not "+
				"reachable, which is not a refusal", st.Code(), err)
		}
	}

	// What remains must be a CAPABILITY refusal, asserted by type.
	//
	// This used to be a message-substring match, because until go-qrdb#141 a
	// refusal had no type on any transport and arrived as codes.Internal over
	// gRPC. It now carries transport.CapabilityUnsupportedError on both
	// transports, so the gate asserts identity rather than prose — which also
	// means a refusal that changes its wording no longer silently stops being
	// recognised.
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		return fmt.Errorf("%w is not a transport.CapabilityUnsupportedError; only a typed "+
			"capability refusal proves this cell lacks the verb rather than having hit an "+
			"unrelated fault", err)
	}
	message := unsupported.Message

	// The message still has to say what is absent. A typed refusal carrying no
	// account of WHICH capability is missing is not much better than an
	// untyped one for whoever has to act on it.
	if !strings.Contains(message, "not configured") &&
		!strings.Contains(message, "unknown opcode") &&
		!strings.Contains(message, "not supported") {
		return fmt.Errorf("capability refusal %q names neither an absent opcode nor an "+
			"unwired subsystem, so it does not say what is missing", message)
	}
	return nil
}

// assertCapabilityRefusal requires that err is specifically a refusal for lack
// of the capability, and not merely some error.
//
// `err != nil` is not good enough here, and the reason is sharp: every call in
// this suite carries a bounded context, so a cell whose shard WEDGED — the
// exact go-qrdb#129 regression these cells exist to pin — returns
// DeadlineExceeded. A test that accepted any non-nil error would score that as
// "refused as required" and go green on the defect it was written to catch.
// The same goes for an authorization failure, a lost connection, or a
// not-leader error: all are non-nil, none is a refusal. classifyRefusal
// rejects those, requires transport.CapabilityUnsupportedError on every
// transport, and requires the refusal to name what is missing.
func assertCapabilityRefusal(t *testing.T, verb string, err error) {
	t.Helper()
	if reason := classifyRefusal(err); reason != nil {
		t.Errorf("%s: %v", verb, reason)
		return
	}
	t.Logf("%s refused as required: %v", verb, err)
}
