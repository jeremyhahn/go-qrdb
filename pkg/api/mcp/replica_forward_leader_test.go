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
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mcpLeader is the system shard's leader: a real MCP server whose admin
// service accepts every member removal and records who asked for it.
type mcpLeader struct {
	srv    *Server
	calls  atomic.Int32
	caller atomic.Value // string: the subject the leader authorized
}

func startMCPLeader(t *testing.T, ca *replicaforwardtest.CA) *mcpLeader {
	t.Helper()
	l := &mcpLeader{}
	admin := &mockAdminService{removeMemberFn: func(ctx context.Context, _, _ uint64) error {
		l.calls.Add(1)
		if id := auth.IdentityFromContext(ctx); id != nil {
			l.caller.Store(id.Subject)
		}
		return nil
	}}
	mw := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithUserService(authtest.NewBindingUsers()))
	l.srv = NewServer(newMockKVService(), admin, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-3")), WithAuthMiddleware(mw))
	if err := l.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.srv.Stop)
	return l
}

// startMCPFollower serves a follower of the system shard: its admin service
// refuses every member removal with *service.NotLeaderError naming
// leaderAddr, and leader, at raft-leader, is the one member it can resolve.
func startMCPFollower(t *testing.T, ca *replicaforwardtest.CA, leader *mcpLeader, leaderAddr string) *Server {
	t.Helper()
	members := replicaforwardtest.Members{
		{NodeID: 3, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolMCP: leader.srv.Addr()}},
	}
	admin := &mockAdminService{removeMemberFn: func(context.Context, uint64, uint64) error {
		return &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: leaderAddr}
	}}
	caller := authtest.NewFixedAuthenticator(&auth.Identity{Subject: "alice", TenantID: authtest.BindingTenant})
	mw := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithAuthenticator(caller), authmw.WithUserService(authtest.NewBindingUsers()))
	follower := NewServer(newMockKVService(), admin, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := follower.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(follower.Stop)
	return follower
}

func callRemoveMember(t *testing.T, ca *replicaforwardtest.CA, srv *Server) (*mcpgo.CallToolResult, error) {
	t.Helper()
	client := dialMCP(t, srv.Addr(), &http.Client{Transport: &http.Transport{TLSClientConfig: ca.TLS(t, "")}})
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_remove_member"
	req.Params.Arguments = map[string]any{"shard_id": 1, "replica_id": 7}
	return client.CallTool(callCtx(t), req)
}

func TestReplicaForwardMCP_NotLeaderForwardsToTheLeaderAsTheCaller(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startMCPLeader(t, ca)
	follower := startMCPFollower(t, ca, leader, "raft-leader")

	res, err := callRemoveMember(t, ca, follower)
	if err != nil {
		t.Fatalf("qrdb_remove_member on a follower: %v; want the leader's answer", err)
	}
	if res.IsError {
		t.Fatalf("result = %+v; want the leader's success", res)
	}
	if n := leader.calls.Load(); n != 1 {
		t.Fatalf("leader removed %d members; want 1", n)
	}
	if got, _ := leader.caller.Load().(string); got != "alice" {
		t.Fatalf("leader authorized %q; want the original caller alice", got)
	}
}

func TestReplicaForwardMCP_NotLeaderNamingAnUnknownLeaderIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startMCPLeader(t, ca)

	for _, leaderAddr := range []string{"raft-unknown", ""} {
		follower := startMCPFollower(t, ca, leader, leaderAddr)
		res, err := callRemoveMember(t, ca, follower)
		var answer string
		switch {
		case err != nil:
			answer = err.Error()
		case res.IsError:
			answer = resultText(t, res)
		default:
			t.Fatalf("leader %q: qrdb_remove_member succeeded; want the follower's own NotLeader refusal", leaderAddr)
		}
		if !strings.Contains(answer, "not leader") {
			t.Fatalf("leader %q: answer %q; want the follower's own NotLeader refusal", leaderAddr, answer)
		}
	}
	if n := leader.calls.Load(); n != 0 {
		t.Fatalf("an unresolvable leader was forwarded to (%d leader calls)", n)
	}
}
