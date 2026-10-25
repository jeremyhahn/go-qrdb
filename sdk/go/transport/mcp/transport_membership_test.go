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
	"fmt"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// membershipAdmin is the mock admin service grown the optional membership
// interfaces the MCP tools type-assert for. Each verb records its arguments
// as one line in calls, and refuses with refuse when it is set, so a test
// sees exactly what crossed the wire and how a refusal comes back.
type membershipAdmin struct {
	*mockAdminService
	calls   []string
	refuse  error
	pending []*service.PendingJoinRequest
}

var (
	_ service.MemberPromoter         = (*membershipAdmin)(nil)
	_ service.LeaderTransferrer      = (*membershipAdmin)(nil)
	_ service.ClusterJoinTokenMinter = (*membershipAdmin)(nil)
	_ service.JoinRequestReviewer    = (*membershipAdmin)(nil)
	_ service.ObserverMemberAdder    = (*membershipAdmin)(nil)
	_ service.JoinIdentityRetirer    = (*membershipAdmin)(nil)
)

func (m *membershipAdmin) record(format string, args ...any) error {
	m.calls = append(m.calls, fmt.Sprintf(format, args...))
	return m.refuse
}

func (m *membershipAdmin) PromoteMember(_ context.Context, shardID, replicaID uint64) error {
	return m.record("promote %d %d", shardID, replicaID)
}

func (m *membershipAdmin) TransferLeadership(_ context.Context, shardID, target uint64) error {
	return m.record("transfer %d %d", shardID, target)
}

func (m *membershipAdmin) MintJoinToken(_ context.Context, nodeID string, ttl time.Duration) (string, time.Time, error) {
	if err := m.record("mint %s %s", nodeID, ttl); err != nil {
		return "", time.Time{}, err
	}
	return "join-token", time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC), nil
}

func (m *membershipAdmin) ListPendingJoinRequests(context.Context) ([]*service.PendingJoinRequest, error) {
	if err := m.record("list"); err != nil {
		return nil, err
	}
	return m.pending, nil
}

func (m *membershipAdmin) ApproveJoinRequest(_ context.Context, id, nodeID string) error {
	return m.record("approve %s %s", id, nodeID)
}

func (m *membershipAdmin) RejectJoinRequest(_ context.Context, id, reason string) error {
	return m.record("reject %s %q", id, reason)
}

func (m *membershipAdmin) AddObserverMember(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	return m.record("observer %d %d %s %s", shardID, replicaID, raftAddr, identity)
}

func (m *membershipAdmin) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	return m.record("retire %s %q", nodeID, reason)
}

