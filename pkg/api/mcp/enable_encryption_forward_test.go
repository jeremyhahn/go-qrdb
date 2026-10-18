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
	"net/http"
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

// A follower's qrdb_barrier_enable_encryption, refused not-leader by its
// migrator, reaches the system-shard leader's migrator unchanged: the
// passphrase the leader seals with is the caller's.
func TestReplicaForwardMCP_EnableEncryptionForwardsTheCallUnchanged(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)

	leaderMig := &captureMCPMigrator{ch: make(chan mcpMigrateCall, 1)}
	leaderMW := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithUserService(authtest.NewBindingUsers()))
	leader := NewServer(newMockKVService(), &mockAdminService{}, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-3")), WithAuthMiddleware(leaderMW),
		WithEncryptionMigrator(leaderMig))
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Stop)

	members := replicaforwardtest.Members{
		{NodeID: 3, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolMCP: leader.Addr()}},
	}
	followerMig := &captureMCPMigrator{
		ch:  make(chan mcpMigrateCall, 1),
		err: &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "raft-leader"},
	}
	caller := authtest.NewFixedAuthenticator(&auth.Identity{Subject: "alice", TenantID: authtest.BindingTenant})
	followerMW := authmw.New(&guardMCPAllowAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(caller), authmw.WithUserService(authtest.NewBindingUsers()))
	follower := NewServer(newMockKVService(), &mockAdminService{}, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(followerMW),
		WithEncryptionMigrator(followerMig),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := follower.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(follower.Stop)

	client := dialMCP(t, follower.Addr(), &http.Client{Transport: &http.Transport{TLSClientConfig: ca.TLS(t, "")}})
	req := mcpgo.CallToolRequest{}
	req.Params.Name = "qrdb_barrier_enable_encryption"
	req.Params.Arguments = map[string]any{"strategy": "passphrase", "passphrase": "correct horse"}
	res, err := client.CallTool(callCtx(t), req)
	if err != nil {
		t.Fatalf("qrdb_barrier_enable_encryption on a follower: %v; want the leader's answer", err)
	}
	if res.IsError {
		t.Fatalf("result = %+v; want the leader's success", res)
	}
	select {
	case call := <-leaderMig.ch:
		if string(call.secret) != "correct horse" {
			t.Fatalf("leader sealed with passphrase %q; want the caller's", call.secret)
		}
	default:
		t.Fatal("the leader's migrator was never called; the follower did not forward")
	}
}
