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

// Package integration_test contains real end-to-end integration tests for the
// qrdb-sqlite gateway. Tests start an in-process qrdbd subprocess, connect to
// it over every supported transport protocol, and verify the full path:
//
//	SQLite SQL → virtual table → SDK adapter → protocol transport → qrdbd → Raft
//
// Each test iterates over all protocols, creating an isolated testEnv per
// protocol subtest, so data written through one transport can be cross-checked
// through the SDK and vice versa.
package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite/vtab"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// ---------------------------------------------------------------------------
// Test lifecycle — one server for the entire test binary run
// ---------------------------------------------------------------------------

// shared holds the single qrdbd subprocess shared across all test functions.
// It is populated by TestMain before any test runs and cleaned up after all
// tests complete.
var shared *testServer

// TestMain builds qrdbd, starts a single-node cluster, waits for it to
// be healthy, runs the full test suite, then shuts the server down.
func TestMain(m *testing.M) {
	// Pre-warm the qrdb-sqlite CGO build before any test runs so that no
	// per-test context timeout (the binary tests bound each invocation at
	// 30s) is consumed by the build, which can take tens of seconds under
	// -race on a loaded machine.
	if err := ensureQRDBSQLiteBuilt(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: build qrdb-sqlite: %v\n", err)
		os.Exit(1)
	}

	var err error
	shared, err = startSharedServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: startSharedServer: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	shared.stop()
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Protocol enumeration
// ---------------------------------------------------------------------------

// protocol identifies a transport type for table-driven multi-protocol tests.
type protocol string

const (
	protoREST protocol = "rest"
	protoGRPC protocol = "grpc"
	protoUnix protocol = "unix"
	protoQUIC protocol = "quic"
	protoMCP  protocol = "mcp"
)

// allProtocols returns every transport protocol that the shared server is
// running. QUIC and MCP are excluded from the default set:
//   - QUIC requires UDP buffer tuning that may not be available in CI.
//   - MCP requires SSE streaming endpoint compatibility that is tested
//     separately in the main integration test suite.
func allProtocols() []protocol {
	protos := []protocol{protoREST, protoGRPC, protoUnix}
	if shared != nil && shared.quicPort > 0 {
		protos = append(protos, protoQUIC)
	}
	return protos
}

// ---------------------------------------------------------------------------
// testEnv — per-protocol test environment
// ---------------------------------------------------------------------------

// testEnv holds a fully configured in-memory SQLite database with all virtual
// tables registered against real SDK adapters that talk to a live qrdbd server
// over a specific transport protocol. Each test creates its own isolated
// testEnv via newTestEnv.
type testEnv struct {
	db     *sql.DB
	client *sdk.Client
	pragma *vtab.PragmaStore
	proto  protocol
}

// driverSeq ensures each test environment registers a uniquely named SQLite
// driver so that parallel subtests never collide on the global driver registry.
var driverSeq struct {
	sync.Mutex
	n int
}

// uniqueDriverName returns a unique SQLite driver name for each call.
func uniqueDriverName() string {
	driverSeq.Lock()
	defer driverSeq.Unlock()
	driverSeq.n++
	return fmt.Sprintf("sqlite3_integration_%d", driverSeq.n)
}

// newTestEnv builds a complete test environment for the given transport
// protocol. It creates an SDK client connected to the shared qrdbd server,
// wires all store adapters, registers every virtual table module, creates
// the virtual tables in a fresh in-memory SQLite database, and returns the
// ready environment. All resources are cleaned up via t.Cleanup.
func newTestEnv(t *testing.T, proto protocol) *testEnv {
	t.Helper()

	if shared == nil {
		t.Fatal("newTestEnv: shared server is nil — TestMain did not start the server")
	}

	client := newSDKClient(t, proto)

	// Warm up the Raft leader so read operations complete without timing out.
	warmupViaSDK(t, client)

	// Thin SDK adapters satisfying the vtab store interfaces.
	kvAdapter := &testKVAdapter{kv: client}
	dpAdapter := &testDatapointAdapter{ts: client}
	metAdapter := &testMetricsAdapter{ts: client}
	nsAdapter := &testNamespaceAdapter{vec: client}
	vecAdapter := &testVectorAdapter{vec: client}
	collAdapter := &testCollectionAdapter{coll: client}

	pragma := vtab.NewPragmaStore(string(proto), shared.addrFor(proto), "default")

	driverName := uniqueDriverName()
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			if err := vtab.RegisterKVOnConn(conn, kvAdapter, pragma, 256); err != nil {
				return err
			}
			if err := vtab.RegisterDatapointModule(conn, dpAdapter, 256); err != nil {
				return err
			}
			if err := vtab.RegisterMetricsModule(conn, metAdapter, 256); err != nil {
				return err
			}
			if err := vtab.RegisterNamespaceOnConn(conn, nsAdapter, 256); err != nil {
				return err
			}
			if err := vtab.RegisterVectorOnConn(conn, vecAdapter, 256); err != nil {
				return err
			}
			// Register the docs collection virtual table. The schema must be
			// declared here so SQLite knows the column layout.
			collMeta := vtab.TableMeta{
				Name:     "docs",
				Type:     vtab.TableCollection,
				Database: "default",
				Columns: []vtab.ColumnMeta{
					{Name: "_id", SQLiteType: "INTEGER", PrimaryKey: true},
					{Name: "body", SQLiteType: "TEXT"},
					{Name: "score", SQLiteType: "REAL"},
					{Name: "title", SQLiteType: "TEXT"},
				},
			}
			return vtab.RegisterCollectionFromMeta(conn, collMeta, collAdapter, 256)
		},
	})

	db, err := sql.Open(driverName, ":memory:")
	require.NoError(t, err, "open in-memory SQLite database")

	// Limit the connection pool to one connection so all SQLite calls share the
	// same *sqlite3.SQLiteConn and the virtual table modules registered in the
	// ConnectHook are always available.
	db.SetMaxOpenConns(1)

	require.NoError(t, db.Ping(), "ping in-memory SQLite database")

	// Create virtual tables in the fresh in-memory database.
	for _, ddl := range []string{
		"CREATE VIRTUAL TABLE kv USING qrdb_kv",
		"CREATE VIRTUAL TABLE datapoints USING qrdb_ts_datapoints",
		"CREATE VIRTUAL TABLE metrics USING qrdb_ts_metrics",
		"CREATE VIRTUAL TABLE namespaces USING qrdb_vector_namespaces",
		"CREATE VIRTUAL TABLE vectors USING qrdb_vectors",
		"CREATE VIRTUAL TABLE docs USING qrdb_collection_default_docs",
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err, "DDL: %s", ddl)
	}

	t.Cleanup(func() {
		_ = db.Close()
		_ = client.Close()
	})

	return &testEnv{
		db:     db,
		client: client,
		pragma: pragma,
		proto:  proto,
	}
}

