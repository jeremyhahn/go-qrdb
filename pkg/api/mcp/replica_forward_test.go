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

package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mcpTarget is a replica's MCP listener: the real server, authenticating a
// forwarded call by its on-behalf assertion against the node's user store,
// over a KV that answers with answer.
type mcpTarget struct {
	srv      *Server
	raftAddr string
	calls    atomic.Int32
	answer   func(ctx context.Context) error

	mu   sync.Mutex
	seen []string
}

func startMCPTarget(t *testing.T, ca *replicaforwardtest.CA, nodeID, raftAddr string) *mcpTarget {
	t.Helper()
	mt := &mcpTarget{raftAddr: raftAddr}
	kv := newMockKVService()
	kv.putFn = func(ctx context.Context, _ string, _ []byte) error {
		mt.calls.Add(1)
		subject := ""
		if id := auth.IdentityFromContext(ctx); id != nil && authmw.IsOnBehalf(id) {
			subject = id.Subject
		}
		mt.mu.Lock()
		mt.seen = append(mt.seen, subject)
		mt.mu.Unlock()
		if mt.answer != nil {
			return mt.answer(ctx)
		}
		return nil
	}
	mw := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithUserService(authtest.NewBindingUsers()))
	mt.srv = NewServer(kv, nil, "127.0.0.1:0", WithTLS(ca.TLS(t, nodeID)), WithAuthMiddleware(mw))
	if err := mt.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mt.srv.Stop)
	return mt
}

func (mt *mcpTarget) lastSeen(t *testing.T) string {
	t.Helper()
	mt.mu.Lock()
	defer mt.mu.Unlock()
	if len(mt.seen) == 0 {
		t.Fatal("the target saw no call")
	}
	return mt.seen[len(mt.seen)-1]
}

// mcpFwdCluster is an ingress node holding no replica -- a real listener
// whose KV refuses every write as a non-replica -- in front of the replicas.
type mcpFwdCluster struct {
	cache  *replicaforward.LeaderCache
	srv    *Server
	client *mcpclient.Client
}

func newMCPFwdCluster(t *testing.T, ca *replicaforwardtest.CA, timeout time.Duration, targets ...*mcpTarget) *mcpFwdCluster {
	t.Helper()
	c := &mcpFwdCluster{cache: replicaforward.NewLeaderCache()}
	refusal := &placement.NotAReplicaError{TenantID: authtest.BindingTenant, Database: "orders", ShardID: 42, NodeID: "node-a", LeaderReplicaID: 1}
	var members replicaforwardtest.Members
	for i, mt := range targets {
		refusal.Replicas = append(refusal.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: "node-" + strconv.Itoa(i+1), RaftAddr: mt.raftAddr})
		members = append(members, cluster.Member{NodeID: uint64(i + 1), Address: mt.raftAddr,
			APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolMCP: mt.endpoint()}})
	}
	kv := newMockKVService()
	kv.putFn = func(context.Context, string, []byte) error { return refusal }
	caller := authtest.NewFixedAuthenticator(&auth.Identity{Subject: "alice", TenantID: authtest.BindingTenant})
	mw := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithAuthenticator(caller), authmw.WithUserService(authtest.NewBindingUsers()))
	c.srv = NewServer(kv, nil, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: timeout}))
	if err := c.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.srv.Stop)
	c.client = dialMCP(t, c.srv.Addr(), &http.Client{Transport: &http.Transport{TLSClientConfig: ca.TLS(t, "")}})
	return c
}

// endpoint is the target's MCP endpoint as a member record names it; a
// target that was never started has a closed one.
func (mt *mcpTarget) endpoint() string {
	if mt.srv == nil {
		return mt.raftAddr
	}
	return mt.srv.Addr()
}

