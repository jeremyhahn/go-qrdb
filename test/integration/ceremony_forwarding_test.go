// Copyright 2026 Jeremy Hahn
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

package integration_test

// Node-parity regression guard (owner ruling 2026-10-06): POST
// /v1/ceremony/initialize's admin-credential step must run on the system
// shard's leader (service.CeremonyServiceWithLeaderGate). A follower now
// forwards the identical call to the leader through that transport's own
// leader-forwarding path instead of answering its own refusal -- on every
// network transport a node serves, not only REST.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	mcpwire "github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// grpcCeremonyInitializeCode calls CeremonyInitialize over gRPC at addr (or,
// with unix set, over the Unix socket at addr, which serves the same
// qrdbServiceServer) as the admin identity, and returns the gRPC status
// code: a comparable "what happened" that does not depend on knowing the
// system barrier's real SO PIN.
func grpcCeremonyInitializeCode(t *testing.T, ca *testCA, addr string, unix bool) codes.Code {
	t.Helper()
	cert, _, _ := ca.adminIdentity(t, t.TempDir())
	cfg := &tls.Config{RootCAs: ca.Pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	target := addr
	if unix {
		target, cfg.ServerName = "unix://"+addr, "127.0.0.1"
	}
	cc, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = cc.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = pb.NewQRDBServiceClient(cc).CeremonyInitialize(ctx, &pb.CeremonyInitializeRequest{})
	return status.Code(err)
}

// quicCeremonyInitializeStatus calls ceremony.Initialize over native QUIC at
// addr as the admin identity, and returns the wire status code.
func quicCeremonyInitializeStatus(t *testing.T, ca *testCA, addr string) uint16 {
	t.Helper()
	cert, _, _ := ca.adminIdentity(t, t.TempDir())
	payload, err := proto.Marshal(&pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, addr, &tls.Config{
		RootCAs: ca.Pool, Certificates: []tls.Certificate{cert}, NextProtos: []string{wire.ProtocolV3}, MinVersion: tls.VersionTLS13,
	}, &quicgo.Config{})
	if err != nil {
		t.Fatalf("quic dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := wire.WriteRequest(stream, "ceremony.Initialize", "", 1, payload); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, st, _, err := wire.ReadResponse(stream)
	_ = stream.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return st
}

// mcpCeremonyInitializeCode calls qrdb_ceremony_initialize over MCP at addr
// as the admin identity, and returns the structured error code (wire.CodeOK
// for a non-error result) -- a comparable "what happened" that does not
// depend on knowing the system barrier's real SO PIN.
func mcpCeremonyInitializeCode(t *testing.T, ca *testCA, addr string) mcpwire.Code {
	t.Helper()
	cert, _, _ := ca.adminIdentity(t, t.TempDir())
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: ca.Pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13,
	}}}
	client, err := mcpclient.NewStreamableHttpClient("https://"+addr+"/mcp", mcptransport.WithHTTPBasicClient(hc))
	if err != nil {
		t.Fatalf("mcp client: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("mcp start: %v", err)
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "ceremony-forwarding-test", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_ceremony_initialize"
	req.Params.Arguments = map[string]any{}
	res, err := client.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		return mcpwire.CodeOK
	}
	p, ok := mcpwire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		t.Fatalf("result is an error with no structured error payload: %+v", res)
	}
	return p.Code
}