// newSDKClient constructs an SDK client connected via the given transport
// protocol to the shared server. Certificates are loaded from the shared
// testServer. The client is closed via t.Cleanup on the testEnv.
func newSDKClient(t *testing.T, proto protocol) *sdk.Client {
	t.Helper()

	var (
		tr  sdktransport.Transport
		err error
	)

	// mtlsOpts returns the common TLS options for protocols that use mTLS.
	mtlsOpts := func(addr string) []sdktransport.Option {
		return []sdktransport.Option{
			sdktransport.WithAddress(addr),
			sdktransport.WithTimeout(30 * time.Second),
			sdktransport.WithMTLS(shared.clientCertPath, shared.clientKeyPath, shared.ca.CertPath),
		}
	}

	switch proto {
	case protoREST:
		tr, err = resttransport.New(mtlsOpts(shared.restAddr())...)

	case protoGRPC:
		tr, err = grpctransport.New(mtlsOpts(shared.grpcAddr())...)

	case protoUnix:
		// Unix (grpc-over-UDS) is "verified TLS 1.3 only", identical to the
		// TCP gRPC transport: the socket-file mode is defense-in-depth, the
		// TLS client certificate is the identity the server authenticates.
		tr, err = unixtransport.New(mtlsOpts(shared.unixSocket)...)

	case protoQUIC:
		tr, err = quictransport.New(mtlsOpts(shared.quicAddr())...)

	case protoMCP:
		tr, err = mcptransport.New(mtlsOpts(shared.mcpAddr())...)

	default:
		t.Fatalf("newSDKClient: unknown protocol %q", proto)
	}

	if err != nil {
		t.Fatalf("newSDKClient %s: create transport: %v", proto, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("newSDKClient %s: connect: %v", proto, err)
	}

	client, err := sdk.NewClient(tr)
	require.NoError(t, err, "create SDK client for %s", proto)

	return client
}

// warmupViaSDK performs a single KV write and delete through the SDK client to
// ensure the Raft leader has committed its no-op entry and the ReadIndex
// pipeline is operational before tests issue reads.
func warmupViaSDK(t *testing.T, client *sdk.Client) {
	t.Helper()
	ctx := context.Background()
	const key = "__warmup__"
	require.NoError(t, client.KV().Put(ctx, key, []byte("1")), "warmup PUT")
	require.NoError(t, client.KV().Delete(ctx, key), "warmup DELETE")
}
