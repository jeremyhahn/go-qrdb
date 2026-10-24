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

package unix

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// joinAdminSvc is the admin double extended with the optional membership and
// join-review interfaces the server type-asserts for. Each method records
// what the server handler passed and answers with err when it is set.
type joinAdminSvc struct {
	*mockAdminService

	err error

	shardID, replicaID uint64
	raftAddr, identity string
	nodeID, reason, id string
	ttl                time.Duration
}

var (
	_ service.MemberPromoter         = (*joinAdminSvc)(nil)
	_ service.ClusterJoinTokenMinter = (*joinAdminSvc)(nil)
	_ service.ObserverMemberAdder    = (*joinAdminSvc)(nil)
	_ service.JoinIdentityRetirer    = (*joinAdminSvc)(nil)
	_ service.JoinRequestReviewer    = (*joinAdminSvc)(nil)
)

func (m *joinAdminSvc) PromoteMember(_ context.Context, shardID, replicaID uint64) error {
	m.shardID, m.replicaID = shardID, replicaID
	return m.err
}

func (m *joinAdminSvc) MintJoinToken(_ context.Context, nodeID string, ttl time.Duration) (string, time.Time, error) {
	m.nodeID, m.ttl = nodeID, ttl
	if m.err != nil {
		return "", time.Time{}, m.err
	}
	return "join-token-" + nodeID, time.Unix(1700003600, 0), nil
}

func (m *joinAdminSvc) AddObserverMember(_ context.Context, shardID, replicaID uint64, raftAddr, identity string) error {
	m.shardID, m.replicaID, m.raftAddr, m.identity = shardID, replicaID, raftAddr, identity
	return m.err
}

func (m *joinAdminSvc) RetireJoinIdentity(_ context.Context, nodeID, reason string) error {
	m.nodeID, m.reason = nodeID, reason
	return m.err
}

func (m *joinAdminSvc) ListPendingJoinRequests(context.Context) ([]*service.PendingJoinRequest, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []*service.PendingJoinRequest{{
		ID: "req-1", NodeURI: "spiffe://db.example.com/node/n4", ReplicaID: 4, Features: []string{"raft"},
		SubmittedAt: time.Unix(1700000000, 0), ExpiresAt: time.Unix(1700000300, 0),
	}}, nil
}

func (m *joinAdminSvc) ApproveJoinRequest(_ context.Context, id, nodeID string) error {
	m.id, m.nodeID = id, nodeID
	return m.err
}

func (m *joinAdminSvc) RejectJoinRequest(_ context.Context, id, reason string) error {
	m.id, m.reason = id, reason
	return m.err
}

// startJoinServer is startAuthServer serving admin as the admin service.
func startJoinServer(t *testing.T, admin service.AdminServicer) (*Transport, func()) {
	t.Helper()
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(newMockKVService(), admin, socketPath, unixserver.WithTLS(testUnixServerTLS))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		srv.Stop()
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		srv.Stop()
		t.Fatalf("Connect: %v", err)
	}
	return tr, func() {
		_ = tr.Close()
		srv.Stop()
	}
}

func TestUnixRT_Membership_PromoteAddObserverRetire(t *testing.T) {
	admin := &joinAdminSvc{mockAdminService: newMockAdminService()}
	tr, cleanup := startJoinServer(t, admin)
	defer cleanup()
	ctx := context.Background()

	if err := tr.AddObserverMember(ctx, 3, 9, "10.0.0.9:63001", "spiffe://db.example.com/node/n9"); err != nil {
		t.Fatalf("AddObserverMember: %v", err)
	}
	if admin.shardID != 3 || admin.replicaID != 9 || admin.raftAddr != "10.0.0.9:63001" || admin.identity != "spiffe://db.example.com/node/n9" {
		t.Fatalf("AddObserverMember reached the service as %+v", admin)
	}
	if err := tr.PromoteMember(ctx, 4, 10); err != nil {
		t.Fatalf("PromoteMember: %v", err)
	}
	if admin.shardID != 4 || admin.replicaID != 10 {
		t.Fatalf("PromoteMember reached the service as shard %d replica %d", admin.shardID, admin.replicaID)
	}
	if err := tr.RetireJoinIdentity(ctx, "n9", "decommissioned"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}
	if admin.nodeID != "n9" || admin.reason != "decommissioned" {
		t.Fatalf("RetireJoinIdentity reached the service as node %q reason %q", admin.nodeID, admin.reason)
	}
}