func dialMCP(t *testing.T, addr string, hc *http.Client) *mcpclient.Client {
	t.Helper()
	client, err := mcpclient.NewStreamableHttpClient("https://"+addr+"/mcp", transport.WithHTTPBasicClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := callCtx(t)
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "replica-forward-test", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return client
}

func putCall(t *testing.T, client *mcpclient.Client) *mcpgo.CallToolResult {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_put"
	req.Params.Arguments = map[string]any{"key": "k1", "value": base64.StdEncoding.EncodeToString([]byte("v1")), "database": "orders"}
	res, err := client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func forwardedTo(res *mcpgo.CallToolResult) string {
	if res.Meta == nil {
		return ""
	}
	v, _ := res.Meta.AdditionalFields[replicaforward.MetadataForwardedTo].(string)
	return v
}

// mcpRefusal decodes res as the NOT_A_REPLICA refusal.
func mcpRefusal(t *testing.T, res *mcpgo.CallToolResult) *placement.NotAReplicaError {
	t.Helper()
	p, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !res.IsError || !ok || p.Reason != replicaforward.ReasonNotAReplica || p.Code != wire.CodeUnavailable {
		t.Fatalf("result %+v (%+v); want the NOT_A_REPLICA refusal", res, p)
	}
	refusal, err := replicaforward.ParseNotAReplica(p.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

// fixedCeremonyService is a service.CeremonyServicer double that answers
// every call with a fixed result or error.
type fixedCeremonyService struct {
	result *service.CeremonyInitResult
	err    error
}

func (f fixedCeremonyService) CeremonyInitialize(context.Context, ceremony.CeremonyCA, *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return f.result, f.err
}

func (f fixedCeremonyService) CeremonySignCSR(context.Context, string, string, string, string) (*service.CeremonySignCSRResult, error) {
	return nil, f.err
}

func (f fixedCeremonyService) CeremonyStatus(context.Context) (*service.CeremonyStatusInfo, error) {
	return nil, f.err
}

// TestReplicaForwardMCP_CeremonyInitializeForwardsOnAdminCredentialNotLeaderError
// is the node-parity regression guard (owner ruling 2026-10-06): the admin-
// credential step of qrdb_ceremony_initialize must run on the system shard's
// leader (service.CeremonyServiceWithLeaderGate); a follower forwards the
// identical call to the leader through the SAME session machinery the
// per-database NotAReplicaError path uses, rather than answering its own
// refusal.
func TestReplicaForwardMCP_CeremonyInitializeForwardsOnAdminCredentialNotLeaderError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)

	leaderMW := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithUserService(authtest.NewBindingUsers()))
	leader := NewServer(newMockKVService(), nil, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-1")), WithAuthMiddleware(leaderMW),
		WithCeremonyService(fixedCeremonyService{result: &service.CeremonyInitResult{SPKIPin: "leader-answered"}}))
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Stop)

	cache := replicaforward.NewLeaderCache()
	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolMCP: leader.Addr()}},
	}
	caller := authtest.NewFixedAuthenticator(&auth.Identity{Subject: "alice", TenantID: authtest.BindingTenant})
	ingressMW := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithAuthenticator(caller), authmw.WithUserService(authtest.NewBindingUsers()))
	ingress := NewServer(newMockKVService(), nil, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(ingressMW),
		WithCeremonyService(fixedCeremonyService{err: &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}}),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := ingress.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ingress.Stop)

	client := dialMCP(t, ingress.Addr(), &http.Client{Transport: &http.Transport{TLSClientConfig: ca.TLS(t, "")}})
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_ceremony_initialize"
	req.Params.Arguments = map[string]any{"admin_generate": true}
	res, err := client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("result = %+v; want the leader's success", res)
	}
	if text := resultText(t, res); !strings.Contains(text, "leader-answered") {
		t.Fatalf("result text = %q; want the leader's spki_pin", text)
	}
}

// TestForwardToLeader_EmptyLeaderAddressDoesNotForward proves forwardToLeader
// refuses to forward when the refusal names no leader at all.
func TestForwardToLeader_EmptyLeaderAddressDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	f := newReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ca.TLS(t, "node-a"))
	defer f.close()

	if _, _, ok := f.forwardToLeader(context.Background(), mcpgo.CallToolRequest{}, ""); ok {
		t.Fatal("forwardToLeader forwarded with an empty leader address")
	}
}