// TestCluster_CeremonyInitialize_ForwardsToLeader_EveryNetworkTransport runs
// one independent 3-node cluster PER transport: the admin-credential step's
// own per-source attempt accounting lives on the leader's ceremony service,
// so two transports sharing one cluster would mutate each other's state (an
// earlier version of this test proved exactly that -- a later transport's
// pair of calls could tip over a threshold four earlier calls had already
// pushed close to). An independent cluster per transport keeps each
// comparison to the two calls it actually makes.
func TestCluster_CeremonyInitialize_ForwardsToLeader_EveryNetworkTransport(t *testing.T) {
	t.Run("grpc", func(t *testing.T) {
		ca, leaderAddr, followerAddr := ceremonyForwardingClusterFor(t, func(c ceremonyClusterPorts) (string, string) {
			return fmt.Sprintf("127.0.0.1:%d", c.grpc[c.leader]), fmt.Sprintf("127.0.0.1:%d", c.grpc[c.follower])
		})
		leaderCode := grpcCeremonyInitializeCode(t, ca, leaderAddr, false)
		if leaderCode == codes.Unavailable {
			t.Fatalf("leader itself answered Unavailable; test fixture assumption broken")
		}
		if followerCode := grpcCeremonyInitializeCode(t, ca, followerAddr, false); followerCode != leaderCode {
			t.Fatalf("gRPC follower code = %s, want %s (the leader's)", followerCode, leaderCode)
		}
	})

	t.Run("unix", func(t *testing.T) {
		ca, leaderAddr, followerAddr := ceremonyForwardingClusterFor(t, func(c ceremonyClusterPorts) (string, string) {
			return c.unix[c.leader], c.unix[c.follower]
		})
		leaderCode := grpcCeremonyInitializeCode(t, ca, leaderAddr, true)
		if leaderCode == codes.Unavailable {
			t.Fatalf("leader itself answered Unavailable; test fixture assumption broken")
		}
		if followerCode := grpcCeremonyInitializeCode(t, ca, followerAddr, true); followerCode != leaderCode {
			t.Fatalf("Unix follower code = %s, want %s (the leader's)", followerCode, leaderCode)
		}
	})

	t.Run("quic", func(t *testing.T) {
		ca, leaderAddr, followerAddr := ceremonyForwardingClusterFor(t, func(c ceremonyClusterPorts) (string, string) {
			return fmt.Sprintf("127.0.0.1:%d", c.quic[c.leader]), fmt.Sprintf("127.0.0.1:%d", c.quic[c.follower])
		})
		leaderStatus := quicCeremonyInitializeStatus(t, ca, leaderAddr)
		if leaderStatus == wire.StatusUnavailable {
			t.Fatalf("leader itself answered StatusUnavailable; test fixture assumption broken")
		}
		if followerStatus := quicCeremonyInitializeStatus(t, ca, followerAddr); followerStatus != leaderStatus {
			t.Fatalf("QUIC follower status = %d, want %d (the leader's)", followerStatus, leaderStatus)
		}
	})

	t.Run("mcp", func(t *testing.T) {
		ca, leaderAddr, followerAddr := ceremonyForwardingClusterFor(t, func(c ceremonyClusterPorts) (string, string) {
			return fmt.Sprintf("127.0.0.1:%d", c.mcp[c.leader]), fmt.Sprintf("127.0.0.1:%d", c.mcp[c.follower])
		})
		leaderCode := mcpCeremonyInitializeCode(t, ca, leaderAddr)
		if leaderCode == mcpwire.CodeUnavailable {
			t.Fatalf("leader itself answered CodeUnavailable; test fixture assumption broken")
		}
		if followerCode := mcpCeremonyInitializeCode(t, ca, followerAddr); followerCode != leaderCode {
			t.Fatalf("MCP follower code = %q, want %q (the leader's)", followerCode, leaderCode)
		}
	})
}

// ceremonyClusterPorts is one freshly started cluster's per-transport ports
// plus which index is the leader and which is a follower.
type ceremonyClusterPorts struct {
	grpc, quic, mcp  []int
	unix             []string
	leader, follower int
}

// ceremonyForwardingClusterFor starts a fresh 3-node, all-transports cluster,
// waits for a settled leader/follower view, and extracts the leader and
// follower addresses addrsOf names for the caller's transport.
func ceremonyForwardingClusterFor(t *testing.T, addrsOf func(ceremonyClusterPorts) (leaderAddr, followerAddr string)) (ca *testCA, leaderAddr, followerAddr string) {
	t.Helper()
	adminPorts, grpcPorts, quicPorts, mcpPorts, unixPaths, ca := startThreeNodeClusterAllTransports(t, "pebble")
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	c := ceremonyClusterPorts{grpc: grpcPorts, quic: quicPorts, mcp: mcpPorts, unix: unixPaths, leader: -1, follower: -1}
	for i, p := range adminPorts {
		if p == leaderPort {
			c.leader = i
		} else if c.follower == -1 {
			c.follower = i
		}
	}
	if c.leader == -1 || c.follower == -1 {
		t.Fatal("could not identify leader and follower admin ports")
	}
	waitForSettledFollowerView(t, ca, adminPorts[c.follower], 30*time.Second)

	leaderAddr, followerAddr = addrsOf(c)
	return ca, leaderAddr, followerAddr
}