func TestUnixRT_Join_MintListApproveReject(t *testing.T) {
	admin := &joinAdminSvc{mockAdminService: newMockAdminService()}
	tr, cleanup := startJoinServer(t, admin)
	defer cleanup()
	ctx := context.Background()

	tok, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n4", TTLSeconds: 600})
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if admin.nodeID != "n4" || admin.ttl != 10*time.Minute {
		t.Fatalf("MintJoinToken reached the service as node %q ttl %v", admin.nodeID, admin.ttl)
	}
	if tok.Token != "join-token-n4" || !tok.ExpiresAt.Equal(time.Unix(1700003600, 0)) {
		t.Fatalf("MintJoinTokenResponse = %+v", tok)
	}

	pending, err := tr.ListPendingJoinRequests(ctx)
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	want := &transport.PendingJoinRequest{
		ID: "req-1", NodeURI: "spiffe://db.example.com/node/n4", ReplicaID: 4, Features: []string{"raft"},
		SubmittedAt: time.Unix(1700000000, 0).UTC(), ExpiresAt: time.Unix(1700000300, 0).UTC(),
	}
	if len(pending) != 1 || !reflect.DeepEqual(pending[0], want) {
		t.Fatalf("pending = %+v, want %+v", pending, want)
	}

	if err := tr.ApproveJoinRequest(ctx, "req-1", "n4"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	if admin.id != "req-1" || admin.nodeID != "n4" {
		t.Fatalf("ApproveJoinRequest reached the service as id %q node %q", admin.id, admin.nodeID)
	}
	if err := tr.RejectJoinRequest(ctx, "req-2", "unknown host"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}
	if admin.id != "req-2" || admin.reason != "unknown host" {
		t.Fatalf("RejectJoinRequest reached the service as id %q reason %q", admin.id, admin.reason)
	}
}

// TestUnixRT_Join_ServiceErrors: each membership RPC's service failure is a
// typed RequestError naming the RPC.
func TestUnixRT_Join_ServiceErrors(t *testing.T) {
	admin := &joinAdminSvc{mockAdminService: newMockAdminService(), err: errors.New("enrolment store unavailable")}
	tr, cleanup := startJoinServer(t, admin)
	defer cleanup()
	ctx := context.Background()

	calls := map[string]func() error{
		"PromoteMember":      func() error { return tr.PromoteMember(ctx, 1, 2) },
		"AddObserverMember":  func() error { return tr.AddObserverMember(ctx, 1, 2, "a:1", "id") },
		"RetireJoinIdentity": func() error { return tr.RetireJoinIdentity(ctx, "n", "") },
		"MintJoinToken": func() error {
			_, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n"})
			return err
		},
		"ListPendingJoinRequests": func() error { _, err := tr.ListPendingJoinRequests(ctx); return err },
		"ApproveJoinRequest":      func() error { return tr.ApproveJoinRequest(ctx, "r", "n") },
		"RejectJoinRequest":       func() error { return tr.RejectJoinRequest(ctx, "r", "x") },
	}
	for method, call := range calls {
		requireUnixRequestError(t, call(), method)
	}
}

// TestUnixRT_Join_UnsupportedByTheAdminService: an admin service without the
// optional interfaces is refused by the server, and the refusal surfaces as
// an error rather than a silent success.
func TestUnixRT_Join_UnsupportedByTheAdminService(t *testing.T) {
	tr, cleanup := startJoinServer(t, newMockAdminService())
	defer cleanup()
	ctx := context.Background()

	if err := tr.PromoteMember(ctx, 1, 2); err == nil {
		t.Fatal("PromoteMember against an admin service that cannot promote returned nil")
	}
	if _, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n"}); err == nil {
		t.Fatal("MintJoinToken against an admin service that cannot mint returned nil")
	}
	if _, err := tr.ListPendingJoinRequests(ctx); err == nil {
		t.Fatal("ListPendingJoinRequests against an admin service that cannot review returned nil")
	}
}

func TestUnixRT_Join_Closed(t *testing.T) {
	tr, cleanup := startJoinServer(t, &joinAdminSvc{mockAdminService: newMockAdminService()})
	cleanup()
	ctx := context.Background()

	assertClosed(t, tr.PromoteMember(ctx, 1, 2))
	assertClosed(t, tr.AddObserverMember(ctx, 1, 2, "a:1", "id"))
	assertClosed(t, tr.RetireJoinIdentity(ctx, "n", ""))
	_, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n"})
	assertClosed(t, err)
	_, err = tr.ListPendingJoinRequests(ctx)
	assertClosed(t, err)
	assertClosed(t, tr.ApproveJoinRequest(ctx, "r", "n"))
	assertClosed(t, tr.RejectJoinRequest(ctx, "r", "x"))
}

// TestUnixRT_WithDatabase_ScopesAndSharesTheConnection: a named scope dials
// its own connection that serves requests; the default scope shares the
// parent's; an unconnected parent yields an unconnected clone; and a closed
// parent yields a closed clone.
func TestUnixRT_WithDatabase_ScopesAndSharesTheConnection(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()
	ctx := context.Background()

	scoped, err := tr.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	st := scoped.(*Transport)
	defer func() { _ = st.Close() }()
	if st.activeDatabase != "orders" || st.conn == nil || st.conn == tr.conn {
		t.Fatalf("scoped clone: database %q, own conn %v", st.activeDatabase, st.conn != tr.conn)
	}
	if _, err := st.Health(ctx); err != nil {
		t.Fatalf("Health through the scoped clone: %v", err)
	}

	def, err := tr.WithDatabase("")
	if err != nil {
		t.Fatalf("WithDatabase(\"\"): %v", err)
	}
	if d := def.(*Transport); d.activeDatabase != "" || d.conn != tr.conn || d.client != tr.client {
		t.Fatalf("default-scope clone does not share the parent connection: %+v", d)
	}

	unconnected, err := New(transport.WithAddress(tempSocketPath(t)), testClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if u := unconnected.withDatabase("orders"); u.conn != nil || u.activeDatabase != "orders" {
		t.Fatalf("clone of an unconnected transport = %+v", u)
	}

	_ = tr.Close()
	closed := tr.withDatabase("orders")
	defer func() { _ = closed.Close() }()
	_, err = closed.Health(ctx)
	assertClosed(t, err)
}
