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
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
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
)

// outcome is what a cell is required to do with the guarded plane.
type outcome int

const (
	// fullPassage: every guarded verb works, with identical semantics to
	// every other fullPassage cell.
	fullPassage outcome = iota

	// typedRefusal: this cell's state machine does not carry the capability,
	// so every guarded verb must refuse with a typed error AND leave the node
	// serving. Refusing safely is a real conformance promise, and it is what
	// pins go-qrdb#129 permanently: before that fix these cells did not refuse,
	// they killed the shard.
	typedRefusal
)

func (o outcome) String() string {
	if o == fullPassage {
		return "full-passage"
	}
	return "typed-refusal"
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

func (c cell) name() string {
	return fmt.Sprintf("%s/%s/%s", c.backend, c.mode, c.transport)
}

// matrix is the 12 cells go-qrdb#121 requires: {file, memory, pebble} x
// {standalone, raft} x {embedded, remote-gRPC}.
//
// The expected outcome per cell follows the scope ruled for v0.12: the
// guarded/transaction plane under raft is promised on the DEFAULT sm_type
// only. The non-default state machines never had a working MVCC store —
// WithKVMVCCStore has no production caller — so scoping removes no working
// promise, and giving them one is tracked post-v0.12 as go-qrdb#136. When that
// lands, those four cells flip from typedRefusal to fullPassage, and the flip
// is that issue's acceptance test.
func matrix() []cell {
	var cells []cell
	for _, tk := range []transportKind{embeddedTransport, grpcTransport} {
		cells = append(cells,
			// Standalone: databases[].backend selects the engine directly.
			cell{backend: database.BackendMemory, mode: standalone, transport: tk, want: fullPassage},
			cell{backend: database.BackendFile, mode: standalone, transport: tk, want: fullPassage},
			cell{backend: database.BackendPebble, mode: standalone, transport: tk, want: fullPassage},

			// Raft, default sm_type: the MVCC state machine over PebbleDB.
			cell{backend: database.BackendPebble, mode: raft, transport: tk, want: fullPassage,
				smType: ""},

			// Raft, non-default sm_types: no MVCC store, so the guarded plane
			// must refuse cleanly and keep serving.
			cell{backend: database.BackendMemory, mode: raft, transport: tk, want: typedRefusal,
				smType: "memory"},
			cell{backend: database.BackendFile, mode: raft, transport: tk, want: typedRefusal,
				smType: "disk", smStorage: "file"},
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

// raftPortSeed hands each raft cell a distinct advertised address. A
// single-member group runs with the transport disabled so nothing binds, but
// distinct addresses keep the node identities apart.
var raftPortSeed = 63940

func (c cell) config(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Server.Host = "127.0.0.1"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = c.transport == grpcTransport
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	if c.transport == grpcTransport {
		cfg.Server.GRPCPort = freeTCPPort(t)
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
		// A non-default sm_type is hard-refused while the seal barrier is
		// enabled (sdk/go/node.clusteredCreateFunc), which is itself part of
		// why scoping these cells to typed refusal costs so little in practice.
		cfg.Seal.Enabled = c.smType == ""
		if cfg.Seal.Enabled {
			cfg.Seal.Strategy = "none"
		}
	}
	return cfg
}

// applySMStorage installs the raft storage-backend selection, which
// NewClusteredNode does not read from config — only the daemon calls these
// setters. They are process-global and mutex-guarded, so cells that need them
// must not run in parallel, and each restores the previous value.
func applySMStorage(t *testing.T, cfg *qrdbconfig.Config) {
	t.Helper()
	if cfg.Raft.SMStorage == "" {
		return
	}
	statemachine.SetDataDirRoot(cfg.Raft.DataDir)
	statemachine.SetSMStorage(cfg.Raft.SMStorage)
	t.Cleanup(func() {
		statemachine.SetDataDirRoot("")
		statemachine.SetSMStorage("")
	})
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
	applySMStorage(t, cfg)

	var serverOpts []server.ServerOption
	var ca *qrdbdharness.CA
	var clientKP *qrdbdharness.KeyPair
	if c.transport == grpcTransport {
		ca, clientKP, serverOpts = grpcServerTLS(t)
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

	if c.transport == grpcTransport {
		seedGuardedIdentity(t, n)
		return dialGRPC(t, n, ca, clientKP)
	}

	guarded, ok := embeddedKV.(transport.GuardedKVClient)
	if !ok {
		t.Fatalf("%s: the embedded KV client does not implement transport.GuardedKVClient", c.name())
	}
	return clients{kv: embeddedKV, guarded: guarded, txn: n.Client().Transaction()}
}

// grpcServerTLS builds the mTLS material the gRPC listener needs. The gRPC
// transport is TLS-only by design — the client certificate IS the identity the
// auth middleware authenticates — so there is no insecure shortcut here.
func grpcServerTLS(t *testing.T) (*qrdbdharness.CA, *qrdbdharness.KeyPair, []server.ServerOption) {
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
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "matrix",
		TenantID: tenant.SystemTenantID,
		RoleIDs:  []string{r.ID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

// dialGRPC connects a real SDK gRPC transport to the node's in-process
// listener.
func dialGRPC(t *testing.T, n *node.Node, ca *qrdbdharness.CA, kp *qrdbdharness.KeyPair) clients {
	t.Helper()
	addr := n.Server().GRPCAddr()
	if addr == "" {
		t.Fatal("the node reports no gRPC address; the listener did not bind")
	}
	clientCert, err := kp.TLSCertificate()
	if err != nil {
		t.Fatalf("client TLS cert: %v", err)
	}
	tr, err := grpctransport.New(
		transport.WithAddress(addr),
		transport.WithTLSConfig(&tls.Config{
			Certificates: []tls.Certificate{clientCert},
			RootCAs:      ca.Pool,
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS13,
		}),
	)
	if err != nil {
		t.Fatalf("new gRPC transport: %v", err)
	}
	c, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("new SDK client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Connect(boundedCtx(t)); err != nil {
		t.Fatalf("gRPC connect: %v", err)
	}

	kv := c.KV()
	guarded, ok := kv.(transport.GuardedKVClient)
	if !ok {
		t.Fatal("the gRPC KV client does not implement transport.GuardedKVClient")
	}
	return clients{kv: kv, guarded: guarded, txn: c.Transaction()}
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

// assertCapabilityRefusal requires that err is specifically a refusal for lack
// of the capability, and not merely some error.
//
// `err != nil` is not good enough here, and the reason is sharp: every call in
// this suite carries a bounded context, so a cell whose shard WEDGED — the
// exact go-qrdb#129 regression these cells exist to pin — returns
// DeadlineExceeded. A test that accepted any non-nil error would score that as
// "refused as required" and go green on the defect it was written to catch.
// The same goes for an authorization failure, a lost connection, or a
// not-leader error: all are non-nil, none is a refusal.
//
// So the wrong classes are rejected by type or status code, and the refusal
// itself must carry the state machine's own account of what is missing. That
// last check is a string match, which is worth being honest about: this
// refusal has no distinct Go type and no distinct gRPC code today. It travels
// as an application error encoded into sm.Result.Data, surfaces embedded as a
// generic RequestError, and maps to codes.Internal over gRPC — a capability
// refusal presented as an internal server fault, which a client cannot
// distinguish from a real one. Matching the message is therefore matching the
// only contract that currently exists; go-qrdb#141 tracks giving it a typed
// one, and this helper is where that would be tightened.
// classifyRefusal reports why err is NOT a capability refusal, or nil when it
// is one. Split out from the assertion so the classification itself is
// directly testable — an assertion helper that only ever calls t.Errorf cannot
// be shown to reject anything, which is the same shape of gap it exists to
// close.
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

	// The message still has to say what is absent. A typed refusal carrying no
	// account of WHICH capability is missing is not much better than an
	// untyped one for whoever has to act on it.
	if !strings.Contains(unsupported.Message, "not configured") &&
		!strings.Contains(unsupported.Message, "unknown opcode") {
		return fmt.Errorf("capability refusal %q names neither an absent opcode nor an "+
			"unwired subsystem, so it does not say what is missing", unsupported.Message)
	}
	return nil
}

func assertCapabilityRefusal(t *testing.T, verb string, err error) {
	t.Helper()
	if reason := classifyRefusal(err); reason != nil {
		t.Errorf("%s: %v", verb, reason)
		return
	}
	t.Logf("%s refused as required: %v", verb, err)
}
