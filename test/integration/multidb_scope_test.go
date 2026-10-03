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

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// scopeNode bundles the connection coordinates for a single multi-transport
// node used by the multi-database scope test.
type scopeNode struct {
	grpcPort int
	quicPort int
	mcpPort  int
	unixSock string
	certPath string
	keyPath  string
	ca       *testCA
}

// startScopeNode starts a single MVCC node with the gRPC (mTLS), native QUIC,
// MCP, and Unix socket transports enabled, and returns their connection
// coordinates. It blocks until the node is healthy, the leader is elected, and
// the warmup write has committed.
func startScopeNode(t *testing.T) scopeNode {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	mcpPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()
	unixSock := filepath.Join(t.TempDir(), "qrdb.sock")

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPorts[0]),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPorts[0]),
		"--enable-unix",
		"--unix-socket-path", unixSock,
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, "[scope] ")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)

	certPath, keyPath := ca.boundAdminCert(t, dataDir)
	return scopeNode{
		grpcPort: grpcPorts[0],
		quicPort: quicPorts[0],
		mcpPort:  mcpPorts[0],
		unixSock: unixSock,
		certPath: certPath,
		keyPath:  keyPath,
		ca:       ca,
	}
}

// assertIsolation puts a key in the system (unscoped) database and a different
// value under the same key in db2 (scoped), then asserts each client reads its
// own value and neither sees a key written exclusively in the other database.
func assertIsolation(t *testing.T, base *sdk.Client, label string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	scoped, err := base.DatabaseScoped("db2")
	if err != nil {
		t.Fatalf("[%s] DatabaseScoped(db2): %v", label, err)
	}
	sharedKey := "scope/" + label + "/shared"
	sysOnlyKey := "scope/" + label + "/sys-only"
	dbOnlyKey := "scope/" + label + "/db2-only"

	if err := base.KV().Put(ctx, sharedKey, []byte("sys-value")); err != nil {
		t.Fatalf("[%s] system put: %v", label, err)
	}
	if err := scoped.KV().Put(ctx, sharedKey, []byte("db2-value")); err != nil {
		t.Fatalf("[%s] db2 put: %v", label, err)
	}
	if err := base.KV().Put(ctx, sysOnlyKey, []byte("only-sys")); err != nil {
		t.Fatalf("[%s] system put sys-only: %v", label, err)
	}
	if err := scoped.KV().Put(ctx, dbOnlyKey, []byte("only-db2")); err != nil {
		t.Fatalf("[%s] db2 put db2-only: %v", label, err)
	}

	// Same key resolves to different values per database.
	if v, err := base.KV().Get(ctx, sharedKey); err != nil || string(v) != "sys-value" {
		t.Fatalf("[%s] system get shared = %q, %v; want sys-value", label, v, err)
	}
	if v, err := scoped.KV().Get(ctx, sharedKey); err != nil || string(v) != "db2-value" {
		t.Fatalf("[%s] db2 get shared = %q, %v; want db2-value", label, v, err)
	}

	// A key written only in system is invisible to db2 and vice-versa.
	if _, err := scoped.KV().Get(ctx, sysOnlyKey); !isNotFound(err) {
		t.Fatalf("[%s] db2 must NOT see system-only key; got err=%v", label, err)
	}
	if _, err := base.KV().Get(ctx, dbOnlyKey); !isNotFound(err) {
		t.Fatalf("[%s] system must NOT see db2-only key; got err=%v", label, err)
	}
}

// isNotFound reports whether err is the SDK key-not-found error.
func isNotFound(err error) bool {
	var knf *transport.KeyNotFoundError
	return errors.As(err, &knf)
}

// TestMultiDB_Scope_AllTransports proves per-(tenant, database) keyspace
// isolation over every non-REST transport (gRPC, Unix, native QUIC, and MCP)
// end-to-end through the Go SDK's DatabaseScoped client.
func TestMultiDB_Scope_AllTransports(t *testing.T) {
	node := startScopeNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// gRPC client over mTLS (CN=admin -> Admin@system).
	gt, err := grpctransport.New(
		transport.WithAddress(fmt.Sprintf("127.0.0.1:%d", node.grpcPort)),
		transport.WithMTLS(node.certPath, node.keyPath, node.ca.CertPath),
	)
	if err != nil {
		t.Fatalf("grpc transport: %v", err)
	}
	grpcClient, err := sdk.NewClient(gt)
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer grpcClient.Close()
	if err := grpcClient.Connect(ctx); err != nil {
		t.Fatalf("grpc connect: %v", err)
	}

	// Create db2 once (shared database manager backs every transport).
	if _, err := grpcClient.Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name:    "db2",
		Engine:  "kv",
		Backend: "memory",
	}); err != nil {
		t.Fatalf("create db2: %v", err)
	}

	t.Run("grpc", func(t *testing.T) {
		assertIsolation(t, grpcClient, "grpc")
	})

	t.Run("unix", func(t *testing.T) {
		// Unix (grpc-over-UDS) is "verified TLS 1.3 only", identical to the
		// TCP gRPC transport: present the admin client certificate and
		// verify the server, exactly like the "grpc" subtest above.
		ut, err := unixtransport.New(
			transport.WithAddress(node.unixSock),
			transport.WithMTLS(node.certPath, node.keyPath, node.ca.CertPath),
		)
		if err != nil {
			t.Fatalf("unix transport: %v", err)
		}
		unixClient, err := sdk.NewClient(ut)
		if err != nil {
			t.Fatalf("unix client: %v", err)
		}
		defer unixClient.Close()
		uctx, ucancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ucancel()
		if err := unixClient.Connect(uctx); err != nil {
			t.Fatalf("unix connect: %v", err)
		}
		assertIsolation(t, unixClient, "unix")
	})

	t.Run("quic", func(t *testing.T) {
		qt, err := quictransport.New(
			transport.WithAddress(fmt.Sprintf("127.0.0.1:%d", node.quicPort)),
			transport.WithMTLS(node.certPath, node.keyPath, node.ca.CertPath),
		)
		if err != nil {
			t.Fatalf("quic transport: %v", err)
		}
		quicClient, err := sdk.NewClient(qt)
		if err != nil {
			t.Fatalf("quic client: %v", err)
		}
		defer quicClient.Close()
		qctx, qcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer qcancel()
		if err := quicClient.Connect(qctx); err != nil {
			t.Fatalf("quic connect: %v", err)
		}
		assertIsolation(t, quicClient, "quic")
	})

	t.Run("mcp", func(t *testing.T) {
		mt, err := mcptransport.New(
			transport.WithAddress(fmt.Sprintf("https://127.0.0.1:%d/mcp", node.mcpPort)),
			transport.WithMTLS(node.certPath, node.keyPath, node.ca.CertPath),
		)
		if err != nil {
			t.Fatalf("mcp transport: %v", err)
		}
		mcpClient, err := sdk.NewClient(mt)
		if err != nil {
			t.Fatalf("mcp client: %v", err)
		}
		defer mcpClient.Close()
		mctx, mcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer mcancel()
		if err := mcpClient.Connect(mctx); err != nil {
			t.Fatalf("mcp connect: %v", err)
		}
		assertIsolation(t, mcpClient, "mcp")
	})
}