// connectMembership connects a Transport to the real MCP server over admin.
func connectMembership(t *testing.T, admin *membershipAdmin) *Transport {
	t.Helper()
	srv := mcpapi.NewServer(newMockKVService(), admin, ":0")
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestMCP_Membership_EveryVerbReachesTheService(t *testing.T) {
	submitted := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	admin := &membershipAdmin{
		mockAdminService: newMockAdminService(),
		pending: []*service.PendingJoinRequest{{
			ID: "req-1", NodeURI: "spiffe://db.example.com/node/n4", ReplicaID: 4,
			Features: []string{"mvcc"}, SubmittedAt: submitted, ExpiresAt: submitted.Add(time.Hour),
		}},
	}
	tr := connectMembership(t, admin)
	ctx := context.Background()

	if err := tr.PromoteMember(ctx, 1, 4); err != nil {
		t.Fatalf("PromoteMember: %v", err)
	}
	if err := tr.TransferLeadership(ctx, 1, 2); err != nil {
		t.Fatalf("TransferLeadership: %v", err)
	}
	if err := tr.AddObserverMember(ctx, 1, 5, "10.0.0.5:63001", "n5"); err != nil {
		t.Fatalf("AddObserverMember: %v", err)
	}
	if err := tr.AddObserverMember(ctx, 1, 6, "10.0.0.6:63001", ""); err != nil {
		t.Fatalf("AddObserverMember(no identity): %v", err)
	}
	if err := tr.RetireJoinIdentity(ctx, "n3", "decommissioned"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}

	tok, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n4", TTLSeconds: 600})
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if tok.Token != "join-token" || !tok.ExpiresAt.Equal(time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)) {
		t.Fatalf("MintJoinToken = %+v", tok)
	}
	if _, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n7"}); err != nil {
		t.Fatalf("MintJoinToken(default ttl): %v", err)
	}

	pending, err := tr.ListPendingJoinRequests(ctx)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "req-1" || pending[0].ReplicaID != 4 ||
		pending[0].NodeURI != "spiffe://db.example.com/node/n4" || !pending[0].SubmittedAt.Equal(submitted) {
		t.Fatalf("ListPendingJoinRequests = %+v", pending)
	}
	if err := tr.ApproveJoinRequest(ctx, "req-1", "n4"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	if err := tr.RejectJoinRequest(ctx, "req-2", "unknown host"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}
	if err := tr.RejectJoinRequest(ctx, "req-3", ""); err != nil {
		t.Fatalf("RejectJoinRequest(no reason): %v", err)
	}

	want := []string{
		"promote 1 4",
		"transfer 1 2",
		"observer 1 5 10.0.0.5:63001 n5",
		"observer 1 6 10.0.0.6:63001 ",
		`retire n3 "decommissioned"`,
		"mint n4 10m0s",
	}
	if len(admin.calls) < len(want) {
		t.Fatalf("calls = %q", admin.calls)
	}
	for i, w := range want {
		if admin.calls[i] != w {
			t.Fatalf("call %d = %q, want %q (all: %q)", i, admin.calls[i], w, admin.calls)
		}
	}
	tail := admin.calls[len(want)+1:]
	wantTail := []string{"list", "approve req-1 n4", `reject req-2 "unknown host"`, `reject req-3 ""`}
	if fmt.Sprint(tail) != fmt.Sprint(wantTail) {
		t.Fatalf("calls after the default-ttl mint = %q, want %q", tail, wantTail)
	}
}

func TestMCP_Membership_RefusalsAndClosed(t *testing.T) {
	admin := &membershipAdmin{mockAdminService: newMockAdminService(), refuse: &service.ClusterJoinTokenNotConfiguredError{}}
	tr := connectMembership(t, admin)
	ctx := context.Background()

	for name, err := range map[string]error{
		"PromoteMember":      tr.PromoteMember(ctx, 1, 4),
		"TransferLeadership": tr.TransferLeadership(ctx, 1, 2),
		"AddObserverMember":  tr.AddObserverMember(ctx, 1, 5, "a", ""),
		"RetireJoinIdentity": tr.RetireJoinIdentity(ctx, "n3", ""),
		"ApproveJoinRequest": tr.ApproveJoinRequest(ctx, "r", "n"),
		"RejectJoinRequest":  tr.RejectJoinRequest(ctx, "r", ""),
	} {
		if err == nil {
			t.Errorf("%s succeeded when the service refused", name)
		}
	}
	if _, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n4"}); err == nil {
		t.Error("MintJoinToken succeeded when the service refused")
	}
	if _, err := tr.ListPendingJoinRequests(ctx); err == nil {
		t.Error("ListPendingJoinRequests succeeded when the service refused")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requireClosed(t, tr.PromoteMember(ctx, 1, 4))
	requireClosed(t, tr.TransferLeadership(ctx, 1, 2))
	requireClosed(t, tr.AddObserverMember(ctx, 1, 5, "a", ""))
	requireClosed(t, tr.RetireJoinIdentity(ctx, "n3", ""))
	requireClosed(t, tr.ApproveJoinRequest(ctx, "r", "n"))
	requireClosed(t, tr.RejectJoinRequest(ctx, "r", ""))
	_, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n4"})
	requireClosed(t, err)
	_, err = tr.ListPendingJoinRequests(ctx)
	requireClosed(t, err)
}