// TestForwardToLeader_UnresolvableLeaderDoesNotForward proves forwardToLeader
// falls back when the named leader has no member record.
func TestForwardToLeader_UnresolvableLeaderDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	f := newReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ca.TLS(t, "node-a"))
	defer f.close()

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	if _, _, ok := f.forwardToLeader(ctx, mcpgo.CallToolRequest{}, "raft-unknown"); ok {
		t.Fatal("forwardToLeader forwarded to a leader with no member record")
	}
}

// TestForwardToLeader_NoMCPEndpointDoesNotForward proves forwardToLeader
// falls back when the leader's member record advertises no MCP endpoint.
func TestForwardToLeader_NoMCPEndpointDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: "127.0.0.1:1"}},
	}
	f := newReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ca.TLS(t, "node-a"))
	defer f.close()

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	if _, _, ok := f.forwardToLeader(ctx, mcpgo.CallToolRequest{}, "raft-leader"); ok {
		t.Fatal("forwardToLeader forwarded to a leader with no MCP endpoint")
	}
}

// TestForwardToLeader_UnreachableLeaderDoesNotForward proves forwardToLeader
// falls back when the leader's member record resolves but nothing answers.
func TestForwardToLeader_UnreachableLeaderDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolMCP: replicaforwardtest.ClosedAddr(t)}},
	}
	f := newReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 200 * time.Millisecond,
	}, ca.TLS(t, "node-a"))
	defer f.close()

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	if _, _, ok := f.forwardToLeader(ctx, mcpgo.CallToolRequest{}, "raft-leader"); ok {
		t.Fatal("forwardToLeader forwarded to an unreachable leader")
	}
}

func TestReplicaForwardMCP_ForwardsAsTheCallerAndRelaysTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startMCPTarget(t, ca, "node-1", "raft-b")
	c := newMCPFwdCluster(t, ca, 5*time.Second, b)

	res := putCall(t, c.client)
	if res.IsError || forwardedTo(res) != "1" {
		t.Fatalf("result %+v; want the replica's success, forwarded to replica 1", res)
	}
	if got := b.lastSeen(t); got != "alice" {
		t.Fatalf("the target ran the call as %q; want alice, on-behalf", got)
	}
	if l, ok := c.cache.Get(42); !ok || l.RaftAddr != "raft-b" {
		t.Fatalf("cache = %+v, %v; want the replica that served", l, ok)
	}
}

func TestReplicaForwardMCP_NotLeaderRetriesTheNamedLeader(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startMCPTarget(t, ca, "node-1", "raft-b")
	cc := startMCPTarget(t, ca, "node-2", "raft-c")
	b.answer = func(context.Context) error {
		return &service.NotLeaderError{ShardID: 42, LeaderID: 2, LeaderAddress: "raft-c"}
	}
	c := newMCPFwdCluster(t, ca, 5*time.Second, b, cc)

	if res := putCall(t, c.client); res.IsError || forwardedTo(res) != "2" {
		t.Fatalf("result %+v; want the named leader's", res)
	}
	if b.calls.Load() != 1 || cc.calls.Load() != 1 {
		t.Fatalf("calls b=%d c=%d; want one each", b.calls.Load(), cc.calls.Load())
	}
}

func TestReplicaForwardMCP_UnreachableRetriesTheNextReplica(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	dead := &mcpTarget{raftAddr: replicaforwardtest.ClosedAddr(t)}
	cc := startMCPTarget(t, ca, "node-2", "raft-c")
	c := newMCPFwdCluster(t, ca, 5*time.Second, dead, cc)

	if res := putCall(t, c.client); res.IsError || forwardedTo(res) != "2" || cc.calls.Load() != 1 {
		t.Fatalf("result %+v after %d calls to the next replica; want it served there", res, cc.calls.Load())
	}
}

func TestReplicaForwardMCP_AFailureAfterSendingIsNotRetried(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startMCPTarget(t, ca, "node-1", "raft-b")
	cc := startMCPTarget(t, ca, "node-2", "raft-c")
	b.answer = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	c := newMCPFwdCluster(t, ca, 500*time.Millisecond, b, cc)

	res := putCall(t, c.client)
	p, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !res.IsError || !ok || p.Reason == replicaforward.ReasonNotAReplica {
		t.Fatalf("result %+v (%+v); want the failure, not the refusal", res, p)
	}
	if cc.calls.Load() != 0 {
		t.Fatalf("a call that may have run was sent again (%d)", cc.calls.Load())
	}
}

func TestReplicaForwardMCP_TheTargetsErrorIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startMCPTarget(t, ca, "node-1", "raft-b")
	b.answer = func(context.Context) error { return &service.KeyNotFoundError{Key: "k1"} }
	c := newMCPFwdCluster(t, ca, 5*time.Second, b)

	res := putCall(t, c.client)
	if p, ok := wire.DecodeErrorPayload(res.StructuredContent); !res.IsError || !ok || p.Code != wire.CodeNotFound || forwardedTo(res) != "1" {
		t.Fatalf("result %+v (%+v); want the replica's not found", res, p)
	}
}

func TestReplicaForwardMCP_EveryReplicaRefusingIsTheRefusal(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	refuse := func(context.Context) error {
		return &placement.NotAReplicaError{TenantID: authtest.BindingTenant, Database: "orders", NodeID: "node-x"}
	}
	b := startMCPTarget(t, ca, "node-1", "raft-b")
	cc := startMCPTarget(t, ca, "node-2", "raft-c")
	b.answer, cc.answer = refuse, refuse
	c := newMCPFwdCluster(t, ca, 5*time.Second, b, cc)
	if got := mcpRefusal(t, putCall(t, c.client)); got.NodeID != "node-a" || len(got.Replicas) != 2 {
		t.Fatalf("refusal = %+v; want the ingress node's, with its replica set", got)
	}
}

// TestReplicaForwardMCP_AForwardedCallIsNeverForwardedAgain: a call a
// cluster node forwarded here is answered with the refusal, not forwarded.
func TestReplicaForwardMCP_AForwardedCallIsNeverForwardedAgain(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startMCPTarget(t, ca, "node-1", "raft-b")
	c := newMCPFwdCluster(t, ca, 5*time.Second, b)

	nodeTLS := ca.TLS(t, "node-x")
	key, nodeID := forward.NodeIdentity(nodeTLS)
	rt := &onBehalfTransport{base: &http.Transport{TLSClientConfig: nodeTLS}, caller: &auth.Identity{Subject: "alice", TenantID: authtest.BindingTenant}, key: key, nodeID: nodeID}
	forwarded := dialMCP(t, c.srv.Addr(), &http.Client{Transport: rt})
	mcpRefusal(t, putCall(t, forwarded))
	if b.calls.Load() != 0 {
		t.Fatalf("a forwarded call was forwarded again (%d target calls)", b.calls.Load())
	}
}

func TestClassifyMCPAnswer(t *testing.T) {
	refusal := func(err error) *mcpgo.CallToolResult { return classifiedToolError("qrdb_put", err) }
	for name, tc := range map[string]struct {
		res     *mcpgo.CallToolResult
		refused bool
		outcome replicaforward.Outcome
	}{
		"success":         {res: mcpgo.NewToolResultText("ok")},
		"not a replica":   {res: refusal(&placement.NotAReplicaError{}), refused: true, outcome: replicaforward.NotAReplica},
		"not leader":      {res: refusal(&service.NotLeaderError{LeaderAddress: "x"}), refused: true, outcome: replicaforward.NotLeader},
		"another failure": {res: refusal(errors.New("sealed"))},
		"uncoded":         {res: mcpgo.NewToolResultError("key is required")},
	} {
		out, refused := classifyMCPAnswer(tc.res)
		if refused != tc.refused || (refused && out.Outcome != tc.outcome) {
			t.Errorf("%s: classify = %+v, %v", name, out, refused)
		}
	}
	if isToolCall([]byte(`{"method":"initialize"}`)) || !isToolCall([]byte(`{"method":"tools/call"}`)) || isToolCall([]byte("{")) {
		t.Fatal("isToolCall misread a request")
	}
}
